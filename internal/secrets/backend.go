package secrets

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/99designs/keyring"

	"github.com/steipete/gogcli/internal/config"
	"github.com/steipete/gogcli/internal/termutil"
)

const (
	keyringPasswordEnv    = "GOG_KEYRING_PASSWORD" //nolint:gosec // env var name, not a credential
	keyringBackendEnv     = "GOG_KEYRING_BACKEND"  //nolint:gosec // env var name, not a credential
	keyringServiceNameEnv = "GOG_KEYRING_SERVICE_NAME"
)

var (
	errNoTTY                 = errors.New("no TTY available for keyring file backend password prompt")
	errInvalidKeyringBackend = errors.New("invalid keyring backend")
	errKeyringTimeout        = errors.New("keyring connection timed out")
	errNilConfigStore        = errors.New("config store is nil")
	openKeyringFunc          = openKeyring
	keyringOpenFunc          = keyring.Open
)

type KeyringBackendInfo struct {
	Value  string
	Source string
}

const (
	keyringBackendSourceEnv     = "env"
	keyringBackendSourceConfig  = "config"
	keyringBackendSourceDefault = "default"
	keyringBackendAuto          = "auto"
)

func ResolveKeyringBackendInfo() (KeyringBackendInfo, error) {
	store, err := config.DefaultConfigStore()
	if err != nil {
		return KeyringBackendInfo{}, fmt.Errorf("resolve keyring backend: %w", err)
	}

	return ResolveKeyringBackendInfoFor(store)
}

func ResolveKeyringBackendInfoFor(store *config.ConfigStore) (KeyringBackendInfo, error) {
	if v := normalizeKeyringBackend(os.Getenv(keyringBackendEnv)); v != "" {
		return KeyringBackendInfo{Value: v, Source: keyringBackendSourceEnv}, nil
	}

	if store == nil {
		return KeyringBackendInfo{}, errNilConfigStore
	}

	cfg, err := store.Read()
	if err != nil {
		return KeyringBackendInfo{}, fmt.Errorf("resolve keyring backend: %w", err)
	}

	if cfg.KeyringBackend != "" {
		if v := normalizeKeyringBackend(cfg.KeyringBackend); v != "" {
			return KeyringBackendInfo{Value: v, Source: keyringBackendSourceConfig}, nil
		}
	}

	return KeyringBackendInfo{Value: keyringBackendAuto, Source: keyringBackendSourceDefault}, nil
}

func allowedBackends(info KeyringBackendInfo) ([]keyring.BackendType, error) {
	switch info.Value {
	case "", keyringBackendAuto:
		return nil, nil
	case "keychain":
		return []keyring.BackendType{keyring.KeychainBackend}, nil
	case "file":
		return []keyring.BackendType{keyring.FileBackend}, nil
	default:
		return nil, fmt.Errorf("%w: %q (expected %s, keychain, or file)", errInvalidKeyringBackend, info.Value, keyringBackendAuto)
	}
}

// wrapKeychainError wraps keychain errors with helpful guidance on macOS.
func wrapKeychainError(err error) error {
	if err == nil {
		return nil
	}

	if IsKeychainLockedError(err.Error()) {
		return fmt.Errorf("%w\n\nYour macOS keychain is locked. To unlock it, run:\n  security unlock-keychain ~/Library/Keychains/login.keychain-db", err)
	}

	return err
}

func fileKeyringPasswordFuncFrom(password string, passwordSet bool, isTTY bool) keyring.PromptFunc {
	// Treat "set to empty string" as intentional; empty passphrase is valid.
	if passwordSet {
		return keyring.FixedStringPrompt(password)
	}

	if isTTY {
		return keyring.TerminalPrompt
	}

	return func(_ string) (string, error) {
		return "", fmt.Errorf("%w; set %s", errNoTTY, keyringPasswordEnv)
	}
}

func fileKeyringPasswordFunc() keyring.PromptFunc {
	password, passwordSet := os.LookupEnv(keyringPasswordEnv)
	return fileKeyringPasswordFuncFrom(password, passwordSet, termutil.IsTerminal(os.Stdin))
}

func normalizeKeyringBackend(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func keyringServiceName() string {
	if serviceName := strings.TrimSpace(os.Getenv(keyringServiceNameEnv)); serviceName != "" {
		return serviceName
	}

	return config.AppName
}

// keyringOpenTimeout is the maximum time to wait for keyring.Open() to complete.
// On headless Linux, D-Bus SecretService can hang indefinitely if gnome-keyring
// is installed but not running.
const (
	keyringOpenTimeout = 10 * time.Second
	goosDarwin         = "darwin"
	goosLinux          = "linux"
)

func shouldForceFileBackend(goos string, backendInfo KeyringBackendInfo, dbusAddr string) bool {
	return goos == goosLinux && backendInfo.Value == keyringBackendAuto && dbusAddr == ""
}

func shouldUseKeyringTimeout(goos string, backendInfo KeyringBackendInfo, dbusAddr string) bool {
	return goos == goosLinux && backendInfo.Value == keyringBackendAuto && dbusAddr != ""
}

func shouldUseKeyringOperationTimeout(goos string, backendInfo KeyringBackendInfo, dbusAddr string) bool {
	if goos == goosDarwin {
		return backendInfo.Value == keyringBackendAuto || backendInfo.Value == "keychain"
	}

	return goos == goosLinux && backendInfo.Value == keyringBackendAuto && dbusAddr != ""
}

func keyringTimeoutHint(goos string) string {
	switch goos {
	case goosDarwin:
		return "macOS Keychain may be waiting for a permission prompt; run `gog auth list` from a terminal and click \"Always Allow\" when prompted"
	case goosLinux:
		return "D-Bus SecretService may be unresponsive"
	default:
		return "keyring backend may be unresponsive"
	}
}

func isFileKeyring(ring keyring.Keyring) bool {
	if ring == nil {
		return false
	}

	return reflect.TypeOf(ring).String() == "*keyring.fileKeyring"
}

func openKeyring() (keyring.Keyring, error) {
	layout, err := config.ResolveSystemLayoutFor("", config.PathKindConfig, config.PathKindData)
	if err != nil {
		return nil, fmt.Errorf("resolve keyring layout: %w", err)
	}

	store := config.NewConfigStore(layout)

	return openKeyringWithConfig(layout, store)
}

func openKeyringWithConfig(layout config.Layout, store *config.ConfigStore) (keyring.Keyring, error) {
	// On Linux/WSL/containers, OS keychains (secret-service/kwallet) may be unavailable.
	// In that case github.com/99designs/keyring falls back to the "file" backend,
	// which *requires* both a directory and a password prompt function.
	keyringDir, err := layout.EnsureKeyringDir()
	if err != nil {
		return nil, fmt.Errorf("ensure keyring dir: %w", err)
	}

	backendInfo, err := ResolveKeyringBackendInfoFor(store)
	if err != nil {
		return nil, err
	}

	backends, err := allowedBackends(backendInfo)
	if err != nil {
		return nil, err
	}
	wrapFileKeys := fileKeyringBackendOnly(backends)

	dbusAddr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	// On Linux with "auto" backend and no D-Bus session, force file backend.
	// Without DBUS_SESSION_BUS_ADDRESS, SecretService will hang indefinitely
	// trying to connect (common on headless systems like Raspberry Pi).
	if shouldForceFileBackend(runtime.GOOS, backendInfo, dbusAddr) {
		backends = []keyring.BackendType{keyring.FileBackend}
		wrapFileKeys = true
	}

	cfg := keyring.Config{
		ServiceName: keyringServiceName(),
		// KeychainTrustApplication is intentionally false to support Homebrew upgrades.
		// When true, macOS Keychain ties access control to the specific binary hash.
		// Homebrew upgrades install a new binary with a different hash, causing the
		// new binary to lose access to existing keychain items. With false, users may
		// see a one-time keychain prompt after upgrade (click "Always Allow"), but
		// tokens survive across upgrades. See: https://github.com/steipete/gogcli/issues/86
		KeychainTrustApplication: false,
		AllowedBackends:          backends,
		FileDir:                  keyringDir,
		FilePasswordFunc:         fileKeyringPasswordFunc(),
	}

	// On Linux with D-Bus present, keyring.Open() can still hang if SecretService
	// is unresponsive (e.g., gnome-keyring installed but not running).
	// Use a timeout as a safety net.
	if shouldUseKeyringTimeout(runtime.GOOS, backendInfo, dbusAddr) {
		timeoutRing, timeoutErr := openKeyringWithTimeout(cfg, keyringOpenTimeout)
		if timeoutErr != nil {
			return nil, timeoutErr
		}

		return prepareKeyring(timeoutRing, backendInfo, wrapFileKeys, dbusAddr), nil
	}

	ring, err := keyringOpenFunc(cfg)
	if err != nil {
		return nil, fmt.Errorf("open keyring: %w", err)
	}

	return prepareKeyring(ring, backendInfo, wrapFileKeys, dbusAddr), nil
}

func prepareKeyring(ring keyring.Keyring, backendInfo KeyringBackendInfo, wrapFileKeys bool, dbusAddr string) keyring.Keyring {
	if wrapFileKeys || isFileKeyring(ring) {
		ring = newFileSafeKeyring(ring)
	}

	if shouldUseKeyringOperationTimeout(runtime.GOOS, backendInfo, dbusAddr) {
		ring = newTimeoutKeyring(ring, keyringOpenTimeout, keyringTimeoutHint(runtime.GOOS))
	}

	return ring
}

type keyringResult struct {
	ring keyring.Keyring
	err  error
}

// openKeyringWithTimeout wraps keyring.Open with a timeout to prevent indefinite
// hangs when D-Bus SecretService is unresponsive (e.g., gnome-keyring installed
// but not running on headless Linux).
//
// Note: If timeout occurs, the spawned goroutine continues blocking on keyring.Open()
// and will leak. This is acceptable for a CLI tool since the process exits on this
// error, but would need refactoring for long-running use.
func openKeyringWithTimeout(cfg keyring.Config, timeout time.Duration) (keyring.Keyring, error) {
	return openKeyringWithTimeoutFunc(cfg, timeout, keyringOpenFunc)
}

func openKeyringWithTimeoutFunc(cfg keyring.Config, timeout time.Duration, open func(keyring.Config) (keyring.Keyring, error)) (keyring.Keyring, error) {
	ch := make(chan keyringResult, 1)

	go func() {
		ring, err := open(cfg)
		ch <- keyringResult{ring, err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("open keyring: %w", res.err)
		}

		return res.ring, nil
	case <-time.After(timeout):
		return nil, keyringTimeoutError("opening keyring", timeout, keyringTimeoutHint(runtime.GOOS))
	}
}

func OpenDefault() (Store, error) {
	return openDefaultRepository()
}

func openDefaultRepository() (Repository, error) {
	ring, err := openKeyringFunc()
	if err != nil {
		return nil, err
	}

	lock, _, err := keyringLockForRing(ring)
	if err != nil {
		return nil, err
	}

	return &KeyringStore{ring: ring, lock: lock}, nil
}

func OpenWithConfig(layout config.Layout, store *config.ConfigStore) (Repository, error) {
	ring, err := openKeyringWithConfig(layout, store)
	if err != nil {
		return nil, err
	}

	lock, _, err := keyringLockForRingInDir(ring, layout.KeyringDir())
	if err != nil {
		return nil, err
	}

	return &KeyringStore{ring: ring, lock: lock}, nil
}

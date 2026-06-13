package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (l Layout) EnsureDataDir() (string, error) {
	if err := os.MkdirAll(l.DataDir, 0o700); err != nil {
		return "", fmt.Errorf("ensure data dir: %w", err)
	}

	return l.DataDir, nil
}

func (l Layout) ExistingServiceAccountPath(email string) (string, error) {
	paths := []string{l.ServiceAccountPath(email)}
	if !l.ExplicitData {
		paths = append(paths, l.ServiceAccountLegacyPath(email))
	}

	return firstExistingServiceAccountPath(paths...)
}

func (l Layout) ExistingKeepServiceAccountPath(email string) (string, error) {
	paths := []string{l.KeepServiceAccountPath(email)}
	if !l.ExplicitData {
		paths = append(paths,
			l.KeepServiceAccountLegacySafePath(email),
			l.KeepServiceAccountLegacyPath(email),
		)
	}

	return firstExistingServiceAccountPath(paths...)
}

func (l Layout) RemoveServiceAccountFiles(email string) (bool, error) {
	paths := []string{
		l.ServiceAccountPath(email),
		l.KeepServiceAccountPath(email),
	}
	if !l.ExplicitData {
		paths = append(paths,
			l.ServiceAccountLegacyPath(email),
			l.KeepServiceAccountLegacySafePath(email),
		)
		if path, ok := l.keepServiceAccountLegacyDeletePath(email); ok {
			paths = append(paths, path)
		}
	}

	removed := false

	for _, path := range uniquePaths(paths...) {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return removed, fmt.Errorf("remove service account file: %w", err)
		}
		removed = true
	}

	return removed, nil
}

func (l Layout) ListServiceAccountEmails() ([]string, error) {
	out := make([]string, 0)
	seen := make(map[string]struct{})

	dirs := []string{l.DataDir}
	if !l.ExplicitData {
		dirs = append(dirs, l.ConfigDir)
	}

	for _, dir := range uniquePaths(dirs...) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return nil, fmt.Errorf("read service account dir: %w", err)
		}

		for _, entry := range entries {
			email := serviceAccountEmailFromEntry(entry)
			if email == "" {
				continue
			}

			if _, ok := seen[email]; ok {
				continue
			}
			seen[email] = struct{}{}
			out = append(out, email)
		}
	}

	sort.Strings(out)

	return out, nil
}

func (l Layout) keepServiceAccountLegacyDeletePath(email string) (string, bool) {
	if strings.ContainsAny(email, `/\`) {
		return "", false
	}

	path := filepath.Clean(l.KeepServiceAccountLegacyPath(email))

	base := filepath.Base(path)
	if filepath.Dir(path) != filepath.Clean(l.ConfigDir) ||
		!strings.HasPrefix(base, "keep-sa-") ||
		!strings.HasSuffix(base, ".json") {
		return "", false
	}

	return path, true
}

func firstExistingServiceAccountPath(paths ...string) (string, error) {
	var first string
	for _, path := range paths {
		if first == "" {
			first = path
		}

		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat service account path: %w", err)
		}
	}

	return first, nil
}

func serviceAccountEmailFromEntry(entry os.DirEntry) string {
	if entry.IsDir() {
		return ""
	}

	name := entry.Name()
	var encoded string

	switch {
	case strings.HasPrefix(name, "sa-") && strings.HasSuffix(name, ".json"):
		encoded = strings.TrimSuffix(strings.TrimPrefix(name, "sa-"), ".json")
	case strings.HasPrefix(name, "keep-sa-") && strings.HasSuffix(name, ".json"):
		encoded = strings.TrimSuffix(strings.TrimPrefix(name, "keep-sa-"), ".json")
	default:
		return ""
	}

	if decoded, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		return strings.ToLower(strings.TrimSpace(string(decoded)))
	}

	if strings.HasPrefix(name, "keep-sa-") {
		return strings.ToLower(strings.TrimSpace(encoded))
	}

	return ""
}

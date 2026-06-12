package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"

	"github.com/steipete/gogcli/internal/app"
)

func TestExecuteRuntimeRoutesMigratedCommandOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := &app.Runtime{IO: app.IO{
		In:  strings.NewReader(""),
		Out: &stdout,
		Err: &stderr,
	}}

	if err := executeWithRuntime([]string{"--json", "version"}, runtime); err != nil {
		t.Fatalf("executeWithRuntime() error = %v, stderr = %q", err, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout=%q", err, stdout.String())
	}
	if got["version"] == "" {
		t.Fatalf("stdout = %#v, want version", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteRuntimeRoutesEarlyErrors(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := &app.Runtime{IO: app.IO{
		In:  strings.NewReader(""),
		Out: &stdout,
		Err: &stderr,
	}}

	err := executeWithRuntime([]string{"--json", "--plain", "version"}, runtime)
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("executeWithRuntime() error = %v, want exit code 2", err)
	}
	if !strings.Contains(stderr.String(), "cannot combine --json and --plain") {
		t.Fatalf("stderr = %q, want output mode error", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestDriveServiceUsesRuntimeFactory(t *testing.T) {
	t.Parallel()

	want := &drive.Service{}
	var gotAccount string
	runtime := &app.Runtime{Services: app.Services{
		Drive: func(_ context.Context, account string) (*drive.Service, error) {
			gotAccount = account
			return want, nil
		},
	}}
	ctx := app.WithRuntime(context.Background(), runtime)

	got, err := driveService(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("driveService() error = %v", err)
	}
	if got != want {
		t.Fatalf("driveService() = %p, want %p", got, want)
	}
	if gotAccount != "test@example.com" {
		t.Fatalf("factory account = %q, want test@example.com", gotAccount)
	}
}

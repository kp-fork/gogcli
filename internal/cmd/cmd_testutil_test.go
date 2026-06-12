package cmd

import (
	"context"
	"io"
	"testing"

	"github.com/steipete/gogcli/internal/app"
	"github.com/steipete/gogcli/internal/outfmt"
	"github.com/steipete/gogcli/internal/ui"
)

func newCmdOutputContext(t *testing.T, stdout, stderr io.Writer) context.Context {
	t.Helper()

	u, err := ui.New(ui.Options{Stdout: stdout, Stderr: stderr, Color: "never"})
	if err != nil {
		t.Fatalf("ui.New: %v", err)
	}
	return ui.WithUI(context.Background(), u)
}

func newCmdRuntimeOutputContext(t *testing.T, stdout, stderr io.Writer) context.Context {
	t.Helper()
	return app.WithRuntime(newCmdOutputContext(t, stdout, stderr), &app.Runtime{IO: app.IO{
		Out: stdout,
		Err: stderr,
	}})
}

func newCmdJSONContext(t *testing.T) context.Context {
	t.Helper()
	return newCmdJSONOutputContext(t, io.Discard, io.Discard)
}

func newCmdJSONOutputContext(t *testing.T, stdout, stderr io.Writer) context.Context {
	t.Helper()
	return outfmt.WithMode(newCmdOutputContext(t, stdout, stderr), outfmt.Mode{JSON: true})
}

func newCmdRuntimeJSONOutputContext(t *testing.T, stdout, stderr io.Writer) context.Context {
	t.Helper()
	return outfmt.WithMode(newCmdRuntimeOutputContext(t, stdout, stderr), outfmt.Mode{JSON: true})
}

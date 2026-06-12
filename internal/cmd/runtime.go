package cmd

import (
	"context"
	"io"
	"os"

	"google.golang.org/api/drive/v3"

	"github.com/steipete/gogcli/internal/app"
	"github.com/steipete/gogcli/internal/googleapi"
)

// Mutable until legacy command tests inject services through Runtime.
var newDriveService = googleapi.NewDrive

func newDefaultRuntime() *app.Runtime {
	return &app.Runtime{
		IO: app.IO{
			In:  os.Stdin,
			Out: os.Stdout,
			Err: os.Stderr,
		},
		Services: app.Services{
			Drive: newDriveService,
		},
	}
}

func normalizedRuntime(runtime *app.Runtime) *app.Runtime {
	defaults := newDefaultRuntime()
	if runtime == nil {
		return defaults
	}
	normalized := *runtime
	if normalized.IO.In == nil {
		normalized.IO.In = defaults.IO.In
	}
	if normalized.IO.Out == nil {
		normalized.IO.Out = defaults.IO.Out
	}
	if normalized.IO.Err == nil {
		normalized.IO.Err = defaults.IO.Err
	}
	if normalized.Services.Drive == nil {
		normalized.Services.Drive = defaults.Services.Drive
	}
	return &normalized
}

func commandIO(ctx context.Context) app.IO {
	if runtimeIO, ok := app.IOFromContext(ctx); ok {
		return runtimeIO
	}
	return newDefaultRuntime().IO
}

func stdoutWriter(ctx context.Context) io.Writer {
	return commandIO(ctx).Out
}

func driveService(ctx context.Context, account string) (*drive.Service, error) {
	if runtime, ok := app.FromContext(ctx); ok && runtime.Services.Drive != nil {
		return runtime.Services.Drive(ctx, account)
	}
	return newDriveService(ctx, account)
}

//go:build windows

package codex

import (
	"context"
	"io"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsprocess"
)

//go:generate go run go.uber.org/mock/mockgen -source=ports_windows.go -destination=ports_windows_mock_test.go -package=codex -build_constraint=windows

type commandProcess interface{ Wait() error }

type commandLauncher interface {
	Start(context.Context, string, []string, io.Writer, io.Writer) (commandProcess, error)
}

type timeoutSource interface {
	WithTimeout(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type systemLauncher struct{}

func (systemLauncher) Start(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) (commandProcess, error) {
	command, err := windowsprocess.Start(ctx, executable, args, nil, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return containedCommand{command}, nil
}

type containedCommand struct{ command *windowsprocess.Command }

func (p containedCommand) Wait() error { defer p.command.Close(); return p.command.Wait() }

type systemTimeouts struct{}

func (systemTimeouts) WithTimeout(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, duration)
}

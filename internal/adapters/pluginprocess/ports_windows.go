//go:build windows

package pluginprocess

import (
	"io"
	"time"
)

//go:generate go run go.uber.org/mock/mockgen -source=ports_windows.go -destination=ports_windows_mock_test.go -package=pluginprocess -build_constraint=windows

type childProcess interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait() error
	Terminate()
	Close()
}

type processLauncher interface {
	Start(Config) (childProcess, error)
}

type timer interface {
	C() <-chan time.Time
	Stop() bool
}
type clock interface {
	Now() time.Time
	NewTimer(time.Duration) timer
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
func (systemClock) NewTimer(duration time.Duration) timer {
	return systemTimer{time.NewTimer(duration)}
}

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }
func (t systemTimer) Stop() bool          { return t.timer.Stop() }

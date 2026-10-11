//go:build windows

package windowsprocess

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

//go:generate go run go.uber.org/mock/mockgen -source=ports_windows.go -destination=ports_windows_mock_test.go -package=windowsprocess -build_constraint=windows

type nativeProcess interface {
	Start() error
	Wait() error
	PID() uint32
	Kill() error
	SetCancel(func() error)
}

type nativeSystem interface {
	CreateJob() (windows.Handle, error)
	NewCommand(context.Context, string, []string, io.Reader, io.Writer, io.Writer) nativeProcess
	Assign(windows.Handle, uint32) error
	Resume(uint32) error
	TerminateJob(windows.Handle) error
	CloseJob(windows.Handle) error
}

type windowsSystem struct{}

func (windowsSystem) CreateJob() (windows.Handle, error) { return createKillOnCloseJob() }
func (windowsSystem) NewCommand(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) nativeProcess {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.WaitDelay = 100 * time.Millisecond
	return execProcess{cmd}
}

func (windowsSystem) Assign(job windows.Handle, pid uint32) error {
	return assignProcessToJob(job, pid)
}
func (windowsSystem) Resume(pid uint32) error { return resumeInitialThread(pid) }
func (windowsSystem) TerminateJob(job windows.Handle) error {
	return windows.TerminateJobObject(job, 1)
}
func (windowsSystem) CloseJob(job windows.Handle) error { return windows.CloseHandle(job) }

type execProcess struct{ cmd *exec.Cmd }

func (p execProcess) Start() error { return p.cmd.Start() }
func (p execProcess) Wait() error  { return p.cmd.Wait() }
func (p execProcess) PID() uint32 {
	if p.cmd.Process == nil {
		return 0
	}
	return uint32(p.cmd.Process.Pid)
}

func (p execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p execProcess) SetCancel(cancel func() error) { p.cmd.Cancel = cancel }

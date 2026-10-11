//go:build windows

// Package windowsprocess contains daemon children before their first instruction.
package windowsprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Command struct {
	mu     sync.Mutex
	cmd    nativeProcess
	job    windows.Handle
	system nativeSystem
}

func Start(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer) (*Command, error) {
	return start(ctx, executable, args, stdin, stdout, stderr, windowsSystem{})
}

func start(ctx context.Context, executable string, args []string, stdin io.Reader, stdout, stderr io.Writer, system nativeSystem) (*Command, error) {
	job, err := system.CreateJob()
	if err != nil {
		return nil, fmt.Errorf("create child job object: %w", err)
	}
	cmd := system.NewCommand(ctx, executable, args, stdin, stdout, stderr)
	child := &Command{cmd: cmd, job: job, system: system}
	cmd.SetCancel(func() error { child.Terminate(); return nil })
	if err := cmd.Start(); err != nil {
		child.Close()
		return nil, fmt.Errorf("start child process: %w", err)
	}
	if err := system.Assign(job, cmd.PID()); err != nil {
		child.Terminate()
		_ = cmd.Wait()
		child.Close()
		return nil, fmt.Errorf("contain child process: %w", err)
	}
	if err := system.Resume(cmd.PID()); err != nil {
		child.Terminate()
		_ = cmd.Wait()
		child.Close()
		return nil, fmt.Errorf("resume child process: %w", err)
	}
	return child, nil
}

func (p *Command) Wait() error { return p.cmd.Wait() }

func (p *Command) Terminate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		_ = p.system.TerminateJob(p.job)
	}
	_ = p.cmd.Kill()
}

func (p *Command) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != 0 {
		_ = p.system.TerminateJob(p.job)
		_ = p.system.CloseJob(p.job)
		p.job = 0
	}
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignProcessToJob(job windows.Handle, processID uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(job, process)
}

func resumeInitialThread(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	for err == nil {
		if entry.OwnerProcessID == processID {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			defer windows.CloseHandle(thread)
			previous, resumeErr := windows.ResumeThread(thread)
			if resumeErr != nil {
				return resumeErr
			}
			if previous == 0 {
				return errors.New("initial plugin thread was not suspended")
			}
			return nil
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	return fmt.Errorf("find suspended plugin thread: %w", err)
}

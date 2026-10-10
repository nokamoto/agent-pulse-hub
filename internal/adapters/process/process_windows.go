//go:build windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Job struct {
	handle windows.Handle
	once   sync.Once
	err    error
}

func NewJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Windows job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("configure Windows job object: %w", err)
	}
	return &Job{handle: handle}, nil
}

func Start(command *exec.Cmd, job *Job) error {
	attributes := syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
		attributes.CreationFlags |= windows.CREATE_SUSPENDED
		attributes.HideWindow = true
	}
	command.SysProcAttr = &attributes
	if err := command.Start(); err != nil {
		return err
	}
	if job == nil || job.handle == 0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("process must be assigned to a Windows job object")
	}
	processHandle, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(command.Process.Pid))
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("open suspended child process: %w", err)
	}
	defer windows.CloseHandle(processHandle)
	if err := windows.AssignProcessToJobObject(job.handle, processHandle); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("assign child process to Windows job object: %w", err)
	}
	resume := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")
	if err := resume.Find(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("find NtResumeProcess: %w", err)
	}
	status, _, callErr := resume.Call(uintptr(processHandle))
	if status != 0 {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("resume suspended child process: NTSTATUS 0x%08X (%v)", status, callErr)
	}
	return nil
}

func (job *Job) Close() error {
	if job == nil {
		return nil
	}
	job.once.Do(func() {
		if job.handle != 0 {
			job.err = windows.CloseHandle(job.handle)
			job.handle = 0
		}
	})
	return job.err
}

func WaitForExit(process *os.Process) <-chan struct{} {
	if process == nil {
		return nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(process.Pid))
	if err != nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer windows.CloseHandle(handle)
		_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
	}()
	return done
}

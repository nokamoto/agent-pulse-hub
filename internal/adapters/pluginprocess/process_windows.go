//go:build windows

package pluginprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"golang.org/x/sys/windows"
)

const (
	readyTimeout             = 10 * time.Second
	watchTimeout             = 10 * time.Second
	shutdownGrace            = 5 * time.Second
	maxPluginDiagnosticBytes = 1_024
)

type Config struct {
	Name       string
	Executable string
	Args       []string
}

type Process struct {
	config    Config
	logger    *slog.Logger
	mu        sync.Mutex
	writeGate chan struct{}
	pending   map[string]*watchPending
	handlers  hub.PluginHandlers

	cmd        *exec.Cmd
	stdin      *os.File
	stdout     *os.File
	stderr     *os.File
	job        windows.Handle
	started    bool
	ready      bool
	stopping   bool
	failed     bool
	failure    error
	readyCh    chan struct{}
	doneCh     chan struct{}
	finishOnce sync.Once
	failOnce   sync.Once
}

type watchPending struct {
	deadline time.Time
	activate func() error
	result   chan watchOutcome
}

type watchOutcome struct {
	accepted bool
	err      error
}

func New(config Config, logger *slog.Logger) *Process {
	if logger == nil {
		logger = slog.Default()
	}
	return &Process{
		config:    config,
		logger:    logger,
		writeGate: make(chan struct{}, 1),
		pending:   make(map[string]*watchPending),
		readyCh:   make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (p *Process) Start(ctx context.Context, handlers hub.PluginHandlers) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("plugin process already started")
	}
	p.handlers = handlers
	p.mu.Unlock()

	job, err := createKillOnCloseJob()
	if err != nil {
		return fmt.Errorf("create plugin job object: %w", err)
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("create plugin stdin pipe: %w", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		closeFiles(stdinRead, stdinWrite)
		windows.CloseHandle(job)
		return fmt.Errorf("create plugin stdout pipe: %w", err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		closeFiles(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		windows.CloseHandle(job)
		return fmt.Errorf("create plugin stderr pipe: %w", err)
	}

	command := exec.Command(p.config.Executable, p.config.Args...)
	command.Stdin = stdinRead
	command.Stdout = stdoutWrite
	command.Stderr = stderrWrite
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := command.Start(); err != nil {
		closeFiles(stdinRead, stdinWrite, stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		windows.CloseHandle(job)
		return fmt.Errorf("start plugin process: %w", err)
	}
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()

	if err := assignProcessToJob(job, uint32(command.Process.Pid)); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		closeFiles(stdinWrite, stdoutRead, stderrRead)
		windows.CloseHandle(job)
		return fmt.Errorf("contain plugin process: %w", err)
	}
	if err := resumeInitialThread(uint32(command.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = command.Process.Kill()
		_ = command.Wait()
		closeFiles(stdinWrite, stdoutRead, stderrRead)
		windows.CloseHandle(job)
		return fmt.Errorf("resume plugin process: %w", err)
	}

	p.mu.Lock()
	p.cmd = command
	p.stdin = stdinWrite
	p.stdout = stdoutRead
	p.stderr = stderrRead
	p.job = job
	p.started = true
	p.mu.Unlock()

	go p.drainStderr(stderrRead)
	go p.readStdout(stdoutRead)
	go p.waitProcess(command)
	readiness := time.NewTimer(readyTimeout)
	defer readiness.Stop()
	select {
	case <-p.readyCh:
		return nil
	case <-p.doneCh:
		return fmt.Errorf("plugin exited before readiness: %s", p.failureText())
	case <-readiness.C:
		p.fail(errors.New("plugin readiness deadline expired"))
		return errors.New("plugin readiness deadline expired")
	case <-ctx.Done():
		p.fail(ctx.Err())
		return ctx.Err()
	}
}

func (p *Process) Watch(ctx context.Context, request hub.WatchRequest, activate func() error) error {
	frameBytes, err := pluginprotocol.Encode(pluginprotocol.Frame{
		Type:           "watch",
		RequestID:      request.RequestID,
		SubscriptionID: string(request.SubscriptionID),
		WatchArgs:      request.Arguments,
	})
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			return &hub.WatchError{Code: "resource_exhausted", Message: "Watch request exceeds the protocol frame limit."}
		}
		return &hub.WatchError{Code: "invalid_request", Message: "Watch request is invalid."}
	}
	if err := p.acquireWrite(ctx, time.Time{}); err != nil {
		return &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin is unavailable.", Unavailable: true, Cause: err}
	}
	defer p.releaseWrite()
	deadline := time.Now().Add(watchTimeout)
	pending := &watchPending{deadline: deadline, activate: activate, result: make(chan watchOutcome, 1)}
	p.mu.Lock()
	if !p.ready || p.failed || p.stopping {
		p.mu.Unlock()
		return &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin is unavailable.", Unavailable: true}
	}
	if _, exists := p.pending[request.RequestID]; exists {
		p.mu.Unlock()
		return &hub.WatchError{Code: "invalid_request", Message: "Watch request identifier is already pending."}
	}
	p.pending[request.RequestID] = pending
	p.mu.Unlock()

	if err := p.writeUntil(ctx, frameBytes, deadline); err != nil {
		return p.interruptWatch(request.RequestID, pending, err, "Plugin could not receive the watch request.")
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.err
	case <-timer.C:
		err := errors.New("plugin watch acknowledgement deadline expired")
		return p.interruptWatch(request.RequestID, pending, err, "Plugin did not acknowledge the watch in time.")
	case <-ctx.Done():
		return p.interruptWatch(request.RequestID, pending, ctx.Err(), "Plugin watch was interrupted.")
	case <-p.doneCh:
		p.removePending(request.RequestID, pending)
		return &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin exited before acknowledging the watch.", Unavailable: true, Cause: p.failure}
	}
}

func (p *Process) interruptWatch(requestID string, pending *watchPending, cause error, message string) error {
	p.mu.Lock()
	stillPending := p.pending[requestID] == pending
	if stillPending {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
	if !stillPending {
		select {
		case result := <-pending.result:
			return result.err
		case <-p.doneCh:
			select {
			case result := <-pending.result:
				return result.err
			default:
				return &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin exited before acknowledging the watch.", Unavailable: true, Cause: p.failure}
			}
		}
	}
	p.fail(cause)
	return &hub.WatchError{Code: "plugin_unavailable", Message: message, Unavailable: true, Cause: cause}
}

func (p *Process) Stop(ctx context.Context) error {
	startedAt := time.Now()
	deadline := startedAt.Add(shutdownGrace)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	p.mu.Lock()
	if !p.started {
		p.stopping = true
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	finished := !p.processRunningLocked()
	p.mu.Unlock()
	if finished {
		return nil
	}

	frameBytes, encodeErr := pluginprotocol.Encode(pluginprotocol.Frame{Type: "shutdown"})
	if encodeErr == nil {
		if err := p.acquireWrite(ctx, deadline); err == nil {
			writeErr := p.writeUntil(ctx, frameBytes, deadline)
			p.releaseWrite()
			if writeErr != nil {
				p.logger.Warn("plugin_shutdown_write_failed", "plugin", p.config.Name, "reason", safeText(writeErr.Error(), 256))
			}
		}
	}
	remaining := time.Until(deadline)
	if remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-p.doneCh:
			timer.Stop()
			return nil
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	p.terminate()
	select {
	case <-p.doneCh:
		return nil
	case <-time.After(time.Second):
		return errors.New("plugin process did not exit after termination")
	}
}

func (p *Process) acquireWrite(ctx context.Context, deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case p.writeGate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.doneCh:
			return errors.New("plugin process has exited")
		}
		return nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case p.writeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin process has exited")
	case <-timer.C:
		return errors.New("plugin write deadline expired")
	}
}

func (p *Process) releaseWrite() { <-p.writeGate }

func (p *Process) writeUntil(ctx context.Context, frame []byte, deadline time.Time) error {
	result := make(chan error, 1)
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil {
		return errors.New("plugin stdin is unavailable")
	}
	go func() {
		for len(frame) > 0 {
			written, err := stdin.Write(frame)
			if err != nil {
				result <- err
				return
			}
			if written == 0 {
				result <- io.ErrShortWrite
				return
			}
			frame = frame[written:]
		}
		result <- nil
	}()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errors.New("plugin write deadline expired")
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		return errors.New("plugin write deadline expired")
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin process exited while writing")
	}
}

func (p *Process) readStdout(stdout *os.File) {
	reader := bufio.NewReaderSize(stdout, 4096)
	for {
		line, err := protocol.ReadFrame(reader, protocol.MaxFrameBytes)
		if err != nil {
			p.fail(fmt.Errorf("plugin stdout ended: %w", err))
			return
		}
		value, err := protocol.ParseFrame(line)
		if err != nil {
			p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", "malformed frame")
			continue
		}
		frame, err := pluginprotocol.Decode(value, pluginprotocol.PluginToDaemon)
		if err != nil {
			p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", safeText(err.Error(), 256))
			continue
		}
		p.handleFrame(frame)
	}
}

func (p *Process) handleFrame(frame pluginprotocol.Frame) {
	p.mu.Lock()
	ready := p.ready
	p.mu.Unlock()
	if frame.Type == "ready" {
		if ready {
			p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", "duplicate readiness")
			return
		}
		p.mu.Lock()
		if p.ready || p.failed {
			p.mu.Unlock()
			return
		}
		p.ready = true
		handler := p.handlers.Ready
		p.mu.Unlock()
		if handler != nil {
			handler()
		}
		close(p.readyCh)
		return
	}
	if !ready {
		p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", "frame received before readiness")
		return
	}
	switch frame.Type {
	case "watch_result":
		p.handleWatchResult(frame)
	case "event":
		p.mu.Lock()
		handler := p.handlers.Event
		p.mu.Unlock()
		if handler != nil {
			handler(frame.SubscriptionID, frame.Context)
		}
	default:
		p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", "invalid direction")
	}
}

func (p *Process) handleWatchResult(frame pluginprotocol.Frame) {
	p.mu.Lock()
	pending := p.pending[frame.RequestID]
	if pending == nil {
		p.mu.Unlock()
		p.logger.Warn("plugin_watch_result_rejected", "plugin", p.config.Name, "request_id", frame.RequestID, "reason", "unknown or duplicate request")
		return
	}
	delete(p.pending, frame.RequestID)
	if time.Now().After(pending.deadline) {
		p.mu.Unlock()
		p.fail(errors.New("plugin watch result arrived after its deadline"))
		pending.result <- watchOutcome{err: &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin did not acknowledge the watch in time.", Unavailable: true}}
		return
	}
	p.mu.Unlock()
	if frame.Accepted == nil || !*frame.Accepted {
		pending.result <- watchOutcome{err: &hub.WatchError{Code: "watch_rejected", Message: "Plugin rejected the watch arguments."}}
		return
	}
	if err := pending.activate(); err != nil {
		p.fail(fmt.Errorf("watch acceptance could not be committed: %w", err))
		pending.result <- watchOutcome{err: &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin watch acceptance was uncertain.", Unavailable: true, Cause: err}}
		return
	}
	pending.result <- watchOutcome{accepted: true}
}

func (p *Process) waitProcess(command *exec.Cmd) {
	err := command.Wait()
	p.mu.Lock()
	if err != nil && !p.stopping && !p.failed {
		p.failure = fmt.Errorf("plugin process exited: %w", err)
	}
	unexpected := !p.stopping && !p.failed
	if unexpected && p.failure == nil {
		p.failure = errors.New("plugin process exited")
	}
	job := p.job
	p.job = 0
	stdin, stdout, stderr := p.stdin, p.stdout, p.stderr
	p.stdin, p.stdout, p.stderr = nil, nil, nil
	closePending := p.pending
	p.pending = make(map[string]*watchPending)
	failure := p.failure
	handler := p.handlers.Unavailable
	p.mu.Unlock()
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	closeFiles(stdin, stdout, stderr)
	for _, pending := range closePending {
		pending.result <- watchOutcome{err: &hub.WatchError{Code: "plugin_unavailable", Message: "Plugin exited before acknowledging the watch.", Unavailable: true, Cause: failure}}
	}
	p.finishOnce.Do(func() { close(p.doneCh) })
	if unexpected {
		p.failOnce.Do(func() {
			if handler != nil {
				handler(failure)
			}
		})
	}
}

func (p *Process) drainStderr(stderr *os.File) {
	reader := bufio.NewReader(stderr)
	var line strings.Builder
	truncated := false
	flush := func() {
		if line.Len() == 0 && !truncated {
			return
		}
		message, sanitizedTruncation := sanitizePluginDiagnostic(line.String())
		p.logger.Warn("plugin_diagnostic", "plugin", p.config.Name, "message", message, "truncated", truncated || sanitizedTruncation)
		line.Reset()
		truncated = false
	}
	for {
		fragment, prefix, err := reader.ReadLine()
		if len(fragment) > 0 {
			remaining := maxPluginDiagnosticBytes - line.Len()
			count := min(remaining, len(fragment))
			if count > 0 {
				_, _ = line.Write(fragment[:count])
			}
			if count < len(fragment) {
				truncated = true
			}
		}
		if !prefix {
			flush()
		}
		if err != nil {
			return
		}
	}
}

func sanitizePluginDiagnostic(value string) (string, bool) {
	var output strings.Builder
	truncated := false
	for len(value) > 0 {
		character, size := utf8.DecodeRuneInString(value)
		if unicode.IsControl(character) {
			character = ' '
		}
		characterBytes := utf8.RuneLen(character)
		if output.Len()+characterBytes > maxPluginDiagnosticBytes {
			truncated = true
			break
		}
		output.WriteRune(character)
		value = value[size:]
	}
	return strings.TrimSpace(output.String()), truncated || len(value) > 0
}

func (p *Process) fail(cause error) {
	p.mu.Lock()
	if p.failed || p.stopping {
		p.mu.Unlock()
		return
	}
	p.failed = true
	p.failure = cause
	job := p.job
	process := p.cmd
	stdin := p.stdin
	handler := p.handlers.Unavailable
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
	}
	if process != nil && process.Process != nil {
		_ = process.Process.Kill()
	}
	p.failOnce.Do(func() {
		if handler != nil {
			handler(cause)
		}
	})
}

func (p *Process) terminate() {
	p.mu.Lock()
	job := p.job
	process := p.cmd
	p.mu.Unlock()
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
	}
	if process != nil && process.Process != nil {
		_ = process.Process.Kill()
	}
}

func (p *Process) removePending(requestID string, expected *watchPending) {
	p.mu.Lock()
	if p.pending[requestID] == expected {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
}

func (p *Process) processRunningLocked() bool {
	return p.started && !p.failed && p.cmd != nil && p.cmd.Process != nil
}

func (p *Process) failureText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		return "unknown process failure"
	}
	return safeText(p.failure.Error(), 256)
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

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func safeText(value string, max int) string {
	var out []rune
	used := 0
	for _, char := range value {
		if unicode.IsControl(char) {
			char = ' '
		}
		width := utf8.RuneLen(char)
		if used+width > max {
			break
		}
		out = append(out, char)
		used += width
	}
	return string(out)
}

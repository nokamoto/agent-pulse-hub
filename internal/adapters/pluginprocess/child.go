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
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/config"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/process"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	application "github.com/nokamoto/agent-pulse-hub/internal/application/pulse"
	domain "github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

const pluginShutdownGrace = 5 * time.Second

type (
	EventSink func(string, domain.Event) bool
	ExitSink  func(string, string)
)

type watchCall struct {
	accepted func() bool
	result   chan application.WatchResult
	ctx      context.Context
	handling bool
}

type Child struct {
	name       string
	command    *exec.Cmd
	job        *process.Job
	stdin      *os.File
	stdout     *os.File
	stderr     *os.File
	logger     *slog.Logger
	eventSink  EventSink
	exitSink   ExitSink
	writeMu    sync.Mutex
	mu         sync.Mutex
	ready      chan struct{}
	readyOnce  sync.Once
	available  bool
	stopping   bool
	pending    map[string]*watchCall
	done       chan struct{}
	doneOnce   sync.Once
	exitOnce   sync.Once
	readerDone chan struct{}
	stderrDone chan struct{}
	stderrLog  *boundedBuffer
}

func Start(ctx context.Context, definition config.Plugin, logger *slog.Logger, eventSink EventSink, exitSink ExitSink) (*Child, error) {
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create plugin stdin pipe: %w", err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		return nil, fmt.Errorf("create plugin stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("create plugin stderr pipe: %w", err)
	}
	job, err := process.NewJob()
	if err != nil {
		closeFiles(stdinReader, stdinWriter, stdoutReader, stdoutWriter, stderrReader, stderrWriter)
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	command := exec.Command(definition.Executable, definition.Args...)
	command.Stdin = stdinReader
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	if err := process.Start(command, job); err != nil {
		_ = job.Close()
		closeFiles(stdinReader, stdinWriter, stdoutReader, stdoutWriter, stderrReader, stderrWriter)
		return nil, fmt.Errorf("start plugin %q: %w", definition.Name, err)
	}
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	child := &Child{
		name:       definition.Name,
		command:    command,
		job:        job,
		stdin:      stdinWriter,
		stdout:     stdoutReader,
		stderr:     stderrReader,
		logger:     logger,
		eventSink:  eventSink,
		exitSink:   exitSink,
		ready:      make(chan struct{}),
		available:  true,
		pending:    make(map[string]*watchCall),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		stderrDone: make(chan struct{}),
		stderrLog:  &boundedBuffer{limit: 4096},
	}
	if exited := process.WaitForExit(command.Process); exited != nil {
		go func() {
			<-exited
			_ = child.job.Close()
		}()
	}
	go child.readLoop()
	go child.readDiagnostics()
	go child.waitLoop()
	readyContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-child.ready:
		if !child.Available() {
			return nil, fmt.Errorf("plugin %q exited during startup", definition.Name)
		}
		return child, nil
	case <-child.done:
		return nil, fmt.Errorf("plugin %q exited before readiness", definition.Name)
	case <-readyContext.Done():
		child.Stop("startup readiness timed out")
		return nil, fmt.Errorf("plugin %q did not become ready within 10 seconds", definition.Name)
	}
}

func (child *Child) Available() bool {
	child.mu.Lock()
	defer child.mu.Unlock()
	return child.available && !child.stopping
}

func (child *Child) Watch(ctx context.Context, request domain.WatchRequest, onAccepted func() bool) application.WatchResult {
	child.mu.Lock()
	if !child.available || child.stopping {
		child.mu.Unlock()
		return application.WatchResult{Err: errors.New("plugin is unavailable")}
	}
	call := &watchCall{accepted: onAccepted, result: make(chan application.WatchResult, 1), ctx: ctx}
	child.pending[request.RequestID] = call
	child.mu.Unlock()

	writeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := child.writeFrame(writeContext, protocol.WatchFrame{
		Version:        protocol.Version,
		Type:           "watch",
		RequestID:      request.RequestID,
		SubscriptionID: request.SubscriptionID,
		WatchArgs:      request.WatchArgs,
	})
	cancel()
	if err != nil {
		if !child.cancelPending(request.RequestID, call) {
			return <-call.result
		}
		child.forceStop()
		return application.WatchResult{Uncertain: true, Err: fmt.Errorf("write watch request: %w", err)}
	}
	select {
	case result := <-call.result:
		return result
	case <-ctx.Done():
		if !child.cancelPending(request.RequestID, call) {
			return <-call.result
		}
		child.forceStop()
		return application.WatchResult{Uncertain: true, Err: fmt.Errorf("watch acknowledgement: %w", ctx.Err())}
	case <-child.done:
		if !child.cancelPending(request.RequestID, call) {
			return <-call.result
		}
		return application.WatchResult{Uncertain: true, Err: errors.New("plugin exited before watch acknowledgement")}
	}
}

func (child *Child) Stop(reason string) {
	child.mu.Lock()
	if child.stopping {
		child.mu.Unlock()
		select {
		case <-child.done:
		case <-time.After(pluginShutdownGrace):
			child.forceStop()
			<-child.done
		}
		return
	}
	child.stopping = true
	available := child.available
	child.mu.Unlock()

	if available {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = child.writeFrame(ctx, protocol.ShutdownFrame{Version: protocol.Version, Type: "shutdown"})
		cancel()
	}
	select {
	case <-child.done:
	case <-time.After(pluginShutdownGrace):
		child.forceStop()
		<-child.done
	}
}

func (child *Child) writeFrame(ctx context.Context, frame any) error {
	line, err := protocol.EncodeLine(frame)
	if err != nil {
		return err
	}
	writeDone := make(chan error, 1)
	go func() {
		child.writeMu.Lock()
		defer child.writeMu.Unlock()
		writeDone <- writeAll(child.stdin, line)
	}()
	select {
	case err := <-writeDone:
		return err
	case <-ctx.Done():
		child.forceStop()
		<-writeDone
		return ctx.Err()
	}
}

func (child *Child) readLoop() {
	defer close(child.readerDone)
	reader := bufio.NewReader(child.stdout)
	seenReady := false
	for {
		data, err := protocol.ReadFrame(reader, domain.MaxFrameBytes)
		if errors.Is(err, io.EOF) {
			child.forceStop()
			child.markUnavailable("plugin stdout closed")
			return
		}
		if err != nil {
			if errors.Is(err, protocol.ErrInvalidFrame) {
				child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "reason", boundedText(err.Error(), 256))
				continue
			}
			child.logger.Error("plugin_protocol_error", "plugin", child.name, "reason", "unsafe_frame", "error", boundedText(err.Error(), 256))
			child.forceStop()
			child.markUnavailable("plugin stdout framing failed")
			return
		}
		frame, err := protocol.DecodePluginFrame(data)
		if err != nil {
			child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "reason", boundedText(err.Error(), 256))
			continue
		}
		if !child.Available() {
			child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "reason", "plugin_unavailable")
			continue
		}
		switch frame.Type {
		case "ready":
			if seenReady {
				child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "reason", "duplicate_ready")
				continue
			}
			seenReady = true
			child.readyOnce.Do(func() { close(child.ready) })
		case "watch_result":
			if !seenReady {
				child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "request_id", frame.RequestID, "reason", "watch_result_before_ready")
				continue
			}
			child.handleWatchResult(frame)
		case "event":
			if !seenReady {
				child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "subscription_id", frame.SubscriptionID, "reason", "event_before_ready")
				continue
			}
			if child.eventSink == nil || !child.eventSink(child.name, domain.Event{SubscriptionID: frame.SubscriptionID, Context: frame.Context}) {
				continue
			}
		default:
			child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "reason", "unsupported_frame")
		}
	}
}

func (child *Child) handleWatchResult(frame protocol.PluginFrame) {
	child.mu.Lock()
	call, exists := child.pending[frame.RequestID]
	if exists {
		delete(child.pending, frame.RequestID)
		if !watchCallExpired(call) {
			call.handling = true
		} else {
			exists = false
		}
	}
	child.mu.Unlock()
	if !exists {
		child.logger.Warn("plugin_frame_rejected", "plugin", child.name, "request_id", frame.RequestID, "reason", "unsolicited_watch_result")
		if call != nil {
			call.result <- application.WatchResult{Uncertain: true, Err: errors.New("plugin acknowledged watch after its deadline")}
			child.forceStop()
		}
		return
	}
	if !frame.Accepted {
		call.result <- application.WatchResult{Err: &application.RegistrationError{Code: "watch_rejected", Message: boundedText(frame.Error, 512)}}
		return
	}
	if call.accepted == nil || !call.accepted() {
		call.result <- application.WatchResult{Uncertain: true, Err: errors.New("registration could not be activated after plugin acceptance")}
		return
	}
	call.result <- application.WatchResult{}
}

func watchCallExpired(call *watchCall) bool {
	if call.ctx.Err() != nil {
		return true
	}
	if deadline, exists := call.ctx.Deadline(); exists && !time.Now().Before(deadline) {
		return true
	}
	return false
}

func (child *Child) readDiagnostics() {
	defer close(child.stderrDone)
	_, _ = io.Copy(child.stderrLog, child.stderr)
	text := boundedText(child.stderrLog.String(), 1024)
	if text != "" {
		child.logger.Info("plugin_diagnostics", "plugin", child.name, "output", sanitize(text))
	}
}

func (child *Child) waitLoop() {
	err := child.command.Wait()
	_ = child.job.Close()
	child.markUnavailable(exitReason(err))
	_ = child.stdin.Close()
	select {
	case <-child.readerDone:
	case <-time.After(pluginShutdownGrace):
		_ = child.stdout.Close()
	}
	select {
	case <-child.stderrDone:
	case <-time.After(time.Second):
		_ = child.stderr.Close()
	}
}

func exitReason(err error) string {
	if err == nil {
		return "plugin process exited"
	}
	return "plugin process exited: " + boundedText(err.Error(), 256)
}

func (child *Child) markUnavailable(reason string) {
	child.exitOnce.Do(func() {
		child.mu.Lock()
		child.available = false
		for requestID, call := range child.pending {
			delete(child.pending, requestID)
			call.result <- application.WatchResult{Uncertain: true, Err: errors.New("plugin exited before watch acceptance")}
		}
		child.mu.Unlock()
		child.doneOnce.Do(func() { close(child.done) })
		if child.exitSink != nil {
			child.exitSink(child.name, reason)
		}
		child.logger.Error("plugin_exit", "plugin", child.name, "reason", sanitize(boundedText(reason, 512)))
	})
}

func (child *Child) forceStop() {
	_ = child.job.Close()
	_ = child.stdin.Close()
}

func (child *Child) cancelPending(requestID string, call *watchCall) bool {
	child.mu.Lock()
	defer child.mu.Unlock()
	if call.handling {
		return false
	}
	if child.pending[requestID] == call {
		delete(child.pending, requestID)
		return true
	}
	return false
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func boundedText(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func sanitize(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' {
			return ' '
		}
		if character == 0x7f {
			return ' '
		}
		return character
	}, value)
	return boundedText(value, 1024)
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	value []byte
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - len(buffer.value)
	if remaining > 0 {
		count := len(data)
		if count > remaining {
			count = remaining
		}
		buffer.value = append(buffer.value, data[:count]...)
	}
	return len(data), nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.value)
}

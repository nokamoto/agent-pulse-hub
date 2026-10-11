//go:build windows

package pluginprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
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

	child      childProcess
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	launcher   processLauncher
	clock      clock
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
		launcher:  systemLauncher{},
		clock:     systemClock{},
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

	child, err := p.launcher.Start(p.config)
	if err != nil {
		return err
	}
	stdin, stdout, stderr := child.Stdin(), child.Stdout(), child.Stderr()
	p.mu.Lock()
	p.child, p.stdin, p.stdout, p.stderr, p.started = child, stdin, stdout, stderr, true
	p.mu.Unlock()
	go p.drainStderr(stderr)
	go p.readStdout(stdout)
	go p.waitProcess(child)
	readiness := p.clock.NewTimer(readyTimeout)
	defer readiness.Stop()
	select {
	case <-p.readyCh:
		return nil
	case <-p.doneCh:
		return fmt.Errorf("plugin exited before readiness: %s", p.failureText())
	case <-readiness.C():
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
	deadline := p.clock.Now().Add(watchTimeout)
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

	timer := p.clock.NewTimer(deadline.Sub(p.clock.Now()))
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.err
	case <-timer.C():
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
	startedAt := p.clock.Now()
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
	remaining := deadline.Sub(p.clock.Now())
	if remaining > 0 {
		timer := p.clock.NewTimer(remaining)
		select {
		case <-p.doneCh:
			timer.Stop()
			return nil
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C():
		}
	}
	p.terminate()
	select {
	case <-p.doneCh:
		return nil
	case <-p.clock.NewTimer(time.Second).C():
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
	timer := p.clock.NewTimer(deadline.Sub(p.clock.Now()))
	defer timer.Stop()
	select {
	case p.writeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin process has exited")
	case <-timer.C():
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
	remaining := deadline.Sub(p.clock.Now())
	if remaining <= 0 {
		return errors.New("plugin write deadline expired")
	}
	timer := p.clock.NewTimer(remaining)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C():
		return errors.New("plugin write deadline expired")
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin process exited while writing")
	}
}

func (p *Process) readStdout(stdout io.Reader) {
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
	unavailable := p.failed || p.stopping
	p.mu.Unlock()
	if unavailable {
		return
	}
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
	if p.clock.Now().After(pending.deadline) {
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

func (p *Process) waitProcess(command childProcess) {
	err := command.Wait()
	p.mu.Lock()
	if err != nil && !p.stopping && !p.failed {
		p.failure = fmt.Errorf("plugin process exited: %w", err)
	}
	unexpected := !p.stopping && !p.failed
	if unexpected && p.failure == nil {
		p.failure = errors.New("plugin process exited")
	}
	if unexpected {
		p.failed = true
	}
	child := p.child
	p.child = nil
	p.stdin, p.stdout, p.stderr = nil, nil, nil
	closePending := p.pending
	p.pending = make(map[string]*watchPending)
	failure := p.failure
	handler := p.handlers.Unavailable
	p.mu.Unlock()
	if child != nil {
		child.Close()
	}
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

func (p *Process) drainStderr(stderr io.Reader) {
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
	child := p.child

	stdin := p.stdin
	handler := p.handlers.Unavailable
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if child != nil {
		child.Terminate()
	}
	p.failOnce.Do(func() {
		if handler != nil {
			handler(cause)
		}
	})
}

func (p *Process) terminate() {
	p.mu.Lock()
	child := p.child
	p.mu.Unlock()
	if child != nil {
		child.Terminate()
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
	if !p.started {
		return false
	}
	select {
	case <-p.doneCh:
		return false
	default:
		return true
	}
}

func (p *Process) failureText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		return "unknown process failure"
	}
	return safeText(p.failure.Error(), 256)
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

package pluginprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	readyTimeout             = 10 * time.Second
	watchTimeout             = 10 * time.Second
	controllerShutdownMethod = "/plugin.GRPCController/Shutdown"
)

type Config struct {
	Name       string
	Executable string
	Args       []string
}

// Process owns one go-plugin client and one application stream. It cannot be restarted.
type Process struct {
	config Config
	logger *slog.Logger

	mu         sync.Mutex
	callbackMu sync.Mutex
	client     *plugin.Client
	stream     pluginprotocol.SessionClient
	cancel     context.CancelFunc
	handlers   hub.PluginHandlers
	started    bool
	ready      bool
	stopping   bool
	failed     bool
	failure    error
	deadline   time.Time
	pending    map[string]*watchPending

	writeGate   chan struct{}
	readyCh     chan struct{}
	doneCh      chan struct{}
	setupDone   chan struct{}
	killOnce    sync.Once
	diagnostics []*diagnosticWriter
}

type watchPending struct {
	deadline time.Time
	activate func() error
	result   chan error
	sent     bool
}

var _ hub.PluginPort = (*Process)(nil)

func New(config Config, logger *slog.Logger) *Process {
	if logger == nil {
		logger = slog.Default()
	}
	config.Args = append([]string(nil), config.Args...)
	return &Process{
		config:    config,
		logger:    logger,
		pending:   make(map[string]*watchPending),
		writeGate: make(chan struct{}, 1),
		readyCh:   make(chan struct{}),
		doneCh:    make(chan struct{}),
		setupDone: make(chan struct{}),
	}
}

func (p *Process) Start(ctx context.Context, handlers hub.PluginHandlers) error {
	readiness, cancelReadiness := context.WithTimeout(ctx, readyTimeout)
	defer cancelReadiness()
	deadline, _ := readiness.Deadline()

	p.mu.Lock()
	if p.started || p.stopping {
		p.mu.Unlock()
		return errors.New("plugin process cannot be started again")
	}
	if err := readiness.Err(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.started = true
	p.handlers = handlers
	p.deadline = deadline
	sessionCtx, cancelSession := context.WithCancel(context.WithoutCancel(ctx))
	p.cancel = cancelSession
	stderr := p.newDiagnosticWriter("stderr")
	stdout := p.newDiagnosticWriter("stdout")
	syncStderr := p.newDiagnosticWriter("synced_stderr")
	p.diagnostics = []*diagnosticWriter{stderr, stdout, syncStderr}
	p.client = plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins: plugin.PluginSet{PluginKey: &FrameworkPlugin{
			onExit: func() { p.fail(errors.New("plugin process exited")) },
		}},
		Cmd:                 exec.Command(p.config.Executable, p.config.Args...),
		AllowedProtocols:    []plugin.Protocol{plugin.ProtocolGRPC},
		GRPCDialOptions:     sessionLifetimeOptions(sessionCtx),
		StartTimeout:        max(time.Until(deadline), time.Nanosecond),
		Logger:              hclog.NewNullLogger(),
		Stderr:              stderr,
		SyncStdout:          stdout,
		SyncStderr:          syncStderr,
		PluginLogBufferSize: maxPluginDiagnosticBytes,
	})
	client := p.client
	p.mu.Unlock()

	go p.openSession(sessionCtx, client)
	select {
	case <-p.readyCh:
		p.mu.Lock()
		available := p.ready && !p.failed && !p.stopping
		failure := p.failure
		p.mu.Unlock()
		if available {
			return nil
		}
		return unavailable("Plugin became unavailable during startup.", failure)
	case <-p.doneCh:
		return unavailable("Plugin exited before readiness.", p.failureCause())
	case <-readiness.Done():
		cause := readiness.Err()
		p.fail(cause)
		return fmt.Errorf("plugin readiness deadline interrupted: %w", cause)
	}
}

func (p *Process) openSession(ctx context.Context, client *plugin.Client) {
	stream, err := p.connectSession(ctx, client)
	close(p.setupDone)
	if err != nil {
		p.fail(err)
		return
	}
	p.mu.Lock()
	if p.failed || p.stopping {
		p.mu.Unlock()
		return
	}
	p.stream = stream
	p.mu.Unlock()
	go p.receive(stream)
}

func (p *Process) connectSession(ctx context.Context, client *plugin.Client) (pluginprotocol.SessionClient, error) {
	rpcClient, err := client.Client()
	if err != nil {
		return nil, fmt.Errorf("start plugin framework client: %w", err)
	}
	value, err := rpcClient.Dispense(PluginKey)
	if err != nil {
		return nil, fmt.Errorf("dispense plugin service: %w", err)
	}
	connection, ok := value.(grpc.ClientConnInterface)
	if !ok {
		return nil, errors.New("plugin framework returned an invalid connection")
	}
	stream, err := pluginprotocol.OpenSession(ctx, connection, pluginprotocol.ClientCallOptions()...)
	if err != nil {
		return nil, fmt.Errorf("open plugin session: %w", err)
	}
	return stream, nil
}

func (p *Process) Watch(ctx context.Context, request hub.WatchRequest, activate func() error) error {
	watchCtx, cancel := context.WithTimeout(ctx, watchTimeout)
	defer cancel()
	deadline, _ := watchCtx.Deadline()
	payload, err := pluginprotocol.Encode(pluginprotocol.Frame{
		Type:           "watch",
		RequestID:      request.RequestID,
		SubscriptionID: string(request.SubscriptionID),
		WatchArgs:      request.Arguments,
	})
	if err != nil {
		if status.Code(err) == codes.ResourceExhausted {
			return &hub.WatchError{Code: "resource_exhausted", Message: "Watch request exceeds the protocol payload limit."}
		}
		return &hub.WatchError{Code: "invalid_request", Message: "Watch request is invalid.", Cause: err}
	}
	if activate == nil {
		return &hub.WatchError{Code: "invalid_request", Message: "Watch activation is required."}
	}
	pending := &watchPending{deadline: deadline, activate: activate, result: make(chan error, 1)}
	p.mu.Lock()
	if !p.ready || p.failed || p.stopping || p.stream == nil {
		p.mu.Unlock()
		return unavailable("Plugin is unavailable.", p.failureCause())
	}
	if p.pending[request.RequestID] != nil {
		p.mu.Unlock()
		return &hub.WatchError{Code: "invalid_request", Message: "Watch request identifier is already pending."}
	}
	p.pending[request.RequestID] = pending
	p.mu.Unlock()

	if err := p.acquireWrite(watchCtx); err != nil {
		return p.interruptWatch(request.RequestID, pending, err)
	}
	p.mu.Lock()
	stream := p.stream
	available := !p.failed && !p.stopping && watchCtx.Err() == nil
	if available {
		pending.sent = true
	}
	p.mu.Unlock()
	if !available {
		p.releaseWrite()
		cause := watchCtx.Err()
		if cause == nil {
			cause = p.failureCause()
		}
		return p.interruptWatch(request.RequestID, pending, cause)
	}
	if err := p.sendUntil(watchCtx, stream, payload); err != nil {
		// Mark the instance unavailable before another sender acquires the gate.
		p.fail(fmt.Errorf("send plugin watch: %w", err))
		p.releaseWrite()
		return unavailable("Plugin could not receive the watch request.", err)
	}
	p.releaseWrite()

	select {
	case result := <-pending.result:
		return result
	case <-watchCtx.Done():
		return p.interruptWatch(request.RequestID, pending, watchCtx.Err())
	case <-p.doneCh:
		return unavailable("Plugin exited before acknowledging the watch.", p.failureCause())
	}
}

func (p *Process) acquireWrite(ctx context.Context) error {
	select {
	case p.writeGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin instance is unavailable")
	}
}

func (p *Process) releaseWrite() { <-p.writeGate }

func (p *Process) sendUntil(ctx context.Context, stream pluginprotocol.SessionClient, payload []byte) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(payload) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-p.doneCh:
		return errors.New("plugin instance became unavailable while sending")
	}
}

func (p *Process) interruptWatch(requestID string, pending *watchPending, cause error) error {
	p.mu.Lock()
	stillPending := p.pending[requestID] == pending
	if stillPending {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
	if stillPending {
		p.fail(cause)
		return unavailable("Plugin did not acknowledge the watch before interruption.", cause)
	}
	// The receive loop already claimed a timely acknowledgement. Its activation
	// must complete before the caller can report successful registration.
	select {
	case result := <-pending.result:
		return result
	case <-p.doneCh:
		return unavailable("Plugin exited before acknowledging the watch.", p.failureCause())
	}
}

func (p *Process) receive(stream pluginprotocol.SessionClient) {
	for {
		payload, err := stream.Recv()
		if err != nil {
			p.fail(fmt.Errorf("plugin session ended: %w", err))
			return
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		if err != nil {
			if status.Code(err) == codes.ResourceExhausted {
				p.fail(fmt.Errorf("plugin payload exceeds limit: %w", err))
				return
			}
			p.rejectFrame(err.Error())
			continue
		}
		p.handleFrame(frame)
	}
}

func (p *Process) handleFrame(frame pluginprotocol.Frame) {
	p.callbackMu.Lock()
	p.mu.Lock()
	if p.failed || p.stopping {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		return
	}
	if frame.Type == "ready" {
		if p.ready {
			p.mu.Unlock()
			p.callbackMu.Unlock()
			p.rejectFrame("duplicate readiness")
			return
		}
		if !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
			p.mu.Unlock()
			p.callbackMu.Unlock()
			p.fail(errors.New("plugin readiness deadline expired"))
			return
		}
		p.ready = true
		handler := p.handlers.Ready
		p.mu.Unlock()
		if handler != nil {
			handler()
		}
		close(p.readyCh)
		p.callbackMu.Unlock()
		return
	}
	if !p.ready {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		p.rejectFrame("frame received before readiness")
		return
	}
	handler := p.handlers.Event
	p.mu.Unlock()
	switch frame.Type {
	case "event":
		if handler != nil {
			handler(frame.SubscriptionID, frame.Context)
		}
		p.callbackMu.Unlock()
	case "watch_result":
		p.handleWatchResult(frame)
	default:
		p.callbackMu.Unlock()
		p.rejectFrame("invalid direction")
	}
}

// handleWatchResult owns callbackMu and releases it before returning.
func (p *Process) handleWatchResult(frame pluginprotocol.Frame) {
	p.mu.Lock()
	pending := p.pending[frame.RequestID]
	if pending == nil || !pending.sent {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		p.logger.Warn("plugin_watch_result_rejected", "plugin", p.config.Name,
			"request_id", safeText(frame.RequestID, 256), "reason", "unknown, early, or duplicate request")
		return
	}
	delete(p.pending, frame.RequestID)
	if !time.Now().Before(pending.deadline) {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		cause := errors.New("plugin watch acknowledgement deadline expired")
		p.fail(cause)
		pending.result <- unavailable("Plugin did not acknowledge the watch in time.", cause)
		return
	}
	p.mu.Unlock()
	if frame.Accepted == nil || !*frame.Accepted {
		pending.result <- &hub.WatchError{Code: "watch_rejected", Message: "Plugin rejected the watch arguments."}
		p.callbackMu.Unlock()
		return
	}
	if err := pending.activate(); err != nil {
		p.callbackMu.Unlock()
		p.fail(fmt.Errorf("watch acceptance could not be committed: %w", err))
		pending.result <- unavailable("Plugin watch acceptance was uncertain.", err)
		return
	}
	pending.result <- nil
	p.callbackMu.Unlock()
}

func (p *Process) fail(cause error) {
	if cause == nil {
		cause = errors.New("plugin instance is unavailable")
	}
	p.mu.Lock()
	if p.failed || p.stopping {
		p.mu.Unlock()
		return
	}
	p.failed = true
	p.failure = cause
	cancel := p.cancel
	handler := p.handlers.Unavailable
	pending := p.finishLocked()
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.completeUnavailable(pending, cause)
	go p.kill()
	p.logger.Warn("plugin_instance_invalidated", "plugin", p.config.Name, "reason", safeText(cause.Error(), 256))
	p.callbackMu.Lock()
	if handler != nil {
		handler(cause)
	}
	p.callbackMu.Unlock()
}

func (p *Process) Stop(_ context.Context) error {
	p.mu.Lock()
	if !p.stopping {
		p.stopping = true
		if p.failure == nil {
			p.failure = errors.New("plugin instance stopped")
		}
	}
	cancel := p.cancel
	pending := p.finishLocked()
	failure := p.failure
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.completeUnavailable(pending, failure)
	p.kill()
	return nil
}

// finishLocked closes the instance signal once and transfers pending ownership.
func (p *Process) finishLocked() map[string]*watchPending {
	select {
	case <-p.doneCh:
	default:
		close(p.doneCh)
	}
	pending := p.pending
	p.pending = make(map[string]*watchPending)
	return pending
}

func (p *Process) completeUnavailable(pending map[string]*watchPending, cause error) {
	for _, watch := range pending {
		watch.result <- unavailable("Plugin became unavailable before acknowledging the watch.", cause)
	}
}

func (p *Process) kill() {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		return
	}
	p.killOnce.Do(func() {
		// Kill must not race a framework Start that has not yet created its runner.
		<-p.setupDone
		client.Kill()
		for _, diagnostic := range p.diagnostics {
			diagnostic.flush()
		}
		cause := p.failureCause()
		reason := ""
		if cause != nil {
			reason = safeText(cause.Error(), 256)
		}
		p.logger.Info("plugin_process_exit", "plugin", p.config.Name, "exited", client.Exited(), "reason", reason)
	})
}

func (p *Process) failureCause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

func (p *Process) rejectFrame(reason string) {
	p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "reason", safeText(reason, 256))
}

func unavailable(message string, cause error) error {
	return &hub.WatchError{Code: "plugin_unavailable", Message: message, Unavailable: true, Cause: cause}
}

// Framework RPC setup also observes cancellation of the owned session. Without
// this, a peer that announces a socket but stalls its framework gRPC service
// could leave Client.Client blocked after the readiness deadline.
func sessionLifetimeOptions(lifetime context.Context) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithUnaryInterceptor(sessionLifetimeUnaryInterceptor(lifetime)),
		grpc.WithStreamInterceptor(func(ctx context.Context, description *grpc.StreamDesc, connection *grpc.ClientConn, method string, create grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
			callCtx, cancel := lifetimeContext(ctx, lifetime)
			stream, err := create(callCtx, description, connection, method, options...)
			if err != nil {
				cancel()
			}
			return stream, err
		}),
	}
}

func sessionLifetimeUnaryInterceptor(lifetime context.Context) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, request, response any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if method == controllerShutdownMethod {
			return invoke(ctx, method, request, response, connection, options...)
		}
		callCtx, cancel := lifetimeContext(ctx, lifetime)
		defer cancel()
		return invoke(callCtx, method, request, response, connection, options...)
	}
}

func lifetimeContext(ctx, lifetime context.Context) (context.Context, context.CancelFunc) {
	callCtx, cancel := context.WithCancel(ctx)
	if lifetime.Err() != nil {
		cancel()
		return callCtx, cancel
	}
	detach := context.AfterFunc(lifetime, cancel)
	context.AfterFunc(callCtx, func() { detach() })
	return callCtx, cancel
}

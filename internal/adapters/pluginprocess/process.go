package pluginprocess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	readyTimeout             = 10 * time.Second
	registrationTimeout      = 10 * time.Second
	deliveryTimeout          = 30 * time.Second
	controllerShutdownMethod = "/plugin.GRPCController/Shutdown"
)

type Config struct {
	Name                 string
	Role                 domain.PluginRole
	Executable           string
	Args                 []string
	RuntimeStateMaxBytes uint64
}

type pendingRequest struct {
	stream         *checkpointTransfer
	restoreEnd     bool
	kind           string
	deadline       time.Time
	activate       func() error
	watchID        string
	watch          *watchState
	subscriptionID string
	result         chan requestResult
	sent           bool
	claimed        bool
	cancel         context.CancelFunc
}

type watchState struct {
	orderMu            sync.Mutex
	active             bool
	stateful           bool
	activationRequired bool
	activationClosed   bool
	granted            bool
	generation         string
	stagedGeneration   string
	lastEvent          uint64
	pending            map[string]bool
	notices            map[string]*noticeSend
	quiesced           bool
	cancelling         bool
	cancelSent         bool
}

type noticeSend struct {
	done chan struct{}
	err  error
}

type requestResult struct {
	frame pluginprotocol.Frame
	err   error
}

// Process owns one configured go-plugin child and its role-specific stream.
// It cannot be restarted.
type Process struct {
	notificationCorrelations map[string]*notificationCorrelation
	config                   Config
	logger                   *slog.Logger

	mu                  sync.Mutex
	callbackMu          sync.Mutex
	client              *plugin.Client
	stream              pluginprotocol.SessionClient
	cancel              context.CancelFunc
	handlers            hub.PluginHandlers
	started             bool
	ready               bool
	capabilities        map[string]bool
	watches             map[string]*watchState
	runtimePendingCount int
	stopping            bool
	failed              bool
	failure             error
	deadline            time.Time
	pending             map[string]*pendingRequest
	streamFinished      map[string]*checkpointTransfer
	streamFinishedWatch map[string]string

	writeGate   chan struct{}
	readyCh     chan struct{}
	doneCh      chan struct{}
	setupDone   chan struct{}
	killOnce    sync.Once
	terminated  bool
	diagnostics []*diagnosticWriter
}

var _ hub.PluginPort = (*Process)(nil)

func New(config Config, logger *slog.Logger) *Process {
	if logger == nil {
		logger = slog.Default()
	}
	config.Args = append([]string(nil), config.Args...)
	return &Process{
		config:              config,
		logger:              logger,
		pending:             make(map[string]*pendingRequest),
		streamFinished:      make(map[string]*checkpointTransfer),
		streamFinishedWatch: make(map[string]string),
		capabilities:        make(map[string]bool),
		watches:             make(map[string]*watchState),
		writeGate:           make(chan struct{}, 1),
		readyCh:             make(chan struct{}),
		doneCh:              make(chan struct{}),
		setupDone:           make(chan struct{}),
	}
}

func (p *Process) Start(ctx context.Context, handlers hub.PluginHandlers) error {
	if p.config.Name == "" || (p.config.Role != domain.InputRole && p.config.Role != domain.OutputRole) {
		return errors.New("plugin name and valid role are required")
	}
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
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins: plugin.PluginSet{PluginKey(string(p.config.Role)): &FrameworkPlugin{
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
	p.client = client
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
	value, err := rpcClient.Dispense(PluginKey(string(p.config.Role)))
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

func (p *Process) ValidateDestination(ctx context.Context, request hub.DestinationRequest) error {
	if p.config.Role != domain.OutputRole {
		return &hub.DestinationError{Code: "invalid_role", Message: "Destination validation requires an output plugin."}
	}
	frame, err := p.request(ctx, pluginprotocol.Frame{
		Type: "validate_destination", RequestID: request.RequestID, DestinationArgs: request.Arguments,
	}, "destination_result", registrationTimeout, nil)
	if err != nil {
		return &hub.DestinationError{Code: "plugin_unavailable", Message: "Output plugin could not validate the destination.", Unavailable: isUnavailable(err), Cause: err}
	}
	if !*frame.Accepted {
		return &hub.DestinationError{Code: "destination_rejected", Message: "Output plugin rejected the destination: " + safeText(frame.Error, 256)}
	}
	return nil
}

func (p *Process) Watch(ctx context.Context, request hub.WatchRequest, activate func() error) error {
	p.mu.Lock()
	stream := p.capabilities[pluginprotocol.RuntimeStateStreamCapability]
	p.mu.Unlock()
	if stream {
		return p.WatchStream(ctx, request, request.Source, activate)
	}
	if p.config.Role != domain.InputRole {
		return &hub.WatchError{Code: "invalid_role", Message: "Watch requires an input plugin."}
	}
	if activate == nil {
		return &hub.WatchError{Code: "invalid_request", Message: "Watch activation is required."}
	}
	p.mu.Lock()
	activationRequired := p.capabilities[pluginprotocol.RuntimeStateV2Capability]
	participates := p.capabilities[pluginprotocol.RuntimeStateCapability] || activationRequired
	p.mu.Unlock()
	if request.RuntimeState != participates {
		return &hub.WatchError{Code: "invalid_request", Message: "Watch runtime-state mode does not match the input capability."}
	}
	frameType := "watch"
	if request.RuntimeState {
		frameType = "state_watch"
	}
	watch := &watchState{
		stateful: request.RuntimeState, pending: make(map[string]bool),
		activationRequired: activationRequired,
		notices:            make(map[string]*noticeSend),
	}
	frame, err := p.requestWithWatch(ctx, pluginprotocol.Frame{
		Type: frameType, RequestID: request.RequestID, SubscriptionID: string(request.SubscriptionID), WatchArgs: request.Arguments, State: request.State,
	}, "watch_result", registrationTimeout, activate, string(request.SubscriptionID), watch)
	if err != nil {
		var operationErr *hub.WatchError
		if errors.As(err, &operationErr) {
			return err
		}
		return &hub.WatchError{Code: "plugin_unavailable", Message: "Input plugin could not confirm the watch.", Unavailable: isUnavailable(err), Cause: err}
	}
	if !*frame.Accepted {
		return &hub.WatchError{Code: "watch_rejected", Message: "Input plugin rejected the watch: " + safeText(frame.Error, 256)}
	}
	return nil
}

// ActivateWatch sends a one-way v2 grant. Its caller supplies the shared batch
// deadline, so waiting for the serialized stream consumes that same budget.
func (p *Process) ActivateWatch(ctx context.Context, id domain.SubscriptionID) error {
	return p.activateGeneration(ctx, id, "")
}

func (p *Process) ActivateGeneration(ctx context.Context, id domain.SubscriptionID, generation string) error {
	if generation == "" {
		return &hub.RuntimeError{Code: "invalid_watch", Message: "a notification generation is required"}
	}
	return p.activateGeneration(ctx, id, generation)
}

func (p *Process) activateGeneration(ctx context.Context, id domain.SubscriptionID, generation string) error {
	payload, err := pluginprotocol.Encode(pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: string(id), Generation: generation})
	if err != nil {
		return &hub.RuntimeError{Code: "invalid_watch", Message: "activation grant is invalid", Cause: err}
	}
	if err := p.acquireWrite(ctx); err != nil {
		p.mu.Lock()
		watch := p.watches[string(id)]
		fenced := watch == nil || watch.cancelling
		p.mu.Unlock()
		if fenced {
			return &hub.RuntimeError{Code: "invalid_watch", Message: "activation watch was cancelled before issuance"}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			p.fail(err)
		}
		return &hub.RuntimeError{Code: "activation_send", Message: "activation grant could not enter the stream", Unavailable: !errors.Is(err, context.Canceled), Cause: err}
	}
	p.mu.Lock()
	watch := p.watches[string(id)]
	if watch == nil || watch.cancelling {
		p.mu.Unlock()
		p.releaseWrite()
		return &hub.RuntimeError{Code: "invalid_watch", Message: "activation watch was cancelled before issuance"}
	}
	if !p.ready || p.failed || p.stopping || p.stream == nil || ctx.Err() != nil {
		cause := ctx.Err()
		p.mu.Unlock()
		p.releaseWrite()
		if errors.Is(cause, context.DeadlineExceeded) {
			p.fail(cause)
		}
		return &hub.RuntimeError{Code: "activation_send", Message: "input plugin is unavailable before activation", Unavailable: !errors.Is(cause, context.Canceled), Cause: cause}
	}
	if watch == nil || !watch.active || !watch.activationRequired || watch.cancelling {
		p.mu.Unlock()
		p.releaseWrite()
		return &hub.RuntimeError{Code: "invalid_watch", Message: "activation requires an accepted runtime-state v2 watch"}
	}
	rewait := p.capabilities[pluginprotocol.RewaitCapability]
	if rewait != (generation != "") || (rewait && watch.generation != "" && generation != watch.generation && generation != watch.stagedGeneration) {
		p.mu.Unlock()
		p.releaseWrite()
		return &hub.RuntimeError{Code: "invalid_watch", Message: "activation generation has no matching preparation"}
	}
	if (watch.granted && (!rewait || generation == watch.generation)) || watch.activationClosed || watch.quiesced {
		p.mu.Unlock()
		p.releaseWrite()
		return nil
	}
	// A source may respond before Send returns. Install admission before
	// handing the grant to gRPC; a failed send invalidates this whole Session.
	watch.granted = true
	watch.generation = generation
	watch.stagedGeneration = ""
	// FenceWatch and issuance share p.mu. Starting the ordered sender while this
	// lock is held prevents an admitted but still-unsent grant from escaping a
	// fence between the final watch/context checks and sender issuance.
	stream := p.stream
	sendResult := make(chan error, 1)
	go func() { sendResult <- stream.Send(payload) }()
	p.mu.Unlock()
	if sendErr, pendingSend := p.awaitSend(ctx, sendResult, nil); sendErr != nil {
		if !pendingSend {
			p.releaseWrite()
		}
		if !errors.Is(sendErr, context.Canceled) {
			p.fail(fmt.Errorf("send activation grant: %w", sendErr))
		}
		return &hub.RuntimeError{Code: "activation_send", Message: "activation grant could not be sent", Unavailable: !errors.Is(sendErr, context.Canceled), Cause: sendErr}
	}
	p.releaseWrite()
	return nil
}

func (p *Process) ResumeWatch(ctx context.Context, request hub.ResumeWatchRequest) error {
	p.mu.Lock()
	watch := p.watches[string(request.SubscriptionID)]
	if !p.capabilities[pluginprotocol.RewaitCapability] || watch == nil || !watch.active || watch.quiesced || watch.activationClosed || watch.cancelling {
		p.mu.Unlock()
		return &hub.RuntimeError{Code: "rewait_unsupported", Message: "input watch is unavailable for preparation"}
	}
	p.mu.Unlock()
	frame, err := p.request(ctx, pluginprotocol.Frame{Type: "resume_watch", RequestID: request.RequestID, SubscriptionID: string(request.SubscriptionID), Generation: request.Generation, ConfirmationPosition: request.ConfirmationPosition}, "resume_result", 10*time.Second, nil)
	if err != nil {
		return &hub.RuntimeError{Code: "resume_failed", Message: "rewait preparation could not be confirmed", Unavailable: isUnavailable(err), Cause: err}
	}
	if frame.Accepted == nil || !*frame.Accepted {
		return &hub.PositionRejectedError{}
	}
	p.mu.Lock()
	if p.watches[string(request.SubscriptionID)] == watch && !watch.cancelling && !watch.quiesced && !watch.activationClosed {
		watch.stagedGeneration = request.Generation
	} else {
		p.mu.Unlock()
		return &hub.RuntimeError{Code: "resume_failed", Message: "watch was cancelled during preparation"}
	}
	p.mu.Unlock()
	return nil
}

func (p *Process) NotifyDelivery(ctx context.Context, notice hub.RuntimeNotice) error {
	p.mu.Lock()
	watch := p.watches[string(notice.SubscriptionID)]
	p.mu.Unlock()
	if watch == nil || !watch.stateful {
		return &hub.RuntimeError{Code: "invalid_watch", Message: "delivery notice has no participating watch"}
	}
	watch.orderMu.Lock()
	orderLocked := true
	unlockOrder := func() {
		if orderLocked {
			orderLocked = false
			watch.orderMu.Unlock()
		}
	}
	defer unlockOrder()
	err := p.notifyDeliveryLocked(ctx, watch, notice)
	unlockOrder()
	return err
}

func (p *Process) Checkpoint(ctx context.Context, request hub.RuntimeCheckpointRequest) (*jsonvalue.Value, error) {
	p.mu.Lock()
	watch := p.watches[string(request.SubscriptionID)]
	p.mu.Unlock()
	if watch == nil || !watch.stateful {
		return nil, &hub.RuntimeError{Code: "invalid_watch", Message: "checkpoint has no participating watch"}
	}
	watch.orderMu.Lock()
	orderLocked := true
	unlockOrder := func() {
		if orderLocked {
			orderLocked = false
			watch.orderMu.Unlock()
		}
	}
	defer unlockOrder()
	p.mu.Lock()
	if watch.quiesced || watch.cancelling || p.watches[string(request.SubscriptionID)] != watch {
		p.mu.Unlock()
		return nil, &hub.RuntimeError{Code: "invalid_watch", Message: "participating watch is already quiescent"}
	}
	p.mu.Unlock()
	requestID, err := randomUUID()
	if err != nil {
		return nil, &hub.RuntimeError{Code: "request_id", Message: "checkpoint request identifier could not be created", Cause: err}
	}
	frame, err := p.requestWithSent(ctx, pluginprotocol.Frame{
		Type: "checkpoint", RequestID: requestID, SubscriptionID: string(request.SubscriptionID), Quiesce: request.Quiesce,
	}, "checkpoint_result", registrationTimeout, func() {
		if request.Quiesce && watch.activationRequired {
			p.mu.Lock()
			watch.activationClosed = true
			p.mu.Unlock()
		}
		unlockOrder()
	})
	if err != nil {
		if request.Quiesce && requestWasSent(err) {
			p.mu.Lock()
			watch.quiesced = true
			p.mu.Unlock()
		}
		runtimeErr := &hub.RuntimeError{Code: "checkpoint_failed", Message: "input checkpoint did not complete before its deadline", Unavailable: isUnavailable(err), Cause: err}
		unlockOrder()
		return nil, runtimeErr
	}
	if request.Quiesce {
		p.mu.Lock()
		watch.quiesced = true
		p.mu.Unlock()
	}
	if frame.Accepted == nil {
		return nil, &hub.RuntimeError{Code: "checkpoint_failed", Message: "checkpoint result is invalid"}
	}
	if !*frame.Accepted {
		return nil, &hub.RuntimeError{Code: "checkpoint_rejected", Message: safeText(frame.Error, 256)}
	}
	return frame.State, nil
}

func (p *Process) Deliver(ctx context.Context, event domain.Event) hub.DeliveryResult {
	return p.DeliverGuarded(ctx, event, nil)
}

// DeliverGuarded lets the owner serialize its cancellation fence and issuance.
// The callback invokes issue while holding its lifecycle lock; issue only starts
// the ordered sender and never waits for the stream or its response.
func (p *Process) DeliverGuarded(ctx context.Context, event domain.Event, admit func(issue func()) bool) hub.DeliveryResult {
	if p.config.Role != domain.OutputRole {
		return hub.DeliveryResult{Outcome: domain.DeliveryFailed, Detail: "delivery requires an output plugin"}
	}
	requestID, err := randomUUID()
	if err != nil {
		return hub.DeliveryResult{Outcome: domain.DeliveryFailed, Detail: "delivery request identifier could not be created"}
	}
	confirmed, err := p.requestInternal(ctx, pluginprotocol.Frame{
		Type: "deliver", RequestID: requestID, DeliveryID: string(event.ID), InputPlugin: event.InputPlugin,
		SubscriptionID: string(event.SubscriptionID), DestinationArgs: event.DestinationArgs,
		Context: event.Context, ExternalData: boolPointer(true), PermissionNotice: pluginprotocol.PermissionNotice,
	}, "delivery_result", deliveryTimeout, nil, "", nil, nil, admit)
	if err != nil {
		var deliveryErr *requestError
		if errors.As(err, &deliveryErr) && deliveryErr.sent {
			return hub.DeliveryResult{Outcome: domain.DeliveryUnknown, Detail: "output did not confirm delivery before the deadline"}
		}
		return hub.DeliveryResult{Outcome: domain.DeliveryFailed, Detail: "output did not receive the delivery request"}
	}
	return hub.DeliveryResult{Outcome: domain.DeliveryOutcome(confirmed.Status), Detail: confirmed.Error}
}

// request serializes Session.Send calls while allowing results for concurrent
// registration requests to arrive in any order. Its timeout includes gate wait,
// send and result wait.
func (p *Process) request(parent context.Context, frame pluginprotocol.Frame, resultType string, timeout time.Duration, activate func() error) (pluginprotocol.Frame, error) {
	return p.requestInternal(parent, frame, resultType, timeout, activate, "", nil, nil, nil)
}

func (p *Process) requestWithWatch(parent context.Context, frame pluginprotocol.Frame, resultType string, timeout time.Duration, activate func() error, watchID string, watch *watchState) (pluginprotocol.Frame, error) {
	return p.requestInternal(parent, frame, resultType, timeout, activate, watchID, watch, nil, nil)
}

func (p *Process) requestWithSent(parent context.Context, frame pluginprotocol.Frame, resultType string, timeout time.Duration, onSent func()) (pluginprotocol.Frame, error) {
	return p.requestInternal(parent, frame, resultType, timeout, nil, "", nil, onSent, nil)
}

func (p *Process) requestInternal(parent context.Context, frame pluginprotocol.Frame, resultType string, timeout time.Duration, activate func() error, watchID string, watch *watchState, onSent func(), admit func(func()) bool) (pluginprotocol.Frame, error) {
	return p.requestExtended(parent, frame, resultType, timeout, activate, watchID, watch, onSent, nil, nil, admit)
}

func (p *Process) requestExtended(parent context.Context, frame pluginprotocol.Frame, resultType string, timeout time.Duration, activate func() error, watchID string, watch *watchState, onSent func(), transfer *checkpointTransfer, afterSend func(context.Context, *pendingRequest) error, admit func(func()) bool) (pluginprotocol.Frame, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return pluginprotocol.Frame{}, &requestError{message: "request exceeds the protocol limit", cause: err}
	}
	pending := &pendingRequest{kind: resultType, deadline: deadline, activate: activate, watchID: watchID, watch: watch, result: make(chan requestResult, 1), stream: transfer, restoreEnd: afterSend == nil, subscriptionID: frame.SubscriptionID, cancel: cancel}
	if transfer != nil {
		transfer.ctx = ctx
	}
	p.mu.Lock()
	if !p.ready || p.failed || p.stopping || p.stream == nil {
		cause := p.failure
		p.mu.Unlock()
		return pluginprotocol.Frame{}, &requestError{message: "plugin is unavailable", cause: cause, unavailable: true}
	}
	if target := p.watches[frame.SubscriptionID]; target != nil && target.cancelling && (frame.Type == "checkpoint" || frame.Type == "resume_watch") {
		p.mu.Unlock()
		return pluginprotocol.Frame{}, &requestError{message: "watch cancellation closed request admission"}
	}
	if _, duplicate := p.pending[frame.RequestID]; duplicate || frame.RequestID == "" {
		p.mu.Unlock()
		return pluginprotocol.Frame{}, &requestError{message: "request identifier is empty or already pending"}
	}
	if correlation := p.notificationCorrelations[frame.RequestID]; correlation != nil {
		correlation.deadline = deadline
	}
	p.pending[frame.RequestID] = pending
	p.mu.Unlock()

	if err := p.acquireWrite(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			if resultType != "checkpoint_result" && resultType != "cancel_result" {
				p.fail(err)
			}
			p.removePending(frame.RequestID, pending)
			return pluginprotocol.Frame{}, &requestError{message: "request deadline expired before send", cause: err, unavailable: resultType != "checkpoint_result"}
		}
		p.removePending(frame.RequestID, pending)
		return pluginprotocol.Frame{}, &requestError{message: "plugin became unavailable before send", cause: err, unavailable: resultType != "checkpoint_result"}
	}
	p.mu.Lock()
	stream := p.stream
	available := !p.failed && !p.stopping && ctx.Err() == nil && p.pending[frame.RequestID] == pending
	fenced := false
	if target := p.watches[frame.SubscriptionID]; target != nil && target.cancelling && (frame.Type == "checkpoint" || frame.Type == "resume_watch") {
		available = false
		fenced = true
	}
	p.mu.Unlock()
	if !available {
		p.releaseWrite()
		p.removePending(frame.RequestID, pending)
		cause := ctx.Err()
		if cause == nil {
			cause = p.failureCause()
		}
		return pluginprotocol.Frame{}, &requestError{message: "plugin became unavailable before send", cause: cause, unavailable: !fenced && !errors.Is(cause, context.Canceled)}
	}
	sendResult := make(chan error, 1)
	issued := false
	issue := func() {
		if issued {
			return
		}
		p.mu.Lock()
		if p.pending[frame.RequestID] != pending || ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		issued = true
		pending.sent = true
		if correlation := p.notificationCorrelations[frame.RequestID]; correlation != nil {
			stamp := time.Now().UTC()
			correlation.issuedAt = &stamp
		}
		go func() { sendResult <- stream.Send(payload) }()
		p.mu.Unlock()
	}
	if admit == nil {
		issue()
	} else {
		admit(issue)
	}
	if !issued {
		p.releaseWrite()
		p.removePending(frame.RequestID, pending)
		return pluginprotocol.Frame{}, &requestError{message: "delivery was fenced before issuance"}
	}
	if sendErr, pendingSend := p.awaitSend(ctx, sendResult, nil); sendErr != nil {
		if !pendingSend {
			p.releaseWrite()
		}
		if resultType != "checkpoint_result" && !(resultType == "cancel_result" && errors.Is(sendErr, ctx.Err())) && !(resultType == "resume_result" && errors.Is(sendErr, context.Canceled)) {
			p.fail(fmt.Errorf("send plugin request: %w", sendErr))
		}
		p.removePending(frame.RequestID, pending)
		return pluginprotocol.Frame{}, &requestError{
			message: "plugin request send failed", cause: sendErr, sent: true,
			unavailable: (resultType != "checkpoint_result" || !errors.Is(sendErr, ctx.Err())) && !(resultType == "resume_result" && errors.Is(sendErr, context.Canceled)),
		}
	}
	if onSent != nil {
		onSent()
	}
	p.releaseWrite()
	if afterSend != nil {
		if err := afterSend(ctx, pending); err != nil {
			p.removePending(frame.RequestID, pending)
			p.fail(err)
			return pluginprotocol.Frame{}, &requestError{message: "stream restoration could not complete", cause: err, sent: true, unavailable: true}
		}
	}

	select {
	case result := <-pending.result:
		return result.frame, result.err
	case <-ctx.Done():
		p.mu.Lock()
		stillPending := p.pending[frame.RequestID] == pending
		sent := pending.sent
		claimed := pending.claimed
		if stillPending && !claimed {
			delete(p.pending, frame.RequestID)
		}
		p.mu.Unlock()
		if claimed {
			select {
			case result := <-pending.result:
				return result.frame, result.err
			case <-p.doneCh:
				return pluginprotocol.Frame{}, &requestError{message: "plugin became unavailable while committing its result", cause: p.failureCause(), sent: sent, unavailable: true}
			}
		}
		if stillPending && sent && resultType != "checkpoint_result" && resultType != "cancel_result" && !(resultType == "resume_result" && errors.Is(ctx.Err(), context.Canceled)) {
			p.fail(ctx.Err())
		}
		return pluginprotocol.Frame{}, &requestError{message: "plugin response deadline expired", cause: ctx.Err(), sent: sent, unavailable: sent && resultType != "checkpoint_result" && !(resultType == "resume_result" && errors.Is(ctx.Err(), context.Canceled))}
	case <-p.doneCh:
		p.removePending(frame.RequestID, pending)
		return pluginprotocol.Frame{}, &requestError{message: "plugin exited before its response", cause: p.failureCause(), sent: pending.sent, unavailable: true}
	}
}

type requestError struct {
	message     string
	cause       error
	sent        bool
	unavailable bool
}

func (e *requestError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "plugin request failed"
}

func (e *requestError) Unwrap() error { return e.cause }

func isUnavailable(err error) bool {
	var requestErr *requestError
	return errors.As(err, &requestErr) && requestErr.unavailable
}

func (p *Process) removePending(requestID string, pending *pendingRequest) {
	p.mu.Lock()
	if p.pending[requestID] == pending {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
}

func (p *Process) notifyDeliveryLocked(ctx context.Context, watch *watchState, notice hub.RuntimeNotice) error {
	p.mu.Lock()
	_, pending := watch.pending[notice.EventID]
	attempt := watch.notices[notice.EventID]
	available := p.ready && !p.failed && !p.stopping && watch.active && !watch.quiesced && !watch.cancelSent && p.watches[string(notice.SubscriptionID)] == watch && pending
	stream := p.stream
	p.mu.Unlock()
	if !available {
		return &hub.RuntimeError{Code: "invalid_notice", Message: "delivery notice does not match an outstanding participating event"}
	}
	payload, err := pluginprotocol.Encode(pluginprotocol.Frame{
		Type: "delivery_notice", SubscriptionID: string(notice.SubscriptionID), EventID: notice.EventID, Status: notice.Status,
	})
	if err != nil {
		return &hub.RuntimeError{Code: "invalid_notice", Message: "delivery notice is invalid", Cause: err}
	}
	if attempt != nil {
		select {
		case <-attempt.done:
			p.mu.Lock()
			err := attempt.err
			if watch.notices[notice.EventID] == attempt {
				delete(watch.notices, notice.EventID)
			}
			if err == nil {
				p.releasePendingLocked(watch, notice.EventID)
			}
			p.mu.Unlock()
			if err != nil {
				return &hub.RuntimeError{Code: "notice_send", Message: "delivery notice could not be sent", Unavailable: true, Cause: err}
			}
			return nil
		case <-ctx.Done():
			return &hub.RuntimeError{Code: "notice_send", Message: "delivery notice could not be sent before its deadline", Cause: ctx.Err()}
		}
	}
	if err := p.acquireWrite(ctx); err != nil {
		return &hub.RuntimeError{Code: "notice_send", Message: "delivery notice could not be sent before its deadline", Unavailable: !errors.Is(err, ctx.Err()), Cause: err}
	}
	p.mu.Lock()
	available = p.stream == stream && !p.failed && !p.stopping && watch.active && !watch.quiesced && !watch.cancelSent && p.watches[string(notice.SubscriptionID)] == watch
	p.mu.Unlock()
	if !available {
		p.releaseWrite()
		return &hub.RuntimeError{Code: "notice_send", Message: "input plugin became unavailable before the delivery notice", Unavailable: true}
	}
	attempt = &noticeSend{done: make(chan struct{})}
	p.mu.Lock()
	watch.notices[notice.EventID] = attempt
	p.mu.Unlock()
	completeAttempt := func(sendErr error) {
		p.mu.Lock()
		attempt.err = sendErr
		close(attempt.done)
		p.mu.Unlock()
	}
	if sendErr, pendingSend := p.sendUntil(ctx, stream, payload, completeAttempt); sendErr != nil {
		if !pendingSend {
			p.releaseWrite()
			p.mu.Lock()
			if watch.notices[notice.EventID] == attempt {
				delete(watch.notices, notice.EventID)
			}
			p.mu.Unlock()
		}
		// Callers invalidate the instance after releasing any receive callback
		// lock they may hold.
		return &hub.RuntimeError{Code: "notice_send", Message: "delivery notice could not be sent", Unavailable: !errors.Is(sendErr, ctx.Err()), Cause: sendErr}
	}
	p.releaseWrite()
	p.mu.Lock()
	p.releasePendingLocked(watch, notice.EventID)
	if watch.notices[notice.EventID] == attempt {
		delete(watch.notices, notice.EventID)
	}
	p.mu.Unlock()
	return nil
}

func (p *Process) releasePendingLocked(watch *watchState, eventID string) {
	tracked, exists := watch.pending[eventID]
	if !exists {
		return
	}
	delete(watch.pending, eventID)
	if tracked {
		p.runtimePendingCount--
	}
}

func (p *Process) rejectEvent(frame pluginprotocol.Frame, reason string) {
	p.logger.Warn("plugin_event_rejected", "plugin", p.config.Name, "subscription_id", safeText(frame.SubscriptionID, 256), "event_id", safeText(frame.EventID, 256), "type", frame.Type, "reason", reason)
}

func requestWasSent(err error) bool {
	var requestErr *requestError
	return errors.As(err, &requestErr) && requestErr.sent
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

func (p *Process) sendUntil(ctx context.Context, stream pluginprotocol.SessionClient, payload []byte, onLate func(error)) (error, bool) {
	result := make(chan error, 1)
	go func() { result <- stream.Send(payload) }()
	return p.awaitSend(ctx, result, onLate)
}

func (p *Process) awaitSend(ctx context.Context, result <-chan error, onLate func(error)) (error, bool) {
	select {
	case err := <-result:
		return err, false
	case <-ctx.Done():
		go func() {
			err := <-result
			p.releaseWrite()
			if onLate != nil {
				onLate(err)
			}
			if err != nil {
				p.fail(fmt.Errorf("send plugin request: %w", err))
			}
		}()
		return ctx.Err(), true
	case <-p.doneCh:
		go func() {
			err := <-result
			p.releaseWrite()
			if onLate != nil {
				onLate(err)
			}
			if err != nil {
				p.fail(fmt.Errorf("send plugin request: %w", err))
			}
		}()
		return errors.New("plugin instance became unavailable while sending"), true
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
			p.rejectMalformedStream(payload)
			if status.Code(err) == codes.ResourceExhausted {
				p.fail(fmt.Errorf("plugin payload exceeds limit: %w", err))
				return
			}
			p.rejectFrame("malformed plugin protocol payload")
			continue
		}
		p.handleFrame(frame)
	}
}

func (p *Process) handleFrame(frame pluginprotocol.Frame) {
	if frame.Type == "checkpoint_stream_begin" || frame.Type == "checkpoint_stream_chunk" || frame.Type == "checkpoint_stream_end" {
		p.handleCheckpointStream(frame)
		return
	}
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
		if frame.Role != string(p.config.Role) {
			p.mu.Unlock()
			p.callbackMu.Unlock()
			p.fail(errors.New("plugin readiness role does not match configuration"))
			return
		}
		if !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
			p.mu.Unlock()
			p.callbackMu.Unlock()
			p.fail(errors.New("plugin readiness deadline expired"))
			return
		}
		p.ready = true
		p.capabilities = make(map[string]bool, len(frame.Capabilities))
		for _, capability := range frame.Capabilities {
			p.capabilities[capability] = true
		}
		handler := p.handlers.Ready
		capabilities := append([]string(nil), frame.Capabilities...)
		p.mu.Unlock()
		if handler != nil {
			handler(capabilities)
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
	if frame.Type == "event" || frame.Type == "state_event" {
		if p.config.Role != domain.InputRole {
			p.mu.Unlock()
			p.callbackMu.Unlock()
			p.rejectFrame("output plugin sent an event")
			return
		}
		watch := p.watches[frame.SubscriptionID]
		p.mu.Unlock()
		if watch == nil {
			if frame.Type == "state_event" {
				p.rejectEvent(frame, "state event arrived before a positive watch result")
				p.callbackMu.Unlock()
				return
			}
			// Preserve the ordinary event path: the Hub owns registration
			// admission and records events that arrive before watch acceptance.
			if handler := p.handlers.Event; handler != nil {
				handler(frame.SubscriptionID, "", frame.Context)
			}
			p.callbackMu.Unlock()
			return
		}
		if frame.Type == "state_event" {
			p.mu.Lock()
			if p.capabilities[pluginprotocol.RewaitCapability] != (frame.Generation != "") {
				p.mu.Unlock()
				p.rejectEvent(frame, "state event generation does not match the capability profile")
				p.callbackMu.Unlock()
				return
			}
			current := p.watches[frame.SubscriptionID]
			if current != watch || !watch.active || !watch.stateful || watch.quiesced || watch.cancelling {
				p.mu.Unlock()
				p.rejectEvent(frame, "state event does not belong to an active participating watch")
				p.callbackMu.Unlock()
				return
			}
			eventID, parseErr := strconv.ParseUint(frame.EventID, 10, 64)
			if parseErr != nil || eventID <= watch.lastEvent {
				p.mu.Unlock()
				p.rejectEvent(frame, "state event identifier is not fresh and increasing")
				p.callbackMu.Unlock()
				return
			}
			p.mu.Unlock()
			watch.orderMu.Lock()
			p.mu.Lock()
			current = p.watches[frame.SubscriptionID]
			if current != watch || !watch.active || !watch.stateful || watch.quiesced || watch.cancelling || eventID <= watch.lastEvent {
				p.mu.Unlock()
				watch.orderMu.Unlock()
				p.rejectEvent(frame, "state event does not belong to an active participating watch")
				p.callbackMu.Unlock()
				return
			}
			overflow := p.runtimePendingCount >= hub.MaxDeliveries
			dormant := watch.activationRequired && !watch.granted
			watch.lastEvent = eventID
			watch.pending[frame.EventID] = !overflow
			if !overflow {
				p.runtimePendingCount++
			}
			handler := p.handlers.Event
			generationHandler := p.handlers.StateEvent
			rewait := p.capabilities[pluginprotocol.RewaitCapability]
			p.mu.Unlock()
			status := "discarded"
			if !overflow && !dormant && ((rewait && generationHandler != nil) || (!rewait && handler != nil)) {
				terminal := ""
				if rewait {
					terminal = generationHandler(frame.SubscriptionID, frame.EventID, frame.Generation, frame.Context)
				} else {
					terminal = handler(frame.SubscriptionID, frame.EventID, frame.Context)
				}
				if terminal != "" {
					status = terminal
				} else {
					status = ""
				}
			}
			if overflow {
				p.logger.Warn("event_rejected", "input_plugin", p.config.Name, "subscription_id", safeText(frame.SubscriptionID, 256), "event_id", safeText(frame.EventID, 256), "reason", "runtime event capacity is full")
			}
			if dormant {
				p.rejectEvent(frame, "state event arrived before activation grant")
			}
			var noticeErr error
			if status != "" {
				noticeCtx, cancel := context.WithTimeout(context.Background(), registrationTimeout)
				noticeErr = p.notifyDeliveryLocked(noticeCtx, watch, hub.RuntimeNotice{
					SubscriptionID: domain.SubscriptionID(frame.SubscriptionID), EventID: frame.EventID, Status: status,
				})
				cancel()
				if noticeErr != nil {
					p.logger.Warn("runtime_state_persistence_error", "input_plugin", p.config.Name, "subscription_id", safeText(frame.SubscriptionID, 256), "operation", "delivery notice", "reason", safeText(noticeErr.Error(), 256), "message", "runtime continuity at next restart cannot be guaranteed")
				}
			}
			watch.orderMu.Unlock()
			p.callbackMu.Unlock()
			if noticeErr != nil {
				// The input has no Hub-side retry owner for this immediate terminal
				// notice. Stop it after a failed send so pending event records stay
				// bounded and no later checkpoint can pass the missing outcome.
				p.fail(fmt.Errorf("send delivery notice: %w", noticeErr))
			}
			return
		}
		p.mu.Lock()
		if !watch.active || watch.stateful || watch.quiesced || watch.cancelling {
			p.mu.Unlock()
			p.rejectEvent(frame, "ordinary event does not belong to an active nonparticipating watch")
			p.callbackMu.Unlock()
			return
		}
		handler := p.handlers.Event
		p.mu.Unlock()
		if handler != nil {
			handler(frame.SubscriptionID, "", frame.Context)
		}
		p.callbackMu.Unlock()
		return
	}
	if handled, callback := p.handleNotificationFrameLocked(frame); handled {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		if callback != nil {
			callback()
		}
		return
	}
	if frame.Type != "watch_result" && frame.Type != "destination_result" && frame.Type != "delivery_result" && frame.Type != "checkpoint_result" && frame.Type != "resume_result" && frame.Type != "cancel_result" {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		p.rejectFrame("invalid direction")
		return
	}
	pending := p.pending[frame.RequestID]
	if pending == nil || !pending.sent || pending.claimed || pending.kind != frame.Type {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		p.logger.Warn("plugin_result_rejected", "plugin", p.config.Name, "request_id", safeText(frame.RequestID, 256), "type", frame.Type, "reason", "unknown, early, duplicate, or mismatched request")
		return
	}
	if pending.stream != nil {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		if frame.Type != "checkpoint_result" || frame.Accepted == nil || *frame.Accepted {
			p.expireCheckpoint(frame.RequestID, pending, errors.New("stream checkpoint returned a positive legacy result"))
			return
		}
		p.expireCheckpoint(frame.RequestID, pending, errors.New(safeText(frame.Error, 256)))
		return
	}
	if frame.Type == "watch_result" && frame.Accepted != nil && *frame.Accepted && !pending.restoreEnd {
		p.mu.Unlock()
		p.callbackMu.Unlock()
		p.fail(errors.New("stream watch accepted before complete state handoff"))
		return
	}
	pending.claimed = true
	if !time.Now().Before(pending.deadline) {
		delete(p.pending, frame.RequestID)
		p.mu.Unlock()
		p.callbackMu.Unlock()
		cause := errors.New("plugin response deadline expired")
		checkpoint := pending.kind == "checkpoint_result" || pending.kind == "cancel_result"
		pending.result <- requestResult{err: &requestError{message: cause.Error(), cause: cause, sent: true, unavailable: !checkpoint}}
		if !checkpoint {
			p.fail(cause)
		}
		return
	}
	if frame.Type == "watch_result" && *frame.Accepted && pending.watch != nil {
		if _, exists := p.watches[pending.watchID]; exists {
			delete(p.pending, frame.RequestID)
			p.mu.Unlock()
			p.callbackMu.Unlock()
			cause := errors.New("watch identifier is already active in this Session")
			p.fail(cause)
			pending.result <- requestResult{err: &requestError{message: cause.Error(), cause: cause, sent: true, unavailable: true}}
			return
		}
		pending.watch.active = true
		p.watches[pending.watchID] = pending.watch
	}
	p.mu.Unlock()
	if frame.Type == "watch_result" && *frame.Accepted && pending.activate != nil {
		if err := pending.activate(); err != nil {
			p.mu.Lock()
			stillPending := p.pending[frame.RequestID] == pending
			if stillPending {
				delete(p.pending, frame.RequestID)
			}
			if pending.watch != nil && p.watches[pending.watchID] == pending.watch {
				delete(p.watches, pending.watchID)
			}
			p.mu.Unlock()
			p.callbackMu.Unlock()
			if !stillPending {
				return
			}
			p.fail(fmt.Errorf("watch acceptance could not be committed: %w", err))
			pending.result <- requestResult{err: &requestError{message: "watch acceptance could not be committed", cause: err, sent: true, unavailable: true}}
			return
		}
	}
	p.mu.Lock()
	stillPending := p.pending[frame.RequestID] == pending
	if stillPending {
		delete(p.pending, frame.RequestID)
	}
	p.mu.Unlock()
	if stillPending {
		pending.result <- requestResult{frame: frame}
	}
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
	p.finishLocked()
	for _, pending := range p.pending {
		pending.result <- requestResult{err: &requestError{message: "plugin became unavailable", cause: cause, sent: pending.sent, unavailable: true}}
	}
	p.pending = make(map[string]*pendingRequest)
	p.notificationCorrelations = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	go p.kill()
	p.logger.Warn("plugin_instance_invalidated", "plugin", p.config.Name, "role", p.config.Role, "reason", safeText(cause.Error(), 256))
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
	p.finishLocked()
	for _, pending := range p.pending {
		pending.result <- requestResult{err: &requestError{message: "plugin instance stopped", cause: p.failure, sent: pending.sent, unavailable: true}}
	}
	p.pending = make(map[string]*pendingRequest)
	p.notificationCorrelations = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.kill()
	return nil
}

func (p *Process) finishLocked() {
	select {
	case <-p.doneCh:
	default:
		close(p.doneCh)
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
		<-p.setupDone
		client.Kill()
		p.mu.Lock()
		p.terminated = client.Exited()
		p.mu.Unlock()
		for _, diagnostic := range p.diagnostics {
			diagnostic.flush()
		}
		cause := p.failureCause()
		reason := ""
		if cause != nil {
			reason = safeText(cause.Error(), 256)
		}
		p.logger.Info("plugin_process_exit", "plugin", p.config.Name, "role", p.config.Role, "exited", client.Exited(), "reason", reason)
	})
}

func (p *Process) failureCause() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

func (p *Process) rejectFrame(reason string) {
	p.logger.Warn("plugin_frame_rejected", "plugin", p.config.Name, "role", p.config.Role, "reason", safeText(reason, 256))
}

func unavailable(message string, cause error) error {
	return &hub.WatchError{Code: "plugin_unavailable", Message: message, Unavailable: true, Cause: cause}
}

func randomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	hex.Encode(encoded[9:13], raw[4:6])
	hex.Encode(encoded[14:18], raw[6:8])
	hex.Encode(encoded[19:23], raw[8:10])
	hex.Encode(encoded[24:36], raw[10:16])
	encoded[8], encoded[13], encoded[18], encoded[23] = '-', '-', '-', '-'
	return string(encoded), nil
}

func boolPointer(value bool) *bool { return &value }

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

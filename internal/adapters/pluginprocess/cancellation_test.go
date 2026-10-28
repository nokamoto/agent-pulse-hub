package pluginprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func enableCancellation(p *Process) {
	p.mu.Lock()
	p.capabilities[pluginprotocol.CancelWatchCapability] = true
	p.mu.Unlock()
}

func TestCancellationPositiveRemovesOnlyTargetAndBlocksLaterWatchWork(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	enableCancellation(p)
	acceptV2Watch(t, p, session, "a")
	acceptV2Watch(t, p, session, "b")
	p.mu.Lock()
	p.watches["a"].pending["1"] = true
	p.runtimePendingCount = 1
	p.mu.Unlock()
	result := make(chan error, 1)
	go func() { result <- p.CancelWatch(context.Background(), "a") }()
	request := readFrame(t, session)
	if request.Type != "cancel_watch" || request.SubscriptionID != "a" {
		t.Fatalf("request = %+v", request)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "cancel_result", RequestID: request.RequestID, Accepted: boolPointer(true)})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	a, b, count, failed := p.watches["a"], p.watches["b"], p.runtimePendingCount, p.failed
	p.mu.Unlock()
	if a != nil || b == nil || count != 0 || failed {
		t.Fatalf("cleanup = %v %v %d %v", a, b, count, failed)
	}
	if err := p.ActivateWatch(context.Background(), "a"); err == nil {
		t.Fatal("cancelled watch received grant")
	}
	if _, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "a"}); err == nil {
		t.Fatal("cancelled watch received checkpoint")
	}
	if err := p.NotifyDelivery(context.Background(), hub.RuntimeNotice{SubscriptionID: "a", EventID: "1", Status: "accepted"}); err == nil {
		t.Fatal("cancelled watch received notice")
	}
	if len(session.outgoing) != 0 {
		t.Fatal("post-cancel frame emitted")
	}
	if err := p.ActivateWatch(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if readFrame(t, session).SubscriptionID != "b" {
		t.Fatal("unrelated watch stopped")
	}
}

func TestCancellationRejectTimeoutAndFreshExplicitRetry(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	enableCancellation(p)
	acceptV2Watch(t, p, session, "a")
	result := make(chan error, 1)
	go func() { result <- p.CancelWatch(context.Background(), "a") }()
	first := readFrame(t, session)
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "cancel_result", RequestID: first.RequestID, Accepted: boolPointer(false), Error: "monitor still stopping"})
	var watchErr *hub.WatchError
	if err := await(t, result); !errors.As(err, &watchErr) || watchErr.Code != "cancellation_rejected" {
		t.Fatalf("negative = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	go func() { result <- p.CancelWatch(ctx, "a") }()
	timedOut := readFrame(t, session)
	if err := await(t, result); !errors.As(err, &watchErr) || watchErr.Code != "plugin_unavailable" {
		t.Fatalf("timeout = %v", err)
	}
	p.handleFrame(pluginprotocol.Frame{Type: "cancel_result", RequestID: timedOut.RequestID, Accepted: boolPointer(true)})
	p.mu.Lock()
	failed, pending, watch := p.failed, len(p.pending), p.watches["a"]
	p.mu.Unlock()
	if failed || pending != 0 || watch == nil || !watch.cancelling {
		t.Fatal("timeout killed shared input, retained correlation, or removed target")
	}
	go func() { result <- p.CancelWatch(context.Background(), "a") }()
	retry := readFrame(t, session)
	if retry.RequestID == first.RequestID || retry.RequestID == timedOut.RequestID {
		t.Fatal("explicit retry reused correlation")
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "cancel_result", RequestID: retry.RequestID, Accepted: boolPointer(true)})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedCancellationTimeoutRemovesUnsentRequestWithoutInvalidation(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	enableCancellation(p)
	p.writeGate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.CancelWatch(ctx, "saved"); err == nil {
		t.Fatal("queued cancellation succeeded")
	}
	p.releaseWrite()
	p.mu.Lock()
	failed, pending := p.failed, len(p.pending)
	p.mu.Unlock()
	if failed || pending != 0 || len(session.outgoing) != 0 {
		t.Fatalf("expired unsent request = %v %d %d", failed, pending, len(session.outgoing))
	}
	if p.InputTerminated() {
		t.Fatal("in-memory session claimed process termination")
	}
}

func TestCancellationSendTimeoutPreservesSharedInputAndLateSendReleasesGate(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	enableCancellation(p)
	// No receiver accepts this send until after the caller's deadline.
	session.outgoing = make(chan []byte)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.CancelWatch(ctx, "saved"); err == nil {
		t.Fatal("blocked cancellation send succeeded")
	}
	request := readFrame(t, session)
	if request.Type != "cancel_watch" {
		t.Fatalf("late frame = %+v", request)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := p.acquireWrite(waitCtx); err != nil {
		t.Fatalf("late completed send retained ordered sender: %v", err)
	}
	p.releaseWrite()
	p.mu.Lock()
	failed, pending := p.failed, len(p.pending)
	p.mu.Unlock()
	if failed || pending != 0 {
		t.Fatal("send timeout invalidated shared input or kept correlation")
	}
}

func TestCancellationSendsOwedNoticeFirstAndActualSendFailureInvalidates(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	enableCancellation(p)
	acceptV2Watch(t, p, session, "a")
	p.mu.Lock()
	p.watches["a"].pending["1"] = true
	p.runtimePendingCount++
	p.mu.Unlock()
	if err := p.NotifyDelivery(context.Background(), hub.RuntimeNotice{SubscriptionID: "a", EventID: "1", Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- p.CancelWatch(context.Background(), "a") }()
	if notice := readFrame(t, session); notice.Type != "delivery_notice" {
		t.Fatalf("owed notice was overtaken: %+v", notice)
	}
	request := readFrame(t, session)
	p.handleFrame(pluginprotocol.Frame{Type: "cancel_result", RequestID: "wrong", Accepted: boolPointer(true)})
	select {
	case err := <-result:
		t.Fatalf("mismatched cancellation result completed request: %v", err)
	default:
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "cancel_result", RequestID: request.RequestID, Accepted: boolPointer(true)})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	// A real transport error retains normal input invalidation handling.
	session.sendErr = errors.New("stream transport failed")
	if err := p.CancelWatch(context.Background(), "saved"); err == nil {
		t.Fatal("send failure reported cancellation success")
	}
	p.mu.Lock()
	failed := p.failed
	p.mu.Unlock()
	if !failed || p.InputTerminated() {
		t.Fatal("send failure did not invalidate, or session loss claimed child cleanup")
	}
}

func TestFenceExpiresCheckpointAndPreparationWithoutWaitingForTheirResults(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	acceptV2Watch(t, p, session, "a")
	p.mu.Lock()
	p.capabilities[pluginprotocol.RewaitCapability] = true
	p.mu.Unlock()
	checkpointResult := make(chan error, 1)
	resumeResult := make(chan error, 1)
	go func() {
		_, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "a"})
		checkpointResult <- err
	}()
	checkpoint := readFrame(t, session)
	go func() {
		resumeResult <- p.ResumeWatch(context.Background(), hub.ResumeWatchRequest{RequestID: "resume", SubscriptionID: "a", Generation: "new", ConfirmationPosition: "position"})
	}()
	readFrame(t, session)
	if err := p.FenceWatch("a"); err != nil {
		t.Fatal(err)
	}
	if await(t, checkpointResult) == nil || await(t, resumeResult) == nil {
		t.Fatal("fenced operation succeeded")
	}
	p.handleFrame(pluginprotocol.Frame{Type: "checkpoint_result", RequestID: checkpoint.RequestID, Accepted: boolPointer(true), State: jsonObject(t, `{"schema":"v1","payload":{}}`)})
	p.handleFrame(pluginprotocol.Frame{Type: "resume_result", RequestID: "resume", Accepted: boolPointer(true)})
	p.mu.Lock()
	staged, failed, pending := p.watches["a"].stagedGeneration, p.failed, len(p.pending)
	p.mu.Unlock()
	if staged != "" || failed || pending != 0 {
		t.Fatalf("late work revived watch: %q %v %d", staged, failed, pending)
	}
}

func TestDeliveryGuardControlsIssuanceAndDoesNotHoldOwnerLockForResponse(t *testing.T) {
	p, session := readyProcess(t, domain.OutputRole, hub.PluginHandlers{})
	event := domain.Event{ID: "delivery", SubscriptionID: "a", InputPlugin: "input", DestinationArgs: jsonObject(t, `{}`), Context: "context"}
	result := p.DeliverGuarded(context.Background(), event, func(func()) bool { return false })
	if result.Outcome != domain.DeliveryFailed || len(session.outgoing) != 0 {
		t.Fatal("fenced delivery issued")
	}
	admitted := make(chan struct{})
	outcome := make(chan hub.DeliveryResult, 1)
	go func() {
		outcome <- p.DeliverGuarded(context.Background(), event, func(issue func()) bool { issue(); close(admitted); return true })
	}()
	await(t, admitted)
	frame := readFrame(t, session)
	select {
	case <-outcome:
		t.Fatal("delivery completed before action result")
	default:
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "delivery_result", RequestID: frame.RequestID, Status: "accepted"})
	if await(t, outcome).Outcome != domain.DeliveryAccepted {
		t.Fatal("issued action outcome lost")
	}
}

func TestCancellationFenceWinsQueuedActivationAndPreservesOtherWatch(t *testing.T) {
	for _, rewait := range []bool{false, true} {
		t.Run(map[bool]string{false: "v2", true: "rewait"}[rewait], func(t *testing.T) {
			p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
			acceptV2Watch(t, p, session, "a")
			acceptV2Watch(t, p, session, "b")
			generation := ""
			if rewait {
				generation = "next"
				p.mu.Lock()
				p.capabilities[pluginprotocol.RewaitCapability] = true
				p.watches["a"].stagedGeneration = generation
				p.mu.Unlock()
			}
			// The stream cannot issue the target grant until after the fence.
			p.writeGate <- struct{}{}
			result := make(chan error, 1)
			go func() { result <- p.activateGeneration(context.Background(), "a", generation) }()
			if err := p.FenceWatch("a"); err != nil {
				t.Fatal(err)
			}
			p.releaseWrite()
			if err := await(t, result); err == nil {
				t.Fatal("fenced queued grant succeeded")
			}
			p.mu.Lock()
			granted, staged, failed := p.watches["a"].granted, p.watches["a"].stagedGeneration, p.failed
			p.mu.Unlock()
			if granted || staged != "" || failed || len(session.outgoing) != 0 {
				t.Fatal("fenced grant issued, retained preparation, or invalidated input")
			}
			if err := p.activateGeneration(context.Background(), "b", generation); err != nil {
				t.Fatal(err)
			}
			if grant := readFrame(t, session); grant.SubscriptionID != "b" {
				t.Fatalf("unrelated activation = %+v", grant)
			}
		})
	}
}

func TestCancellationFencePreventsExpiredQueuedGrantFromInvalidatingSharedInput(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	acceptV2Watch(t, p, session, "a")
	p.writeGate <- struct{}{}
	if err := p.FenceWatch("a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if err := p.ActivateWatch(ctx, "a"); err == nil {
		t.Fatal("expired fenced grant succeeded")
	}
	p.releaseWrite()
	p.mu.Lock()
	failed := p.failed
	p.mu.Unlock()
	if failed || len(session.outgoing) != 0 {
		t.Fatal("fenced grant deadline invalidated shared input or issued a grant")
	}
}

type activationGateSession struct {
	*memorySession
	entered chan struct{}
	release chan struct{}
}

func (s *activationGateSession) Send(payload []byte) error {
	close(s.entered)
	<-s.release
	return s.memorySession.Send(payload)
}

func TestCancellationFenceDoesNotWaitForAlreadyIssuedActivationSend(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	acceptV2Watch(t, p, session, "a")
	gate := &activationGateSession{memorySession: session, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	})
	p.mu.Lock()
	p.stream = gate
	p.mu.Unlock()
	result := make(chan error, 1)
	go func() { result <- p.ActivateWatch(context.Background(), "a") }()
	await(t, gate.entered)
	fenced := make(chan error, 1)
	go func() { fenced <- p.FenceWatch("a") }()
	if err := await(t, fenced); err != nil {
		t.Fatal(err)
	}
	// Cancellation closes future admission without waiting for this issued send.
	close(gate.release)
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	if frame := readFrame(t, session); frame.Type != "activate_watch" || frame.SubscriptionID != "a" {
		t.Fatalf("already-issued grant = %+v", frame)
	}
	if err := p.ActivateWatch(context.Background(), "a"); err == nil {
		t.Fatal("fenced follow-up grant succeeded")
	}
	if len(session.outgoing) != 0 {
		t.Fatal("extra activation issued after cancellation")
	}
}

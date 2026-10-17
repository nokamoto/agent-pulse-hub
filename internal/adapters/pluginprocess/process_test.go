package pluginprocess

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"google.golang.org/grpc"
)

type memorySession struct {
	ctx      context.Context
	incoming chan []byte
	outgoing chan []byte
	sendErr  error
}

func (s *memorySession) Send(payload []byte) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	select {
	case s.outgoing <- append([]byte(nil), payload...):
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *memorySession) Recv() ([]byte, error) {
	select {
	case payload, open := <-s.incoming:
		if !open {
			return nil, io.EOF
		}
		return payload, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *memorySession) Context() context.Context { return s.ctx }
func (s *memorySession) CloseSend() error         { return nil }

func readyProcess(t *testing.T, role domain.PluginRole, handlers hub.PluginHandlers) (*Process, *memorySession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := New(Config{Name: "test", Role: role}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &memorySession{ctx: ctx, incoming: make(chan []byte, 16), outgoing: make(chan []byte, 16)}
	p.started = true
	p.ready = true
	p.stream = s
	p.cancel = cancel
	p.handlers = handlers
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	go p.receive(s)
	return p, s
}

func jsonObject(t *testing.T, text string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(text), 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func payload(t *testing.T, frame pluginprotocol.Frame) []byte {
	t.Helper()
	encoded, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func await[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("in-memory coordination did not complete")
		var zero T
		return zero
	}
}

func readFrame(t *testing.T, session *memorySession) pluginprotocol.Frame {
	t.Helper()
	frame, err := pluginprotocol.ParsePayload(await(t, session.outgoing), pluginprotocol.DaemonToPlugin)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestWatchAcceptanceActivatesBeforeImmediateEventAndRejectsDuplicates(t *testing.T) {
	var activated atomic.Int32
	events := make(chan string, 2)
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{Event: func(_, _, context string) string { events <- context; return "" }})
	result := make(chan error, 1)
	go func() {
		result <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "watch-1", SubscriptionID: "subscription-1", Arguments: jsonObject(t, `{}`),
		}, func() error { activated.Add(1); return nil })
	}()
	if frame := readFrame(t, session); frame.Type != "watch" || frame.RequestID != "watch-1" {
		t.Fatalf("watch frame = %+v", frame)
	}
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "watch-1", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "event", SubscriptionID: "subscription-1", Context: "immediate"})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	if got := await(t, events); got != "immediate" || activated.Load() != 1 {
		t.Fatalf("activation=%d event=%q", activated.Load(), got)
	}
}

func TestRuntimeWatchOrdersNoticeBeforeCheckpointAndQuiesces(t *testing.T) {
	events := make(chan string, 1)
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{
		Event: func(_, eventID, context string) string {
			events <- eventID + ":" + context
			return ""
		},
	})
	p.capabilities[pluginprotocol.RuntimeStateCapability] = true
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "state-watch-1", SubscriptionID: "subscription-state", Arguments: jsonObject(t, `{}`),
			RuntimeState: true, State: jsonvalue.NewNull(),
		}, func() error { return nil })
	}()
	watch := readFrame(t, session)
	if watch.Type != "state_watch" || watch.State == nil || watch.State.Kind() != jsonvalue.Null {
		t.Fatalf("initial state watch = %+v", watch)
	}
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "state-watch-1", Accepted: &accepted})
	if err := await(t, watchResult); err != nil {
		t.Fatal(err)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "state_event", SubscriptionID: "subscription-state", EventID: "1", Context: "U1"})
	if got := await(t, events); got != "1:U1" {
		t.Fatalf("state event callback = %q", got)
	}
	if err := p.NotifyDelivery(context.Background(), hub.RuntimeNotice{
		SubscriptionID: "subscription-state", EventID: "1", Status: "accepted",
	}); err != nil {
		t.Fatal(err)
	}
	notice := readFrame(t, session)
	if notice.Type != "delivery_notice" || notice.EventID != "1" || notice.Status != "accepted" {
		t.Fatalf("delivery notice = %+v", notice)
	}

	checkpointResult := make(chan struct {
		state *jsonvalue.Value
		err   error
	}, 1)
	go func() {
		value, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "subscription-state", Quiesce: true})
		checkpointResult <- struct {
			state *jsonvalue.Value
			err   error
		}{value, err}
	}()
	checkpoint := readFrame(t, session)
	if checkpoint.Type != "checkpoint" || !checkpoint.Quiesce || checkpoint.SubscriptionID != "subscription-state" {
		t.Fatalf("checkpoint request = %+v", checkpoint)
	}
	checkpointState := jsonObject(t, `{"schema":"probe-v1","payload":{"confirmed":["U1"]}}`)
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: checkpoint.RequestID, Accepted: &accepted, State: checkpointState})
	result := await(t, checkpointResult)
	if result.err != nil || result.state == nil || !result.state.Equivalent(checkpointState) {
		t.Fatalf("checkpoint result state=%v error=%v", result.state, result.err)
	}
	p.mu.Lock()
	watchState := p.watches["subscription-state"]
	quiesced := watchState != nil && watchState.quiesced
	p.mu.Unlock()
	if !quiesced {
		t.Fatal("positive quiesce checkpoint left the watch active")
	}
}

func TestRuntimeCheckpointTimeoutDoesNotInvalidateInput(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	p.capabilities[pluginprotocol.RuntimeStateCapability] = true
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "state-watch-timeout", SubscriptionID: "subscription-timeout", Arguments: jsonObject(t, `{}`),
			RuntimeState: true,
		}, func() error { return nil })
	}()
	readFrame(t, session)
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "state-watch-timeout", Accepted: &accepted})
	if err := await(t, watchResult); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	checkpointResult := make(chan error, 1)
	var requestID string
	go func() {
		_, err := p.Checkpoint(ctx, hub.RuntimeCheckpointRequest{SubscriptionID: "subscription-timeout"})
		checkpointResult <- err
	}()
	checkpoint := readFrame(t, session)
	requestID = checkpoint.RequestID
	if err := await(t, checkpointResult); err == nil {
		t.Fatal("expired checkpoint unexpectedly succeeded")
	}
	if processFailed(p) {
		t.Fatal("checkpoint-only timeout invalidated a healthy input")
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: requestID, Accepted: &accepted, State: jsonObject(t, `{"schema":"probe-v1","payload":{}}`)})
	time.Sleep(10 * time.Millisecond)
	if processFailed(p) {
		t.Fatal("late checkpoint result invalidated a healthy input")
	}
}

func TestStateEventNoticeSendFailureDoesNotDeadlockCallbackOrCheckpoint(t *testing.T) {
	unavailable := make(chan error, 1)
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{
		Event:       func(_, _, _ string) string { return "discarded" },
		Unavailable: func(err error) { unavailable <- err },
	})
	p.capabilities[pluginprotocol.RuntimeStateCapability] = true
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "state-watch-send-error", SubscriptionID: "subscription-send-error", Arguments: jsonObject(t, `{}`),
			RuntimeState: true,
		}, func() error { return nil })
	}()
	readFrame(t, session)
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "state-watch-send-error", Accepted: &accepted})
	if err := await(t, watchResult); err != nil {
		t.Fatal(err)
	}
	session.sendErr = errors.New("injected notice send failure")
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "state_event", SubscriptionID: "subscription-send-error", EventID: "1", Context: "rejected"})
	if err := await(t, unavailable); err == nil || !strings.Contains(err.Error(), "delivery notice") {
		t.Fatalf("notice send failure did not invalidate input: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Checkpoint(ctx, hub.RuntimeCheckpointRequest{SubscriptionID: "subscription-send-error", Quiesce: true}); err == nil {
		t.Fatal("checkpoint unexpectedly succeeded after notice transport failure")
	}
}

func TestRuntimeOutstandingEventsAreBoundedWithoutBlockingCheckpointResults(t *testing.T) {
	processed := make(chan struct{}, hub.MaxDeliveries+1)
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{
		Event: func(_, _, _ string) string {
			processed <- struct{}{}
			return ""
		},
	})
	p.capabilities[pluginprotocol.RuntimeStateCapability] = true
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "state-watch-capacity", SubscriptionID: "subscription-capacity", Arguments: jsonObject(t, `{}`),
			RuntimeState: true,
		}, func() error { return nil })
	}()
	readFrame(t, session)
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "state-watch-capacity", Accepted: &accepted})
	if err := await(t, watchResult); err != nil {
		t.Fatal(err)
	}
	checkpointResult := make(chan error, 1)
	go func() {
		_, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "subscription-capacity"})
		checkpointResult <- err
	}()
	checkpoint := readFrame(t, session)
	if checkpoint.Type != "checkpoint" || checkpoint.SubscriptionID != "subscription-capacity" {
		t.Fatalf("checkpoint request = %+v", checkpoint)
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for eventID := 1; eventID <= hub.MaxDeliveries+1; eventID++ {
			session.incoming <- payload(t, pluginprotocol.Frame{
				Type: "state_event", SubscriptionID: "subscription-capacity", EventID: strconv.Itoa(eventID), Context: "update",
			})
		}
	}()
	for i := 0; i < hub.MaxDeliveries; i++ {
		await(t, processed)
	}
	overflowNotice := readFrame(t, session)
	if overflowNotice.Type != "delivery_notice" || overflowNotice.EventID != strconv.Itoa(hub.MaxDeliveries+1) || overflowNotice.Status != "discarded" {
		t.Fatalf("capacity overflow notice = %+v", overflowNotice)
	}
	await(t, sendDone)
	session.incoming <- payload(t, pluginprotocol.Frame{
		Type: "checkpoint_result", RequestID: checkpoint.RequestID, Accepted: &accepted,
		State: jsonObject(t, `{"schema":"probe-v1","payload":{}}`),
	})
	if err := await(t, checkpointResult); err != nil {
		t.Fatalf("checkpoint result after event overflow = %v", err)
	}
	p.mu.Lock()
	watch := p.watches["subscription-capacity"]
	gotPending := len(watch.pending)
	p.mu.Unlock()
	p.mu.Lock()
	gotOutstanding := p.runtimePendingCount
	p.mu.Unlock()
	if gotPending != hub.MaxDeliveries || gotOutstanding != hub.MaxDeliveries {
		t.Fatalf("outstanding event count = pending %d, reserved %d", gotPending, gotOutstanding)
	}
	if err := p.NotifyDelivery(context.Background(), hub.RuntimeNotice{
		SubscriptionID: "subscription-capacity", EventID: "1", Status: "accepted",
	}); err != nil {
		t.Fatal(err)
	}
	if notice := readFrame(t, session); notice.Type != "delivery_notice" || notice.EventID != "1" {
		t.Fatalf("capacity releasing notice = %+v", notice)
	}
}

func TestMalformedStatePayloadDiagnosticDoesNotExposePayloadKeys(t *testing.T) {
	logs := &lockedBuffer{}
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	p.logger = slog.New(slog.NewJSONHandler(logs, nil))
	session.incoming <- []byte(`{"version":2,"type":"checkpoint_result","request_id":"r1","accepted":true,"state":{"schema":"v1","payload":{"private-token":1,"private-token":2}}}`)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), "plugin_frame_rejected") {
		time.Sleep(time.Millisecond)
	}
	got := logs.String()
	if !strings.Contains(got, "plugin_frame_rejected") || strings.Contains(got, "private-token") {
		t.Fatalf("malformed state diagnostic = %s", got)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func processFailed(p *Process) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed
}

func TestDestinationAndWatchRejectionsDoNotInvalidateProcess(t *testing.T) {
	output, outputSession := readyProcess(t, domain.OutputRole, hub.PluginHandlers{})
	destinationResult := make(chan error, 1)
	go func() {
		destinationResult <- output.ValidateDestination(context.Background(), hub.DestinationRequest{
			RequestID: "destination-1", Arguments: jsonObject(t, `{}`),
		})
	}()
	if frame := readFrame(t, outputSession); frame.Type != "validate_destination" {
		t.Fatalf("destination frame = %+v", frame)
	}
	rejected := false
	outputSession.incoming <- payload(t, pluginprotocol.Frame{Type: "destination_result", RequestID: "destination-1", Accepted: &rejected, Error: "bad path"})
	err := await(t, destinationResult)
	var destinationErr *hub.DestinationError
	if !errors.As(err, &destinationErr) || destinationErr.Code != "destination_rejected" || destinationErr.Unavailable {
		t.Fatalf("destination rejection = %v", err)
	}

	input, inputSession := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- input.Watch(context.Background(), hub.WatchRequest{
			RequestID: "watch-reject", SubscriptionID: "s1", Arguments: jsonObject(t, `{}`),
		}, func() error { return nil })
	}()
	readFrame(t, inputSession)
	inputSession.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "watch-reject", Accepted: &rejected, Error: "unsupported"})
	err = await(t, watchResult)
	var watchErr *hub.WatchError
	if !errors.As(err, &watchErr) || watchErr.Code != "watch_rejected" || watchErr.Unavailable {
		t.Fatalf("watch rejection = %v", err)
	}
	if input.failed || output.failed {
		t.Fatal("explicit rejection invalidated a healthy plugin")
	}
}

func TestDeliveryCorrelatesResultAndDoesNotRetryFailedOutcome(t *testing.T) {
	p, session := readyProcess(t, domain.OutputRole, hub.PluginHandlers{})
	results := make(chan hub.DeliveryResult, 1)
	go func() {
		results <- p.Deliver(context.Background(), domain.Event{
			ID: "delivery-1", InputPlugin: "manual", OutputPlugin: "model", SubscriptionID: "subscription-1",
			DestinationArgs: jsonObject(t, `{"output_file":"/tmp/events.jsonl"}`), Context: "PULSE-context",
		})
	}()
	frame := readFrame(t, session)
	if frame.Type != "deliver" || frame.InputPlugin != "manual" || frame.Context != "PULSE-context" || frame.ExternalData == nil || !*frame.ExternalData || frame.PermissionNotice != pluginprotocol.PermissionNotice {
		t.Fatalf("deliver frame = %+v", frame)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "delivery_result", RequestID: frame.RequestID, Status: "failed", Error: "disk full"})
	result := await(t, results)
	if result.Outcome != domain.DeliveryFailed || result.Detail != "disk full" || len(session.outgoing) != 0 {
		t.Fatalf("delivery result = %+v; extra sends=%d", result, len(session.outgoing))
	}
	if p.failed {
		t.Fatal("explicit failed result invalidated output process")
	}
}

func TestPossiblySentRequestTimeoutInvalidatesInstance(t *testing.T) {
	unavailable := make(chan error, 1)
	p, _ := readyProcess(t, domain.OutputRole, hub.PluginHandlers{Unavailable: func(err error) { unavailable <- err }})
	_, err := p.request(context.Background(), pluginprotocol.Frame{
		Type: "validate_destination", RequestID: "timeout", DestinationArgs: jsonObject(t, `{}`),
	}, "destination_result", 20*time.Millisecond, nil)
	var requestErr *requestError
	if !errors.As(err, &requestErr) || !requestErr.sent || !requestErr.unavailable {
		t.Fatalf("timed out request = %v", err)
	}
	if cause := await(t, unavailable); !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("invalidated cause = %v", cause)
	}
}

func TestTimeoutBeforeSerializedSendDoesNotClaimPossibleDelivery(t *testing.T) {
	unavailable := make(chan error, 1)
	p, _ := readyProcess(t, domain.OutputRole, hub.PluginHandlers{Unavailable: func(err error) { unavailable <- err }})
	p.writeGate <- struct{}{}
	_, err := p.request(context.Background(), pluginprotocol.Frame{
		Type: "validate_destination", RequestID: "queued", DestinationArgs: jsonObject(t, `{}`),
	}, "destination_result", 20*time.Millisecond, nil)
	p.releaseWrite()
	var requestErr *requestError
	if !errors.As(err, &requestErr) || requestErr.sent || !requestErr.unavailable {
		t.Fatalf("pre-send timeout = %v", err)
	}
	if cause := await(t, unavailable); !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("pre-send timeout invalidated with %v", cause)
	}
	if !p.failed {
		t.Fatal("pre-send timeout left the instance available")
	}
}

func TestResultWithWrongCorrelationIsDiscardedUntilMatchingResponse(t *testing.T) {
	p, session := readyProcess(t, domain.OutputRole, hub.PluginHandlers{})
	result := make(chan error, 1)
	go func() {
		result <- p.ValidateDestination(context.Background(), hub.DestinationRequest{RequestID: "right", Arguments: jsonObject(t, `{}`)})
	}()
	readFrame(t, session)
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "destination_result", RequestID: "wrong", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "destination_result", RequestID: "right", Accepted: &accepted})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
	if p.failed {
		t.Fatal("unknown result invalidated process")
	}
}

func TestWrongReadyRoleInvalidatesProcess(t *testing.T) {
	unavailable := make(chan error, 1)
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{Unavailable: func(err error) { unavailable <- err }})
	p.ready = false
	p.deadline = time.Now().Add(time.Second)
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "ready", Role: "output"})
	if err := await(t, unavailable); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("wrong role failure = %v", err)
	}
}

func TestSessionCancellationAndShutdownInterceptor(t *testing.T) {
	for _, cancelSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "session"}[cancelSession], func(t *testing.T) {
			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			lifetime, cancelLifetime := context.WithCancel(context.Background())
			defer cancelLifetime()
			callCtx, cancelCall := lifetimeContext(caller, lifetime)
			defer cancelCall()
			if cancelSession {
				cancelLifetime()
			} else {
				cancelCaller()
			}
			await(t, callCtx.Done())
			if !errors.Is(callCtx.Err(), context.Canceled) {
				t.Fatalf("framework call context = %v", callCtx.Err())
			}
		})
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	cancelLifetime()
	interceptor := sessionLifetimeUnaryInterceptor(lifetime)
	err := interceptor(context.Background(), controllerShutdownMethod, nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("controller shutdown context = %v", err)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticWriterBoundsAndSanitizesChunks(t *testing.T) {
	var output bytes.Buffer
	p := New(Config{Name: "test", Role: domain.InputRole}, slog.New(slog.NewJSONHandler(&output, nil)))
	writer := p.newDiagnosticWriter("stderr")
	first := []byte("prefix\t" + strings.Repeat("x", maxPluginDiagnosticBytes))
	if n, err := writer.Write(first); err != nil || n != len(first) {
		t.Fatalf("diagnostic write = %d, %v", n, err)
	}
	if _, err := writer.Write([]byte("tail\n")); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 || output.Len() > maxPluginDiagnosticBytes+256 || strings.Contains(output.String(), "\t") {
		t.Fatalf("diagnostic output is unbounded or unsanitized: %q", output.String())
	}
}

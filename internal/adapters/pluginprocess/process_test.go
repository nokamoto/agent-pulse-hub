package pluginprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"google.golang.org/grpc"
)

// memorySession exercises stream coordination without processes or network I/O.
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

func readyProcess(t *testing.T, handlers hub.PluginHandlers) (*Process, *memorySession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := New(Config{Name: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func watchRequest(id string) hub.WatchRequest {
	return hub.WatchRequest{
		RequestID:      id,
		SubscriptionID: domain.SubscriptionID("subscription-" + id),
		Arguments:      jsonvalue.NewObject(map[string]*jsonvalue.Value{}),
	}
}

func launchWatch(p *Process, id string, activate func() error) <-chan error {
	result := make(chan error, 1)
	go func() { result <- p.Watch(context.Background(), watchRequest(id), activate) }()
	return result
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

func TestConcurrentWatchesSendBeforeEarlierAcknowledgement(t *testing.T) {
	p, session := readyProcess(t, hub.PluginHandlers{})
	var activations atomic.Int32
	activate := func() error { activations.Add(1); return nil }
	first := launchWatch(p, "first", activate)
	firstFrame, err := pluginprotocol.ParsePayload(await(t, session.outgoing), pluginprotocol.DaemonToPlugin)
	if err != nil || firstFrame.RequestID != "first" {
		t.Fatalf("first watch = %+v, %v", firstFrame, err)
	}
	second := launchWatch(p, "second", activate)
	secondFrame, err := pluginprotocol.ParsePayload(await(t, session.outgoing), pluginprotocol.DaemonToPlugin)
	if err != nil || secondFrame.RequestID != "second" {
		t.Fatalf("second watch = %+v, %v", secondFrame, err)
	}
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "second", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "first", Accepted: &accepted})
	if err := await(t, first); err != nil {
		t.Fatal(err)
	}
	if err := await(t, second); err != nil {
		t.Fatal(err)
	}
	if activations.Load() != 2 {
		t.Fatalf("activated %d watches", activations.Load())
	}
}

func TestActivationCompletesBeforeImmediateEvent(t *testing.T) {
	var activated atomic.Bool
	events := make(chan bool, 1)
	p, session := readyProcess(t, hub.PluginHandlers{
		Event: func(_, _ string) { events <- activated.Load() },
	})
	result := launchWatch(p, "watch", func() error { activated.Store(true); return nil })
	await(t, session.outgoing)
	accepted := true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "watch", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "event", SubscriptionID: "subscription-watch", Context: "immediate"})
	if !await(t, events) {
		t.Fatal("immediate event arrived before activation")
	}
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedWatchKeepsInstanceAvailableAndDiscardsDuplicateResult(t *testing.T) {
	events := make(chan struct{}, 1)
	p, session := readyProcess(t, hub.PluginHandlers{Event: func(_, _ string) { events <- struct{}{} }})
	var activations atomic.Int32
	activate := func() error { activations.Add(1); return nil }
	first := launchWatch(p, "rejected", activate)
	await(t, session.outgoing)
	accepted := false
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "rejected", Accepted: &accepted, Error: "unsupported arguments"})
	err := await(t, first)
	var watchError *hub.WatchError
	if !errors.As(err, &watchError) || watchError.Code != "watch_rejected" || watchError.Unavailable {
		t.Fatalf("rejection = %v", err)
	}
	second := launchWatch(p, "accepted", activate)
	await(t, session.outgoing)
	accepted = true
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "accepted", Accepted: &accepted})
	if err := await(t, second); err != nil {
		t.Fatal(err)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "accepted", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "unknown", Accepted: &accepted})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "event", SubscriptionID: "subscription-accepted", Context: "barrier"})
	await(t, events)
	if activations.Load() != 1 {
		t.Fatalf("activated %d watches", activations.Load())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed || !p.ready {
		t.Fatal("rejection or duplicate result invalidated the instance")
	}
}

func TestMalformedPayloadAndPreReadyFramesAreDiscarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan string, 2)
	var readiness atomic.Int32
	p := New(Config{Name: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.started = true
	p.cancel = cancel
	p.handlers = hub.PluginHandlers{
		Ready: func() { readiness.Add(1) },
		Event: func(_, text string) { events <- text },
	}
	session := &memorySession{ctx: ctx, incoming: make(chan []byte, 16), outgoing: make(chan []byte, 1)}
	p.stream = session
	go p.receive(session)
	session.incoming <- []byte(`{"version":1,"type":`)
	session.incoming <- []byte(`{"version":1,"type":"ready","extra":true}`)
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "event", SubscriptionID: "id", Context: "early"})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "ready"})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "ready"})
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "event", SubscriptionID: "id", Context: "after readiness"})
	if event := await(t, events); event != "after readiness" {
		t.Fatalf("accepted event %q", event)
	}
	if readiness.Load() != 1 {
		t.Fatalf("readiness called %d times", readiness.Load())
	}
	_ = p.Stop(context.Background())
}

func TestInterruptedWriteGateInvalidatesInstance(t *testing.T) {
	unavailableEvents := make(chan error, 1)
	p, _ := readyProcess(t, hub.PluginHandlers{Unavailable: func(err error) { unavailableEvents <- err }})
	p.writeGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Watch(ctx, watchRequest("queued"), func() error { t.Error("interrupted watch activated"); return nil })
	var watchError *hub.WatchError
	if !errors.As(err, &watchError) || !watchError.Unavailable {
		t.Fatalf("interrupted watch = %v", err)
	}
	if cause := await(t, unavailableEvents); !errors.Is(cause, context.Canceled) {
		t.Fatalf("unavailable cause = %v", cause)
	}
	p.releaseWrite()
}

func TestSendFailureInvalidatesInstance(t *testing.T) {
	unavailableEvents := make(chan error, 1)
	p, session := readyProcess(t, hub.PluginHandlers{Unavailable: func(err error) { unavailableEvents <- err }})
	session.sendErr = io.ErrClosedPipe
	err := p.Watch(context.Background(), watchRequest("failed"), func() error { t.Error("failed watch activated"); return nil })
	var watchError *hub.WatchError
	if !errors.As(err, &watchError) || !watchError.Unavailable || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("send failure = %v", err)
	}
	await(t, unavailableEvents)
}

func TestOversizedPayloadInvalidatesOnlyItsInstance(t *testing.T) {
	unavailableEvents := make(chan error, 1)
	failed, session := readyProcess(t, hub.PluginHandlers{Unavailable: func(err error) { unavailableEvents <- err }})
	healthy, healthySession := readyProcess(t, hub.PluginHandlers{})
	session.incoming <- make([]byte, pluginprotocol.MaxPayloadBytes+1)
	await(t, unavailableEvents)
	err := failed.Watch(context.Background(), watchRequest("failed"), func() error { return nil })
	var watchError *hub.WatchError
	if !errors.As(err, &watchError) || !watchError.Unavailable {
		t.Fatalf("failed instance watch = %v", err)
	}
	result := launchWatch(healthy, "healthy", func() error { return nil })
	await(t, healthySession.outgoing)
	accepted := true
	healthySession.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "healthy", Accepted: &accepted})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestTransportClosureInvalidatesInstance(t *testing.T) {
	unavailableEvents := make(chan error, 1)
	_, session := readyProcess(t, hub.PluginHandlers{Unavailable: func(err error) { unavailableEvents <- err }})
	close(session.incoming)
	if cause := await(t, unavailableEvents); !errors.Is(cause, io.EOF) {
		t.Fatalf("transport closure cause = %v", cause)
	}
}

func TestFrameworkContextObservesSessionAndCallerCancellation(t *testing.T) {
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
}

func TestFrameworkShutdownOutlivesSessionCancellation(t *testing.T) {
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	cancelLifetime()
	interceptor := sessionLifetimeUnaryInterceptor(lifetime)

	err := interceptor(context.Background(), controllerShutdownMethod, nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("controller shutdown context = %v", err)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("controller shutdown = %v", err)
	}

	err = interceptor(context.Background(), "/example.Service/Health", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("framework call context = %v, want canceled", ctx.Err())
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("framework call = %v", err)
	}
}

func TestDiagnosticWriterBoundsAndSanitizesChunks(t *testing.T) {
	var output bytes.Buffer
	p := New(Config{Name: "test"}, slog.New(slog.NewJSONHandler(&output, nil)))
	writer := p.newDiagnosticWriter("stderr")
	first := []byte("prefix\t" + strings.Repeat("x", maxPluginDiagnosticBytes))
	if n, err := writer.Write(first); err != nil || n != len(first) {
		t.Fatalf("diagnostic write = %d, %v", n, err)
	}
	if _, err := writer.Write([]byte("tail\n")); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Message   string `json:"message"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Message) > maxPluginDiagnosticBytes || strings.ContainsAny(record.Message, "\t\n") || !record.Truncated {
		t.Fatalf("unbounded or unsanitized diagnostic: %+v", record)
	}
}

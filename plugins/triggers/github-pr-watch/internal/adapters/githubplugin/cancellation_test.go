package githubplugin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"go.uber.org/mock/gomock"
)

func TestCancellationDiscardsPreparedRewaitAndJoinsOnlyTargetMonitor(t *testing.T) {
	ctrl := gomock.NewController(t)
	credentials, github := NewMockCredentials(ctrl), NewMockGitHub(ctrl)
	credentials.EXPECT().Token(gomock.Any(), "github.com").Return("token", nil).AnyTimes()
	github.EXPECT().Validate(gomock.Any(), gomock.Any(), "token").Return(nil).AnyTimes()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseTarget := sync.OnceFunc(func() { close(release) })
	defer releaseTarget()
	github.EXPECT().Observe(gomock.Any(), gomock.Any(), "token").DoAndReturn(func(ctx context.Context, _ domain.WatchConfig, _ string) (domain.Observation, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return domain.Observation{}, ctx.Err()
	}).Times(1)
	ctx, cancel := context.WithCancel(context.Background())
	stream := &testSession{ctx: ctx, incoming: make(chan []byte, 16), outgoing: make(chan []byte, 16)}
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), credentials, github, "github.com", time.Hour)
	done := make(chan error, 1)
	go func() { done <- p.Session(stream) }()
	defer func() {
		releaseTarget()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Session: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Session did not stop")
		}
	}()
	ready := receiveFrame(t, stream)
	if len(ready.Capabilities) != 3 || ready.Capabilities[2] != pluginprotocol.CancelWatchCapability {
		t.Fatalf("readiness: %+v", ready)
	}
	a := stateWatch(t, "a")
	config, err := domain.ParseWatch(a.WatchArgs, "github.com")
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := domain.NewTracker(config, make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	observation := domain.Observation{StartedAt: now, EndedAt: now, PR: domain.Aggregate{ID: "PR1", URL: "https://github.com/o/r/pull/1", Head: "H1", State: "OPEN"}, Items: []domain.Item{}}
	if err := tracker.Reconcile(observation); err != nil {
		t.Fatal(err)
	}
	observation.PR.Head = "H2"
	if err := tracker.Reconcile(observation); err != nil {
		t.Fatal(err)
	}
	a.State, err = tracker.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []pluginprotocol.Frame{a, stateWatch(t, "b")} {
		sendFrame(t, stream, frame)
		assertResult(t, receiveFrame(t, stream), "watch_result", frame.RequestID, true)
	}
	sendFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "a", Generation: "generation-1"})
	event := receiveFrame(t, stream)
	if event.Type != "state_event" || event.SubscriptionID != "a" {
		t.Fatalf("initial retained change: %+v", event)
	}
	var eventContext struct {
		Position string `json:"confirmation_position"`
	}
	if err := json.Unmarshal([]byte(event.Context), &eventContext); err != nil || eventContext.Position == "" {
		t.Fatalf("notification position: %v", err)
	}
	sendFrame(t, stream, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "a", EventID: event.EventID, Status: "accepted"})
	sendFrame(t, stream, pluginprotocol.Frame{Type: "resume_watch", RequestID: "prepare-a", SubscriptionID: "a", Generation: "generation-2", ConfirmationPosition: eventContext.Position})
	assertResult(t, receiveFrame(t, stream), "resume_result", "prepare-a", true)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("A monitor did not start")
	}
	sendFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "cancel-a-1", SubscriptionID: "a"})
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("A source operation was not cancelled")
	}
	sendFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "cancel-a-2", SubscriptionID: "a"})
	sendFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: "b-live", SubscriptionID: "b"})
	assertResult(t, receiveFrame(t, stream), "checkpoint_result", "b-live", true)
	select {
	case frame := <-stream.outgoing:
		t.Fatalf("cancellation acknowledged before source exit: %s", frame)
	default:
	}
	releaseTarget()
	seen := map[string]bool{}
	for range 2 {
		result := receiveFrame(t, stream)
		assertResult(t, result, "cancel_result", result.RequestID, true)
		seen[result.RequestID] = true
	}
	if !seen["cancel-a-1"] || !seen["cancel-a-2"] {
		t.Fatalf("overlapping cancellation results: %v", seen)
	}
	sendFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "a", Generation: "generation-2"})
	sendFrame(t, stream, pluginprotocol.Frame{Type: "resume_watch", RequestID: "late-prepare", SubscriptionID: "a", Generation: "generation-3", ConfirmationPosition: eventContext.Position})
	assertResult(t, receiveFrame(t, stream), "resume_result", "late-prepare", false)
	sendFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: "a-absent", SubscriptionID: "a"})
	assertResult(t, receiveFrame(t, stream), "checkpoint_result", "a-absent", false)
	sendFrame(t, stream, stateWatch(t, "fresh-a"))
	assertResult(t, receiveFrame(t, stream), "watch_result", "watch-fresh-a", true)
	sendFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "absent", SubscriptionID: "a"})
	assertResult(t, receiveFrame(t, stream), "cancel_result", "absent", true)
}

func stateWatch(t *testing.T, id string) pluginprotocol.Frame {
	t.Helper()
	args, err := jsonvalue.Parse([]byte(`{"host":"github.com","owner":"o","repository":"r","pull_number":1,"waiting_reason":"review","represented_actor":"author"}`), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return pluginprotocol.Frame{Type: "state_watch", RequestID: "watch-" + id, SubscriptionID: id, WatchArgs: args, State: jsonvalue.NewNull()}
}

type testSession struct {
	ctx      context.Context
	incoming chan []byte
	outgoing chan []byte
}

func (s *testSession) Context() context.Context { return s.ctx }
func (s *testSession) Send(payload []byte) error {
	select {
	case s.outgoing <- payload:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *testSession) Recv() ([]byte, error) {
	select {
	case payload := <-s.incoming:
		return payload, nil
	case <-s.ctx.Done():
		return nil, io.EOF
	}
}

func sendFrame(t *testing.T, stream *testSession, frame pluginprotocol.Frame) {
	t.Helper()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case stream.incoming <- payload:
	case <-time.After(2 * time.Second):
		t.Fatal("send timed out")
	}
}

func receiveFrame(t *testing.T, stream *testSession) pluginprotocol.Frame {
	t.Helper()
	select {
	case payload := <-stream.outgoing:
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		if err != nil {
			t.Fatal(err)
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("receive timed out")
		return pluginprotocol.Frame{}
	}
}

func assertResult(t *testing.T, frame pluginprotocol.Frame, kind, requestID string, accepted bool) {
	t.Helper()
	if frame.Type != kind || frame.RequestID != requestID || frame.Accepted == nil || *frame.Accepted != accepted {
		t.Fatalf("got %+v, want %s %s accepted=%v", frame, kind, requestID, accepted)
	}
}

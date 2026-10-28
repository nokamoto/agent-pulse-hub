package githubplugin

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
	"go.uber.org/mock/gomock"
)

func TestCancellationWaitsForTargetExitWhileOtherWatchesRemainUsable(t *testing.T) {
	ctrl := gomock.NewController(t)
	credentials, github := NewMockCredentials(ctrl), NewMockGitHub(ctrl)
	credentials.EXPECT().Token(gomock.Any(), gomock.Any()).Return("token", nil).AnyTimes()
	github.EXPECT().ResolveUser(gomock.Any(), gomock.Any(), "token").Return(application.User{ID: 7, Login: "operator"}, nil).AnyTimes()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseTarget := sync.OnceFunc(func() { close(release) })
	defer releaseTarget()
	github.EXPECT().Observe(gomock.Any(), gomock.Any(), "token").DoAndReturn(func(ctx context.Context, _ domain.WatchConfig, _ string) (map[uint64]string, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return map[uint64]string{1: "https://github.com/o/r/issues/1"}, nil
	}).Times(1)
	ctx, cancel := context.WithCancel(context.Background())
	stream := &testSession{ctx: ctx, incoming: make(chan []byte, 16), outgoing: make(chan []byte, 16)}
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), credentials, github)
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
	ready := receiveTestFrame(t, stream)
	if len(ready.Capabilities) != 2 || ready.Capabilities[1] != pluginprotocol.CancelWatchCapability {
		t.Fatalf("readiness: %+v", ready)
	}
	for _, id := range []string{"a", "b"} {
		sendTestFrame(t, stream, stateWatchFrame(id, "github.com"))
		assertCancellationResult(t, receiveTestFrame(t, stream), "watch_result", "request-"+id, true)
	}
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "a"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("A monitor did not start")
	}
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "cancel-a-1", SubscriptionID: "a"})
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("A monitor was not cancelled")
	}
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "cancel-a-2", SubscriptionID: "a"})
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: "b-live", SubscriptionID: "b"})
	assertCancellationResult(t, receiveTestFrame(t, stream), "checkpoint_result", "b-live", true)
	// The B response proves the coordinator is live while A still owns its
	// source operation; neither overlapping cancellation may acknowledge yet.
	select {
	case frame := <-stream.outgoing:
		t.Fatalf("premature cancellation output: %s", frame)
	default:
	}
	releaseTarget()
	seen := map[string]bool{}
	for range 2 {
		result := receiveTestFrame(t, stream)
		assertCancellationResult(t, result, "cancel_result", result.RequestID, true)
		seen[result.RequestID] = true
	}
	if !seen["cancel-a-1"] || !seen["cancel-a-2"] {
		t.Fatalf("overlapping cancellation results: %v", seen)
	}
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "a"})
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "a", EventID: "1", Status: "accepted"})
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: "a-absent", SubscriptionID: "a"})
	assertCancellationResult(t, receiveTestFrame(t, stream), "checkpoint_result", "a-absent", false)
	sendTestFrame(t, stream, stateWatchFrame("fresh-a", "github.com"))
	assertCancellationResult(t, receiveTestFrame(t, stream), "watch_result", "request-fresh-a", true)
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "cancel_watch", RequestID: "absent", SubscriptionID: "a"})
	assertCancellationResult(t, receiveTestFrame(t, stream), "cancel_result", "absent", true)
}

func assertCancellationResult(t *testing.T, frame pluginprotocol.Frame, kind, requestID string, accepted bool) {
	t.Helper()
	if frame.Type != kind || frame.RequestID != requestID || frame.Accepted == nil || *frame.Accepted != accepted {
		t.Fatalf("got %+v, want %s %s accepted=%v", frame, kind, requestID, accepted)
	}
}

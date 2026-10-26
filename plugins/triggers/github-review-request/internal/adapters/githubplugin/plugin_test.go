package githubplugin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

func TestSessionValidatesWatchesWithoutBlockingProtocolHandling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &testSession{ctx: ctx, incoming: make(chan []byte, 4), outgoing: make(chan []byte, 4)}
	credentials := &blockingCredentials{slowStarted: make(chan struct{}), releaseSlow: make(chan struct{})}
	plugin := New(slog.New(slog.NewTextHandler(io.Discard, nil)), credentials, testGitHub{})
	done := make(chan error, 1)
	go func() { done <- plugin.Session(stream) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Session() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Session() did not stop after cancellation")
		}
	}()

	if frame := receiveTestFrame(t, stream); frame.Type != "ready" {
		t.Fatalf("first frame = %q, want ready", frame.Type)
	}
	sendTestFrame(t, stream, stateWatchFrame("slow", "slow.example.test"))
	select {
	case <-credentials.slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow credential lookup did not start")
	}
	sendTestFrame(t, stream, stateWatchFrame("fast", "fast.example.test"))

	fastResult := receiveTestFrame(t, stream)
	if fastResult.Type != "watch_result" || fastResult.RequestID != "request-fast" || fastResult.Accepted == nil || !*fastResult.Accepted {
		t.Fatalf("fast watch result = %+v, want accepted request-fast", fastResult)
	}
	select {
	case <-credentials.releaseSlow:
		t.Fatal("slow credential lookup was released before the fast result")
	default:
	}

	close(credentials.releaseSlow)
	slowResult := receiveTestFrame(t, stream)
	if slowResult.Type != "watch_result" || slowResult.RequestID != "request-slow" || slowResult.Accepted == nil || !*slowResult.Accepted {
		t.Fatalf("slow watch result = %+v, want accepted request-slow", slowResult)
	}
}

type testSession struct {
	ctx      context.Context
	incoming chan []byte
	outgoing chan []byte
}

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

func (s *testSession) Context() context.Context { return s.ctx }

type blockingCredentials struct {
	slowStarted chan struct{}
	releaseSlow chan struct{}
}

func (c *blockingCredentials) Token(ctx context.Context, host string) (string, error) {
	if host == "slow.example.test" {
		close(c.slowStarted)
		select {
		case <-c.releaseSlow:
			return "slow-token", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "fast-token", nil
}

type testGitHub struct{}

func (testGitHub) ResolveUser(context.Context, domain.WatchConfig, string) (application.User, error) {
	return application.User{ID: 7, Login: "operator"}, nil
}

func (testGitHub) Observe(context.Context, domain.WatchConfig, string) (map[uint64]string, error) {
	return map[uint64]string{}, nil
}

func stateWatchFrame(subscriptionID, host string) pluginprotocol.Frame {
	return pluginprotocol.Frame{
		Type:           "state_watch",
		RequestID:      "request-" + subscriptionID,
		SubscriptionID: subscriptionID,
		WatchArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"host":          jsonvalue.NewString(host),
			"poll_interval": jsonvalue.NewString("1h"),
		}),
		State: jsonvalue.NewNull(),
	}
}

func sendTestFrame(t *testing.T, stream *testSession, frame pluginprotocol.Frame) {
	t.Helper()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case stream.incoming <- payload:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending protocol frame")
	}
}

func receiveTestFrame(t *testing.T, stream *testSession) pluginprotocol.Frame {
	t.Helper()
	select {
	case payload := <-stream.outgoing:
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		if err != nil {
			t.Fatalf("parse plugin frame: %v", err)
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for plugin frame")
		return pluginprotocol.Frame{}
	}
}

var (
	_ application.Credentials      = (*blockingCredentials)(nil)
	_ application.GitHub           = testGitHub{}
	_ pluginprotocol.SessionServer = (*testSession)(nil)
)

func TestSessionDoesNotStartValidationForDuplicatePendingSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &testSession{ctx: ctx, incoming: make(chan []byte, 4), outgoing: make(chan []byte, 4)}
	credentials := &blockingCredentials{slowStarted: make(chan struct{}), releaseSlow: make(chan struct{})}
	plugin := New(slog.New(slog.NewTextHandler(io.Discard, nil)), credentials, testGitHub{})
	done := make(chan error, 1)
	go func() { done <- plugin.Session(stream) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Session() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Session() did not stop after cancellation")
		}
	}()
	if frame := receiveTestFrame(t, stream); frame.Type != "ready" {
		t.Fatalf("first frame = %q, want ready", frame.Type)
	}
	sendTestFrame(t, stream, stateWatchFrame("same", "slow.example.test"))
	select {
	case <-credentials.slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow credential lookup did not start")
	}
	duplicate := stateWatchFrame("same", "fast.example.test")
	duplicate.RequestID = "request-duplicate"
	sendTestFrame(t, stream, duplicate)
	result := receiveTestFrame(t, stream)
	if result.Type != "watch_result" || result.RequestID != "request-duplicate" || result.Accepted == nil || *result.Accepted || !strings.Contains(result.Error, "already active") {
		t.Fatalf("duplicate result = %+v, want rejection for active or pending watch", result)
	}
	close(credentials.releaseSlow)
	result = receiveTestFrame(t, stream)
	if result.RequestID != "request-same" || result.Accepted == nil || !*result.Accepted {
		t.Fatalf("original watch result = %+v, want accepted request-same", result)
	}
}

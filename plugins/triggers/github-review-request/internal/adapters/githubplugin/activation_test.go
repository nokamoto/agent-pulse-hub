package githubplugin

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
	"go.uber.org/mock/gomock"
)

func TestSessionWaitsForActivationAndNeverReactivatesQuiescedWatches(t *testing.T) {
	for _, quiesceBeforeGrant := range []bool{false, true} {
		name := "activate_once"
		if quiesceBeforeGrant {
			name = "quiesce_before_grant"
		}
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			credentials := NewMockCredentials(ctrl)
			github := NewMockGitHub(ctrl)
			credentials.EXPECT().Token(gomock.Any(), "github.com").Return("token", nil)
			github.EXPECT().ResolveUser(gomock.Any(), gomock.Any(), "token").Return(application.User{ID: 7, Login: "operator"}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			stream := &testSession{ctx: ctx, incoming: make(chan []byte, 8), outgoing: make(chan []byte, 8)}
			var diagnostics bytes.Buffer
			plugin := New(slog.New(slog.NewJSONHandler(&diagnostics, nil)), credentials, github)
			done := make(chan error, 1)
			go func() { done <- plugin.Session(stream) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("Session() = %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Error("Session did not join dormant/active watches")
				}
			}()
			ready := receiveTestFrame(t, stream)
			if len(ready.Capabilities) != 2 || ready.Capabilities[0] != pluginprotocol.RuntimeStateV2Capability || ready.Capabilities[1] != pluginprotocol.CancelWatchCapability {
				t.Fatalf("capabilities = %v", ready.Capabilities)
			}
			sendTestFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "unknown"})
			sendTestFrame(t, stream, stateWatchFrame("prepared", "github.com"))
			accepted := receiveTestFrame(t, stream)
			if accepted.Type != "watch_result" || accepted.Accepted == nil || !*accepted.Accepted {
				t.Fatalf("watch result = %+v", accepted)
			}
			// Observe has no expectation while dormant: accepting and checkpointing
			// a prepared watch cannot start any source work.
			checkpoint := func(id string, quiesce bool) {
				t.Helper()
				sendTestFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: id, SubscriptionID: "prepared", Quiesce: quiesce})
				result := receiveTestFrame(t, stream)
				if result.Type != "checkpoint_result" || result.RequestID != id || result.Accepted == nil || !*result.Accepted {
					t.Fatalf("checkpoint result = %+v", result)
				}
			}
			checkpoint("dormant", quiesceBeforeGrant)
			started := make(chan struct{})
			if !quiesceBeforeGrant {
				github.EXPECT().Observe(gomock.Any(), gomock.Any(), "token").DoAndReturn(func(ctx context.Context, _ domain.WatchConfig, _ string) (map[uint64]string, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				}).Times(1)
			}
			grant := pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "prepared"}
			sendTestFrame(t, stream, grant)
			sendTestFrame(t, stream, grant)
			checkpoint("after-grants", false)
			if !quiesceBeforeGrant {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("grant did not start the initial observation")
				}
			}
			checkpoint("stop", true)
			sendTestFrame(t, stream, grant)
			checkpoint("late-grant", false)
			if text := diagnostics.String(); !strings.Contains(text, "github_review_request_activation_ignored") || !strings.Contains(text, `"registration_id":"unknown"`) {
				t.Fatalf("unknown grant was not diagnosed: %s", text)
			}
		})
	}
}

func TestSessionSchedulesPollsFromResultAndWaitsForPendingEmissions(t *testing.T) {
	for _, interval := range []time.Duration{time.Minute, 250 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				testPollAndEmissionSchedule(t, interval)
			})
		})
	}
}

func testPollAndEmissionSchedule(t *testing.T, interval time.Duration) {
	ctrl := gomock.NewController(t)
	credentials := NewMockCredentials(ctrl)
	github := NewMockGitHub(ctrl)
	credentials.EXPECT().Token(gomock.Any(), "github.com").Return("token", nil)
	github.EXPECT().ResolveUser(gomock.Any(), gomock.Any(), "token").Return(application.User{ID: 7, Login: "operator"}, nil)
	polls := make(chan time.Time, 8)
	type reply struct {
		matches map[uint64]string
		err     error
	}
	responses := make(chan reply, 1)
	github.EXPECT().Observe(gomock.Any(), gomock.Any(), "token").DoAndReturn(func(ctx context.Context, _ domain.WatchConfig, _ string) (map[uint64]string, error) {
		polls <- time.Now()
		select {
		case result := <-responses:
			return result.matches, result.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}).AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &testSession{ctx: ctx, incoming: make(chan []byte, 8), outgoing: make(chan []byte, 8)}
	plugin := New(slog.New(slog.NewTextHandler(io.Discard, nil)), credentials, github)
	done := make(chan error, 1)
	go func() { done <- plugin.Session(stream) }()
	receiveTestFrame(t, stream)
	watchFrame := stateWatchFrame("watch", "github.com")
	if interval == time.Minute {
		watchFrame.WatchArgs = jsonvalue.NewObject(nil)
	} else {
		watchFrame.WatchArgs = jsonvalue.NewObject(map[string]*jsonvalue.Value{"poll_interval": jsonvalue.NewString(interval.String())})
	}
	sendTestFrame(t, stream, watchFrame)
	result := receiveTestFrame(t, stream)
	assertCancellationResult(t, result, "watch_result", "request-watch", true)
	sendTestFrame(t, stream, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "watch"})
	checkPoll := func(want time.Time) {
		t.Helper()
		synctest.Wait()
		select {
		case got := <-polls:
			if !got.Equal(want) {
				t.Fatalf("poll at %s, want %s", got, want)
			}
		default:
			t.Fatal("expected a poll")
		}
	}
	checkNoPoll := func() {
		t.Helper()
		synctest.Wait()
		select {
		case at := <-polls:
			t.Fatalf("unexpected poll at %s", at)
		default:
		}
	}
	checkpoint := func(id string, quiesce bool) string {
		t.Helper()
		sendTestFrame(t, stream, pluginprotocol.Frame{Type: "checkpoint", RequestID: id, SubscriptionID: "watch", Quiesce: quiesce})
		frame := receiveTestFrame(t, stream)
		assertCancellationResult(t, frame, "checkpoint_result", id, true)
		return string(frame.State.Marshal())
	}
	accept := func(id string) {
		t.Helper()
		sendTestFrame(t, stream, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "watch", EventID: id, Status: "accepted"})
	}

	checkPoll(time.Now())
	time.Sleep(3 * time.Second)
	checkNoPoll()
	a := "https://github.com/one/repo/pull/1"
	b := "https://github.com/two/repo/pull/1"
	responses <- reply{}
	time.Sleep(interval - time.Nanosecond)
	checkNoPoll()
	time.Sleep(time.Nanosecond)
	checkPoll(time.Now())

	responses <- reply{matches: map[uint64]string{1: a, 2: b}}
	first := receiveTestFrame(t, stream)
	if first.Type != "state_event" || first.Context != a || first.EventID != "1" {
		t.Fatalf("first event = %+v", first)
	}
	time.Sleep(interval)
	checkNoPoll()
	if state := checkpoint("pending", false); !strings.Contains(state, `"entries":[["1",1],["2",0]]`) {
		t.Fatalf("pending checkpoint = %s", state)
	}
	accept(first.EventID)
	second := receiveTestFrame(t, stream)
	if second.Type != "state_event" || second.Context != b || second.EventID != "2" {
		t.Fatalf("second event = %+v", second)
	}
	checkPoll(time.Now())
	accept(second.EventID)

	if interval < time.Second {
		steps := []struct {
			kind application.FailureKind
			wait time.Duration
		}{
			{application.FailureNetwork, time.Second},
			{application.FailureIncomplete, interval},
			{application.FailureService, 2 * time.Second},
			{"", interval},
			{application.FailureNetwork, time.Second},
		}
		for _, step := range steps {
			result := reply{matches: map[uint64]string{1: a, 2: b}}
			if step.kind != "" {
				result = reply{err: &application.Failure{Kind: step.kind}}
			}
			responses <- result
			time.Sleep(step.wait - time.Nanosecond)
			checkNoPoll()
			time.Sleep(time.Nanosecond)
			checkPoll(time.Now())
		}
	}
	if state := checkpoint("stop", true); !strings.Contains(state, `"entries":[["1",2],["2",2]]`) {
		t.Fatalf("final checkpoint = %s", state)
	}
	time.Sleep(interval)
	checkNoPoll()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

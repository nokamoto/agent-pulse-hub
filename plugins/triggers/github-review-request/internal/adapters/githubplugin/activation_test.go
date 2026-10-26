package githubplugin

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
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
			if len(ready.Capabilities) != 1 || ready.Capabilities[0] != pluginprotocol.RuntimeStateV2Capability {
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

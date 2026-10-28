package githubplugin

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func TestSignalsRejectIncompatibleStateBeforeCredentialsAndActivation(t *testing.T) {
	for _, schema := range []string{"github-pr-watch-v1", "github-pr-watch-v2"} {
		t.Run(schema, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			ctx, cancel := context.WithCancel(context.Background())
			stream := &testSession{ctx: ctx, incoming: make(chan []byte, 16), outgoing: make(chan []byte, 16)}
			var log bytes.Buffer
			p := New(slog.New(slog.NewTextHandler(&log, nil)), NewMockCredentials(ctrl), NewMockGitHub(ctrl), "github.com", time.Hour, WithSignals(true))
			done := make(chan error, 1)
			go func() { done <- p.Session(stream) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("Session did not stop")
				}
			}()
			_ = receiveFrame(t, stream)
			watch := stateWatch(t, "old")
			watch.Schema = schema
			watch.ChunkCount = "1"
			sendFrame(t, stream, watch)
			sendFrame(t, stream, pluginprotocol.Frame{Type: "state_stream_chunk", RequestID: watch.RequestID, SubscriptionID: "old", Index: "0", Data: jsonvalue.NewObject(map[string]*jsonvalue.Value{})})
			sendFrame(t, stream, pluginprotocol.Frame{Type: "state_stream_end", RequestID: watch.RequestID, SubscriptionID: "old", ChunkCount: "1"})
			assertResult(t, receiveFrame(t, stream), "watch_result", watch.RequestID, false)
			if !bytes.Contains(log.Bytes(), []byte("signal_state_incompatible")) {
				t.Fatal(log.String())
			}
		})
	}
}

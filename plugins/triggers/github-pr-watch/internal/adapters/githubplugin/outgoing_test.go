package githubplugin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func TestCheckpointTransportFailureUnblocksPendingControlBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := NewMockSessionServer(gomock.NewController(t))
	failure := errors.New("transport closed")
	started, release := make(chan struct{}), make(chan struct{})
	transport.EXPECT().Send(gomock.Any()).DoAndReturn(func([]byte) error { close(started); <-release; return failure })
	queued := newQueuedSession(ctx, transport)
	if err := queued.checkpoint(ctx, pluginprotocol.Frame{Type: "checkpoint_stream_begin", RequestID: "checkpoint", SubscriptionID: "watch", Schema: "github-pr-watch-v2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("checkpoint transport did not start")
	}
	result := make(chan error, 1)
	go func() { result <- queued.sendBarrier([]byte("positive watch_result")) }()
	// The writer is held in the previous checkpoint Send. Observe and put back
	// the accepted barrier queue entry before causing that Send to fail.
	var pending outgoing
	select {
	case pending = <-queued.control:
	case <-time.After(time.Second):
		t.Fatal("control barrier was not queued")
	}
	queued.control <- pending
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, failure) {
			t.Fatalf("barrier error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed writer stranded a control barrier")
	}
	// Draining the main-loop failure notification does not hide failure from
	// future queue and barrier waiters; writerDone is a broadcast fence.
	select {
	case <-queued.failed:
	case <-time.After(time.Second):
		t.Fatal("main-loop failure missing")
	}
	if err := queued.Send([]byte("late")); !errors.Is(err, failure) {
		t.Fatalf("late send=%v", err)
	}
	if err := queued.sendBarrier([]byte("late barrier")); !errors.Is(err, failure) {
		t.Fatalf("late barrier=%v", err)
	}
	if err := queued.checkpoint(ctx, pluginprotocol.Frame{Type: "checkpoint_stream_end", RequestID: "checkpoint", SubscriptionID: "watch", ChunkCount: "1"}); !errors.Is(err, failure) {
		t.Fatalf("late end=%v", err)
	}
}

func TestExpiredCheckpointIsDroppedBeforeTransport(t *testing.T) {
	transport := NewMockSessionServer(gomock.NewController(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued := &queuedSession{SessionServer: transport, ctx: context.Background()}
	done := make(chan struct{})
	if err := queued.dispatch(outgoing{payload: []byte("expired"), ctx: ctx, done: done}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("expired end did not release its immutable view")
	}
	transport.EXPECT().Send([]byte("control")).Return(nil)
	if err := queued.dispatch(outgoing{payload: []byte("control"), ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointDrainReleasesExpiredQueueBeforeCancellationResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := NewMockSessionServer(gomock.NewController(t))
	started, release := make(chan struct{}), make(chan struct{})
	first := transport.EXPECT().Send([]byte("in-flight checkpoint")).DoAndReturn(func([]byte) error {
		close(started)
		<-release
		return nil
	})
	transport.EXPECT().Send([]byte("cancel-result")).Return(nil).After(first)
	queued := newQueuedSession(ctx, transport)
	queued.checkpoints <- outgoing{payload: []byte("in-flight checkpoint"), ctx: ctx}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("checkpoint did not enter transport")
	}
	expired, expire := context.WithCancel(ctx)
	expire()
	for range cap(queued.checkpoints) {
		queued.checkpoints <- outgoing{payload: []byte("expired checkpoint"), ctx: expired}
	}
	drained := make(chan error, 1)
	go func() { drained <- queued.drainCheckpoints() }()
	select {
	case err := <-drained:
		t.Fatalf("drain completed before transport released: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled checkpoint queue did not drain")
	}
	if len(queued.checkpoints) != 0 {
		t.Fatal("checkpoint payloads retained after drain")
	}
	if err := queued.sendBarrier([]byte("cancel-result")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointQueueKeepsOneDeadlineAndBoundsBufferedObjects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := NewMockSessionServer(gomock.NewController(t))
	queued := &queuedSession{SessionServer: transport, ctx: ctx, checkpoints: make(chan outgoing, 4)}
	requestCtx, requestCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	objects := make([]*jsonvalue.Value, 20)
	for i := range objects {
		objects[i] = jsonvalue.NewObject(map[string]*jsonvalue.Value{})
	}
	go func() {
		sendCheckpoint(requestCtx, requestCancel, queued, pluginprotocol.Frame{RequestID: "checkpoint", SubscriptionID: "watch"}, objects)
		close(done)
	}()
	// Drain four records under explicit control. The sender is subject to the
	// same context before and after each record, rather than renewing a timer.
	for i := 0; i < 4; i++ {
		select {
		case message := <-queued.checkpoints:
			if message.ctx != requestCtx {
				t.Fatal("checkpoint deadline changed")
			}
		case <-time.After(time.Second):
			t.Fatal("checkpoint sender stalled")
		}
	}
	requestCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expired sender retained its checkpoint")
	}
	if len(queued.checkpoints) > 4 {
		t.Fatal("unbounded checkpoint queue")
	}
	for len(queued.checkpoints) > 0 {
		if err := queued.dispatch(<-queued.checkpoints); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestoreStagesUntilEndAndRejectsInvalidOrder(t *testing.T) {
	for _, test := range []struct{ name, index, count string }{{"out of order", "1", "1"}, {"excess chunk", "0", "0"}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := NewMockSessionServer(gomock.NewController(t))
			queued := &queuedSession{SessionServer: transport, ctx: ctx, control: make(chan outgoing, 16)}
			p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, "github.com", time.Minute)
			watches := map[string]*watch{}
			pending := map[string]*restoring{}
			validations := make(chan validation, 2)
			observations := make(chan observation)
			begin := pluginprotocol.Frame{Type: "state_stream_watch", RequestID: "request", SubscriptionID: "watch", Schema: "github-pr-watch-v2", ChunkCount: "1"}
			if test.count == "0" {
				begin.Schema = ""
				begin.ChunkCount = "0"
			}
			if err := p.handle(ctx, queued, watches, pending, validations, observations, begin); err != nil {
				t.Fatal(err)
			}
			if len(watches) != 0 || len(queued.control) != 0 {
				t.Fatal("partial state was accepted")
			}
			if err := p.handle(ctx, queued, watches, pending, validations, observations, pluginprotocol.Frame{Type: "state_stream_chunk", RequestID: "request", SubscriptionID: "watch", Index: test.index, Data: jsonvalue.NewObject(nil)}); err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 || len(watches) != 0 {
				t.Fatal("invalid state was retained")
			}
			message := <-queued.control
			frame, err := pluginprotocol.ParsePayload(message.payload, pluginprotocol.PluginToDaemon)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type != "watch_result" || frame.Accepted == nil || *frame.Accepted {
				t.Fatal("invalid restoration was accepted")
			}
		})
	}
}

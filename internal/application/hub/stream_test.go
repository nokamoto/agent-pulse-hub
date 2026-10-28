package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func TestRuntimeFilesystemQueuesBoundBackpressureAndYieldAcrossWatches(t *testing.T) {
	pool := newRuntimeFSPool()
	release := make(chan struct{})
	started := make(chan struct{})
	if err := pool.submit(context.Background(), "a", func() { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	for i := 0; i < 4; i++ {
		if err := pool.submit(context.Background(), "a", func() {}); err != nil {
			t.Fatal(err)
		}
	}
	for _, queue := range pool.queues {
		if cap(queue) != 4 {
			t.Fatal("filesystem queue is not bounded to four objects")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pool.submit(ctx, "a", func() { t.Error("expired queue item ran") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("full queue = %v", err)
	}
	other := make(chan struct{})
	if err := pool.submit(context.Background(), "b", func() { close(other) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("one watch blocked unrelated filesystem worker")
	}
	close(release)
	pool.close()
	if err := pool.submit(context.Background(), "a", func() {}); err == nil {
		t.Fatal("closed worker pool accepted job")
	}
}

type memoryRuntimeTransaction struct {
	appendErr          error
	published, aborted bool
	start, release     chan struct{}
}

func (t *memoryRuntimeTransaction) Append(uint64, []byte) error { return t.appendErr }
func (t *memoryRuntimeTransaction) Commit(_ uint64, publish func(func() error) error) (uint64, error) {
	if t.start != nil {
		close(t.start)
		<-t.release
	}
	if err := publish(func() error { t.published = true; return nil }); err != nil {
		return 0, err
	}
	return 1, nil
}
func (t *memoryRuntimeTransaction) Abort() error { t.aborted = true; return nil }

type memoryRuntimeStreamStore struct {
	tx       *memoryRuntimeTransaction
	beginErr error
}

func (s *memoryRuntimeStreamStore) LoadRuntimeSources([]domain.Subscription, uint64) (map[domain.SubscriptionID]RuntimeSource, error) {
	return nil, nil
}

func (s *memoryRuntimeStreamStore) BeginRuntimeStream(domain.Subscription, string, uint64) (RuntimeStreamTransaction, error) {
	return s.tx, s.beginErr
}

func TestStreamSinkPublicationFenceExpiryAndFilesystemFailures(t *testing.T) {
	for _, kind := range []string{"success", "removed_owner", "aborted", "append_failure", "begin_failure", "expired_end", "valid_end_slow_filesystem"} {
		t.Run(kind, func(t *testing.T) {
			h := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
			defer h.Shutdown()
			watch := &runtimeWatch{registration: domain.Subscription{ID: "s"}}
			h.runtimeWatches["s"] = watch
			tx := &memoryRuntimeTransaction{}
			store := &memoryRuntimeStreamStore{tx: tx}
			if kind == "append_failure" {
				tx.appendErr = errors.New("injected chunk write failure")
			}
			if kind == "begin_failure" {
				store.beginErr = errors.New("injected staging failure")
			}
			sink := h.newRuntimeStreamSink(watch, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := sink.Begin(ctx, "schema"); err != nil {
				t.Fatal(err)
			}
			if err := sink.Append(ctx, 0, []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if kind == "removed_owner" {
				h.mu.Lock()
				delete(h.runtimeWatches, "s")
				h.mu.Unlock()
			}
			if kind == "aborted" {
				sink.Abort(errors.New("stream expired"))
			}
			if kind == "expired_end" {
				cancel()
			}
			if kind == "valid_end_slow_filesystem" {
				tx.start = make(chan struct{})
				tx.release = make(chan struct{})
			}
			err := sink.Complete(ctx, 1)
			if kind == "expired_end" || kind == "aborted" {
				if err == nil {
					t.Fatal("expired stream end accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if kind == "valid_end_slow_filesystem" {
				<-tx.start
				cancel()
				close(tx.release)
			}
			select {
			case result := <-sink.done:
				success := kind == "success" || kind == "valid_end_slow_filesystem"
				if success != (result.generation == 1 && result.err == nil) {
					t.Fatalf("result=%+v", result)
				}
				if tx.published != success {
					t.Fatalf("publication=%v", tx.published)
				}
			case <-time.After(time.Second):
				t.Fatal("stream result did not complete")
			}
			// An expired correlation may arrive after daemon shutdown. It cannot queue
			// a new filesystem operation or panic by sending to a closed worker queue.
			h.Shutdown()
			sink.Abort(errors.New("late frame after shutdown"))
		})
	}
}

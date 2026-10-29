package manualplugin

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
)

func TestCancellationJoinsMonitorAndReleasesOnlyItsPath(t *testing.T) {
	files := newFakeTriggerFiles()
	p := newWithFiles(slog.New(slog.NewTextHandler(io.Discard, nil)), files)
	p.active = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &recordingSession{ctx: ctx}
	aCtx, aCancel := context.WithCancel(ctx)
	aPath := filepath.Join(files.directory, "a")
	a := &watcher{subscriptionID: "a", target: watchTarget{path: aPath, directory: files.directory}, cancel: aCancel, done: make(chan struct{})}
	p.watches["a"], p.paths[aPath] = a, "a"
	if err := p.acceptWatch(ctx, stream, watchFrame("b", filepath.Join(files.directory, "b"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.stopWatches)
	releaseTarget := sync.OnceFunc(func() { close(a.done) })
	t.Cleanup(releaseTarget)
	completed := make(chan error, 2)
	for _, requestID := range []string{"cancel-a-1", "cancel-a-2"} {
		go func() {
			completed <- p.cancelWatch(stream, pluginprotocol.Frame{RequestID: requestID, SubscriptionID: "a"})
		}()
	}
	select {
	case <-aCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the target")
	}
	select {
	case err := <-completed:
		t.Fatalf("cancellation completed before monitor exit: %v", err)
	default:
	}
	p.mu.Lock()
	b := p.watches["b"]
	reserved := p.paths[aPath] == "a"
	p.mu.Unlock()
	if b == nil || !reserved {
		t.Fatal("cancellation released resources before the monitor stopped or removed B")
	}
	releaseTarget()
	for range 2 {
		select {
		case err := <-completed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not complete after monitor exit")
		}
	}
	if err := p.cancelWatch(stream, pluginprotocol.Frame{RequestID: "absent", SubscriptionID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := p.acceptWatch(ctx, stream, watchFrame("fresh-a", aPath)); err != nil {
		t.Fatal(err)
	}
	frames := stream.snapshot()
	result := frames[len(frames)-1]
	if result.Type != "watch_result" || result.Accepted == nil || !*result.Accepted {
		t.Fatalf("fresh registration after resource release: %+v", result)
	}
	results := 0
	for _, frame := range stream.snapshot() {
		if frame.Type == "cancel_result" {
			results++
			if frame.Accepted == nil || !*frame.Accepted {
				t.Fatalf("cancellation result: %+v", frame)
			}
		}
	}
	if results != 3 || p.watches["b"] != b {
		t.Fatalf("cancel results=%d or B changed", results)
	}
}

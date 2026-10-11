//go:build windows

package pluginprocess

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func unitProcess(t *testing.T) (*Process, *Mockclock) {
	t.Helper()
	ctrl := gomock.NewController(t)
	p := New(Config{Name: "fixture", Executable: "fixture"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := NewMockclock(ctrl)
	p.clock = clock
	clock.EXPECT().Now().Return(time.Unix(100, 0)).AnyTimes()
	return p, clock
}

func neverTimer(t *testing.T) *Mocktimer {
	t.Helper()
	mock := NewMocktimer(gomock.NewController(t))
	mock.EXPECT().C().Return(make(chan time.Time)).AnyTimes()
	mock.EXPECT().Stop().Return(true).AnyTimes()
	return mock
}

func TestProcessRejectsStartupFailureWithoutStartingStreams(t *testing.T) {
	p, _ := unitProcess(t)
	launcher := NewMockprocessLauncher(gomock.NewController(t))
	failure := errors.New("launcher failed")
	launcher.EXPECT().Start(p.config).Return(nil, failure)
	p.launcher = launcher
	if err := p.Start(context.Background(), hub.PluginHandlers{}); !errors.Is(err, failure) {
		t.Fatalf("error %v", err)
	}
	if p.started || p.ready {
		t.Fatal("failed child became usable")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWatchResultDecisionsAndReadOrdering(t *testing.T) {
	for _, test := range []struct {
		name          string
		accepted      bool
		activationErr error
		wantCode      string
		wantFailed    bool
	}{
		{name: "accepted", accepted: true},
		{name: "rejected", wantCode: "watch_rejected"},
		{name: "activation uncertain", accepted: true, activationErr: errors.New("stopped"), wantCode: "plugin_unavailable", wantFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, clock := unitProcess(t)
			clock.EXPECT().NewTimer(10 * time.Second).Return(neverTimer(t)).AnyTimes()
			p.ready = true
			activated := false
			eventAfterActivation := false
			p.handlers.Event = func(_ string, _ string) { eventAfterActivation = activated }
			p.stdin = &memoryWriter{write: func(data []byte) (int, error) {
				value, err := protocol.ParseFrame(bytes.TrimSuffix(data, []byte{'\n'}))
				if err != nil {
					t.Error(err)
				}
				frame, err := pluginprotocol.Decode(value, pluginprotocol.DaemonToPlugin)
				if err != nil {
					t.Error(err)
				}
				p.handleFrame(pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: &test.accepted})
				p.handleFrame(pluginprotocol.Frame{Type: "event", SubscriptionID: frame.SubscriptionID, Context: "immediate"})
				return len(data), nil
			}}
			err := p.Watch(context.Background(), hub.WatchRequest{RequestID: "opaque", SubscriptionID: domain.SubscriptionID("opaque-sub"), Arguments: jsonvalue.NewObject(nil)}, func() error { activated = true; return test.activationErr })
			var watchErr *hub.WatchError
			if test.wantCode == "" {
				if err != nil || !eventAfterActivation {
					t.Fatalf("error %v immediate event activation %v", err, eventAfterActivation)
				}
			} else if !errors.As(err, &watchErr) || watchErr.Code != test.wantCode {
				t.Fatalf("error %v want %s", err, test.wantCode)
			}
			if p.failed != test.wantFailed {
				t.Fatalf("failed=%v", p.failed)
			}
		})
	}
}

type memoryWriter struct {
	write func([]byte) (int, error)
	close func() error
}

func (w *memoryWriter) Write(data []byte) (int, error) { return w.write(data) }
func (w *memoryWriter) Close() error {
	if w.close != nil {
		return w.close()
	}
	return nil
}

func TestWatchRejectsOversizedFrameWithoutStoppingPlugin(t *testing.T) {
	p, _ := unitProcess(t)
	p.ready = true
	err := p.Watch(context.Background(), hub.WatchRequest{RequestID: "r", SubscriptionID: "s", Arguments: jsonvalue.NewObject(map[string]*jsonvalue.Value{"data": jsonvalue.NewString(strings.Repeat("x", protocol.MaxFrameBytes))})}, func() error { t.Fatal("oversize activated"); return nil })
	var watchErr *hub.WatchError
	if !errors.As(err, &watchErr) || watchErr.Code != "resource_exhausted" || p.failed {
		t.Fatalf("error %v failed %v", err, p.failed)
	}
}

func TestWatchFixedDeadlineIncludesBlockedWrite(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing acknowledgement", true: "blocked write"}[blocked], func(t *testing.T) {
			p, clock := unitProcess(t)
			p.ready = true
			trigger := make(chan time.Time, 1)
			expired := NewMocktimer(gomock.NewController(t))
			expired.EXPECT().C().Return(trigger).AnyTimes()
			expired.EXPECT().Stop().Return(true).AnyTimes()
			if blocked {
				clock.EXPECT().NewTimer(10 * time.Second).Return(expired)
			} else {
				gomock.InOrder(clock.EXPECT().NewTimer(10*time.Second).Return(neverTimer(t)), clock.EXPECT().NewTimer(10*time.Second).Return(expired))
			}
			written := make(chan struct{})
			closed := make(chan struct{})
			var once sync.Once
			p.stdin = &memoryWriter{write: func(data []byte) (int, error) {
				close(written)
				if blocked {
					<-closed
					return 0, io.ErrClosedPipe
				}
				return len(data), nil
			}, close: func() error { once.Do(func() { close(closed) }); return nil }}
			done := make(chan error, 1)
			go func() {
				done <- p.Watch(context.Background(), hub.WatchRequest{RequestID: "r", SubscriptionID: "s", Arguments: jsonvalue.NewObject(nil)}, func() error { t.Error("timeout activated"); return nil })
			}()
			<-written
			trigger <- time.Unix(110, 0)
			var watchErr *hub.WatchError
			if err := <-done; !errors.As(err, &watchErr) || !watchErr.Unavailable || !p.failed || len(p.pending) != 0 {
				t.Fatalf("timeout result %v failed %v pending %d", err, p.failed, len(p.pending))
			}
		})
	}
}

func TestFailedAndZeroByteWritesInvalidateOnlyTheirPlugin(t *testing.T) {
	for _, writeErr := range []error{io.ErrClosedPipe, nil} {
		p, clock := unitProcess(t)
		clock.EXPECT().NewTimer(10 * time.Second).Return(neverTimer(t)).AnyTimes()
		p.ready = true
		unavailable := 0
		p.handlers.Unavailable = func(error) { unavailable++ }
		p.stdin = &memoryWriter{write: func([]byte) (int, error) { return 0, writeErr }}
		err := p.Watch(context.Background(), hub.WatchRequest{RequestID: "r", SubscriptionID: "s", Arguments: jsonvalue.NewObject(nil)}, func() error { t.Error("write failure activated"); return nil })
		var watchErr *hub.WatchError
		if !errors.As(err, &watchErr) || !watchErr.Unavailable || unavailable != 1 || !p.failed || len(p.pending) != 0 {
			t.Fatalf("result %v unavailable %d pending %d", err, unavailable, len(p.pending))
		}
	}
}

func TestUnknownDuplicateAndLateWatchResults(t *testing.T) {
	for _, name := range []string{"unknown", "duplicate", "late", "out of order"} {
		t.Run(name, func(t *testing.T) {
			p, _ := unitProcess(t)
			p.ready = true
			accepted := true
			calls := 0
			pending := &watchPending{deadline: time.Unix(110, 0), activate: func() error { calls++; return nil }, result: make(chan watchOutcome, 1)}
			if name == "late" {
				pending.deadline = time.Unix(99, 0)
			}
			if name != "unknown" {
				p.pending["r"] = pending
			}
			if name == "out of order" {
				p.pending["other"] = &watchPending{deadline: time.Unix(110, 0), activate: func() error { calls++; return nil }, result: make(chan watchOutcome, 1)}
				p.handleWatchResult(pluginprotocol.Frame{RequestID: "other", Accepted: &accepted})
			}
			p.handleWatchResult(pluginprotocol.Frame{RequestID: "r", Accepted: &accepted})
			if name == "duplicate" {
				p.handleWatchResult(pluginprotocol.Frame{RequestID: "r", Accepted: &accepted})
			}
			want := 1
			if name == "unknown" || name == "late" {
				want = 0
			}
			if name == "out of order" {
				want = 2
			}
			if calls != want || p.failed != (name == "late") {
				t.Fatalf("activations %d failed %v", calls, p.failed)
			}
		})
	}
}

func TestReadStreamRecoversMalformedBoundedFrame(t *testing.T) {
	p, _ := unitProcess(t)
	ready, events := 0, 0
	p.handlers = hub.PluginHandlers{Ready: func() { ready++ }, Event: func(_ string, text string) {
		if text != "valid" {
			t.Errorf("context %q", text)
		}
		events++
	}}
	stream := append([]byte("bad\n"), []byte("{\"version\":1,\"type\":\"ready\"}\r\n{\"version\":1,\"type\":\"ready\"}\n{\"version\":1,\"type\":\"event\",\"subscription_id\":\"opaque\",\"context\":\"valid\"}\n")...)
	p.readStdout(bytes.NewReader(stream))
	if ready != 1 || events != 1 || !p.failed {
		t.Fatalf("ready %d events %d failed %v", ready, events, p.failed)
	}
}

func TestReadStreamRejectsPartialAndOversizedInput(t *testing.T) {
	for _, input := range []string{`{"version":1,"type":"ready"}`, strings.Repeat("x", protocol.MaxFrameBytes) + "\n"} {
		p, _ := unitProcess(t)
		p.readStdout(strings.NewReader(input))
		if !p.failed || p.ready {
			t.Fatalf("partial or oversized frame failed=%v ready=%v", p.failed, p.ready)
		}
	}
}

func TestDrainStderrLogsBoundedSanitizedDiagnostics(t *testing.T) {
	var output bytes.Buffer
	p := New(Config{Name: "manual"}, slog.New(slog.NewJSONHandler(&output, nil)))
	p.drainStderr(strings.NewReader("claimed file CLAIMED_PATH failed \x1b[31m" + strings.Repeat("x", maxPluginDiagnosticBytes+32) + "\n"))
	logged := output.String()
	if !strings.Contains(logged, "CLAIMED_PATH") || !strings.Contains(logged, `"truncated":true`) || strings.Contains(logged, `\u001b`) {
		t.Fatalf("diagnostic %s", logged)
	}
}

func TestInterruptedAcceptedWatchWaitsForCompletedActivation(t *testing.T) {
	p, _ := unitProcess(t)
	pending := &watchPending{deadline: time.Unix(99, 0), result: make(chan watchOutcome)}
	done := make(chan error, 1)
	go func() { done <- p.interruptWatch("r", pending, context.Canceled, "interrupted") }()
	pending.result <- watchOutcome{accepted: true}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedWatchWinsCancellationDuringActivation(t *testing.T) {
	p, clock := unitProcess(t)
	clock.EXPECT().NewTimer(10 * time.Second).Return(neverTimer(t)).AnyTimes()
	p.ready = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := true
	p.stdin = &memoryWriter{write: func(data []byte) (int, error) {
		p.handleWatchResult(pluginprotocol.Frame{RequestID: "r", Accepted: &accepted})
		return len(data), nil
	}}
	err := p.Watch(ctx, hub.WatchRequest{RequestID: "r", SubscriptionID: "s", Arguments: jsonvalue.NewObject(nil)}, func() error { cancel(); return nil })
	if err != nil || p.failed {
		t.Fatalf("accepted watch error %v failed %v", err, p.failed)
	}
}

func TestUnavailableAndPreReadyFramesNeverReachCallbacks(t *testing.T) {
	for _, state := range []string{"not ready", "failed", "stopping"} {
		p, _ := unitProcess(t)
		p.ready = state != "not ready"
		p.failed = state == "failed"
		p.stopping = state == "stopping"
		p.handlers.Event = func(string, string) { t.Errorf("event admitted in state %s", state) }
		p.handleFrame(pluginprotocol.Frame{Type: "event", SubscriptionID: "opaque", Context: "not admitted"})
	}
}

func TestReadinessDeadlineAndCancellationUseMockedTime(t *testing.T) {
	for _, mode := range []string{"ready", "timeout", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p, clock := unitProcess(t)
			ctrl := gomock.NewController(t)
			launcher, child := NewMockprocessLauncher(ctrl), NewMockchildProcess(ctrl)
			p.launcher = launcher
			reader, writer := io.Pipe()
			finished := make(chan struct{})
			var finish sync.Once
			stdin := &memoryWriter{write: func(data []byte) (int, error) { return len(data), nil }}
			launcher.EXPECT().Start(p.config).Return(child, nil)
			child.EXPECT().Stdin().Return(stdin)
			child.EXPECT().Stdout().Return(reader)
			child.EXPECT().Stderr().Return(io.NopCloser(strings.NewReader("")))
			child.EXPECT().Wait().DoAndReturn(func() error { <-finished; return nil })
			child.EXPECT().Terminate().Do(func() { finish.Do(func() { close(finished) }) }).Times(1)
			child.EXPECT().Close().Do(func() { _ = reader.Close(); _ = writer.Close() }).Times(1)
			deadline := make(chan time.Time, 1)
			timer := NewMocktimer(ctrl)
			timer.EXPECT().C().Return(deadline).AnyTimes()
			timer.EXPECT().Stop().Return(true)
			clock.EXPECT().NewTimer(10 * time.Second).Return(timer).Times(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "ready" {
				go func() { _, _ = io.WriteString(writer, "{\"version\":1,\"type\":\"ready\"}\n") }()
			} else if mode == "timeout" {
				deadline <- time.Unix(110, 0)
			} else {
				cancel()
			}
			err := p.Start(ctx, hub.PluginHandlers{})
			if (err == nil) != (mode == "ready") {
				t.Fatalf("mode %s error %v", mode, err)
			}
			p.fail(errors.New("test cleanup"))
			select {
			case <-p.doneCh:
			case <-time.After(time.Second):
				t.Fatal("terminated child was not reaped")
			}
		})
	}
}

func TestShutdownGraceTerminatesAndAwaitsChildIncludingFailedProcess(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal grace", true: "already unavailable"}[failed], func(t *testing.T) {
			p, clock := unitProcess(t)
			p.started, p.ready, p.failed = true, true, failed
			child := NewMockchildProcess(gomock.NewController(t))
			p.child = child
			child.EXPECT().Terminate().Do(func() { close(p.doneCh) }).Times(1)
			writes := 0
			p.stdin = &memoryWriter{write: func(data []byte) (int, error) {
				writes++
				if failed {
					return 0, io.ErrClosedPipe
				}
				if string(data) != "{\"type\":\"shutdown\",\"version\":1}\n" {
					t.Errorf("shutdown %q", data)
				}
				return len(data), nil
			}}
			calls := 0
			clock.EXPECT().NewTimer(gomock.Any()).DoAndReturn(func(duration time.Duration) timer {
				calls++
				if calls <= 3 && duration != 5*time.Second {
					t.Errorf("grace timer %v", duration)
				}
				if calls == 3 {
					expired := NewMocktimer(gomock.NewController(t))
					ch := make(chan time.Time, 1)
					ch <- time.Unix(105, 0)
					expired.EXPECT().C().Return(ch)
					return expired
				}
				return neverTimer(t)
			}).AnyTimes()
			if err := p.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if writes != 1 || !p.stopping {
				t.Fatalf("shutdown writes %d stopping %v", writes, p.stopping)
			}
		})
	}
}

func TestShutdownBlockedWriteCannotExtendFiveSecondGrace(t *testing.T) {
	p, clock := unitProcess(t)
	p.started, p.ready = true, true
	child := NewMockchildProcess(gomock.NewController(t))
	p.child = child
	release, writeFinished := make(chan struct{}), make(chan struct{})
	p.stdin = &memoryWriter{write: func([]byte) (int, error) { <-release; close(writeFinished); return 0, io.ErrClosedPipe }}
	child.EXPECT().Terminate().Do(func() { close(release); close(p.doneCh) })
	calls := 0
	clock.EXPECT().NewTimer(gomock.Any()).DoAndReturn(func(duration time.Duration) timer {
		calls++
		if calls == 1 || duration == time.Second {
			return neverTimer(t)
		}
		if duration != 5*time.Second {
			t.Errorf("grace timer %v", duration)
		}
		mock := NewMocktimer(gomock.NewController(t))
		expired := make(chan time.Time, 1)
		expired <- time.Unix(105, 0)
		mock.EXPECT().C().Return(expired).AnyTimes()
		mock.EXPECT().Stop().Return(true).AnyTimes()
		return mock
	}).AnyTimes()
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-writeFinished
}

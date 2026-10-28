package pluginprocess

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type memoryStreamSink struct {
	mu           sync.Mutex
	schema       string
	count, ended uint64
	aborted      bool
	failure      error
}

func (s *memoryStreamSink) Begin(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schema = "begun"
	return s.failure
}

func (s *memoryStreamSink) Append(_ context.Context, index uint64, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aborted {
		return errors.New("aborted")
	}
	if index != s.count {
		return errors.New("wrong index")
	}
	s.count++
	return s.failure
}

func (s *memoryStreamSink) Complete(_ context.Context, count uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = count
	return s.failure
}
func (s *memoryStreamSink) Abort(error) { s.mu.Lock(); s.aborted = true; s.mu.Unlock() }
func streamProcess(t *testing.T) (*Process, *memorySession) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	p.capabilities[pluginprotocol.RuntimeStateV2Capability] = true
	p.capabilities[pluginprotocol.RuntimeStateStreamCapability] = true
	return p, session
}

func TestStreamWatchZeroAndLegacyHandoffBeforeAcceptance(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy], func(t *testing.T) {
			p, session := streamProcess(t)
			result := make(chan error, 1)
			request := hub.WatchRequest{RequestID: "w", SubscriptionID: "s", Arguments: jsonvalue.NewObject(nil), RuntimeState: true}
			if legacy {
				request.State = jsonObject(t, `{"schema":"old","payload":{"n":9007199254740993}}`)
			}
			go func() { result <- p.Watch(context.Background(), request, func() error { return nil }) }()
			begin := readFrame(t, session)
			if begin.Type != "state_stream_watch" {
				t.Fatal(begin.Type)
			}
			if legacy {
				chunk := readFrame(t, session)
				if chunk.Index != "0" || chunk.Data == nil {
					t.Fatal("legacy chunk missing")
				}
			} else if begin.Schema != "" || begin.ChunkCount != "0" {
				t.Fatal("fresh watch not null")
			}
			end := readFrame(t, session)
			if end.Type != "state_stream_end" {
				t.Fatal(end.Type)
			}
			session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "w", Accepted: boolPointer(true)})
			if err := await(t, result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamCheckpointValidAndRejectedCorrelations(t *testing.T) {
	for _, kind := range []string{"valid", "foreign", "duplicate_begin", "wrong_index", "wrong_count", "legacy_result", "negative_after_begin", "budget", "sink_failure", "timeout", "malformed", "malformed_duplicate"} {
		t.Run(kind, func(t *testing.T) {
			p, session := streamProcess(t)
			p.watches["s"] = &watchState{active: true, stateful: true, activationRequired: true, pending: make(map[string]bool), notices: make(map[string]*noticeSend)}
			if kind == "budget" {
				p.config.RuntimeStateMaxBytes = 1
			}
			sink := &memoryStreamSink{}
			if kind == "sink_failure" {
				sink.failure = errors.New("write queue exhausted")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- p.CheckpointStream(ctx, hub.RuntimeCheckpointRequest{SubscriptionID: "s", Quiesce: true}, sink)
			}()
			request := readFrame(t, session)
			send := func(frame pluginprotocol.Frame) {
				frame.RequestID = request.RequestID
				frame.SubscriptionID = "s"
				p.handleFrame(frame)
			}
			if kind == "timeout" {
				cancel()
				if err := await(t, done); err == nil {
					t.Fatal("timeout succeeded")
				}
				send(pluginprotocol.Frame{Type: "checkpoint_stream_begin", Schema: "late"})
				if processFailed(p) {
					t.Fatal("checkpoint timeout killed healthy input")
				}
				return
			}
			send(pluginprotocol.Frame{Type: "checkpoint_stream_begin", Schema: "schema"})
			switch kind {
			case "malformed_duplicate":
				p.rejectMalformedStream([]byte(`{"version":2,"type":"checkpoint_stream_chunk","request_id":"` + request.RequestID + `","subscription_id":"s","index":"0","index":"0","data":{}}`))
				send(pluginprotocol.Frame{Type: "checkpoint_stream_chunk", Index: "0", Data: jsonvalue.NewObject(nil)})
				send(pluginprotocol.Frame{Type: "checkpoint_stream_end", ChunkCount: "1"})
			case "malformed":
				p.rejectMalformedStream([]byte(`{"version":2,"type":"checkpoint_stream_chunk","request_id":"` + request.RequestID + `","subscription_id":"s","index":"0","data":{},"extra":true}`))
				send(pluginprotocol.Frame{Type: "checkpoint_stream_chunk", Index: "0", Data: jsonvalue.NewObject(nil)})
				send(pluginprotocol.Frame{Type: "checkpoint_stream_end", ChunkCount: "1"})
			case "foreign":
				p.handleFrame(pluginprotocol.Frame{Type: "checkpoint_stream_chunk", RequestID: request.RequestID, SubscriptionID: "foreign", Index: "0", Data: jsonvalue.NewObject(nil)})
			case "duplicate_begin":
				send(pluginprotocol.Frame{Type: "checkpoint_stream_begin", Schema: "schema"})
			case "legacy_result":
				send(pluginprotocol.Frame{Type: "checkpoint_result", Accepted: boolPointer(true), State: jsonObject(t, `{"schema":"old","payload":{}}`)})
			case "negative_after_begin":
				send(pluginprotocol.Frame{Type: "checkpoint_result", Accepted: boolPointer(false), Error: "source failed"})
			default:
				index := "0"
				if kind == "wrong_index" {
					index = "1"
				}
				send(pluginprotocol.Frame{Type: "checkpoint_stream_chunk", Index: index, Data: jsonvalue.NewObject(nil)})
				count := "1"
				if kind == "wrong_count" {
					count = "2"
				}
				send(pluginprotocol.Frame{Type: "checkpoint_stream_end", ChunkCount: count})
			}
			err := await(t, done)
			if (err == nil) != (kind == "valid") {
				t.Fatalf("result=%v", err)
			}
			sink.mu.Lock()
			ended := sink.ended
			aborted := sink.aborted
			sink.mu.Unlock()
			if kind == "valid" && ended != 1 {
				t.Fatal("no complete sink")
			}
			if kind != "valid" && !aborted {
				t.Fatal("failed stream not aborted")
			}
			if processFailed(p) {
				t.Fatal("bounded snapshot failure killed plugin")
			}
			send(pluginprotocol.Frame{Type: "checkpoint_stream_end", ChunkCount: "1"})
			if err := p.ActivateWatch(context.Background(), "s"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-session.outgoing:
				t.Fatal("late grant rearmed quiesced stream")
			case <-time.After(time.Millisecond):
			}
		})
	}
}

func TestStreamWatchRejectsPositiveWhileEndWaitsForWriteGate(t *testing.T) {
	p, session := streamProcess(t)
	source := &gatedRestoreSource{p: p, blocked: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- p.WatchStream(context.Background(), hub.WatchRequest{RequestID: "w", SubscriptionID: "s", Arguments: jsonvalue.NewObject(nil), RuntimeState: true}, source, func() error { return nil })
	}()
	if frame := readFrame(t, session); frame.Type != "state_stream_watch" {
		t.Fatal(frame.Type)
	}
	<-source.blocked
	// The source holds the write gate before the chunk/end sends. A positive
	// watch result cannot install the dormant watch ahead of the complete handoff.
	p.handleFrame(pluginprotocol.Frame{Type: "watch_result", RequestID: "w", Accepted: boolPointer(true)})
	close(source.release)
	if err := await(t, done); err == nil {
		t.Fatal("accepted watch before end send")
	}
}

type gatedRestoreSource struct {
	p                *Process
	blocked, release chan struct{}
}

func (s *gatedRestoreSource) Schema() string { return "schema" }
func (s *gatedRestoreSource) Count() uint64  { return 1 }
func (s *gatedRestoreSource) Streamed() bool { return true }
func (s *gatedRestoreSource) Chunk(uint64) (*jsonvalue.Value, error) {
	if err := s.p.acquireWrite(context.Background()); err != nil {
		return nil, err
	}
	close(s.blocked)
	<-s.release
	s.p.releaseWrite()
	return jsonvalue.NewObject(nil), nil
}

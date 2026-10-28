package githubplugin

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

//go:generate go run go.uber.org/mock/mockgen -destination=session_mock_test.go -package=githubplugin github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol SessionServer

type outgoing struct {
	payload []byte
	ctx     context.Context
	done    chan struct{}
	result  chan error
}

// queuedSession keeps a delayed checkpoint from holding the control reader.
// A single transport writer preserves Send serialization; at most four chunk
// objects wait for it, and expired correlations are discarded before sending.
type queuedSession struct {
	pluginprotocol.SessionServer
	ctx         context.Context
	control     chan outgoing
	checkpoints chan outgoing
	failed      chan error
	writerDone  chan struct{}
	writerErr   error
}

func newQueuedSession(ctx context.Context, stream pluginprotocol.SessionServer) *queuedSession {
	s := &queuedSession{SessionServer: stream, ctx: ctx, control: make(chan outgoing, 16), checkpoints: make(chan outgoing, 4), failed: make(chan error, 1), writerDone: make(chan struct{})}
	go s.write()
	return s
}

func (s *queuedSession) Send(payload []byte) error {
	select {
	case <-s.writerDone:
		return s.writerFailure()
	default:
	}
	select {
	case s.control <- outgoing{payload: payload, ctx: s.ctx}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
}

func (s *queuedSession) write() {
	defer close(s.writerDone)
	for {
		var message outgoing
		// Control messages cannot wait behind a queue of checkpoint chunks.
		select {
		case message = <-s.control:
		default:
			select {
			case message = <-s.control:
			case message = <-s.checkpoints:
			case <-s.ctx.Done():
				return
			}
		}
		if s.ctx.Err() != nil {
			return
		}
		err := s.dispatch(message)
		if err != nil {
			s.writerErr = err
			select {
			case s.failed <- err:
			default:
			}
			return
		}
	}
}

// writerDone broadcasts failure to every waiter. Receiving failed in the main
// loop cannot consume the only notification needed by a concurrent barrier.
func (s *queuedSession) writerFailure() error {
	if s.writerErr != nil {
		return s.writerErr
	}
	return errors.New("Session writer is unavailable")
}

func (s *queuedSession) dispatch(message outgoing) error {
	if message.done != nil {
		defer close(message.done)
	}
	if s.ctx.Err() != nil || message.ctx.Err() != nil {
		if message.result != nil {
			message.result <- context.Canceled
		}
		return nil
	}
	if len(message.payload) == 0 {
		return nil
	}
	err := s.SessionServer.Send(message.payload)
	if message.result != nil {
		message.result <- err
	}
	return err
}

// drainCheckpoints releases queued payloads after their sender has stopped.
// Its queue position also waits for a checkpoint already inside transport Send.
func (s *queuedSession) drainCheckpoints() error {
	done := make(chan struct{})
	select {
	case s.checkpoints <- outgoing{ctx: s.ctx, done: done}:
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
	select {
	case <-done:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
}

func (s *queuedSession) sendBarrier(payload []byte) error {
	result := make(chan error, 1)
	select {
	case s.control <- outgoing{payload: payload, ctx: s.ctx, result: result}:
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
	select {
	case err := <-result:
		return err
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
}

func (s *queuedSession) checkpoint(ctx context.Context, frame pluginprotocol.Frame) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	var done chan struct{}
	if frame.Type == "checkpoint_stream_end" {
		done = make(chan struct{})
	}
	select {
	case s.checkpoints <- outgoing{payload: payload, ctx: ctx, done: done}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-s.writerDone:
		return s.writerFailure()
	}
	if done != nil {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-s.writerDone:
			return s.writerFailure()
		}
	}
	return nil
}

func sendCheckpoint(ctx context.Context, cancel context.CancelFunc, stream *queuedSession, frame pluginprotocol.Frame, objects []*jsonvalue.Value) {
	defer cancel()
	frames := func(f pluginprotocol.Frame) error { return stream.checkpoint(ctx, f) }
	schema := frame.Schema
	if schema == "" {
		schema = "github-pr-watch-v2"
	}
	if err := frames(pluginprotocol.Frame{Type: "checkpoint_stream_begin", RequestID: frame.RequestID, SubscriptionID: frame.SubscriptionID, Schema: schema}); err != nil {
		return
	}
	for i, object := range objects {
		if ctx.Err() != nil {
			return
		}
		if err := frames(pluginprotocol.Frame{Type: "checkpoint_stream_chunk", RequestID: frame.RequestID, SubscriptionID: frame.SubscriptionID, Index: strconv.Itoa(i), Data: object}); err != nil {
			return
		}
	}
	// Wait for the queued end to leave the writer, then release the immutable view.
	// The original request deadline is never renewed by a chunk.
	if err := frames(pluginprotocol.Frame{Type: "checkpoint_stream_end", RequestID: frame.RequestID, SubscriptionID: frame.SubscriptionID, ChunkCount: strconv.Itoa(len(objects))}); err != nil {
		return
	}
}

const checkpointTimeout = 10 * time.Second

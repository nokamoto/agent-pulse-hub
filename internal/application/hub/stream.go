package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const DefaultRuntimeStateMaxBytes uint64 = 67108864

func (h *Hub) SetRuntimeStateMaxBytes(value uint64) error {
	if value == 0 {
		return errors.New("runtime-state-max-bytes must be positive")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runtimeStateMaxBytes = value
	return nil
}

// RuntimeSource describes validated committed storage; Chunk reads one object.
type RuntimeSource interface {
	Schema() string
	Count() uint64
	Streamed() bool
	Chunk(uint64) (*jsonvalue.Value, error)
}

type StreamPluginPort interface {
	WatchStream(context.Context, WatchRequest, RuntimeSource, func() error) error
	CheckpointStream(context.Context, RuntimeCheckpointRequest, StreamCheckpointSink) error
}

// StreamCheckpointSink accepts bounded objects, never a materialized snapshot.
type StreamCheckpointSink interface {
	Begin(context.Context, string) error
	Append(context.Context, uint64, []byte) error
	Complete(context.Context, uint64) error
	Abort(error)
}

type StreamRuntimeStore interface {
	LoadRuntimeSources([]domain.Subscription, uint64) (map[domain.SubscriptionID]RuntimeSource, error)
	BeginRuntimeStream(domain.Subscription, string, uint64) (RuntimeStreamTransaction, error)
}

type RuntimeStreamTransaction interface {
	Append(uint64, []byte) error
	Commit(uint64, func(func() error) error) (uint64, error)
	Abort() error
}

type (
	runtimeFSJob  struct{ run func() }
	runtimeFSPool struct {
		mu     sync.RWMutex
		closed bool
		queues [runtimeSaveWorkers]chan runtimeFSJob
		wait   sync.WaitGroup
	}
)

func newRuntimeFSPool() *runtimeFSPool {
	p := &runtimeFSPool{}
	for i := range p.queues {
		p.queues[i] = make(chan runtimeFSJob, 4)
		p.wait.Add(1)
		go func(q <-chan runtimeFSJob) {
			defer p.wait.Done()
			for job := range q {
				job.run()
			}
		}(p.queues[i])
	}
	return p
}

func (p *runtimeFSPool) submit(ctx context.Context, id domain.SubscriptionID, job func()) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return errors.New("runtime filesystem workers are closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var hash uint32 = 2166136261
	for _, b := range []byte(id) {
		hash = (hash ^ uint32(b)) * 16777619
	}
	select {
	case p.queues[hash%runtimeSaveWorkers] <- runtimeFSJob{run: job}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *runtimeFSPool) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	for _, q := range p.queues {
		close(q)
	}
	p.mu.Unlock()
	p.wait.Wait()
}

type streamSaveResult struct {
	generation uint64
	err        error
}
type runtimeStreamSink struct {
	h           *Hub
	watch       *runtimeWatch
	store       StreamRuntimeStore
	mu          sync.Mutex
	aborted     bool
	completed   bool
	transaction RuntimeStreamTransaction // accessed only by the assigned filesystem worker
	workerErr   error
	done        chan streamSaveResult
	once        sync.Once
}

func (h *Hub) newRuntimeStreamSink(watch *runtimeWatch, store StreamRuntimeStore) *runtimeStreamSink {
	return &runtimeStreamSink{h: h, watch: watch, store: store, done: make(chan streamSaveResult, 1)}
}
func (s *runtimeStreamSink) valid() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.aborted }
func (s *runtimeStreamSink) Begin(ctx context.Context, schema string) error {
	return s.h.runtimeFS.submit(ctx, s.watch.registration.ID, func() {
		if !s.valid() {
			return
		}
		s.transaction, s.workerErr = s.store.BeginRuntimeStream(s.watch.registration, schema, s.h.runtimeStateMaxBytes)
	})
}

func (s *runtimeStreamSink) Append(ctx context.Context, index uint64, data []byte) error {
	return s.h.runtimeFS.submit(ctx, s.watch.registration.ID, func() {
		if !s.valid() || s.workerErr != nil {
			return
		}
		if s.transaction == nil {
			s.workerErr = errors.New("stream has no staging transaction")
			return
		}
		s.workerErr = s.transaction.Append(index, data)
	})
}

func (s *runtimeStreamSink) Complete(ctx context.Context, count uint64) error {
	if err := ctx.Err(); err != nil {
		s.Abort(err)
		return err
	}
	s.mu.Lock()
	if s.aborted || s.completed {
		s.mu.Unlock()
		return errors.New("stream save already terminated")
	}
	s.completed = true
	s.mu.Unlock()
	ready := make(chan struct{})
	defer close(ready)
	err := s.h.runtimeFS.submit(ctx, s.watch.registration.ID, func() {
		<-ready
		err := s.workerErr
		var generation uint64
		if err == nil && s.valid() && s.transaction != nil {
			generation, err = s.transaction.Commit(count, func(publish func() error) error {
				s.h.mu.Lock()
				defer s.h.mu.Unlock()
				if !s.valid() || s.h.runtimeWatches[s.watch.registration.ID] != s.watch {
					return errors.New("runtime stream owner was fenced before publication")
				}
				return publish()
			})
		} else if err == nil {
			err = errors.New("stream save expired before commit")
		}
		if generation == 0 && s.transaction != nil {
			err = errors.Join(err, s.transaction.Abort())
		}
		s.once.Do(func() { s.done <- streamSaveResult{generation: generation, err: err} })
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.Abort(err)
	}
	return err
}

func (s *runtimeStreamSink) Abort(cause error) {
	s.mu.Lock()
	if s.aborted {
		s.mu.Unlock()
		return
	}
	s.aborted = true
	s.mu.Unlock()
	// This queue item has no chunk data. A bounded number of correlations may
	// await cleanup while slow filesystem operations finish.
	go func() {
		err := s.h.runtimeFS.submit(context.Background(), s.watch.registration.ID, func() {
			err := cause
			if s.transaction != nil {
				err = errors.Join(err, s.transaction.Abort())
			}
			s.once.Do(func() { s.done <- streamSaveResult{err: err} })
		})
		if err != nil {
			s.once.Do(func() { s.done <- streamSaveResult{err: errors.Join(cause, err)} })
		}
	}()
}

func (h *Hub) runStreamCheckpoint(ctx context.Context, quiesce bool, watch *runtimeWatch, port StreamPluginPort, store StreamRuntimeStore) runtimeCheckpointResult {
	sink := h.newRuntimeStreamSink(watch, store)
	err := port.CheckpointStream(ctx, RuntimeCheckpointRequest{SubscriptionID: watch.registration.ID, Quiesce: quiesce}, sink)
	if err != nil {
		sink.Abort(err)
	}
	return runtimeCheckpointResult{watch: watch, stream: sink, err: err, operation: "checkpoint"}
}

func (h *Hub) saveStreamResult(result runtimeCheckpointResult) {
	saved := <-result.stream.done
	if saved.generation == 0 {
		h.reportRuntimeStateError(result.watch, "save", saved.err)
		return
	}
	h.logger.Info("runtime_state_saved", "input_plugin", result.watch.inputName, "subscription_id", result.watch.registration.ID, "generation", saved.generation)
	if saved.err != nil {
		h.logger.Warn("runtime_state_cleanup_error", "input_plugin", result.watch.inputName, "subscription_id", result.watch.registration.ID, "generation", saved.generation, "reason", safeError(saved.err))
	}
}

func streamWatchFits(args *jsonvalue.Value, id, request string) bool {
	// A stream reserves its correlation, not 32 KiB inside its admission frame.
	return len(args.Marshal())+len(id)+len(request)+256 <= MaxPluginPayload
}

func validateRuntimeSource(source RuntimeSource, stream bool) error {
	if source != nil && source.Streamed() && !stream {
		return fmt.Errorf("streamed runtime state requires runtime_state_stream_v1")
	}
	return nil
}

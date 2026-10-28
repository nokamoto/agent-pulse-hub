package hub

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	runtimeCheckpointInterval = 5 * time.Second
	runtimeCheckpointDeadline = 10 * time.Second
	runtimeSaveWorkers        = 4
	runtimeWarning            = "runtime continuity at next restart cannot be guaranteed"
)

type RuntimeError struct {
	Code        string
	Message     string
	Unavailable bool
	Cause       error
}

func (e *RuntimeError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "trigger runtime operation failed"
}

func (e *RuntimeError) Unwrap() error { return e.Cause }

// StartRuntimeCheckpoints starts the five-second schedule after all saved
// registrations have passed restoration and the daemon is ready.
func (h *Hub) StartRuntimeCheckpoints() {
	h.mu.Lock()
	if h.runtimeStarted || h.stopping {
		h.mu.Unlock()
		return
	}
	h.runtimeStarted = true
	h.runtimeDone = make(chan struct{})
	h.runtimeLoops = make(map[string]chan struct{})
	for _, inputName := range h.runtimeInputNamesLocked() {
		h.ensureRuntimeLoopLocked(inputName)
	}
	done := h.runtimeDone
	ctx := h.ctx
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		loops := make([]chan struct{}, 0, len(h.runtimeLoops))
		for _, loopDone := range h.runtimeLoops {
			loops = append(loops, loopDone)
		}
		h.mu.Unlock()
		for _, loopDone := range loops {
			<-loopDone
		}
		close(done)
	}()
}

// ensureRuntimeLoopLocked starts one schedule per input instance. Callers hold
// h.mu so shutdown cannot race a new loop into the final wait set.
func (h *Hub) ensureRuntimeLoopLocked(inputName string) {
	if !h.runtimeStarted || h.stopping || h.runtimeLoops[inputName] != nil {
		return
	}
	done := make(chan struct{})
	h.runtimeLoops[inputName] = done
	ctx := h.ctx
	go func() {
		defer close(done)
		timer := time.NewTimer(runtimeCheckpointInterval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				h.runRuntimeCheckpointBatchForInput(ctx, inputName, false)
				timer.Reset(runtimeCheckpointInterval)
			}
		}
	}()
}

func (h *Hub) runtimeInputNamesLocked() []string {
	inputs := make(map[string]struct{})
	for _, watch := range h.runtimeWatches {
		if state := h.plugins[watch.inputName]; state != nil && state.port == watch.inputPort {
			inputs[watch.inputName] = struct{}{}
		}
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// runRuntimeCheckpointBatch runs one batch per input concurrently. Each input
// receives its own protocol deadline and its own five-second-after-completion
// schedule; filesystem saves share the Hub-wide worker limit.
func (h *Hub) runRuntimeCheckpointBatch(parent context.Context, quiesce bool) {
	groups := h.runtimeWatchGroups()
	var wait sync.WaitGroup
	for _, watches := range groups {
		watches := watches
		wait.Add(1)
		go func() {
			defer wait.Done()
			h.runRuntimeCheckpointBatchForWatches(parent, quiesce, watches)
		}()
	}
	wait.Wait()
}

func (h *Hub) runRuntimeCheckpointBatchForInput(parent context.Context, inputName string, quiesce bool) {
	groups := h.runtimeWatchGroups()
	h.runRuntimeCheckpointBatchForWatches(parent, quiesce, groups[inputName])
}

func (h *Hub) runRuntimeCheckpointBatchForWatches(parent context.Context, quiesce bool, watches []*runtimeWatch) {
	if h.runtimeStore == nil || len(watches) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, runtimeCheckpointDeadline)
	defer cancel()
	results := make(chan runtimeCheckpointResult, len(watches))
	var wait sync.WaitGroup
	for _, watch := range watches {
		watch := watch
		wait.Add(1)
		go func() {
			defer wait.Done()
			h.mu.Lock()
			_, exists := h.registrations[watch.registration.ID]
			if !exists || h.runtimeWatches[watch.registration.ID] != watch || h.cancellations[watch.registration.ID] != nil {
				h.mu.Unlock()
				return
			}
			life := h.lifecycleLocked(watch.registration.ID)
			if notification := h.notificationWaits[watch.registration.ID]; notification != nil {
				if notification.preparing || notification.checkpointBusy {
					h.mu.Unlock()
					return
				}
				notification.checkpointBusy = true
			}
			h.mu.Unlock()
			watch.mu.Lock()
			if !quiesce && life.ctx.Err() != nil {
				watch.mu.Unlock()
				h.finishRewaitCheckpoint(watch)
				return
			}
			flushed, err := h.flushRuntimeOutcomesLocked(ctx, watch)
			if err != nil {
				watch.mu.Unlock()
				h.releasePendingEvents(flushed)
				results <- runtimeCheckpointResult{watch: watch, err: err, operation: "delivery notice"}
				return
			}
			checkpointCtx, checkpointCancel := context.WithCancel(ctx)
			stopCancel := func() bool { return false }
			if !quiesce {
				stopCancel = context.AfterFunc(life.ctx, checkpointCancel)
			}
			state, err := watch.inputPort.Checkpoint(checkpointCtx, RuntimeCheckpointRequest{
				SubscriptionID: watch.registration.ID, Quiesce: quiesce,
			})
			stopCancel()
			checkpointCancel()
			watch.mu.Unlock()
			h.releasePendingEvents(flushed)
			results <- runtimeCheckpointResult{watch: watch, state: state, err: err, operation: "checkpoint"}
		}()
	}
	wait.Wait()
	close(results)
	completed := make([]runtimeCheckpointResult, 0, len(watches))
	for result := range results {
		completed = append(completed, result)
	}

	jobs := make(chan runtimeCheckpointResult, len(completed))
	var saves sync.WaitGroup
	workers := min(runtimeSaveWorkers, len(completed))
	for worker := 0; worker < workers; worker++ {
		saves.Add(1)
		go func() {
			defer saves.Done()
			for result := range jobs {
				if result.err != nil {
					h.reportCheckpointError(ctx, quiesce, result)
					h.finishRewaitCheckpoint(result.watch)
					continue
				}
				h.saveRuntimeState(result.watch, result.state)
				h.finishRewaitCheckpoint(result.watch)
			}
		}()
	}
	for _, result := range completed {
		jobs <- result
	}
	close(jobs)
	saves.Wait()
}

type runtimeCheckpointResult struct {
	watch     *runtimeWatch
	state     *jsonvalue.Value
	err       error
	operation string
}

func (h *Hub) runtimeWatchGroups() map[string][]*runtimeWatch {
	h.mu.Lock()
	groups := make(map[string][]*runtimeWatch)
	for _, watch := range h.runtimeWatches {
		if h.cancellations[watch.registration.ID] != nil {
			continue
		}
		if state := h.plugins[watch.inputName]; state != nil && state.port == watch.inputPort {
			groups[watch.inputName] = append(groups[watch.inputName], watch)
		}
	}
	h.mu.Unlock()
	for _, watches := range groups {
		sort.Slice(watches, func(i, j int) bool {
			return watches[i].registration.ID < watches[j].registration.ID
		})
	}
	return groups
}

func (h *Hub) flushRuntimeOutcomesLocked(ctx context.Context, watch *runtimeWatch) (int, error) {
	flushed := 0
	for len(watch.outcomes) > 0 {
		notice := watch.outcomes[0]
		if err := watch.inputPort.NotifyDelivery(ctx, notice); err != nil {
			return flushed, err
		}
		watch.outcomes[0] = RuntimeNotice{}
		watch.outcomes = watch.outcomes[1:]
		h.mu.Lock()
		if wait := h.notificationWaits[watch.registration.ID]; wait != nil {
			wait.outstanding = false
		}
		h.mu.Unlock()
		flushed++
	}
	return flushed, nil
}

func (h *Hub) finishRewaitCheckpoint(watch *runtimeWatch) {
	h.mu.Lock()
	if wait := h.notificationWaits[watch.registration.ID]; wait != nil {
		wait.checkpointBusy = false
	}
	h.mu.Unlock()
}

func (h *Hub) finishRuntimeOutcome(item queuedEvent, status string) {
	if item.eventID == "" {
		return
	}
	h.mu.Lock()
	watch := h.runtimeWatches[item.event.SubscriptionID]
	stopping := h.stopping
	cancelling := h.cancellations[item.event.SubscriptionID] != nil
	h.mu.Unlock()
	if cancelling {
		h.releasePendingEvents(1)
		return
	}
	if watch == nil || watch.inputPort != item.inputPort {
		h.logger.Warn("runtime_state_persistence_error", "input_plugin", item.event.InputPlugin, "subscription_id", item.event.SubscriptionID, "operation", "delivery notice", "reason", "runtime state watch is unavailable", "message", runtimeWarning)
		h.releasePendingEvents(1)
		return
	}
	watch.mu.Lock()
	h.mu.Lock()
	cancelling = h.cancellations[item.event.SubscriptionID] != nil
	_, exists := h.registrations[item.event.SubscriptionID]
	h.mu.Unlock()
	if cancelling || !exists {
		watch.mu.Unlock()
		h.releasePendingEvents(1)
		return
	}
	if watch.unavailable {
		watch.mu.Unlock()
		h.releasePendingEvents(1)
		h.reportRuntimeStateError(watch, "delivery notice", errors.New("input plugin became unavailable before the delivery notice could be sent"))
		return
	}
	watch.outcomes = append(watch.outcomes, RuntimeNotice{
		SubscriptionID: item.event.SubscriptionID,
		EventID:        item.eventID,
		Status:         status,
	})
	if !stopping {
		h.mu.Lock()
		stopping = h.stopping
		h.mu.Unlock()
	}
	if !stopping {
		ctx, cancel := context.WithTimeout(h.ctx, runtimeCheckpointDeadline)
		flushed, err := h.flushRuntimeOutcomesLocked(ctx, watch)
		cancel()
		watch.mu.Unlock()
		h.releasePendingEvents(flushed)
		if err != nil {
			h.mu.Lock()
			stopping = h.stopping
			h.mu.Unlock()
			if !stopping {
				h.reportRuntimeStateError(watch, "delivery notice", err)
				var runtimeErr *RuntimeError
				if errors.As(err, &runtimeErr) && runtimeErr.Unavailable {
					h.pluginUnavailable(watch.inputName, watch.inputPort, err)
				}
			}
		}
		return
	}
	watch.mu.Unlock()
}

// discardRuntimeOutcomesForInput releases Hub delivery capacity reserved by
// stateful events whose terminal notices can no longer be sent to this input.
// Marking each watch unavailable also closes the race with delivery workers
// that finish after pluginUnavailable has started.
func (h *Hub) discardRuntimeOutcomesForInput(inputName string, port PluginPort) {
	h.mu.Lock()
	watches := make([]*runtimeWatch, 0)
	for _, watch := range h.runtimeWatches {
		if watch.inputName == inputName && watch.inputPort == port {
			watches = append(watches, watch)
		}
	}
	h.mu.Unlock()

	discarded := 0
	var first *runtimeWatch
	for _, watch := range watches {
		watch.mu.Lock()
		watch.unavailable = true
		if len(watch.outcomes) > 0 {
			discarded += len(watch.outcomes)
			watch.outcomes = nil
			if first == nil {
				first = watch
			}
		}
		watch.mu.Unlock()
	}
	h.releasePendingEvents(discarded)
	if first != nil {
		h.reportRuntimeStateError(first, "delivery notice", errors.New("input plugin became unavailable before pending delivery notices could be sent"))
	}
}

func (h *Hub) reportCheckpointError(ctx context.Context, stopping bool, result runtimeCheckpointResult) {
	if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
		h.mu.Lock()
		shuttingDown := h.stopping
		h.mu.Unlock()
		if shuttingDown && !stopping {
			return
		}
	}
	var operationErr *RuntimeError
	if errors.As(result.err, &operationErr) && operationErr.Unavailable {
		h.pluginUnavailable(result.watch.inputName, result.watch.inputPort, result.err)
	}
	h.reportRuntimeStateError(result.watch, result.operation, result.err)
}

func (h *Hub) saveRuntimeState(watch *runtimeWatch, state *jsonvalue.Value) {
	h.runtimeSaveGate <- struct{}{}
	defer func() { <-h.runtimeSaveGate }()
	h.mu.Lock()
	_, exists := h.registrations[watch.registration.ID]
	if !exists || h.runtimeWatches[watch.registration.ID] != watch || h.cancellations[watch.registration.ID] != nil {
		h.mu.Unlock()
		return
	}
	life := h.lifecycleLocked(watch.registration.ID)
	h.mu.Unlock()
	life.save.Lock()
	defer life.save.Unlock()
	h.mu.Lock()
	_, exists = h.registrations[watch.registration.ID]
	fenced := h.cancellations[watch.registration.ID] != nil
	h.mu.Unlock()
	if fenced || !exists {
		return
	}
	if state == nil {
		h.reportRuntimeStateError(watch, "save", errors.New("checkpoint returned no state"))
		return
	}
	generation, err := h.runtimeStore.CommitRuntime(watch.registration, state)
	if generation == 0 {
		if err == nil {
			err = errors.New("runtime state store returned no committed generation")
		}
		h.reportRuntimeStateError(watch, "save", err)
		return
	}
	watch.mu.Lock()
	watch.state = state
	watch.mu.Unlock()
	h.logger.Info("runtime_state_saved", "input_plugin", watch.inputName, "subscription_id", watch.registration.ID, "generation", generation)
	if err != nil {
		h.logger.Warn("runtime_state_cleanup_error", "input_plugin", watch.inputName, "subscription_id", watch.registration.ID, "generation", generation, "reason", safeError(err))
	}
}

func (h *Hub) releasePendingEvents(count int) {
	if count == 0 {
		return
	}
	h.mu.Lock()
	h.pendingEvents -= count
	h.mu.Unlock()
}

func (h *Hub) reportRuntimeStateError(watch *runtimeWatch, operation string, err error) {
	h.logger.Warn("runtime_state_persistence_error", "input_plugin", watch.inputName, "subscription_id", watch.registration.ID, "operation", operation, "reason", safeError(err), "message", runtimeWarning)
}

func (h *Hub) finalRuntimeCheckpoints() {
	if h.runtimeStore == nil {
		return
	}
	h.runRuntimeCheckpointBatch(context.Background(), true)
}

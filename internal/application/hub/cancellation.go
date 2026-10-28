package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=cancellation.go -destination=cancellation_mock_test.go -package=hub

type CancellationStore interface {
	BeginCancellation(domain.Subscription) error
	RemoveCancelled(domain.Subscription) (cleanupErr error, err error)
}

type CancellationPluginPort interface {
	FenceWatch(domain.SubscriptionID) error
	CancelWatch(context.Context, domain.SubscriptionID) error
	InputTerminated() bool
}

type guardedDeliveryPort interface {
	DeliverGuarded(context.Context, domain.Event, func(func()) bool) DeliveryResult
}

type cancellationState struct {
	busy    bool
	durable bool
}

type subscriptionLifecycle struct {
	save   sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// ValidSubscriptionID accepts the canonical lowercase UUID used by storage.
func ValidSubscriptionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (h *Hub) lifecycleLocked(id domain.SubscriptionID) *subscriptionLifecycle {
	life := h.lifecycles[id]
	if life == nil {
		ctx, cancel := context.WithCancel(h.ctx)
		life = &subscriptionLifecycle{ctx: ctx, cancel: cancel}
		h.lifecycles[id] = life
	}
	return life
}

func (h *Hub) cancellationFailure(id domain.SubscriptionID, stage, code string, durable bool, causes ...error) *Error {
	recovery := "No durable intent exists; normal restart may restore the saved watch. Repair the cause and explicitly retry this ID."
	if durable {
		recovery = "A durable intent exists; normal restart completes removal before readiness. Repair the cause and explicitly retry this ID or restart normally."
	}
	reason := "The required capability is unavailable."
	if len(causes) > 0 && causes[0] != nil {
		switch {
		case errors.Is(causes[0], context.DeadlineExceeded):
			reason = "The input exchange deadline expired."
		case errors.Is(causes[0], context.Canceled):
			reason = "The input exchange was interrupted."
		case code == "storage_error":
			reason = bounded(safeError(causes[0]), 512) + "."
		case code == "cancellation_rejected":
			reason = "The input reported incomplete watch termination or resource release."
		default:
			reason = "The input transport or cleanup is unavailable."
		}
	}
	return &Error{Code: code, Message: fmt.Sprintf("Cancellation %s for %s failed. %s The target remains fenced. %s", stage, id, reason, recovery)}
}

// Unsubscribe fences one retained subscription before any external exchange.
func (h *Hub) Unsubscribe(subscriptionID string) *Error {
	if !ValidSubscriptionID(subscriptionID) {
		return &Error{Code: "invalid_request", Message: "A canonical lowercase subscription UUID is required."}
	}
	id := domain.SubscriptionID(subscriptionID)
	h.mu.Lock()
	if h.stopping || !h.admissionOpen {
		h.mu.Unlock()
		return &Error{Code: "plugin_unavailable", Message: "Daemon is not accepting cancellation requests."}
	}
	registration, exists := h.registrations[id]
	if !exists {
		registration, exists = h.savedRegistrations[id]
	}
	if !exists {
		h.mu.Unlock()
		return &Error{Code: "unknown_subscription", Message: "No live or saved registration has this ID."}
	}
	input := h.plugins[registration.InputPlugin]
	var port CancellationPluginPort
	if input != nil {
		port, _ = input.port.(CancellationPluginPort)
	}
	terminated := port != nil && port.InputTerminated()
	if !terminated && (input == nil || !input.capabilities["cancel_watch_v1"] || port == nil) {
		h.mu.Unlock()
		return &Error{Code: "unsubscribe_unsupported", Message: "The running input lacks cancel_watch_v1. Upgrade the input and daemon while stopped, then use the same saved ID."}
	}
	state := h.cancellations[id]
	if state != nil && state.busy {
		h.mu.Unlock()
		return &Error{Code: "unsubscribe_busy", Message: "Cancellation for this ID is already executing; explicitly retry after its result."}
	}
	if state == nil {
		state = &cancellationState{}
		h.cancellations[id] = state
	}
	state.busy = true
	life := h.lifecycleLocked(id)
	life.cancel()
	registration.Active = false
	h.registrations[id] = registration
	discarded := h.discardCancelledQueuedLocked(id)
	targetWatch := h.runtimeWatches[id]
	var stream *runtimeStreamSink
	if targetWatch != nil {
		stream = targetWatch.stream
	}
	for _, receipt := range h.rewaitReceipts {
		if receipt.subscriptionID == subscriptionID && !receipt.completed {
			receipt.result = &Error{Code: "unknown_subscription", Message: "Subscription cancellation closed rewait preparation."}
			receipt.completed = true
			close(receipt.done)
		}
	}
	h.cancellationWait.Add(1)
	if port != nil && !terminated {
		_ = port.FenceWatch(id)
	}
	h.mu.Unlock()
	defer h.cancellationWait.Done()
	defer func() { h.mu.Lock(); state.busy = false; h.mu.Unlock() }()
	if stream != nil {
		stream.Abort(errors.New("subscription cancellation expired the checkpoint stream"))
		// Cleanup is queued behind all target chunks. Drain it before holding the
		// save lock through intent storage and removal.
		<-stream.finished
	}
	if len(discarded) > 0 {
		if targetWatch != nil {
			targetWatch.mu.Lock()
			targetWatch.outcomes = append(targetWatch.outcomes, discarded...)
			targetWatch.mu.Unlock()
		} else {
			h.releasePendingEvents(len(discarded))
		}
	}
	// A save that passed admission finishes before the intent and deletion. Every
	// later saver checks the lifecycle fence while holding the same save lock.
	life.save.Lock()
	defer life.save.Unlock()
	store, ok := h.store.(CancellationStore)
	if !ok {
		return h.cancellationFailure(id, "intent storage", "storage_error", state.durable)
	}
	if err := store.BeginCancellation(registration); err != nil {
		return h.cancellationFailure(id, "intent commit", "storage_error", state.durable, err)
	}
	state.durable = true
	h.mu.Lock()
	notificationSaved := h.cancelNotificationsLocked(id)
	h.mu.Unlock()
	if !notificationSaved {
		return h.cancellationFailure(id, "notification storage", "storage_error", true)
	}
	if !terminated {
		// Notices already owed are sent before the ordered cancellation request.
		h.mu.Lock()
		watch := h.runtimeWatches[id]
		h.mu.Unlock()
		if watch != nil {
			watch.mu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			flushed, err := h.flushRuntimeOutcomesLocked(ctx, watch)
			cancel()
			watch.mu.Unlock()
			h.releasePendingEvents(flushed)
			if err != nil {
				return h.cancellationFailure(id, "delivery notice", "plugin_unavailable", true, err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := port.CancelWatch(ctx, id)
		cancel()
		if err != nil {
			code := "plugin_unavailable"
			var watchErr *WatchError
			if errors.As(err, &watchErr) && watchErr.Code == "cancellation_rejected" {
				code = watchErr.Code
			}
			return h.cancellationFailure(id, "input stop", code, true, err)
		}
	}
	cleanupErr, err := store.RemoveCancelled(registration)
	if err != nil {
		return h.cancellationFailure(id, "persistent removal", "storage_error", true, err)
	}
	h.mu.Lock()
	watch := h.runtimeWatches[id]
	h.mu.Unlock()
	discardedNotices := 0
	if watch != nil {
		watch.mu.Lock()
		discardedNotices = len(watch.outcomes)
		watch.outcomes = nil
		watch.mu.Unlock()
	}
	h.mu.Lock()
	h.pendingEvents -= discardedNotices
	delete(h.registrations, id)
	delete(h.savedRegistrations, id)
	delete(h.retained, id)
	delete(h.runtimeStates, id)
	delete(h.runtimeSources, id)
	delete(h.runtimeWatches, id)
	delete(h.notificationWaits, id)
	delete(h.activeByKey, registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs))
	delete(h.pendingByKey, registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs))
	delete(h.cancellations, id)
	delete(h.lifecycles, id)
	h.mu.Unlock()
	if cleanupErr != nil {
		h.logger.Warn("cancellation_cleanup_error", "subscription_id", id, "reason", safeError(cleanupErr))
	}
	return nil
}

// discardCancelledQueuedLocked removes queued work while event admission is
// serialized. A worker that already dequeued an item still checks the route at
// its issuance boundary, including after successful removal clears the fence.
func (h *Hub) discardCancelledQueuedLocked(id domain.SubscriptionID) []RuntimeNotice {
	retained := make([]queuedEvent, 0, len(h.queue))
	var discarded []RuntimeNotice
	for {
		select {
		case item := <-h.queue:
			if item.event.SubscriptionID == id {
				if item.eventID == "" {
					h.pendingEvents--
				} else {
					discarded = append(discarded, RuntimeNotice{SubscriptionID: id, EventID: item.eventID, Status: "discarded"})
				}
				h.logger.Warn("delivery_discarded", "input_plugin", item.event.InputPlugin, "output_plugin", item.event.OutputPlugin, "subscription_id", id, "delivery_id", item.event.ID, "reason", "subscription cancellation")
			} else {
				retained = append(retained, item)
			}
		default:
			for _, item := range retained {
				h.queue <- item
			}
			if len(retained) > 0 {
				h.signalQueueLocked()
			}
			return discarded
		}
	}
}

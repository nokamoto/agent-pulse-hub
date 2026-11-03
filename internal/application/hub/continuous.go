package hub

import (
	"errors"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

type continuousStore interface {
	CommitContinuous(domain.Subscription, string) error
	BeginContinuousRun(domain.SubscriptionID, string) error
	EndContinuousRun(domain.SubscriptionID, string) error
}

type notificationWait struct {
	eventID     string
	generation  string
	armed       bool
	outstanding bool
}

// continuousRunLocked reserves the marker identity while registration and
// startup still hold the activation barrier.
func (h *Hub) continuousRunLocked() (continuousStore, string, error) {
	store, ok := h.store.(continuousStore)
	if !ok {
		return nil, "", errors.New("continuous watch storage is unavailable")
	}
	if h.continuousSession == "" {
		id, err := newID()
		if err != nil {
			return nil, "", err
		}
		h.continuousSession = id
	}
	return store, h.continuousSession, nil
}

func (h *Hub) clearContinuousRun(watch *runtimeWatch) {
	if watch.continuousSession == "" {
		return
	}
	h.mu.Lock()
	blocked := h.notifications != nil && h.notifications.storageBlocked
	h.mu.Unlock()
	if blocked {
		h.reportRuntimeStateError(watch, "clear continuous run marker", errors.New("notification persistence did not complete"))
		return
	}
	if err := h.store.(continuousStore).EndContinuousRun(watch.registration.ID, watch.continuousSession); err != nil {
		h.reportRuntimeStateError(watch, "clear continuous run marker", err)
	}
}

package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

const activationDeadline = 10 * time.Second

type activationBatch struct {
	port   PluginPort
	ids    []domain.SubscriptionID
	ctx    context.Context
	cancel context.CancelFunc
}

func (p *pluginState) participates() bool {
	return p.capabilities["runtime_state_v1"] || p.capabilities["runtime_state_v2"]
}

func (h *Hub) continueWatchLocked(name string, port PluginPort, notice RuntimeNotice) {
	id := notice.SubscriptionID
	wait := h.notificationWaits[id]
	if wait == nil || !wait.outstanding || wait.eventID != notice.EventID {
		return
	}
	wait.outstanding = false
	wait.eventID = ""
	if h.stopping || h.cancellations[id] != nil {
		return
	}
	regrant := func() {
		state := h.plugins[name]
		registration, exists := h.registrations[id]
		if !h.stopping && h.cancellations[id] == nil && exists && registration.Active && state != nil && state.port == port && state.available && h.notificationWaits[id] == wait {
			h.queueActivationLocked(name, port, id)
		}
	}
	if notice.Status == "discarded" {
		life := h.lifecycleLocked(id)
		h.activationWait.Add(1)
		go func() {
			defer h.activationWait.Done()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-life.ctx.Done():
				return
			case <-h.ctx.Done():
				return
			case <-timer.C:
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			regrant()
		}()
		return
	}
	regrant()
}

// queueActivationLocked runs under the same lifecycle lock as commit and stop.
// Each watch is queued once, when committed. Capacity is bounded by the shared
// registration limit. The deadline begins at enqueue, including stream waits.
func (h *Hub) queueActivationLocked(name string, port PluginPort, id domain.SubscriptionID) {
	if h.stopping || h.cancellations[id] != nil {
		return
	}
	if h.plugins[name].capabilities["continuous_watch_v1"] {
		generation, err := newID()
		if err != nil {
			go h.pluginUnavailable(name, port, fmt.Errorf("create notification generation: %w", err))
			return
		}
		h.notificationWaits[id] = &notificationWait{generation: generation, armed: true}
	}
	batch := h.activationBatches[name]
	if batch == nil {
		ctx, cancel := context.WithTimeout(h.ctx, activationDeadline)
		batch = &activationBatch{port: port, ctx: ctx, cancel: cancel}
		h.activationBatches[name] = batch
		h.activationWait.Add(1)
		go h.dispatchActivations(name, batch)
	}
	batch.ids = append(batch.ids, id)
}

func (h *Hub) dispatchActivations(name string, batch *activationBatch) {
	defer h.activationWait.Done()
	defer batch.cancel()
	defer func() {
		h.mu.Lock()
		if h.activationBatches[name] == batch {
			delete(h.activationBatches, name)
		}
		batch.ids = nil
		h.mu.Unlock()
	}()
	for {
		h.mu.Lock()
		state := h.plugins[name]
		if h.stopping || state == nil || state.port != batch.port || !state.available || len(batch.ids) == 0 {
			// Remove the empty batch before releasing the lock so a newly
			// committed watch starts a fresh batch instead of losing its grant.
			if h.activationBatches[name] == batch {
				delete(h.activationBatches, name)
			}
			h.mu.Unlock()
			return
		}
		id := batch.ids[0]
		batch.ids = batch.ids[1:]
		if registration, exists := h.registrations[id]; !exists || !registration.Active || h.cancellations[id] != nil {
			h.mu.Unlock()
			continue
		}
		life := h.lifecycleLocked(id)
		generation := ""
		if wait := h.notificationWaits[id]; wait != nil {
			generation = wait.generation
		}
		h.mu.Unlock()
		grantCtx, grantCancel := context.WithCancel(batch.ctx)
		stopCancel := context.AfterFunc(life.ctx, grantCancel)

		err := batch.ctx.Err()
		if err == nil {
			if generation != "" {
				if capable, ok := batch.port.(interface {
					ActivateGeneration(context.Context, domain.SubscriptionID, string) error
				}); ok {
					err = capable.ActivateGeneration(grantCtx, id, generation)
				} else {
					err = fmt.Errorf("input does not implement generation activation")
				}
			} else {
				err = batch.port.ActivateWatch(grantCtx, id)
			}
		}
		stopCancel()
		grantCancel()
		if err != nil {
			h.mu.Lock()
			stopping := h.stopping
			cancelled := h.cancellations[id] != nil || h.registrations[id].ID == ""
			h.mu.Unlock()
			if cancelled {
				continue
			}
			if !stopping {
				h.pluginUnavailable(name, batch.port, fmt.Errorf("activate watch %s: %s", id, safeError(err)))
			}
			return
		}
	}
}

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

// queueActivationLocked runs under the same lifecycle lock as commit and stop.
// Each watch is queued once, when committed. Capacity is bounded by the shared
// registration limit. The deadline begins at enqueue, including stream waits.
func (h *Hub) queueActivationLocked(name string, port PluginPort, id domain.SubscriptionID) {
	if h.stopping {
		return
	}
	if h.plugins[name].capabilities["rewait_v1"] {
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
		generation := ""
		if wait := h.notificationWaits[id]; wait != nil {
			generation = wait.generation
		}
		h.mu.Unlock()

		err := batch.ctx.Err()
		if err == nil {
			if generation != "" {
				if capable, ok := batch.port.(RewaitPluginPort); ok {
					err = capable.ActivateGeneration(batch.ctx, id, generation)
				} else {
					err = fmt.Errorf("input does not implement repeated waiting")
				}
			} else {
				err = batch.port.ActivateWatch(batch.ctx, id)
			}
		}
		if err != nil {
			h.mu.Lock()
			stopping := h.stopping
			h.mu.Unlock()
			if !stopping {
				h.pluginUnavailable(name, batch.port, fmt.Errorf("activate watch %s: %s", id, safeError(err)))
			}
			return
		}
	}
}

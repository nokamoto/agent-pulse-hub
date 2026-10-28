package pluginprocess

import (
	"context"
	"errors"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

// FenceWatch closes new watch work while preserving terminal notices already owed.
func (p *Process) FenceWatch(id domain.SubscriptionID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if watch := p.watches[string(id)]; watch != nil {
		watch.cancelling = true
		watch.activationClosed = true
		watch.stagedGeneration = ""
	}
	for requestID, pending := range p.pending {
		if pending.subscriptionID != string(id) || (pending.kind != "checkpoint_result" && pending.kind != "resume_result") {
			continue
		}
		delete(p.pending, requestID)
		if pending.cancel != nil {
			pending.cancel()
		}
		if pending.stream != nil {
			pending.stream.sink.Abort(errors.New("watch cancellation expired the checkpoint stream"))
		}
		pending.result <- requestResult{err: &requestError{message: "watch cancellation expired the request", sent: pending.sent}}
	}
	if requestID := p.streamFinishedWatch[string(id)]; requestID != "" {
		if transfer := p.streamFinished[requestID]; transfer != nil {
			transfer.sink.Abort(errors.New("watch cancellation removed the completed stream correlation"))
		}
		delete(p.streamFinished, requestID)
		delete(p.streamFinishedWatch, string(id))
	}
	return nil
}

// CancelWatch waits for selective termination without killing a shared input on
// timeout. Every explicit invocation uses a fresh request correlation.
func (p *Process) CancelWatch(parent context.Context, id domain.SubscriptionID) error {
	ctx, cancel := context.WithTimeout(parent, registrationTimeout)
	defer cancel()
	p.mu.Lock()
	supported := p.config.Role == domain.InputRole && p.capabilities[pluginprotocol.CancelWatchCapability]
	watch := p.watches[string(id)]
	p.mu.Unlock()
	if !supported {
		return &hub.WatchError{Code: "unsubscribe_unsupported", Message: "Input does not support individual watch cancellation."}
	}
	if err := p.FenceWatch(id); err != nil {
		return err
	}
	// A notice that already owns the per-watch order lock must enter the stream
	// before cancellation. Waiting for it consumes this operation's deadline.
	unlock := func() {}
	if watch != nil {
		locked := make(chan struct{})
		go func() {
			watch.orderMu.Lock()
			select {
			case locked <- struct{}{}:
			case <-ctx.Done():
				watch.orderMu.Unlock()
			}
		}()
		select {
		case <-locked:
			unlock = watch.orderMu.Unlock
		case <-ctx.Done():
			return unavailable("Input cancellation could not enter the ordered stream before its deadline.", ctx.Err())
		}
	}
	defer func() { unlock() }()
	requestID, err := randomUUID()
	if err != nil {
		return unavailable("Cancellation request identifier could not be created.", err)
	}
	result, err := p.requestWithSent(ctx, pluginprotocol.Frame{Type: "cancel_watch", RequestID: requestID, SubscriptionID: string(id)}, "cancel_result", registrationTimeout, func() {
		if watch != nil {
			p.mu.Lock()
			watch.cancelSent = true
			p.mu.Unlock()
		}
		unlock()
		unlock = func() {}
	})
	if err != nil {
		return unavailable("Input cancellation could not be confirmed.", err)
	}
	if result.Accepted == nil || !*result.Accepted {
		return &hub.WatchError{Code: "cancellation_rejected", Message: "Input rejected cancellation: " + safeText(result.Error, 256)}
	}
	p.mu.Lock()
	if current := p.watches[string(id)]; current != nil {
		current.active = false
		for eventID := range current.pending {
			p.releasePendingLocked(current, eventID)
		}
		delete(p.watches, string(id))
	}
	p.mu.Unlock()
	return nil
}

// InputTerminated requires confirmed child cleanup; stream closure is insufficient.
func (p *Process) InputTerminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.config.Role == domain.InputRole && p.terminated
}

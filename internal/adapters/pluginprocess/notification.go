package pluginprocess

import (
	"context"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

type notificationCorrelation struct {
	deliveryID string
	deadline   time.Time
	issuedAt   *time.Time
	unknown    bool
	evidence   func(domain.AttemptEvidence)
}

func notificationEvidence(f pluginprotocol.Frame) domain.AttemptEvidence {
	e := domain.AttemptEvidence{Status: f.Status, Receipt: f.Receipt, Retryable: f.Retryable, RetryAfterMS: f.RetryAfterMS}
	if f.Status != "accepted" {
		e.Reason = &f.Reason
		e.Message = &f.Message
	}
	return e
}

func (p *Process) DeliverNotification(ctx context.Context, event domain.Event, attemptID string, admit func(func()) bool, onIssued func(time.Time), evidence func(domain.AttemptEvidence)) domain.AttemptEvidence {
	external := true
	f := pluginprotocol.Frame{Type: "notification_deliver", RequestID: attemptID, DeliveryID: string(event.ID), InputPlugin: event.InputPlugin, SubscriptionID: string(event.SubscriptionID), DestinationArgs: event.DestinationArgs, Context: event.Context, ExternalData: &external, PermissionNotice: pluginprotocol.PermissionNotice}
	p.mu.Lock()
	if p.notificationCorrelations == nil {
		p.notificationCorrelations = map[string]*notificationCorrelation{}
	}
	correlation := &notificationCorrelation{deliveryID: string(event.ID), deadline: time.Now().Add(deliveryTimeout), evidence: evidence}
	p.notificationCorrelations[attemptID] = correlation
	p.mu.Unlock()
	confirmed, err := p.requestInternal(ctx, f, "notification_result", deliveryTimeout, nil, "", nil, nil, admit)
	p.mu.Lock()
	stamp := correlation.issuedAt
	p.mu.Unlock()
	if stamp != nil && onIssued != nil {
		onIssued(*stamp)
	}
	if err != nil {
		p.mu.Lock()
		delete(p.notificationCorrelations, attemptID)
		p.mu.Unlock()
		reason, message := "notification_transport_unknown", "Output did not confirm the attempt."
		if stamp == nil {
			retry := false
			reason = "notification_transport_unsubmitted"
			message = "The request was not handed to the sender."
			return domain.AttemptEvidence{Status: "unsubmitted", Reason: &reason, Message: &message, Retryable: &retry}
		}
		return domain.AttemptEvidence{Status: "unknown", Reason: &reason, Message: &message}
	}
	if confirmed.Status != "unknown" {
		p.mu.Lock()
		delete(p.notificationCorrelations, attemptID)
		p.mu.Unlock()
	}
	return notificationEvidence(confirmed)
}

// handleNotificationFrameLocked runs under both stream locks. Callbacks run
// after releasing them so the lifecycle fence never nests the stream mutex.
func (p *Process) handleNotificationFrameLocked(f pluginprotocol.Frame) (bool, func()) {
	if f.Type != "notification_result" && f.Type != "notification_evidence" {
		return false, nil
	}
	c := p.notificationCorrelations[f.RequestID]
	if c == nil || !p.capabilities[pluginprotocol.NotificationCapability] || c.deliveryID != f.DeliveryID || !time.Now().Before(c.deadline) {
		p.rejectFrame("notification_correlation_invalid")
		return true, nil
	}
	if f.Type == "notification_result" {
		pending := p.pending[f.RequestID]
		if pending == nil || !pending.sent || pending.claimed || pending.kind != "notification_result" {
			p.rejectFrame("notification_result_duplicate_or_early")
			return true, nil
		}
		pending.claimed = true
		delete(p.pending, f.RequestID)
		if f.Status == "unknown" {
			c.unknown = true
			time.AfterFunc(time.Until(c.deadline), func() {
				p.mu.Lock()
				if p.notificationCorrelations[f.RequestID] == c {
					delete(p.notificationCorrelations, f.RequestID)
				}
				p.mu.Unlock()
			})
		} else {
			delete(p.notificationCorrelations, f.RequestID)
		}
		pending.result <- requestResult{frame: f}
		return true, nil
	}
	if !c.unknown {
		p.rejectFrame("notification_evidence_without_unknown")
		return true, nil
	}
	delete(p.notificationCorrelations, f.RequestID)
	return true, func() {
		if c.evidence != nil {
			c.evidence(notificationEvidence(f))
		}
	}
}

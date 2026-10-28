package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const NotificationRetention = 7 * 24 * time.Hour

type AttemptEvidence struct {
	Status       string
	Reason       *string
	Message      *string
	Receipt      *string
	Retryable    *bool
	RetryAfterMS *int
}

type NotificationAttempt struct {
	ID         string
	Ordinal    int
	ReservedAt time.Time
	IssuedAt   *time.Time
	FinishedAt *time.Time
	AttemptEvidence
}

type Notification struct {
	Event         Event
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Acceptance    string
	RetryState    string
	Reason        *string
	NextAttemptAt *time.Time
	Attempts      []NotificationAttempt
	BindingHash   string
}

func ValidNotificationID(id string) bool {
	if len(id) != 36 || id == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func NotificationBinding(event Event) string {
	tuple := jsonvalue.NewArray(jsonvalue.NewString(string(event.ID)), jsonvalue.NewString(string(event.SubscriptionID)), jsonvalue.NewString(event.InputPlugin), jsonvalue.NewString(event.OutputPlugin), event.DestinationArgs, jsonvalue.NewString(event.Context))
	hash := sha256.Sum256([]byte(tuple.Canonical()))
	return hex.EncodeToString(hash[:])
}

func NewNotification(event Event, now time.Time) Notification {
	return Notification{Event: event, CreatedAt: now, ExpiresAt: now.Add(NotificationRetention), Acceptance: "not_attempted", RetryState: "scheduled", NextAttemptAt: &now, Attempts: []NotificationAttempt{}, BindingHash: NotificationBinding(event)}
}

func (n Notification) Clone() Notification {
	n.Attempts = append([]NotificationAttempt{}, n.Attempts...)
	return n
}

func (n Notification) Terminal() bool {
	return n.RetryState != "scheduled" && n.RetryState != "in_progress"
}

func (n Notification) Eligible(now time.Time) bool {
	if n.RetryState != "scheduled" || n.NextAttemptAt == nil || now.Before(*n.NextAttemptAt) || !now.Before(n.ExpiresAt) || len(n.Attempts) >= 3 {
		return false
	}
	for _, a := range n.Attempts {
		if a.Status != "unsubmitted" || a.Retryable == nil || !*a.Retryable {
			return false
		}
	}
	return true
}

func (n *Notification) Reserve(id string, now time.Time) error {
	if !ValidNotificationID(id) || !n.Eligible(now) {
		return errors.New("notification is not eligible for reservation")
	}
	for _, a := range n.Attempts {
		if a.ID == id {
			return errors.New("attempt identifier already consumed")
		}
	}
	n.Attempts = append(n.Attempts, NotificationAttempt{ID: id, Ordinal: len(n.Attempts) + 1, ReservedAt: now, AttemptEvidence: AttemptEvidence{Status: "in_progress"}})
	n.Acceptance, n.RetryState, n.Reason, n.NextAttemptAt = "in_progress", "in_progress", nil, nil
	return nil
}

func (n *Notification) Finish(id string, evidence AttemptEvidence, now time.Time) error {
	if len(n.Attempts) == 0 || n.Attempts[len(n.Attempts)-1].ID != id {
		return errors.New("attempt correlation mismatch")
	}
	a := &n.Attempts[len(n.Attempts)-1]
	if a.Status != "in_progress" && !(a.Status == "unknown" && evidence.Status == "accepted") {
		return errors.New("duplicate or conflicting result")
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	a.AttemptEvidence, a.FinishedAt = evidence, &now
	terminalReason := n.Reason
	n.NextAttemptAt, n.Reason = nil, evidence.Reason
	n.Acceptance = evidence.Status
	if evidence.Status == "accepted" {
		n.Acceptance = "queue_accepted"
	}
	if n.RetryState == "cancelled" || n.RetryState == "expired" {
		n.Reason = terminalReason
		return nil
	}
	switch evidence.Status {
	case "accepted":
		n.RetryState = "accepted"
	case "unknown":
		n.RetryState = "unknown"
	case "rejected":
		n.RetryState = "permanent"
	case "unsubmitted":
		if !*evidence.Retryable {
			n.RetryState = "permanent"
			break
		}
		if len(n.Attempts) >= 3 {
			n.Stop("exhausted", "attempts_exhausted")
			break
		}
		delay := 2 * time.Second
		if len(n.Attempts) == 2 {
			delay = 10 * time.Second
		}
		if evidence.RetryAfterMS != nil && time.Duration(*evidence.RetryAfterMS)*time.Millisecond > delay {
			delay = time.Duration(*evidence.RetryAfterMS) * time.Millisecond
		}
		due := now.Add(delay)
		if !due.Before(n.ExpiresAt) {
			n.Stop("expired", "notification_expired")
			break
		}
		n.RetryState, n.NextAttemptAt = "scheduled", &due
	}
	return nil
}

func (n *Notification) Stop(state, reason string) {
	n.RetryState, n.Reason, n.NextAttemptAt = state, &reason, nil
}

func (e AttemptEvidence) Validate() error {
	if e.Status == "accepted" {
		if e.Reason != nil || e.Message != nil || e.Retryable != nil || e.RetryAfterMS != nil || (e.Receipt != nil && (*e.Receipt == "" || len(*e.Receipt) > 1024)) {
			return errors.New("invalid acceptance evidence")
		}
		return nil
	}
	if e.Status != "unsubmitted" && e.Status != "rejected" && e.Status != "unknown" {
		return errors.New("invalid attempt status")
	}
	if e.Reason == nil || len(*e.Reason) < 1 || len(*e.Reason) > 128 || e.Message == nil || len(*e.Message) < 1 || len(*e.Message) > 1024 || e.Receipt != nil {
		return errors.New("invalid failure evidence")
	}
	for _, c := range *e.Reason {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return errors.New("invalid reason token")
		}
	}
	if e.Status != "unsubmitted" {
		if e.Retryable != nil || e.RetryAfterMS != nil {
			return errors.New("submission cannot be retried")
		}
		return nil
	}
	if e.Retryable == nil || (e.RetryAfterMS != nil && (!*e.Retryable || *e.RetryAfterMS < 0 || *e.RetryAfterMS > 60000)) {
		return errors.New("invalid retry guidance")
	}
	return nil
}

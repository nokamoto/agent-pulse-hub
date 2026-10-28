package domain

import (
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func notificationForTest() Notification {
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	return NewNotification(Event{ID: "11111111-1111-4111-8111-111111111111", SubscriptionID: "22222222-2222-4222-8222-222222222222", InputPlugin: "source", OutputPlugin: "action", DestinationArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"n": jsonvalue.NewNumber("1.0")}), Context: "exact\ncontext"}, now)
}

func testEvidence(status string, retry bool) AttemptEvidence {
	reason, message := "controlled", "Controlled outcome."
	e := AttemptEvidence{Status: status}
	if status != "accepted" {
		e.Reason = &reason
		e.Message = &message
	}
	if status == "unsubmitted" {
		e.Retryable = &retry
	}
	return e
}

func TestNotificationFiniteRetryAndIdentity(t *testing.T) {
	n := notificationForTest()
	binding := n.BindingHash
	now := n.CreatedAt
	ids := []string{"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"}
	for i, id := range ids {
		if err := n.Reserve(id, now); err != nil {
			t.Fatal(err)
		}
		if n.Eligible(now) || n.Reserve(id, now) == nil {
			t.Fatal("overlapping reservation allowed")
		}
		if err := n.Finish(id, testEvidence("unsubmitted", true), now); err != nil {
			t.Fatal(err)
		}
		if n.BindingHash != binding || n.BindingHash != NotificationBinding(n.Event) {
			t.Fatal("identity drift")
		}
		if i < 2 {
			delay := 2 * time.Second
			if i == 1 {
				delay = 10 * time.Second
			}
			if n.NextAttemptAt == nil || !n.NextAttemptAt.Equal(now.Add(delay)) || n.Eligible(now.Add(delay-time.Nanosecond)) {
				t.Fatal("minimum retry delay was not preserved")
			}
			now = now.Add(delay)
		}
	}
	if n.RetryState != "exhausted" || n.Acceptance != "unsubmitted" || n.NextAttemptAt != nil || n.Eligible(now.Add(time.Hour)) {
		t.Fatalf("exhaustion=%+v", n)
	}
}

func TestNotificationSubmissionStoppingAndMonotonicEvidence(t *testing.T) {
	for _, status := range []string{"accepted", "unknown", "rejected", "unsubmitted"} {
		t.Run(status, func(t *testing.T) {
			n := notificationForTest()
			id := "33333333-3333-4333-8333-333333333333"
			_ = n.Reserve(id, n.CreatedAt)
			if err := n.Finish(id, testEvidence(status, false), n.CreatedAt); err != nil {
				t.Fatal(err)
			}
			if !n.Terminal() || n.Eligible(n.CreatedAt.Add(time.Hour)) {
				t.Fatal("possible or permanent submission retried")
			}
			if status == "unknown" {
				n.Stop("cancelled", "subscription_cancelled")
				if err := n.Finish(id, testEvidence("accepted", false), n.CreatedAt.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if n.Acceptance != "queue_accepted" || n.RetryState != "cancelled" {
					t.Fatalf("late cancellation evidence=%+v", n)
				}
				if n.Finish(id, testEvidence("unknown", false), n.CreatedAt.Add(2*time.Second)) == nil {
					t.Fatal("stale failure erased acceptance")
				}
			}
		})
	}
}

func TestNotificationGuidanceExpiryAndBinding(t *testing.T) {
	n := notificationForTest()
	changed := n.Event
	changed.Context += " "
	if NotificationBinding(changed) == n.BindingHash {
		t.Fatal("context conflict not distinguished")
	}
	changed = n.Event
	changed.DestinationArgs = jsonvalue.NewObject(map[string]*jsonvalue.Value{"n": jsonvalue.NewNumber("1e0")})
	if NotificationBinding(changed) != n.BindingHash {
		t.Fatal("canonical numeric equality lost")
	}
	id := "33333333-3333-4333-8333-333333333333"
	_ = n.Reserve(id, n.CreatedAt)
	e := testEvidence("unsubmitted", true)
	after := 60000
	e.RetryAfterMS = &after
	if err := n.Finish(id, e, n.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if n.NextAttemptAt.Sub(n.CreatedAt) != time.Minute {
		t.Fatal("retry guidance ignored")
	}
	n = notificationForTest()
	now := n.ExpiresAt.Add(-time.Second)
	_ = n.Reserve(id, now)
	_ = n.Finish(id, testEvidence("unsubmitted", true), now)
	if n.RetryState != "expired" || n.Eligible(n.ExpiresAt) {
		t.Fatal("expiry extended")
	}
	for _, id := range []string{"", "00000000-0000-0000-0000-000000000000", "AAAAAAAA-1111-4111-8111-111111111111"} {
		if ValidNotificationID(id) {
			t.Fatal("invalid UUID accepted")
		}
	}
}

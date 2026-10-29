package hub

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func notificationPolicyHub(t *testing.T) (*Hub, *MockNotificationStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	store := NewMockNotificationStore(gomock.NewController(t))
	h := &Hub{logger: slog.Default(), queueReady: make(chan struct{}, 1), rewaitNow: func() time.Time { return now }, admissionOpen: true, plugins: map[string]*pluginState{"source": {available: true}, "action": {available: true}}, registrations: map[domain.SubscriptionID]domain.Subscription{}, cancellations: map[domain.SubscriptionID]*cancellationState{}, notificationWaits: map[domain.SubscriptionID]*notificationWait{}}
	h.registrations["22222222-2222-4222-8222-222222222222"] = domain.Subscription{ID: "22222222-2222-4222-8222-222222222222", Active: true}
	h.notifications = &notificationState{store: store, records: map[string]domain.Notification{}, work: map[string]queuedEvent{}, lastTime: now, earlyEvidence: map[string]domain.AttemptEvidence{}, evidenceUntil: map[string]time.Time{}, cutoff: map[string]bool{}}
	return h, store, &now
}

func policyNotification(now time.Time, id string) domain.Notification {
	return domain.NewNotification(domain.Event{ID: domain.DeliveryID(id), SubscriptionID: "22222222-2222-4222-8222-222222222222", InputPlugin: "source", OutputPlugin: "action", DestinationArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{}), Context: "sensitive external context"}, now)
}

func TestNotificationGatesClockStorageAndInspection(t *testing.T) {
	h, store, now := notificationPolicyHub(t)
	n := policyNotification(*now, "11111111-1111-4111-8111-111111111111")
	id := string(n.Event.ID)
	h.notifications.records[id] = n
	h.notifications.work[id] = queuedEvent{event: n.Event}
	*now = now.Add(-time.Second)
	view, blocked, gate, e := h.InspectNotification(id)
	if e != nil || blocked || gate != "clock_regression" || view.RetryState != "scheduled" {
		t.Fatalf("clock projection: %v %v %v %v", view, blocked, gate, e)
	}
	if candidate, _ := h.nextNotificationLocked(); candidate != nil {
		t.Fatal("clock regression authorized issuance")
	}
	*now = now.Add(time.Second)
	if candidate, _ := h.nextNotificationLocked(); candidate == nil {
		t.Fatal("clock recovery did not recheck eligibility")
	}
	h.plugins["action"].available = false
	if gate := h.notificationGateLocked(n, *now); gate != "plugin_unavailable" {
		t.Fatalf("plugin gate=%s", gate)
	}
	store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).Return(errors.New("controlled save failure"))
	if h.saveNotificationsLocked(*now) {
		t.Fatal("failed save accepted")
	}
	if h.saveNotificationsLocked(*now) {
		t.Fatal("storage gate automatically recovered")
	}
	_, blocked, gate, e = h.InspectNotification(id)
	if e != nil || !blocked || gate != "notification_storage_error" {
		t.Fatal("storage gate priority missing")
	}
}

func TestNotificationDueOrderStaleCallbacksAndRetention(t *testing.T) {
	h, store, now := notificationPolicyHub(t)
	for _, id := range []string{"33333333-3333-4333-8333-333333333333", "11111111-1111-4111-8111-111111111111"} {
		n := policyNotification(*now, id)
		h.notifications.records[id] = n
		h.notifications.work[id] = queuedEvent{event: n.Event}
	}
	first, _ := h.nextNotificationLocked()
	if first == nil || string(first.event.ID) != "11111111-1111-4111-8111-111111111111" {
		t.Fatal("ready tie order is not deterministic")
	}
	n := h.notifications.records[string(first.event.ID)]
	attempt := "44444444-4444-4444-8444-444444444444"
	if n.Reserve(attempt, *now) != nil {
		t.Fatal("reservation")
	}
	h.notifications.records[string(n.Event.ID)] = n
	h.notifications.work[string(n.Event.ID)] = *first
	second, _ := h.nextNotificationLocked()
	if second == nil || second.event.ID == n.Event.ID {
		t.Fatal("stale work issued an overlapping reservation")
	}
	// An issued reservation remains retained across expiry until its result;
	// an expired quiescent record is eligible for bounded compaction.
	*now = now.Add(domain.NotificationRetention)
	store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	h.maintainNotificationsLocked(*now)
	h.notifications.noticeWait.Wait()
	if _, ok := h.notifications.records[string(second.event.ID)]; ok {
		t.Fatal("expired quiescent record was retained")
	}
	if h.notifications.records[string(n.Event.ID)].RetryState != "expired" {
		t.Fatal("issued expiry not stopped")
	}
}

func TestNotificationCancellationAndCutoffSaveBeforeNewIssuance(t *testing.T) {
	h, store, now := notificationPolicyHub(t)
	n := policyNotification(*now, "11111111-1111-4111-8111-111111111111")
	attempt := "33333333-3333-4333-8333-333333333333"
	_ = n.Reserve(attempt, *now)
	id := string(n.Event.ID)
	h.notifications.records[id] = n
	store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).DoAndReturn(func(records []domain.Notification, _ time.Time) error {
		if len(records) != 1 || records[0].Acceptance != "unknown" {
			t.Fatal("cutoff did not persist uncertainty")
		}
		return nil
	})
	h.notificationCutoffLocked()
	if !h.notifications.cutoff[id] || h.notifications.records[id].RetryState != "unknown" {
		t.Fatal("cutoff boundary missing")
	}
	store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).Return(nil)
	if !h.cancelNotificationsLocked(n.Event.SubscriptionID) {
		t.Fatal("cancellation save")
	}
	if h.notifications.records[id].RetryState != "cancelled" {
		t.Fatal("cancellation not retained")
	}
	h.notifications.evidenceUntil[id] = now.Add(time.Second)
	store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).Return(nil)
	h.recordNotificationEvidence(id, attempt, domain.AttemptEvidence{Status: "accepted"})
	got := h.notifications.records[id]
	if got.Acceptance != "queue_accepted" || got.RetryState != "cancelled" {
		t.Fatal("late acceptance revived cancellation")
	}
	*now = now.Add(2 * time.Second)
	h.recordNotificationEvidence(id, attempt, domain.AttemptEvidence{Status: "accepted"})
}

type notificationRegistrationMocks struct {
	*MockRegistrationStore
	*MockNotificationStore
}

func TestNotificationRecoveryStopsUnconfirmedReservationsBeforeRestoration(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "binding_mismatch"}[mismatch], func(t *testing.T) {
			h, store, now := notificationPolicyHub(t)
			n := policyNotification(*now, "11111111-1111-4111-8111-111111111111")
			if err := n.Reserve("33333333-3333-4333-8333-333333333333", *now); err != nil {
				t.Fatal(err)
			}
			h.store = notificationRegistrationMocks{NewMockRegistrationStore(gomock.NewController(t)), store}
			r := domain.Subscription{ID: n.Event.SubscriptionID, InputPlugin: n.Event.InputPlugin, OutputPlugin: n.Event.OutputPlugin, DestinationArgs: n.Event.DestinationArgs}
			if mismatch {
				r.InputPlugin = "foreign"
			}
			h.restore = []domain.Subscription{r}
			h.plugins["action"].capabilities = map[string]bool{"notification_delivery_v1": true}
			store.EXPECT().LoadNotifications().Return([]domain.Notification{n}, *now, false, nil)
			if !mismatch {
				store.EXPECT().CommitNotifications(gomock.Any(), gomock.Any()).DoAndReturn(func(records []domain.Notification, _ time.Time) error {
					if len(records) != 1 || records[0].Acceptance != "unknown" || records[0].RetryState != "unknown" {
						t.Fatal("reservation recovered as eligible")
					}
					return nil
				})
			}
			err := h.PrepareNotifications()
			if (err != nil) != mismatch {
				t.Fatalf("recovery mismatch=%v err=%v", mismatch, err)
			}
			if !mismatch && len(h.notifications.work) != 0 {
				t.Fatal("interrupted reservation scheduled")
			}
		})
	}
}

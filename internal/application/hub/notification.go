package hub

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=notification.go -destination=notification_mock_test.go -package=hub

type NotificationStore interface {
	LoadNotifications() ([]domain.Notification, time.Time, bool, error)
	CommitNotifications([]domain.Notification, time.Time) error
}

type notificationPort interface {
	DeliverNotification(context.Context, domain.Event, string, func(func()) bool, func(time.Time), func(domain.AttemptEvidence)) domain.AttemptEvidence
}

type notificationState struct {
	cutoff         map[string]bool
	noticeWait     sync.WaitGroup
	store          NotificationStore
	records        map[string]domain.Notification
	work           map[string]queuedEvent
	lastTime       time.Time
	storageBlocked bool
	earlyEvidence  map[string]domain.AttemptEvidence
	evidenceUntil  map[string]time.Time
}

func (h *Hub) PrepareNotifications() error {
	store, ok := h.store.(NotificationStore)
	if !ok {
		return errors.New("notification storage is unavailable")
	}
	records, saved, initialized, err := store.LoadNotifications()
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ns := &notificationState{cutoff: map[string]bool{}, store: store, records: map[string]domain.Notification{}, work: map[string]queuedEvent{}, lastTime: saved, earlyEvidence: map[string]domain.AttemptEvidence{}, evidenceUntil: map[string]time.Time{}}
	owners := map[domain.SubscriptionID]domain.Subscription{}
	for _, r := range h.restore {
		owners[r.ID] = r
	}
	for _, n := range records {
		r, exists := owners[n.Event.SubscriptionID]
		if exists && (r.InputPlugin != n.Event.InputPlugin || r.OutputPlugin != n.Event.OutputPlugin || !r.DestinationArgs.Equivalent(n.Event.DestinationArgs)) {
			return errors.New("retained notification route binding differs from saved registration")
		}
		if exists {
			p := h.plugins[r.OutputPlugin]
			if p == nil || !p.available || !p.capabilities["notification_delivery_v1"] {
				return errors.New("retained notification requires its original participating output")
			}
		}
	}
	h.notifications = ns
	now := h.notificationNowLocked()
	if now.Before(ns.lastTime) {
		now = ns.lastTime
	}
	changed := false
	for _, n := range records {
		r, exists := owners[n.Event.SubscriptionID]
		if len(n.Attempts) > 0 && n.Attempts[len(n.Attempts)-1].Status == "in_progress" {
			reason, message := "notification_interrupted", "Interrupted reservation cannot prove non-submission."
			if err := n.Finish(n.Attempts[len(n.Attempts)-1].ID, domain.AttemptEvidence{Status: "unknown", Reason: &reason, Message: &message}, now); err != nil {
				return err
			}
			changed = true
		}
		if !exists {
			n.Stop("cancelled", "subscription_cancelled")
			changed = true
		}
		ns.records[string(n.Event.ID)] = n
		if n.RetryState == "scheduled" {
			ns.work[string(n.Event.ID)] = queuedEvent{event: n.Event, outputPort: h.plugins[r.OutputPlugin].port}
			h.pendingEvents++
		}
	}
	if changed && !h.saveNotificationsLocked(now) {
		return errors.New("notification recovery could not be saved")
	}
	if initialized {
		h.logger.Info("notification_history_initialized")
	}
	return nil
}

func (h *Hub) notificationNowLocked() time.Time {
	now := h.rewaitNow().UTC()
	if h.notifications != nil && now.After(h.notifications.lastTime) {
		h.notifications.lastTime = now
	}
	return now
}

func (h *Hub) notificationGateLocked(n domain.Notification, now time.Time) string {
	ns := h.notifications
	if ns == nil || ns.storageBlocked {
		return "notification_storage_error"
	}
	if now.Before(ns.lastTime) {
		return "clock_regression"
	}
	in, out := h.plugins[n.Event.InputPlugin], h.plugins[n.Event.OutputPlugin]
	if in == nil || out == nil || !in.available || !out.available {
		return "plugin_unavailable"
	}
	return ""
}

func (h *Hub) saveNotificationsLocked(now time.Time) bool {
	ns := h.notifications
	if ns == nil || ns.storageBlocked {
		return false
	}
	records := make([]domain.Notification, 0, len(ns.records))
	for _, n := range ns.records {
		records = append(records, n.Clone())
	}
	saved := now
	if saved.Before(ns.lastTime) {
		saved = ns.lastTime
	}
	if err := ns.store.CommitNotifications(records, saved); err != nil {
		ns.storageBlocked = true
		h.logger.Error("notification_storage_error", "message", "Participating admission and issuance are closed; stop, repair and restart.")
		return false
	}
	ns.lastTime = saved
	return true
}

func (h *Hub) admitNotificationLocked(item queuedEvent) bool {
	ns := h.notifications
	if ns == nil || ns.storageBlocked {
		return false
	}
	now := h.notificationNowLocked()
	h.maintainNotificationsLocked(now)
	if len(ns.records) >= 1024 {
		h.logger.Warn("notification_capacity_exceeded")
		return false
	}
	n := domain.NewNotification(item.event, now)
	id := string(n.Event.ID)
	if _, exists := ns.records[id]; exists {
		return false
	}
	ns.records[id] = n
	if !h.saveNotificationsLocked(now) {
		delete(ns.records, id)
		return false
	}
	ns.work[id] = item
	h.pendingEvents++
	if wait := h.notificationWaits[item.event.SubscriptionID]; wait != nil {
		wait.armed = false
		wait.outstanding = true
	}
	h.signalQueueLocked()
	h.logger.Info("event_admitted", "subscription_id", n.Event.SubscriptionID, "delivery_id", n.Event.ID)
	return true
}

// nextNotificationLocked selects one candidate for the existing single worker.
// A waiting retry owns its work slot but does not occupy the worker.
func (h *Hub) nextNotificationLocked() (*queuedEvent, time.Duration) {
	ns := h.notifications
	if ns == nil || !h.admissionOpen || h.stopping {
		return nil, time.Hour
	}
	now := h.notificationNowLocked()
	delay := time.Hour
	if now.Before(ns.lastTime) {
		return nil, time.Second
	}
	h.maintainNotificationsLocked(now)
	var selected *domain.Notification
	for id := range ns.work {
		n := ns.records[id]
		r, exists := h.registrations[n.Event.SubscriptionID]
		if !exists || !r.Active || h.cancellations[n.Event.SubscriptionID] != nil {
			continue
		}
		if h.notificationGateLocked(n, now) != "" {
			continue
		}
		if n.NextAttemptAt == nil {
			continue
		}
		if wait := n.NextAttemptAt.Sub(now); wait > 0 {
			if wait < delay {
				delay = wait
			}
			continue
		}
		if !n.Eligible(now) {
			continue
		}
		if selected == nil || n.NextAttemptAt.Before(*selected.NextAttemptAt) || n.NextAttemptAt.Equal(*selected.NextAttemptAt) && (n.CreatedAt.Before(selected.CreatedAt) || n.CreatedAt.Equal(selected.CreatedAt) && n.Event.ID < selected.Event.ID) {
			copy := n
			selected = &copy
		}
	}
	if selected == nil {
		return nil, delay
	}
	id := string(selected.Event.ID)
	item := ns.work[id]
	delete(ns.work, id)
	return &item, 0
}

func (h *Hub) deliverNotification(item queuedEvent) {
	port, ok := item.outputPort.(notificationPort)
	if !ok {
		return
	}
	id := string(item.event.ID)
	attemptID, err := newID()
	if err != nil {
		return
	}
	reserved := false
	result := port.DeliverNotification(h.ctx, item.event, attemptID, func(issue func()) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		ns := h.notifications
		n := ns.records[id]
		now := h.notificationNowLocked()
		r, exists := h.registrations[n.Event.SubscriptionID]
		if !exists || !r.Active || h.stopping || h.cancellations[r.ID] != nil || h.notificationGateLocked(n, now) != "" || !n.Eligible(now) {
			return false
		}
		if n.Reserve(attemptID, now) != nil {
			return false
		}
		ns.records[id] = n
		if !h.saveNotificationsLocked(now) {
			return false
		}
		reserved = true
		issue()
		ns.evidenceUntil[id] = now.Add(30 * time.Second)
		return true
	}, func(stamp time.Time) {
		h.mu.Lock()
		defer h.mu.Unlock()
		ns := h.notifications
		n, ok := ns.records[id]
		if !ok || len(n.Attempts) == 0 || n.Attempts[len(n.Attempts)-1].ID != attemptID {
			return
		}
		n.Attempts[len(n.Attempts)-1].IssuedAt = &stamp
		ns.records[id] = n
	}, func(e domain.AttemptEvidence) { h.recordNotificationEvidence(id, attemptID, e) })
	h.mu.Lock()
	ns := h.notifications
	n := ns.records[id]
	now := h.notificationNowLocked()
	if now.Before(ns.lastTime) {
		now = ns.lastTime
	}
	if !reserved {
		terminal := n.Terminal()
		if !terminal {
			ns.work[id] = item
		}
		h.signalQueueLocked()
		h.mu.Unlock()
		if terminal {
			h.completeNotificationItem(item, "failed")
		}
		return
	}
	if err := n.Finish(attemptID, result, now); err != nil && !ns.cutoff[id] {
		h.mu.Unlock()
		return
	}
	ns.records[id] = n
	saved := h.saveNotificationsLocked(now)
	if early, ok := ns.earlyEvidence[attemptID]; ok {
		delete(ns.earlyEvidence, attemptID)
		if n.Finish(attemptID, early, now) == nil {
			ns.records[id] = n
			saved = h.saveNotificationsLocked(now) && saved
		}
	}
	if n.RetryState == "scheduled" && saved && !h.stopping {
		ns.work[id] = item
		h.signalQueueLocked()
		h.mu.Unlock()
		return
	}
	// A failed save closes the global gate. Retain work ownership until normal
	// stop; an in-memory unsubmitted result cannot authorize a future issue.
	if !saved && !h.stopping && !n.Terminal() {
		ns.work[id] = item
		h.mu.Unlock()
		return
	}
	status := "failed"
	if result.Status == "accepted" {
		status = "accepted"
	}
	if result.Status == "unknown" || ns.cutoff[id] {
		status = "unknown"
	}
	h.mu.Unlock()
	h.completeNotificationItem(item, status)
}

func (h *Hub) recordNotificationEvidence(id, attemptID string, e domain.AttemptEvidence) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ns := h.notifications
	if ns == nil || h.stopping {
		return
	}
	n, ok := ns.records[id]
	if !ok || len(n.Attempts) == 0 || n.Attempts[len(n.Attempts)-1].ID != attemptID {
		return
	}
	now := h.notificationNowLocked()
	if !now.Before(ns.evidenceUntil[id]) {
		return
	}
	if n.Attempts[len(n.Attempts)-1].Status == "in_progress" {
		ns.earlyEvidence[attemptID] = e
		return
	}
	if n.Finish(attemptID, e, now) == nil {
		ns.records[id] = n
		h.saveNotificationsLocked(now)
	}
}

func (h *Hub) completeNotificationItem(item queuedEvent, status string) {
	h.mu.Lock()
	_, cancelled := h.cancellations[item.event.SubscriptionID]
	r, exists := h.registrations[item.event.SubscriptionID]
	h.mu.Unlock()
	if cancelled || !exists || !r.Active || item.eventID == "" {
		h.releasePendingEvents(1)
		return
	}
	h.finishRuntimeOutcome(item, status)
}

func (h *Hub) stopNotificationWork() {
	h.mu.Lock()
	ns := h.notifications
	var items []queuedEvent
	if ns != nil {
		for id, item := range ns.work {
			items = append(items, item)
			delete(ns.work, id)
		}
		h.saveNotificationsLocked(h.notificationNowLocked())
	}
	h.mu.Unlock()
	for _, item := range items {
		h.completeNotificationItem(item, "failed")
	}
}

func (h *Hub) cancelNotificationsLocked(id domain.SubscriptionID) bool {
	ns := h.notifications
	if ns == nil {
		return true
	}
	now := h.notificationNowLocked()
	changed := false
	for key, n := range ns.records {
		if n.Event.SubscriptionID == id {
			n.Stop("cancelled", "subscription_cancelled")
			ns.records[key] = n
			changed = true
			if _, ok := ns.work[key]; ok {
				delete(ns.work, key)
				h.pendingEvents--
			}
		}
	}
	return !changed || h.saveNotificationsLocked(now)
}

// InspectNotification returns one immutable lifecycle view. Encoding and chunk
// transport belong to the local-control adapter.
func (h *Hub) InspectNotification(id string) (domain.Notification, bool, string, *Error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifications == nil {
		return domain.Notification{}, false, "", &Error{Code: "notification_storage_error", Message: "Notification storage is unavailable."}
	}
	n, ok := h.notifications.records[id]
	if !ok {
		return domain.Notification{}, false, "", &Error{Code: "notification_not_found", Message: "No retained notification has this ID."}
	}
	now := h.notificationNowLocked()
	return n.Clone(), h.notifications.storageBlocked, h.notificationGateLocked(n, now), nil
}

type NotificationSummary struct {
	Notification domain.Notification
	Gate         string
}

func (h *Hub) ListNotifications(subscriptionID, after string, limit int) ([]NotificationSummary, string, *Error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifications == nil {
		return nil, "", &Error{Code: "notification_storage_error", Message: "Notification storage is unavailable."}
	}
	now := h.notificationNowLocked()
	var ids []string
	for id, n := range h.notifications.records {
		if string(n.Event.SubscriptionID) == subscriptionID && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	result := make([]NotificationSummary, 0, len(ids))
	for _, id := range ids {
		n := h.notifications.records[id]
		result = append(result, NotificationSummary{n.Clone(), h.notificationGateLocked(n, now)})
	}
	return result, next, nil
}

func (h *Hub) notificationCutoffLocked() {
	ns := h.notifications
	if ns == nil {
		return
	}
	now := h.notificationNowLocked()
	if now.Before(ns.lastTime) {
		now = ns.lastTime
	}
	changed := false
	for id, n := range ns.records {
		if len(n.Attempts) == 0 {
			continue
		}
		a := n.Attempts[len(n.Attempts)-1]
		if a.Status != "in_progress" {
			continue
		}
		reason, message := "notification_interrupted", "Attempt was unconfirmed at normal-stop cutoff."
		if n.Finish(a.ID, domain.AttemptEvidence{Status: "unknown", Reason: &reason, Message: &message}, now) == nil {
			ns.records[id] = n
			ns.cutoff[id] = true
			changed = true
		}
	}
	if changed {
		h.saveNotificationsLocked(now)
	}
}

func (h *Hub) maintainNotificationsLocked(now time.Time) {
	ns := h.notifications
	if ns == nil || ns.storageBlocked || now.Before(ns.lastTime) {
		return
	}
	before := make(map[string]domain.Notification, len(ns.records))
	for id, n := range ns.records {
		before[id] = n
	}
	var expiredWork []queuedEvent
	changed := false
	for id, n := range ns.records {
		if now.Before(n.ExpiresAt) {
			continue
		}
		if item, ok := ns.work[id]; ok {
			n.Stop("expired", "notification_expired")
			ns.records[id] = n
			expiredWork = append(expiredWork, item)
			changed = true
			continue
		}
		if len(n.Attempts) > 0 && n.Attempts[len(n.Attempts)-1].Status == "in_progress" {
			if n.RetryState != "expired" {
				n.Stop("expired", "notification_expired")
				ns.records[id] = n
				changed = true
			}
			continue
		}
		if now.Before(ns.evidenceUntil[id]) {
			continue
		}
		delete(ns.records, id)
		changed = true
	}
	if !changed {
		return
	}
	if !h.saveNotificationsLocked(now) {
		ns.records = before
		return
	}
	for _, item := range expiredWork {
		delete(ns.work, string(item.event.ID))
		ns.noticeWait.Add(1)
		go func() { defer ns.noticeWait.Done(); h.completeNotificationItem(item, "failed") }()
	}
	for id := range ns.evidenceUntil {
		if _, ok := ns.records[id]; !ok {
			delete(ns.evidenceUntil, id)
			delete(ns.cutoff, id)
		}
	}
}

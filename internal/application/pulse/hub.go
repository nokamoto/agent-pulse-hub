package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	domain "github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

const WatchAcknowledgementTimeout = 10 * time.Second

type Plugin interface {
	Available() bool
	Watch(context.Context, domain.WatchRequest, func() bool) WatchResult
	Stop(string)
}

type WatchResult struct {
	Uncertain bool
	Err       error
}

type DeliveryPort interface {
	Deliver(context.Context, domain.Delivery) (domain.DeliveryOutcome, error)
}

type RegistrationError struct {
	Code    string
	Message string
}

func (e *RegistrationError) Error() string {
	return e.Code + ": " + e.Message
}

type registrationKey struct {
	plugin    string
	session   string
	arguments string
}

type subscription struct {
	id      string
	plugin  string
	session string
	key     registrationKey
}

type pendingRegistration struct {
	plugin string
	id     string
	key    registrationKey
	done   chan struct{}
	err    error
}

type deliveryJob struct {
	delivery domain.Delivery
}

type Hub struct {
	mu            sync.Mutex
	configured    map[string]struct{}
	plugins       map[string]Plugin
	available     map[string]bool
	subscriptions map[string]subscription
	byKey         map[registrationKey]string
	pending       map[registrationKey]*pendingRegistration
	queue         chan deliveryJob
	slots         chan struct{}
	delivery      DeliveryPort
	logger        *slog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	worker        sync.WaitGroup
	closed        bool
}

func NewHub(pluginNames []string, delivery DeliveryPort, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	configured := make(map[string]struct{}, len(pluginNames))
	available := make(map[string]bool, len(pluginNames))
	for _, name := range pluginNames {
		configured[name] = struct{}{}
		available[name] = false
	}
	ctx, cancel := context.WithCancel(context.Background())
	hub := &Hub{
		configured:    configured,
		plugins:       make(map[string]Plugin, len(pluginNames)),
		available:     available,
		subscriptions: make(map[string]subscription),
		byKey:         make(map[registrationKey]string),
		pending:       make(map[registrationKey]*pendingRegistration),
		queue:         make(chan deliveryJob, domain.MaxDeliveryInFlight),
		slots:         make(chan struct{}, domain.MaxDeliveryInFlight),
		delivery:      delivery,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
	}
	hub.worker.Add(1)
	go hub.runDeliveryWorker()
	return hub
}

func (hub *Hub) SetPlugin(name string, plugin Plugin) error {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if _, exists := hub.configured[name]; !exists {
		return fmt.Errorf("plugin %q is not configured", name)
	}
	if hub.closed {
		return errors.New("daemon is shutting down")
	}
	hub.plugins[name] = plugin
	hub.available[name] = plugin.Available()
	if !hub.available[name] {
		return errors.New("plugin exited before registration became available")
	}
	if !plugin.Available() {
		hub.available[name] = false
		return errors.New("plugin exited while registration became available")
	}
	hub.logger.Info("plugin_ready", "plugin", name)
	return nil
}

func (hub *Hub) MarkUnavailable(name, reason string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if _, exists := hub.configured[name]; !exists {
		return
	}
	wasAvailable := hub.available[name]
	hub.available[name] = false
	for id, current := range hub.subscriptions {
		if current.plugin == name {
			delete(hub.subscriptions, id)
			delete(hub.byKey, current.key)
			hub.logger.Warn("subscription_invalidated", "plugin", name, "subscription_id", id, "reason", reason)
		}
	}
	for key, pending := range hub.pending {
		if pending.plugin == name {
			pending.err = &RegistrationError{Code: "plugin_unavailable", Message: "plugin became unavailable during registration"}
			delete(hub.pending, key)
			close(pending.done)
		}
	}
	if wasAvailable {
		hub.logger.Error("plugin_unavailable", "plugin", name, "reason", reason)
	}
}

func (hub *Hub) Register(ctx context.Context, pluginName, sessionID string, watchArgs json.RawMessage) (string, error) {
	if err := domain.ValidateUUID(sessionID); err != nil {
		return "", &RegistrationError{Code: "missing_identity", Message: "session_id must be a UUID from the current Codex conversation"}
	}
	canonical, err := domain.CanonicalWatchArgs(watchArgs)
	if err != nil {
		return "", &RegistrationError{Code: "invalid_watch_args", Message: "watch_args must be a valid JSON object"}
	}
	key := registrationKey{plugin: pluginName, session: strings.ToLower(sessionID), arguments: canonical}

	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "daemon_unavailable", Message: "daemon is shutting down"}
	}
	if _, exists := hub.configured[pluginName]; !exists {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "unknown_plugin", Message: "plugin name is not configured"}
	}
	if !hub.available[pluginName] {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "plugin_unavailable", Message: "plugin is not available"}
	}
	if id, exists := hub.byKey[key]; exists {
		hub.mu.Unlock()
		return id, nil
	}
	if pending, exists := hub.pending[key]; exists {
		hub.mu.Unlock()
		return waitForRegistration(ctx, pending)
	}
	if len(hub.subscriptions)+len(hub.pending) >= domain.MaxRegistrations {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "resource_exhausted", Message: "registration capacity is full"}
	}
	id, err := domain.NewID()
	if err != nil {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "internal_error", Message: "could not create a subscription identifier"}
	}
	requestID, err := domain.NewID()
	if err != nil {
		hub.mu.Unlock()
		return "", &RegistrationError{Code: "internal_error", Message: "could not create a watch request identifier"}
	}
	pending := &pendingRegistration{plugin: pluginName, id: id, key: key, done: make(chan struct{})}
	hub.pending[key] = pending
	plugin := hub.plugins[pluginName]
	hub.logger.Info("registration_requested", "plugin", pluginName, "request_id", requestID, "subscription_id", id)
	hub.mu.Unlock()

	watchContext, cancel := context.WithTimeout(ctx, WatchAcknowledgementTimeout)
	defer cancel()
	result := plugin.Watch(watchContext, domain.WatchRequest{
		RequestID:      requestID,
		SubscriptionID: id,
		WatchArgs:      append(json.RawMessage(nil), watchArgs...),
	}, func() bool {
		return hub.activate(pending)
	})
	if result.Err != nil {
		if result.Uncertain {
			plugin.Stop("watch acceptance outcome is unknown")
			hub.MarkUnavailable(pluginName, "watch acceptance outcome is unknown")
		} else {
			hub.rejectPending(pending, result.Err)
		}
	} else if !registrationResolved(pending) {
		plugin.Stop("plugin returned without a watch result")
		hub.MarkUnavailable(pluginName, "plugin returned without a watch result")
	}
	return waitForRegistration(context.Background(), pending)
}

func (hub *Hub) activate(pending *pendingRegistration) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if current, exists := hub.pending[pending.key]; !exists || current != pending {
		return false
	}
	if hub.closed || !hub.available[pending.plugin] {
		pending.err = &RegistrationError{Code: "plugin_unavailable", Message: "plugin became unavailable before registration completed"}
		delete(hub.pending, pending.key)
		close(pending.done)
		return false
	}
	hub.subscriptions[pending.id] = subscription{
		id:      pending.id,
		plugin:  pending.plugin,
		session: pending.key.session,
		key:     pending.key,
	}
	hub.byKey[pending.key] = pending.id
	delete(hub.pending, pending.key)
	hub.logger.Info("subscription_active", "plugin", pending.plugin, "subscription_id", pending.id)
	close(pending.done)
	return true
}

func registrationResolved(pending *pendingRegistration) bool {
	select {
	case <-pending.done:
		return true
	default:
		return false
	}
}

func (hub *Hub) rejectPending(pending *pendingRegistration, err error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if current, exists := hub.pending[pending.key]; !exists || current != pending {
		return
	}
	pending.err = err
	delete(hub.pending, pending.key)
	close(pending.done)
}

func waitForRegistration(ctx context.Context, pending *pendingRegistration) (string, error) {
	select {
	case <-pending.done:
		if pending.err != nil {
			return "", pending.err
		}
		return pending.id, nil
	case <-ctx.Done():
		return "", &RegistrationError{Code: "registration_uncertain", Message: "registration is still pending; repeat the same request to recover its result"}
	}
}

func (hub *Hub) AcceptEvent(pluginName string, event domain.Event) bool {
	if err := domain.ValidateEvent(event); err != nil {
		hub.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", event.SubscriptionID, "reason", "invalid_context")
		return false
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed || !hub.available[pluginName] {
		hub.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", event.SubscriptionID, "reason", "plugin_unavailable")
		return false
	}
	subscription, exists := hub.subscriptions[event.SubscriptionID]
	if !exists || subscription.plugin != pluginName {
		hub.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", event.SubscriptionID, "reason", "subscription_not_owned")
		return false
	}
	select {
	case hub.slots <- struct{}{}:
	default:
		hub.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", event.SubscriptionID, "reason", "delivery_capacity_full")
		return false
	}
	deliveryID, err := domain.NewID()
	if err != nil {
		<-hub.slots
		hub.logger.Error("event_rejected", "plugin", pluginName, "subscription_id", event.SubscriptionID, "reason", "identifier_generation_failed")
		return false
	}
	delivery := domain.Delivery{
		ID:             deliveryID,
		Plugin:         pluginName,
		SubscriptionID: subscription.id,
		SessionID:      subscription.session,
		Context:        event.Context,
	}
	hub.queue <- deliveryJob{delivery: delivery}
	hub.logger.Info("event_admitted", "plugin", pluginName, "subscription_id", subscription.id, "delivery_id", deliveryID)
	return true
}

func (hub *Hub) runDeliveryWorker() {
	defer hub.worker.Done()
	for {
		select {
		case <-hub.ctx.Done():
			hub.discardQueued()
			return
		default:
		}
		select {
		case <-hub.ctx.Done():
			hub.discardQueued()
			return
		case job := <-hub.queue:
			if hub.ctx.Err() != nil {
				hub.discard(job, "daemon_shutdown")
				hub.discardQueued()
				return
			}
			hub.deliverOne(job)
		}
	}
}

func (hub *Hub) deliverOne(job deliveryJob) {
	value := job.delivery
	hub.logger.Info("delivery_attempt", "plugin", value.Plugin, "subscription_id", value.SubscriptionID, "delivery_id", value.ID)
	ctx, cancel := context.WithTimeout(hub.ctx, 30*time.Second)
	defer cancel()
	outcome, err := hub.delivery.Deliver(ctx, value)
	if err != nil && outcome == "" {
		outcome = domain.DeliveryUnknown
	}
	if hub.ctx.Err() != nil && outcome != domain.DeliveryAccepted {
		outcome = domain.DeliveryUnknown
	}
	if outcome == "" {
		outcome = domain.DeliveryUnknown
	}
	if err != nil {
		hub.logger.Error("delivery_result", "plugin", value.Plugin, "subscription_id", value.SubscriptionID, "delivery_id", value.ID, "outcome", outcome, "reason", boundedReason(err.Error()))
	} else {
		hub.logger.Info("delivery_result", "plugin", value.Plugin, "subscription_id", value.SubscriptionID, "delivery_id", value.ID, "outcome", outcome)
	}
	<-hub.slots
}

func (hub *Hub) discard(job deliveryJob, reason string) {
	value := job.delivery
	hub.logger.Warn("delivery_discarded", "plugin", value.Plugin, "subscription_id", value.SubscriptionID, "delivery_id", value.ID, "reason", reason)
	<-hub.slots
}

func (hub *Hub) discardQueued() {
	for {
		select {
		case job := <-hub.queue:
			hub.discard(job, "daemon_shutdown")
		default:
			return
		}
	}
}

func (hub *Hub) Close() {
	hub.mu.Lock()
	if !hub.closed {
		hub.closed = true
		for key, pending := range hub.pending {
			pending.err = &RegistrationError{Code: "daemon_unavailable", Message: "daemon is shutting down"}
			delete(hub.pending, key)
			close(pending.done)
		}
		hub.cancel()
	}
	hub.mu.Unlock()
	hub.worker.Wait()
}

func boundedReason(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

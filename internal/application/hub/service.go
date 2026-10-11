package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	MaxRegistrations = 1_024
	MaxDeliveries    = 128
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

type WatchRequest struct {
	RequestID      string
	SubscriptionID domain.SubscriptionID
	Arguments      *jsonvalue.Value
}

type PluginHandlers struct {
	Ready       func()
	Event       func(subscriptionID, context string)
	Unavailable func(error)
}

// PluginPort owns one configured child process and its protocol stream.
//
//go:generate go run go.uber.org/mock/mockgen -source=service.go -destination=service_mock_test.go -package=hub
type PluginPort interface {
	Start(context.Context, PluginHandlers) error
	Watch(context.Context, WatchRequest, func() error) error
	Stop(context.Context) error
}

type DeliveryResult struct {
	Outcome domain.DeliveryOutcome
	Detail  string
}

type DeliveryPort interface {
	Deliver(context.Context, domain.Event) DeliveryResult
}

type pluginState struct {
	port      PluginPort
	available bool
	reported  bool
}

type pendingRegistration struct {
	key       string
	id        domain.SubscriptionID
	plugin    string
	port      PluginPort
	done      chan struct{}
	completed bool
	activated bool
	waiters   int
	resultErr *Error
	resultID  domain.SubscriptionID
	watchArgs *jsonvalue.Value
	sessionID string
}

type queuedEvent struct {
	event domain.Event
}

type Hub struct {
	mu sync.Mutex

	plugins       map[string]*pluginState
	registrations map[domain.SubscriptionID]domain.Subscription
	activeByKey   map[string]domain.SubscriptionID
	pendingByKey  map[string]*pendingRegistration
	queue         chan queuedEvent
	pendingEvents int
	stopping      bool

	stopCh     chan struct{}
	workerDone chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	delivery   DeliveryPort
	logger     *slog.Logger
}

func New(delivery DeliveryPort, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	hub := &Hub{
		plugins:       make(map[string]*pluginState),
		registrations: make(map[domain.SubscriptionID]domain.Subscription),
		activeByKey:   make(map[string]domain.SubscriptionID),
		pendingByKey:  make(map[string]*pendingRegistration),
		queue:         make(chan queuedEvent, MaxDeliveries),
		stopCh:        make(chan struct{}),
		workerDone:    make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		delivery:      delivery,
		logger:        logger,
	}
	go hub.deliveryWorker()
	return hub
}

func (h *Hub) AddPlugin(name string, port PluginPort) error {
	if name == "" || port == nil {
		return errors.New("plugin name and process are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.plugins[name]; exists {
		return fmt.Errorf("duplicate plugin %q", name)
	}
	h.plugins[name] = &pluginState{port: port}
	return nil
}

func (h *Hub) StartPlugins(ctx context.Context) {
	type startup struct {
		name string
		port PluginPort
	}
	h.mu.Lock()
	plugins := make([]startup, 0, len(h.plugins))
	for name, state := range h.plugins {
		plugins = append(plugins, startup{name: name, port: state.port})
	}
	h.mu.Unlock()

	var wait sync.WaitGroup
	for _, item := range plugins {
		item := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			handlers := PluginHandlers{
				Ready: func() { h.pluginReady(item.name, item.port) },
				Event: func(subscriptionID, eventContext string) {
					h.acceptEvent(item.name, item.port, subscriptionID, eventContext)
				},
				Unavailable: func(err error) { h.pluginUnavailable(item.name, item.port, err) },
			}
			if err := item.port.Start(ctx, handlers); err != nil {
				h.pluginUnavailable(item.name, item.port, err)
			}
			h.mu.Lock()
			state := h.plugins[item.name]
			ready := state != nil && state.port == item.port && state.available
			h.mu.Unlock()
			if !ready {
				h.pluginUnavailable(item.name, item.port, errors.New("plugin did not become ready"))
			}
		}()
	}
	wait.Wait()
}

func (h *Hub) pluginReady(name string, port PluginPort) {
	h.mu.Lock()
	state := h.plugins[name]
	if state == nil || state.port != port || h.stopping {
		h.mu.Unlock()
		return
	}
	state.available = true
	state.reported = false
	h.mu.Unlock()
	h.logger.Info("plugin_ready", "plugin", name, "available", true)
}

func (h *Hub) pluginUnavailable(name string, port PluginPort, cause error) {
	h.mu.Lock()
	state := h.plugins[name]
	if state == nil || state.port != port || state.reported {
		h.mu.Unlock()
		return
	}
	wasAvailable := state.available
	state.available = false
	state.reported = true
	if wasAvailable {
		for id, registration := range h.registrations {
			if registration.Plugin != name {
				continue
			}
			delete(h.registrations, id)
			delete(h.activeByKey, registrationKey(registration.Plugin, registration.SessionID, registration.WatchArgs))
			h.logger.Warn("subscription_invalidated", "plugin", name, "subscription_id", id, "reason", safeError(cause))
		}
	}
	for key, pending := range h.pendingByKey {
		if pending.plugin == name && pending.port == port {
			h.completePendingLocked(key, pending, domain.SubscriptionID(""), &Error{Code: "plugin_unavailable", Message: "Plugin is unavailable."})
		}
	}
	h.mu.Unlock()
	h.logger.Error("plugin_unavailable", "plugin", name, "reason", safeError(cause))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = port.Stop(ctx)
	}()
}

func (h *Hub) Register(pluginName, sessionID string, watchArgs *jsonvalue.Value) (domain.SubscriptionID, *Error) {
	if pluginName == "" || watchArgs == nil || watchArgs.Kind() != jsonvalue.Object {
		return "", &Error{Code: "invalid_request", Message: "Registration request is invalid."}
	}
	if !domain.IsUUID(sessionID) {
		return "", &Error{Code: "missing_session", Message: "A valid session UUID is required."}
	}
	key := registrationKey(pluginName, sessionID, watchArgs)

	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		return "", &Error{Code: "plugin_unavailable", Message: "Daemon is shutting down."}
	}
	state := h.plugins[pluginName]
	if state == nil {
		h.mu.Unlock()
		return "", &Error{Code: "unknown_plugin", Message: "Plugin is not configured."}
	}
	if !state.available {
		h.mu.Unlock()
		return "", &Error{Code: "plugin_unavailable", Message: "Plugin is unavailable."}
	}
	if id, exists := h.activeByKey[key]; exists {
		h.mu.Unlock()
		return id, nil
	}
	if pending := h.pendingByKey[key]; pending != nil {
		pending.waiters++
		done := pending.done
		h.mu.Unlock()
		<-done
		return pending.resultID, pending.resultErr
	}
	pendingCount := 0
	for _, candidate := range h.pendingByKey {
		if !candidate.activated {
			pendingCount++
		}
	}
	if len(h.registrations)+pendingCount >= MaxRegistrations {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "Registration capacity is exhausted."}
	}
	id, err := newID()
	if err != nil {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "A subscription identifier could not be created."}
	}
	pending := &pendingRegistration{
		key:       key,
		id:        domain.SubscriptionID(id),
		plugin:    pluginName,
		port:      state.port,
		done:      make(chan struct{}),
		watchArgs: watchArgs,
		sessionID: sessionID,
	}
	h.pendingByKey[key] = pending
	port := state.port
	h.mu.Unlock()

	requestID, err := newID()
	if err == nil {
		err = port.Watch(h.ctx, WatchRequest{
			RequestID:      requestID,
			SubscriptionID: pending.id,
			Arguments:      watchArgs,
		}, func() error { return h.activate(pending) })
	}
	if err != nil {
		var watchErr *WatchError
		if errors.As(err, &watchErr) && watchErr.Unavailable {
			h.pluginUnavailable(pluginName, port, err)
		}
		h.mu.Lock()
		if !pending.completed {
			code := "plugin_unavailable"
			message := "Plugin is unavailable."
			if requestID == "" {
				code = "resource_exhausted"
				message = "A request identifier could not be created."
			}
			if errors.As(err, &watchErr) && watchErr.Code != "" {
				code = watchErr.Code
				message = watchErr.Message
			}
			h.completePendingLocked(key, pending, "", &Error{Code: code, Message: message})
		}
		h.mu.Unlock()
	} else {
		h.mu.Lock()
		if !pending.completed {
			if pending.activated {
				h.completePendingLocked(key, pending, pending.id, nil)
			} else {
				h.completePendingLocked(key, pending, "", &Error{Code: "plugin_unavailable", Message: "Plugin did not confirm the watch."})
			}
		}
		h.mu.Unlock()
	}

	<-pending.done
	return pending.resultID, pending.resultErr
}

func (h *Hub) activate(pending *pendingRegistration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping {
		return errors.New("daemon is shutting down")
	}
	state := h.plugins[pending.plugin]
	if state == nil || state.port != pending.port || !state.available {
		return errors.New("plugin is unavailable")
	}
	if pending.completed || h.pendingByKey[pending.key] != pending {
		return errors.New("registration is no longer pending")
	}
	registration := domain.Subscription{
		ID:        pending.id,
		Plugin:    pending.plugin,
		SessionID: pending.sessionID,
		WatchArgs: pending.watchArgs,
	}
	h.registrations[pending.id] = registration
	h.activeByKey[pending.key] = pending.id
	pending.activated = true
	h.logger.Info("subscription_active", "plugin", pending.plugin, "subscription_id", pending.id)
	return nil
}

func (h *Hub) completePendingLocked(key string, pending *pendingRegistration, id domain.SubscriptionID, resultErr *Error) {
	if pending.completed {
		return
	}
	pending.completed = true
	pending.resultID = id
	pending.resultErr = resultErr
	delete(h.pendingByKey, key)
	close(pending.done)
}

func (h *Hub) acceptEvent(pluginName string, port PluginPort, subscriptionID, eventContext string) {
	if subscriptionID == "" || eventContext == "" {
		h.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "invalid event")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.plugins[pluginName]
	if h.stopping || state == nil || state.port != port || !state.available {
		h.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "plugin unavailable")
		return
	}
	registration, ok := h.registrations[domain.SubscriptionID(subscriptionID)]
	if !ok || registration.Plugin != pluginName {
		h.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "inactive or foreign subscription")
		return
	}
	if h.pendingEvents >= MaxDeliveries {
		h.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery queue is full")
		return
	}
	deliveryID, err := newID()
	if err != nil {
		h.logger.Error("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery identifier unavailable")
		return
	}
	event := domain.Event{
		ID:             domain.DeliveryID(deliveryID),
		Plugin:         pluginName,
		SubscriptionID: domain.SubscriptionID(subscriptionID),
		SessionID:      registration.SessionID,
		Context:        eventContext,
	}
	h.pendingEvents++
	select {
	case h.queue <- queuedEvent{event: event}:
		h.logger.Info("event_admitted", "plugin", pluginName, "subscription_id", subscriptionID, "delivery_id", event.ID, "received_at", time.Now().UTC())
	default:
		h.pendingEvents--
		h.logger.Warn("event_rejected", "plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery queue is full")
	}
}

func (h *Hub) deliveryWorker() {
	defer close(h.workerDone)
	for {
		select {
		case <-h.stopCh:
			h.discardQueued()
			return
		case item := <-h.queue:
			h.mu.Lock()
			stopping := h.stopping
			h.mu.Unlock()
			if stopping {
				h.discardEvent(item.event, "shutdown")
				h.discardQueued()
				return
			}
			h.deliver(item.event)
		}
	}
}

func (h *Hub) deliver(event domain.Event) {
	started := time.Now().UTC()
	h.logger.Info("delivery_attempt", "plugin", event.Plugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "session_id", event.SessionID, "attempted_at", started)
	result := DeliveryResult{Outcome: domain.DeliveryUnknown, Detail: "delivery adapter unavailable"}
	if h.delivery != nil {
		result = h.delivery.Deliver(h.ctx, event)
	}
	if result.Outcome == "" {
		result.Outcome = domain.DeliveryUnknown
	}
	h.logger.Info("delivery_result", "plugin", event.Plugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "outcome", result.Outcome, "detail", bounded(result.Detail, 1024), "completed_at", time.Now().UTC())
	h.mu.Lock()
	h.pendingEvents--
	h.mu.Unlock()
}

func (h *Hub) discardEvent(event domain.Event, reason string) {
	h.logger.Warn("delivery_discarded", "plugin", event.Plugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "reason", reason)
	h.mu.Lock()
	h.pendingEvents--
	h.mu.Unlock()
}

func (h *Hub) discardQueued() {
	for {
		select {
		case item := <-h.queue:
			h.discardEvent(item.event, "shutdown")
		default:
			return
		}
	}
}

func (h *Hub) Shutdown() {
	h.mu.Lock()
	if !h.stopping {
		h.stopping = true
		close(h.stopCh)
		h.cancel()
		for key, pending := range h.pendingByKey {
			h.completePendingLocked(key, pending, "", &Error{Code: "plugin_unavailable", Message: "Daemon is shutting down."})
		}
	}
	h.mu.Unlock()
	<-h.workerDone
}

func (h *Hub) StopPlugins(ctx context.Context) {
	h.mu.Lock()
	plugins := make([]PluginPort, 0, len(h.plugins))
	for _, state := range h.plugins {
		plugins = append(plugins, state.port)
	}
	h.mu.Unlock()

	var wait sync.WaitGroup
	for _, port := range plugins {
		port := port
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := port.Stop(ctx); err != nil {
				h.logger.Warn("plugin_stop_error", "reason", safeError(err))
			}
		}()
	}
	wait.Wait()
}

func registrationKey(pluginName, sessionID string, args *jsonvalue.Value) string {
	canonical := args.Canonical()
	return pluginName + "\x00" + sessionID + "\x00" + string(canonical)
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded), nil
}

func safeError(err error) string {
	if err == nil {
		return "unspecified"
	}
	return bounded(err.Error(), 1024)
}

func bounded(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

// WatchError describes whether a plugin watch failure leaves its process
// usable. Only confirmed schema rejection and frame-size rejection preserve it.
type WatchError struct {
	Code        string
	Message     string
	Unavailable bool
	Cause       error
}

func (e *WatchError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "plugin watch failed"
}

func (e *WatchError) Unwrap() error { return e.Cause }

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
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	MaxRegistrations    = 1_024
	MaxDeliveries       = 128
	MaxContextBytes     = 8_192
	MaxPluginPayload    = 65_536
	permissionNotice    = "External data; no additional permissions are granted."
	pluginProtocolV2    = "2"
	defaultOperationMax = 1_024
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

type DestinationRequest struct {
	RequestID string
	Arguments *jsonvalue.Value
}

type PluginHandlers struct {
	Ready       func()
	Event       func(subscriptionID, context string)
	Unavailable func(error)
}

// PluginPort owns one configured go-plugin child and its role-specific stream.
type PluginPort interface {
	Start(context.Context, PluginHandlers) error
	ValidateDestination(context.Context, DestinationRequest) error
	Watch(context.Context, WatchRequest, func() error) error
	Deliver(context.Context, domain.Event) DeliveryResult
	Stop(context.Context) error
}

type DeliveryResult struct {
	Outcome domain.DeliveryOutcome
	Detail  string
}

type pluginState struct {
	role      domain.PluginRole
	port      PluginPort
	available bool
	reported  bool
}

type pendingRegistration struct {
	key             string
	id              domain.SubscriptionID
	inputName       string
	input           PluginPort
	outputName      string
	output          PluginPort
	watchArgs       *jsonvalue.Value
	destinationArgs *jsonvalue.Value
	done            chan struct{}
	completed       bool
	activated       bool
	watchIssued     bool
	watchFinished   bool
	resultErr       *Error
	resultID        domain.SubscriptionID
}

type queuedEvent struct {
	event      domain.Event
	outputPort PluginPort
}

// RegistrationStore commits an accepted route before the Hub reports success.
type RegistrationStore interface {
	Commit(domain.Subscription) error
}

type Hub struct {
	mu sync.Mutex

	plugins       map[string]*pluginState
	registrations map[domain.SubscriptionID]domain.Subscription
	retained      map[domain.SubscriptionID]struct{}
	activeByKey   map[string]domain.SubscriptionID
	pendingByKey  map[string]*pendingRegistration
	store         RegistrationStore
	restore       []domain.Subscription
	admissionOpen bool
	queue         chan queuedEvent
	pendingEvents int
	stopping      bool

	stopCh     chan struct{}
	workerDone chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	logger     *slog.Logger
}

func New(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	hub := &Hub{
		plugins:       make(map[string]*pluginState),
		registrations: make(map[domain.SubscriptionID]domain.Subscription),
		retained:      make(map[domain.SubscriptionID]struct{}),
		activeByKey:   make(map[string]domain.SubscriptionID),
		pendingByKey:  make(map[string]*pendingRegistration),
		queue:         make(chan queuedEvent, MaxDeliveries),
		stopCh:        make(chan struct{}),
		workerDone:    make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		logger:        logger,
		admissionOpen: true,
	}
	go hub.deliveryWorker()
	return hub
}

// NewWithRegistrationStore keeps registration and event admission closed until
// every saved route has been restored successfully.
func NewWithRegistrationStore(logger *slog.Logger, store RegistrationStore) *Hub {
	hub := New(logger)
	hub.store = store
	hub.admissionOpen = false
	return hub
}

// PrepareRestorations validates all saved routes before any configured plugin
// is launched and retains their IDs for capacity accounting.
func (h *Hub) PrepareRestorations(registrations []domain.Subscription) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store == nil {
		return errors.New("registration store is required for restoration")
	}
	if len(registrations) > MaxRegistrations {
		return fmt.Errorf("saved registrations exceed capacity %d", MaxRegistrations)
	}
	retained := make(map[domain.SubscriptionID]struct{}, len(registrations))
	keys := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if registration.ID == "" || registration.InputPlugin == "" || registration.OutputPlugin == "" || registration.WatchArgs == nil || registration.WatchArgs.Kind() != jsonvalue.Object || registration.DestinationArgs == nil || registration.DestinationArgs.Kind() != jsonvalue.Object {
			return fmt.Errorf("saved registration %q is invalid", registration.ID)
		}
		if _, exists := retained[registration.ID]; exists {
			return fmt.Errorf("saved registration ID %s is duplicated", registration.ID)
		}
		input := h.plugins[registration.InputPlugin]
		if input == nil || input.role != domain.InputRole {
			return fmt.Errorf("saved registration %s references an unavailable input plugin %q", registration.ID, registration.InputPlugin)
		}
		output := h.plugins[registration.OutputPlugin]
		if output == nil || output.role != domain.OutputRole {
			return fmt.Errorf("saved registration %s references an unavailable output plugin %q", registration.ID, registration.OutputPlugin)
		}
		if !registrationPayloadsFit(registration.WatchArgs, registration.DestinationArgs, string(registration.ID), "00000000-0000-4000-8000-000000000000", "00000000-0000-4000-8000-000000000000") {
			return fmt.Errorf("saved registration %s exceeds the plugin protocol payload limit", registration.ID)
		}
		key := registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs)
		if _, exists := keys[key]; exists {
			return fmt.Errorf("saved registration %s duplicates a route", registration.ID)
		}
		keys[key] = struct{}{}
		retained[registration.ID] = struct{}{}
	}
	h.retained = retained
	h.restore = append([]domain.Subscription(nil), registrations...)
	return nil
}

// Restore validates saved destinations and watches in order. Routes remain
// staged and events are discarded until every saved route has succeeded.
func (h *Hub) Restore(ctx context.Context) error {
	h.mu.Lock()
	registrations := append([]domain.Subscription(nil), h.restore...)
	if h.store == nil {
		h.mu.Unlock()
		return errors.New("registration store is required for restoration")
	}
	h.mu.Unlock()

	for _, registration := range registrations {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		if h.stopping {
			h.mu.Unlock()
			return errors.New("daemon stopped during registration restoration")
		}
		input := h.plugins[registration.InputPlugin]
		output := h.plugins[registration.OutputPlugin]
		if input == nil || input.role != domain.InputRole || !input.available || output == nil || output.role != domain.OutputRole || !output.available {
			h.mu.Unlock()
			return fmt.Errorf("restore registration %s: a referenced plugin is unavailable", registration.ID)
		}
		inputPort, outputPort := input.port, output.port
		h.mu.Unlock()

		destinationRequestID, err := newID()
		if err != nil {
			return fmt.Errorf("restore registration %s: could not create destination request ID", registration.ID)
		}
		watchRequestID, err := newID()
		if err != nil {
			return fmt.Errorf("restore registration %s: could not create watch request ID", registration.ID)
		}
		if err := outputPort.ValidateDestination(ctx, DestinationRequest{RequestID: destinationRequestID, Arguments: registration.DestinationArgs}); err != nil {
			var destinationErr *DestinationError
			if errors.As(err, &destinationErr) && destinationErr.Unavailable {
				h.pluginUnavailable(registration.OutputPlugin, outputPort, err)
			}
			return fmt.Errorf("restore registration %s: destination validation failed: %s", registration.ID, safeError(err))
		}
		if err := inputPort.Watch(ctx, WatchRequest{RequestID: watchRequestID, SubscriptionID: registration.ID, Arguments: registration.WatchArgs}, func() error { return nil }); err != nil {
			var watchErr *WatchError
			if errors.As(err, &watchErr) && watchErr.Unavailable {
				h.pluginUnavailable(registration.InputPlugin, inputPort, err)
			}
			return fmt.Errorf("restore registration %s: watch validation failed: %s", registration.ID, safeError(err))
		}
		h.mu.Lock()
		available := !h.stopping && h.plugins[registration.InputPlugin] != nil && h.plugins[registration.InputPlugin].port == inputPort && h.plugins[registration.InputPlugin].available && h.plugins[registration.OutputPlugin] != nil && h.plugins[registration.OutputPlugin].port == outputPort && h.plugins[registration.OutputPlugin].available
		h.mu.Unlock()
		if !available {
			return fmt.Errorf("restore registration %s: a referenced plugin became unavailable", registration.ID)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.stopping {
		return errors.New("daemon stopped during registration restoration")
	}
	for _, registration := range registrations {
		input, output := h.plugins[registration.InputPlugin], h.plugins[registration.OutputPlugin]
		if input == nil || !input.available || output == nil || !output.available {
			return fmt.Errorf("restore registration %s: a referenced plugin became unavailable", registration.ID)
		}
	}
	for _, registration := range registrations {
		registration.Active = true
		key := registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs)
		h.registrations[registration.ID] = registration
		h.activeByKey[key] = registration.ID
	}
	h.admissionOpen = true
	return nil
}

func (h *Hub) AddPlugin(name string, role domain.PluginRole, port PluginPort) error {
	if name == "" || port == nil || (role != domain.InputRole && role != domain.OutputRole) {
		return errors.New("plugin name, role, and process are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.plugins[name]; exists {
		return fmt.Errorf("duplicate plugin %q", name)
	}
	h.plugins[name] = &pluginState{role: role, port: port}
	return nil
}

func (h *Hub) StartPlugins(ctx context.Context) {
	type startup struct {
		name string
		role domain.PluginRole
		port PluginPort
	}
	h.mu.Lock()
	plugins := make([]startup, 0, len(h.plugins))
	for name, state := range h.plugins {
		plugins = append(plugins, startup{name: name, role: state.role, port: state.port})
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
	role := state.role
	h.mu.Unlock()
	h.logger.Info("plugin_ready", "plugin", name, "role", role, "available", true)
}

func (h *Hub) pluginUnavailable(name string, port PluginPort, cause error) {
	h.mu.Lock()
	state := h.plugins[name]
	if state == nil || state.port != port || state.reported {
		h.mu.Unlock()
		return
	}
	role := state.role
	wasAvailable := state.available
	state.available = false
	state.reported = true
	if wasAvailable {
		for id, registration := range h.registrations {
			if registration.InputPlugin != name && registration.OutputPlugin != name {
				continue
			}
			if registration.Active {
				registration.Active = false
				h.registrations[id] = registration
				delete(h.activeByKey, registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs))
			}
			h.logger.Warn("subscription_invalidated", "plugin", name, "role", role, "subscription_id", id, "reason", safeError(cause))
		}
	}
	for key, pending := range h.pendingByKey {
		if (pending.inputName != name || pending.input != port) && (pending.outputName != name || pending.output != port) {
			continue
		}
		if !pending.completed {
			pending.resultErr = &Error{Code: "plugin_unavailable", Message: "A configured plugin is unavailable."}
			pending.resultID = ""
			pending.completed = true
			close(pending.done)
		}
		if !pending.watchIssued || role == domain.InputRole {
			delete(h.pendingByKey, key)
		}
	}
	h.mu.Unlock()
	h.logger.Error("plugin_unavailable", "plugin", name, "role", role, "reason", safeError(cause))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = port.Stop(ctx)
		if role == domain.InputRole {
			h.forgetInputSubscriptions(name)
		}
	}()
}

func (h *Hub) Register(inputName string, watchArgs *jsonvalue.Value, outputName string, destinationArgs *jsonvalue.Value) (domain.SubscriptionID, *Error) {
	if inputName == "" || outputName == "" || watchArgs == nil || watchArgs.Kind() != jsonvalue.Object || destinationArgs == nil || destinationArgs.Kind() != jsonvalue.Object {
		return "", &Error{Code: "invalid_request", Message: "Registration request is invalid."}
	}
	key := registrationKey(inputName, watchArgs, outputName, destinationArgs)

	h.mu.Lock()
	if h.stopping || !h.admissionOpen {
		h.mu.Unlock()
		return "", &Error{Code: "plugin_unavailable", Message: "Daemon is not accepting registrations."}
	}
	inputState := h.plugins[inputName]
	if inputState == nil {
		h.mu.Unlock()
		return "", &Error{Code: "unknown_plugin", Message: "Input plugin is not configured."}
	}
	if inputState.role != domain.InputRole {
		h.mu.Unlock()
		return "", &Error{Code: "wrong_role", Message: "Selected input plugin is configured for the output role."}
	}
	outputState := h.plugins[outputName]
	if outputState == nil {
		h.mu.Unlock()
		return "", &Error{Code: "unknown_plugin", Message: "Output plugin is not configured."}
	}
	if outputState.role != domain.OutputRole {
		h.mu.Unlock()
		return "", &Error{Code: "wrong_role", Message: "Selected output plugin is configured for the input role."}
	}
	if !inputState.available || !outputState.available {
		h.mu.Unlock()
		return "", &Error{Code: "plugin_unavailable", Message: "A configured plugin is unavailable."}
	}
	if id, exists := h.activeByKey[key]; exists {
		h.mu.Unlock()
		return id, nil
	}
	if pending := h.pendingByKey[key]; pending != nil {
		done := pending.done
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		id, resultErr := pending.resultID, pending.resultErr
		h.mu.Unlock()
		return id, resultErr
	}
	if h.registrationCountLocked() >= MaxRegistrations {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "Registration capacity is exhausted."}
	}
	id, err := h.newUniqueSubscriptionIDLocked()
	if err != nil {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "A subscription identifier could not be created."}
	}
	destinationRequestID, err := newID()
	if err != nil {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "A registration request identifier could not be created."}
	}
	watchRequestID, err := newID()
	if err != nil {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "A registration request identifier could not be created."}
	}
	if !registrationPayloadsFit(watchArgs, destinationArgs, id, destinationRequestID, watchRequestID) {
		h.mu.Unlock()
		return "", &Error{Code: "resource_exhausted", Message: "Registration arguments exceed the plugin protocol payload limit."}
	}
	pending := &pendingRegistration{
		key:             key,
		id:              domain.SubscriptionID(id),
		inputName:       inputName,
		input:           inputState.port,
		outputName:      outputName,
		output:          outputState.port,
		watchArgs:       watchArgs,
		destinationArgs: destinationArgs,
		done:            make(chan struct{}),
	}
	h.pendingByKey[key] = pending
	h.mu.Unlock()

	err = pending.output.ValidateDestination(h.ctx, DestinationRequest{RequestID: destinationRequestID, Arguments: destinationArgs})
	if err != nil {
		var destinationErr *DestinationError
		if errors.As(err, &destinationErr) && destinationErr.Unavailable {
			h.pluginUnavailable(outputName, pending.output, err)
		}
		h.finishRegistrationFailure(pending, operationFailure(err, "destination_rejected", "Output plugin rejected the destination."))
		return h.pendingResult(pending)
	}

	h.mu.Lock()
	if pending.completed {
		h.mu.Unlock()
		return h.pendingResult(pending)
	}
	inputState, outputState = h.plugins[inputName], h.plugins[outputName]
	if h.stopping || inputState == nil || outputState == nil || !inputState.available || !outputState.available || inputState.port != pending.input || outputState.port != pending.output {
		h.finishPendingLocked(pending, &Error{Code: "plugin_unavailable", Message: "A configured plugin became unavailable."}, false)
		h.mu.Unlock()
		return h.pendingResult(pending)
	}
	pending.watchIssued = true
	h.mu.Unlock()

	err = pending.input.Watch(h.ctx, WatchRequest{
		RequestID:      watchRequestID,
		SubscriptionID: pending.id,
		Arguments:      watchArgs,
	}, func() error { return h.activate(pending) })
	h.mu.Lock()
	pending.watchFinished = true
	if err != nil {
		var watchErr *WatchError
		if errors.As(err, &watchErr) && watchErr.Unavailable {
			h.mu.Unlock()
			h.pluginUnavailable(inputName, pending.input, err)
			h.mu.Lock()
		}
		if !pending.completed {
			h.finishPendingLocked(pending, operationFailure(err, "watch_rejected", "Input plugin rejected the watch."), false)
		} else {
			delete(h.pendingByKey, pending.key)
		}
	} else if !pending.completed {
		if pending.activated {
			h.finishPendingLocked(pending, nil, true)
		} else {
			h.finishPendingLocked(pending, &Error{Code: "plugin_unavailable", Message: "Input plugin did not confirm the watch."}, false)
		}
	}
	h.mu.Unlock()
	return h.pendingResult(pending)
}

func (h *Hub) activate(pending *pendingRegistration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pending.completed && !pending.watchIssued {
		return errors.New("registration is no longer pending")
	}
	inputState := h.plugins[pending.inputName]
	outputState := h.plugins[pending.outputName]
	if h.stopping || inputState == nil || inputState.port != pending.input || !inputState.available {
		return errors.New("input plugin is unavailable")
	}
	registration := domain.Subscription{
		ID:              pending.id,
		InputPlugin:     pending.inputName,
		OutputPlugin:    pending.outputName,
		WatchArgs:       pending.watchArgs,
		DestinationArgs: pending.destinationArgs,
		Active:          false,
	}
	if outputState != nil && outputState.port == pending.output && outputState.available && !h.stopping {
		if h.store != nil {
			if err := h.store.Commit(registration); err != nil {
				message := "Registration could not be saved: " + bounded(safeError(err), defaultOperationMax-34)
				h.registrations[pending.id] = registration
				h.finishPendingLocked(pending, &Error{Code: "storage_error", Message: message}, true)
				h.logger.Error("registration_storage_error", "subscription_id", pending.id, "reason", safeError(err))
				return nil
			}
			h.retained[pending.id] = struct{}{}
		}
		registration.Active = true
		h.activeByKey[pending.key] = pending.id
		pending.activated = true
		h.registrations[pending.id] = registration
		h.finishPendingLocked(pending, nil, false)
		h.logger.Info("subscription_active", "input_plugin", pending.inputName, "output_plugin", pending.outputName, "subscription_id", pending.id)
		return nil
	}
	// The input accepted a watch after its destination became unavailable. Keep
	// the inert route until input shutdown because the protocol has no cancel RPC.
	h.registrations[pending.id] = registration
	if !pending.completed {
		h.finishPendingLocked(pending, &Error{Code: "plugin_unavailable", Message: "Output plugin became unavailable before the watch was active."}, true)
	} else {
		delete(h.pendingByKey, pending.key)
	}
	h.logger.Warn("subscription_inactive", "input_plugin", pending.inputName, "output_plugin", pending.outputName, "subscription_id", pending.id, "reason", "output plugin unavailable")
	return nil
}

func (h *Hub) finishRegistrationFailure(pending *pendingRegistration, resultErr *Error) {
	h.mu.Lock()
	h.finishPendingLocked(pending, resultErr, false)
	h.mu.Unlock()
}

func (h *Hub) finishPendingLocked(pending *pendingRegistration, resultErr *Error, keepReservation bool) {
	if !pending.completed {
		pending.completed = true
		pending.resultErr = resultErr
		if resultErr == nil {
			pending.resultID = pending.id
		}
		close(pending.done)
	}
	if !keepReservation {
		delete(h.pendingByKey, pending.key)
	}
}

func (h *Hub) pendingResult(pending *pendingRegistration) (domain.SubscriptionID, *Error) {
	<-pending.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return pending.resultID, pending.resultErr
}

func (h *Hub) acceptEvent(pluginName string, port PluginPort, subscriptionID, eventContext string) {
	if subscriptionID == "" || eventContext == "" || !utf8.ValidString(eventContext) || len([]byte(eventContext)) > MaxContextBytes {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "invalid event")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.admissionOpen {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "daemon admission is closed")
		return
	}
	state := h.plugins[pluginName]
	if h.stopping || state == nil || state.role != domain.InputRole || state.port != port || !state.available {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "input plugin unavailable")
		return
	}
	registration, ok := h.registrations[domain.SubscriptionID(subscriptionID)]
	if !ok || !registration.Active || registration.InputPlugin != pluginName {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "inactive or foreign subscription")
		return
	}
	outputState := h.plugins[registration.OutputPlugin]
	if outputState == nil || outputState.role != domain.OutputRole || !outputState.available {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "output_plugin", registration.OutputPlugin, "subscription_id", subscriptionID, "reason", "output plugin unavailable")
		return
	}
	if h.pendingEvents >= MaxDeliveries {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery queue is full")
		return
	}
	deliveryID, err := newID()
	if err != nil {
		h.logger.Error("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery identifier unavailable")
		return
	}
	event := domain.Event{
		ID:              domain.DeliveryID(deliveryID),
		InputPlugin:     pluginName,
		OutputPlugin:    registration.OutputPlugin,
		SubscriptionID:  domain.SubscriptionID(subscriptionID),
		DestinationArgs: registration.DestinationArgs,
		Context:         eventContext,
	}
	if !deliveryFits(event) {
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "output_plugin", registration.OutputPlugin, "subscription_id", subscriptionID, "reason", "delivery payload exceeds protocol limit")
		return
	}
	h.pendingEvents++
	receivedAt := time.Now().UTC()
	select {
	case h.queue <- queuedEvent{event: event, outputPort: outputState.port}:
		h.logger.Info("event_admitted", "input_plugin", pluginName, "output_plugin", registration.OutputPlugin, "subscription_id", subscriptionID, "delivery_id", event.ID, "received_at", receivedAt)
	default:
		h.pendingEvents--
		h.logger.Warn("event_rejected", "input_plugin", pluginName, "subscription_id", subscriptionID, "reason", "delivery queue is full")
	}
}

func (h *Hub) registrationCountLocked() int {
	ids := make(map[domain.SubscriptionID]struct{}, len(h.retained)+len(h.registrations)+len(h.pendingByKey))
	for id := range h.retained {
		ids[id] = struct{}{}
	}
	for id := range h.registrations {
		ids[id] = struct{}{}
	}
	for _, pending := range h.pendingByKey {
		ids[pending.id] = struct{}{}
	}
	return len(ids)
}

func (h *Hub) newUniqueSubscriptionIDLocked() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := newID()
		if err != nil {
			return "", err
		}
		candidate := domain.SubscriptionID(id)
		if _, exists := h.retained[candidate]; exists {
			continue
		}
		if _, exists := h.registrations[candidate]; exists {
			continue
		}
		used := false
		for _, pending := range h.pendingByKey {
			if pending.id == candidate {
				used = true
				break
			}
		}
		if !used {
			return id, nil
		}
	}
	return "", errors.New("unique subscription ID unavailable")
}

func deliveryFits(event domain.Event) bool {
	value := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":           jsonvalue.NewNumber(pluginProtocolV2),
		"type":              jsonvalue.NewString("deliver"),
		"request_id":        jsonvalue.NewString("00000000-0000-4000-8000-000000000000"),
		"delivery_id":       jsonvalue.NewString(string(event.ID)),
		"input_plugin":      jsonvalue.NewString(event.InputPlugin),
		"subscription_id":   jsonvalue.NewString(string(event.SubscriptionID)),
		"destination_args":  event.DestinationArgs,
		"context":           jsonvalue.NewString(event.Context),
		"external_data":     jsonvalue.NewBoolean(true),
		"permission_notice": jsonvalue.NewString(permissionNotice),
	})
	return len(value.Marshal()) <= MaxPluginPayload
}

func registrationPayloadsFit(watchArgs, destinationArgs *jsonvalue.Value, subscriptionID, destinationRequestID, watchRequestID string) bool {
	destination := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":          jsonvalue.NewNumber(pluginProtocolV2),
		"type":             jsonvalue.NewString("validate_destination"),
		"request_id":       jsonvalue.NewString(destinationRequestID),
		"destination_args": destinationArgs,
	})
	watch := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":         jsonvalue.NewNumber(pluginProtocolV2),
		"type":            jsonvalue.NewString("watch"),
		"request_id":      jsonvalue.NewString(watchRequestID),
		"subscription_id": jsonvalue.NewString(subscriptionID),
		"watch_args":      watchArgs,
	})
	return len(destination.Marshal()) <= MaxPluginPayload && len(watch.Marshal()) <= MaxPluginPayload
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
			h.deliver(item)
		}
	}
}

func (h *Hub) deliver(item queuedEvent) {
	event := item.event
	started := time.Now().UTC()
	h.mu.Lock()
	state := h.plugins[event.OutputPlugin]
	available := !h.stopping && state != nil && state.role == domain.OutputRole && state.available && state.port == item.outputPort
	h.mu.Unlock()
	h.logger.Info("delivery_attempt", "input_plugin", event.InputPlugin, "output_plugin", event.OutputPlugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "attempted_at", started)
	result := DeliveryResult{Outcome: domain.DeliveryFailed, Detail: "output plugin unavailable before delivery"}
	if available {
		result = item.outputPort.Deliver(h.ctx, event)
	}
	if result.Outcome != domain.DeliveryAccepted && result.Outcome != domain.DeliveryFailed && result.Outcome != domain.DeliveryUnknown {
		result.Outcome = domain.DeliveryUnknown
	}
	h.logger.Info("delivery_result", "input_plugin", event.InputPlugin, "output_plugin", event.OutputPlugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "outcome", result.Outcome, "detail", bounded(result.Detail, defaultOperationMax), "completed_at", time.Now().UTC())
	h.mu.Lock()
	h.pendingEvents--
	h.mu.Unlock()
}

func (h *Hub) discardEvent(event domain.Event, reason string) {
	h.logger.Warn("delivery_discarded", "input_plugin", event.InputPlugin, "output_plugin", event.OutputPlugin, "subscription_id", event.SubscriptionID, "delivery_id", event.ID, "reason", reason)
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
		for _, pending := range h.pendingByKey {
			if !pending.completed {
				h.finishPendingLocked(pending, &Error{Code: "plugin_unavailable", Message: "Daemon is shutting down."}, false)
			}
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

func (h *Hub) forgetInputSubscriptions(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, registration := range h.registrations {
		if registration.InputPlugin != name {
			continue
		}
		delete(h.activeByKey, registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs))
		delete(h.registrations, id)
	}
	for key, pending := range h.pendingByKey {
		if pending.inputName == name {
			h.finishPendingLocked(pending, &Error{Code: "plugin_unavailable", Message: "Input plugin is unavailable."}, false)
			delete(h.pendingByKey, key)
		}
	}
}

func registrationKey(inputName string, watchArgs *jsonvalue.Value, outputName string, destinationArgs *jsonvalue.Value) string {
	value := jsonvalue.NewArray(
		jsonvalue.NewString(inputName), watchArgs,
		jsonvalue.NewString(outputName), destinationArgs,
	)
	return string(value.Canonical())
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
	return bounded(err.Error(), defaultOperationMax)
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

func operationFailure(err error, fallbackCode, fallbackMessage string) *Error {
	var destinationErr *DestinationError
	if errors.As(err, &destinationErr) && destinationErr.Code != "" {
		return &Error{Code: destinationErr.Code, Message: destinationErr.Message}
	}
	var watchErr *WatchError
	if errors.As(err, &watchErr) && watchErr.Code != "" {
		return &Error{Code: watchErr.Code, Message: watchErr.Message}
	}
	if err != nil {
		return &Error{Code: fallbackCode, Message: fallbackMessage}
	}
	return nil
}

type DestinationError struct {
	Code        string
	Message     string
	Unavailable bool
	Cause       error
}

func (e *DestinationError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "destination validation failed"
}

func (e *DestinationError) Unwrap() error { return e.Cause }

// WatchError identifies expected plugin rejection separately from process loss.
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

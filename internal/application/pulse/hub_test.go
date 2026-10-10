package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

const testSessionID = "01a11c69-4d5f-7bf1-9506-c872a3543cf4"

type fakePlugin struct {
	available atomic.Bool
	watch     func(context.Context, domain.WatchRequest, func() bool) WatchResult
	stopped   atomic.Int32
}

func newFakePlugin(watch func(context.Context, domain.WatchRequest, func() bool) WatchResult) *fakePlugin {
	plugin := &fakePlugin{watch: watch}
	plugin.available.Store(true)
	return plugin
}

func (plugin *fakePlugin) Available() bool { return plugin.available.Load() }

func (plugin *fakePlugin) Watch(ctx context.Context, request domain.WatchRequest, activate func() bool) WatchResult {
	return plugin.watch(ctx, request, activate)
}

func (plugin *fakePlugin) Stop(string) {
	plugin.stopped.Add(1)
	plugin.available.Store(false)
}

type deliveryCall struct {
	value domain.Delivery
	ctx   context.Context
}

type fakeDelivery struct {
	calls chan deliveryCall
	fn    func(context.Context, domain.Delivery) (domain.DeliveryOutcome, error)
}

func newFakeDelivery() *fakeDelivery {
	return &fakeDelivery{calls: make(chan deliveryCall, domain.MaxDeliveryInFlight+1)}
}

func (delivery *fakeDelivery) Deliver(ctx context.Context, value domain.Delivery) (domain.DeliveryOutcome, error) {
	if delivery.fn != nil {
		return delivery.fn(ctx, value)
	}
	delivery.calls <- deliveryCall{value: value, ctx: ctx}
	return domain.DeliveryAccepted, nil
}

func newTestHub(t *testing.T, names []string, delivery DeliveryPort) *Hub {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(names, delivery, logger)
	t.Cleanup(hub.Close)
	return hub
}

func TestRegisterDeduplicatesEquivalentArguments(t *testing.T) {
	var calls atomic.Int32
	plugin := newFakePlugin(func(_ context.Context, _ domain.WatchRequest, activate func() bool) WatchResult {
		calls.Add(1)
		if !activate() {
			return WatchResult{Uncertain: true, Err: errors.New("activation failed")}
		}
		return WatchResult{}
	})
	delivery := newFakeDelivery()
	hub := newTestHub(t, []string{"manual"}, delivery)
	if err := hub.SetPlugin("manual", plugin); err != nil {
		t.Fatal(err)
	}
	first, err := hub.Register(context.Background(), "manual", testSessionID, json.RawMessage(`{"b":1.0,"a":[true,false]}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.Register(context.Background(), "manual", testSessionID, json.RawMessage(`{"a":[true,false],"b":1e0}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("duplicate registrations returned %q and %q", first, second)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("plugin Watch calls = %d, want 1", got)
	}
}

func TestConcurrentEquivalentRegistrationsSharePendingWatch(t *testing.T) {
	started := make(chan struct{})
	complete := make(chan struct{})
	var calls atomic.Int32
	plugin := newFakePlugin(func(ctx context.Context, _ domain.WatchRequest, activate func() bool) WatchResult {
		calls.Add(1)
		close(started)
		select {
		case <-complete:
		case <-ctx.Done():
			return WatchResult{Uncertain: true, Err: ctx.Err()}
		}
		if !activate() {
			return WatchResult{Uncertain: true, Err: errors.New("activation failed")}
		}
		return WatchResult{}
	})
	hub := newTestHub(t, []string{"manual"}, newFakeDelivery())
	if err := hub.SetPlugin("manual", plugin); err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	register := func(args string) {
		defer wait.Done()
		id, err := hub.Register(context.Background(), "manual", testSessionID, json.RawMessage(args))
		results <- id
		errorsFound <- err
	}
	wait.Add(1)
	go register(`{"x":1}`)
	<-started
	wait.Add(1)
	go register(`{"x":1.0}`)
	close(complete)
	wait.Wait()
	close(results)
	close(errorsFound)
	var identifiers []string
	for id := range results {
		identifiers = append(identifiers, id)
	}
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	if len(identifiers) != 2 || identifiers[0] == "" || identifiers[0] != identifiers[1] {
		t.Fatalf("registration IDs = %v, want one shared ID", identifiers)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("plugin Watch calls = %d, want 1", got)
	}
}

func TestRegisterReportsUnknownUnavailableAndRejectedPlugin(t *testing.T) {
	delivery := newFakeDelivery()
	plugin := newFakePlugin(func(context.Context, domain.WatchRequest, func() bool) WatchResult {
		return WatchResult{Err: &RegistrationError{Code: "watch_rejected", Message: "trigger path is already used"}}
	})
	hub := newTestHub(t, []string{"manual", "offline"}, delivery)
	if err := hub.SetPlugin("manual", plugin); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Register(context.Background(), "unknown", testSessionID, json.RawMessage(`{}`)); registrationCode(err) != "unknown_plugin" {
		t.Fatalf("unknown plugin error = %v", err)
	}
	if _, err := hub.Register(context.Background(), "offline", testSessionID, json.RawMessage(`{}`)); registrationCode(err) != "plugin_unavailable" {
		t.Fatalf("unavailable plugin error = %v", err)
	}
	if _, err := hub.Register(context.Background(), "manual", testSessionID, json.RawMessage(`{}`)); registrationCode(err) != "watch_rejected" {
		t.Fatalf("rejected watch error = %v", err)
	}
}

func TestPluginExitInvalidatesSubscriptionsAndEventsKeepFixedOwner(t *testing.T) {
	delivery := newFakeDelivery()
	pluginA := newFakePlugin(func(_ context.Context, _ domain.WatchRequest, activate func() bool) WatchResult {
		activate()
		return WatchResult{}
	})
	pluginB := newFakePlugin(func(_ context.Context, _ domain.WatchRequest, activate func() bool) WatchResult {
		activate()
		return WatchResult{}
	})
	hub := newTestHub(t, []string{"alpha", "beta"}, delivery)
	if err := hub.SetPlugin("alpha", pluginA); err != nil {
		t.Fatal(err)
	}
	if err := hub.SetPlugin("beta", pluginB); err != nil {
		t.Fatal(err)
	}
	alphaID, err := hub.Register(context.Background(), "alpha", testSessionID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if hub.AcceptEvent("beta", domain.Event{SubscriptionID: alphaID, Context: "cross-plugin"}) {
		t.Fatal("cross-plugin event was admitted")
	}
	if !hub.AcceptEvent("alpha", domain.Event{SubscriptionID: alphaID, Context: "review complete"}) {
		t.Fatal("valid event was rejected")
	}
	select {
	case call := <-delivery.calls:
		if call.value.SessionID != testSessionID || call.value.Plugin != "alpha" || call.value.Context != "review complete" {
			t.Fatalf("delivery used an unexpected target or payload: %+v", call.value)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
	}
	hub.MarkUnavailable("alpha", "test exit")
	if hub.AcceptEvent("alpha", domain.Event{SubscriptionID: alphaID, Context: "late"}) {
		t.Fatal("event from an unavailable plugin was admitted")
	}
	if _, err := hub.Register(context.Background(), "alpha", testSessionID, json.RawMessage(`{}`)); registrationCode(err) != "plugin_unavailable" {
		t.Fatalf("registration after plugin exit error = %v", err)
	}
}

func TestSessionIdentityMustBeAUUID(t *testing.T) {
	hub := newTestHub(t, []string{"manual"}, newFakeDelivery())
	if _, err := hub.Register(context.Background(), "manual", "", json.RawMessage(`{}`)); registrationCode(err) != "missing_identity" {
		t.Fatalf("missing session identity error = %v", err)
	}
}

func registrationCode(err error) string {
	var registrationError *RegistrationError
	if errors.As(err, &registrationError) {
		return registrationError.Code
	}
	return ""
}

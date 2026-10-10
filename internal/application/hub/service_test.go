package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	testSessionID       = "11111111-1111-4111-8111-111111111111"
	secondTestSessionID = "22222222-2222-4222-8222-222222222222"
)

func TestRegisterCoalescesConcurrentEquivalentRequestsAndAdmitsImmediateEvent(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{watchStarted: make(chan struct{}), releaseWatch: make(chan struct{}), emitOnAccept: true}
	delivery := &fakeDelivery{results: make(chan domain.Event, 2)}
	service := newTestHub(t, plugin, delivery)

	firstArgs, err := jsonvalue.Parse([]byte(`{"nested":{"n":1.00},"items":["a","b"]}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	secondArgs, err := jsonvalue.Parse([]byte(`{"items":["a","b"],"nested":{"n":1}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	resultIDs := make(chan domain.SubscriptionID, 2)
	resultErrors := make(chan *Error, 2)
	go func() {
		id, registerErr := service.Register("manual", testSessionID, firstArgs)
		resultIDs <- id
		resultErrors <- registerErr
	}()
	select {
	case <-plugin.watchStarted:
	case <-time.After(time.Second):
		t.Fatal("first registration did not start a watch")
	}
	go func() {
		id, registerErr := service.Register("manual", testSessionID, secondArgs)
		resultIDs <- id
		resultErrors <- registerErr
	}()
	waiterDeadline := time.NewTimer(time.Second)
	defer waiterDeadline.Stop()
	for {
		service.mu.Lock()
		pending := service.pendingByKey[registrationKey("manual", testSessionID, firstArgs)]
		waiters := 0
		if pending != nil {
			waiters = pending.waiters
		}
		service.mu.Unlock()
		if waiters != 0 {
			break
		}
		select {
		case <-waiterDeadline.C:
			t.Fatal("equivalent registration did not join the pending operation")
		default:
			runtime.Gosched()
		}
	}
	plugin.mu.Lock()
	callsBeforeRelease := plugin.watchCalls
	plugin.mu.Unlock()
	close(plugin.releaseWatch)

	firstID := <-resultIDs
	secondID := <-resultIDs
	firstErr := <-resultErrors
	secondErr := <-resultErrors
	if firstErr != nil || secondErr != nil {
		t.Fatalf("registration errors = (%v, %v)", firstErr, secondErr)
	}
	if firstID == "" || firstID != secondID {
		t.Fatalf("registration IDs = (%q, %q)", firstID, secondID)
	}
	if callsBeforeRelease != 1 {
		t.Fatalf("plugin watch calls before release = %d, want 1", callsBeforeRelease)
	}
	select {
	case event := <-delivery.results:
		if event.SubscriptionID != firstID || event.SessionID != testSessionID || event.Context != "immediate" {
			t.Fatalf("delivered event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event emitted immediately after acceptance was not delivered")
	}
}

func TestRejectedWatchLeavesPluginAvailable(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{rejectWatch: true}
	service := newTestHub(t, plugin, &fakeDelivery{results: make(chan domain.Event, 1)})
	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, registerErr := service.Register("manual", testSessionID, args)
		if registerErr == nil || registerErr.Code != "watch_rejected" {
			t.Fatalf("registration error = %v, want watch_rejected", registerErr)
		}
	}
	plugin.mu.Lock()
	calls := plugin.watchCalls
	plugin.mu.Unlock()
	if calls != 2 {
		t.Fatalf("plugin watch calls = %d, want 2", calls)
	}
}

func TestRegistrationCapacityRejectsAdditionalWatch(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	service := newTestHub(t, plugin, &fakeDelivery{results: make(chan domain.Event, 1)})
	args, err := jsonvalue.Parse([]byte(`{"watch":true}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxRegistrations; index++ {
		sessionID := fmt.Sprintf("%08x-1111-4111-8111-%012x", index, index)
		if _, registerErr := service.Register("manual", sessionID, args); registerErr != nil {
			t.Fatalf("registration %d failed before capacity: %v", index+1, registerErr)
		}
	}
	if _, registerErr := service.Register("manual", "ffffffff-1111-4111-8111-ffffffffffff", args); registerErr == nil || registerErr.Code != "resource_exhausted" {
		t.Fatalf("registration beyond capacity error = %v, want resource_exhausted", registerErr)
	}
	plugin.mu.Lock()
	watchCalls := plugin.watchCalls
	plugin.mu.Unlock()
	if watchCalls != MaxRegistrations {
		t.Fatalf("watch calls = %d, want %d", watchCalls, MaxRegistrations)
	}
}

func TestActiveEquivalentRegistrationReusesWatchAndSubscription(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 2)}
	service := newTestHub(t, plugin, delivery)
	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}

	firstID, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	secondID, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	plugin.mu.Lock()
	watchCalls := plugin.watchCalls
	plugin.mu.Unlock()
	if firstID == "" || firstID != secondID || watchCalls != 1 {
		t.Fatalf("registrations = (%q, %q), watch calls = %d; want same ID and one watch", firstID, secondID, watchCalls)
	}
	plugin.mu.Lock()
	handlers := plugin.handlers
	plugin.mu.Unlock()
	handlers.Event(string(firstID), "one event")
	select {
	case event := <-delivery.results:
		if event.SubscriptionID != firstID || event.Context != "one event" {
			t.Fatalf("delivered event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered after equivalent active registrations")
	}
	select {
	case event := <-delivery.results:
		t.Fatalf("duplicate registration caused an extra delivery: %#v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestIdleRegistrationDoesNotCallDeliveryUntilEventAdmission(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 1)}
	service := newTestHub(t, plugin, delivery)
	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	id, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	select {
	case event := <-delivery.results:
		t.Fatalf("idle registration called delivery without an event: %#v", event)
	case <-time.After(100 * time.Millisecond):
	}

	plugin.mu.Lock()
	handlers := plugin.handlers
	plugin.mu.Unlock()
	handlers.Event(string(id), "triggered")
	select {
	case event := <-delivery.results:
		if event.SubscriptionID != id || event.Context != "triggered" {
			t.Fatalf("delivered event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery was not called after event admission")
	}
}

func TestDeliveryQueueCapacityRejectsOverflowAndKeepsEventsDistinct(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	delivery := newBlockingDelivery(true)
	service := newTestHub(t, plugin, delivery)
	args, err := jsonvalue.Parse([]byte(`{"watch":true}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	id, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	plugin.mu.Lock()
	handlers := plugin.handlers
	plugin.mu.Unlock()

	handlers.Event(string(id), "event-0")
	select {
	case <-delivery.started:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not block the worker")
	}
	for index := 1; index < MaxDeliveries; index++ {
		handlers.Event(string(id), fmt.Sprintf("event-%d", index))
	}
	handlers.Event(string(id), "overflow")

	service.mu.Lock()
	pending := service.pendingEvents
	queued := len(service.queue)
	service.mu.Unlock()
	if pending != MaxDeliveries || queued != MaxDeliveries-1 {
		t.Fatalf("pending deliveries = %d, queued = %d; want %d total and %d queued", pending, queued, MaxDeliveries, MaxDeliveries-1)
	}

	close(delivery.release)
	seen := make(map[domain.DeliveryID]struct{}, MaxDeliveries)
	for index := 0; index < MaxDeliveries; index++ {
		select {
		case event := <-delivery.calls:
			if event.Context == "overflow" {
				t.Fatal("overflow event reached the delivery adapter")
			}
			if _, exists := seen[event.ID]; exists {
				t.Fatalf("delivery ID %q was reused", event.ID)
			}
			seen[event.ID] = struct{}{}
		case <-time.After(time.Second):
			t.Fatalf("only %d events reached delivery; want %d", len(seen), MaxDeliveries)
		}
	}
}

func TestShutdownCancelsInflightDeliveryAndDiscardsQueuedEvent(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	delivery := newBlockingDelivery(false)
	service := newTestHub(t, plugin, delivery)
	args, err := jsonvalue.Parse([]byte(`{"watch":true}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	id, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	plugin.mu.Lock()
	handlers := plugin.handlers
	plugin.mu.Unlock()
	handlers.Event(string(id), "inflight")
	select {
	case <-delivery.started:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	handlers.Event(string(id), "queued")

	shutdownDone := make(chan struct{})
	go func() {
		service.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the in-flight delivery")
	}

	select {
	case event := <-delivery.calls:
		if event.Context != "inflight" {
			t.Fatalf("delivery after shutdown = %q, want only in-flight event", event.Context)
		}
	default:
	}
	select {
	case event := <-delivery.calls:
		t.Fatalf("queued event was attempted after shutdown: %#v", event)
	default:
	}
	service.mu.Lock()
	pending := service.pendingEvents
	service.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending event count after shutdown = %d, want 0", pending)
	}
}

func TestUnknownAndCrossPluginEventsDoNotBlockAnotherPlugin(t *testing.T) {
	t.Parallel()
	first := &fakePlugin{}
	second := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 4)}
	service := New(delivery, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.AddPlugin("manual", first); err != nil {
		t.Fatal(err)
	}
	if err := service.AddPlugin("other", second); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	t.Cleanup(func() {
		service.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.StopPlugins(ctx)
	})

	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	firstID, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	secondID, registerErr := service.Register("other", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}

	first.mu.Lock()
	firstHandlers := first.handlers
	first.mu.Unlock()
	second.mu.Lock()
	secondHandlers := second.handlers
	second.mu.Unlock()
	firstHandlers.Event("unknown-subscription", "unknown")
	firstHandlers.Event(string(secondID), "cross-plugin")
	secondHandlers.Event(string(secondID), "valid")

	select {
	case event := <-delivery.results:
		if event.Plugin != "other" || event.SubscriptionID != secondID || event.Context != "valid" {
			t.Fatalf("delivery after rejected events = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("valid event from the other plugin was not delivered")
	}
	select {
	case event := <-delivery.results:
		t.Fatalf("unknown or cross-plugin event was delivered: %#v (first subscription %q)", event, firstID)
	default:
	}
}

func TestPluginExitInvalidatesActiveRegistration(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 1)}
	service := newTestHub(t, plugin, delivery)
	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	id, registerErr := service.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	plugin.handlers.Unavailable(errors.New("plugin exited"))
	if _, registerErr = service.Register("manual", testSessionID, args); registerErr == nil || registerErr.Code != "plugin_unavailable" {
		t.Fatalf("registration after exit error = %v, want plugin_unavailable", registerErr)
	}
	plugin.handlers.Event(string(id), "late")
	select {
	case event := <-delivery.results:
		t.Fatalf("late event was delivered: %#v", event)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestPluginExitLeavesOtherPluginAvailable(t *testing.T) {
	t.Parallel()
	first := &fakePlugin{}
	second := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 1)}
	service := New(delivery, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.AddPlugin("first", first); err != nil {
		t.Fatal(err)
	}
	if err := service.AddPlugin("second", second); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	t.Cleanup(func() {
		service.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.StopPlugins(ctx)
	})
	args, err := jsonvalue.Parse([]byte(`{"watch":true}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, registerErr := service.Register("first", testSessionID, args); registerErr != nil {
		t.Fatal(registerErr)
	}
	first.mu.Lock()
	firstHandlers := first.handlers
	first.mu.Unlock()
	firstHandlers.Unavailable(errors.New("first plugin exited"))
	if _, registerErr := service.Register("first", secondTestSessionID, args); registerErr == nil || registerErr.Code != "plugin_unavailable" {
		t.Fatalf("registration for exited plugin = %v, want plugin_unavailable", registerErr)
	}

	secondID, registerErr := service.Register("second", secondTestSessionID, args)
	if registerErr != nil {
		t.Fatalf("registration for unaffected plugin after exit = %v", registerErr)
	}
	second.mu.Lock()
	secondHandlers := second.handlers
	second.mu.Unlock()
	secondHandlers.Event(string(secondID), "other plugin remains available")
	select {
	case event := <-delivery.results:
		if event.Plugin != "second" || event.SubscriptionID != secondID || event.Context != "other plugin remains available" {
			t.Fatalf("delivery after plugin exit = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("unaffected plugin did not register and deliver after another plugin exited")
	}
}

func TestRestartRejectsOldSubscriptionAndRequiresFreshRegistration(t *testing.T) {
	t.Parallel()
	args, err := jsonvalue.Parse([]byte(`{"trigger_file":"C:\\events\\next.txt"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	oldPlugin := &fakePlugin{}
	oldService := newTestHub(t, oldPlugin, &fakeDelivery{results: make(chan domain.Event, 1)})
	oldID, registerErr := oldService.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	oldService.Shutdown()
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	oldService.StopPlugins(stopCtx)

	newPlugin := &fakePlugin{}
	delivery := &fakeDelivery{results: make(chan domain.Event, 1)}
	newService := newTestHub(t, newPlugin, delivery)
	newID, registerErr := newService.Register("manual", testSessionID, args)
	if registerErr != nil {
		t.Fatal(registerErr)
	}
	if oldID == newID {
		t.Fatalf("restart reused subscription %q", oldID)
	}
	newPlugin.mu.Lock()
	handlers := newPlugin.handlers
	newPlugin.mu.Unlock()
	handlers.Event(string(oldID), "stale")
	handlers.Event(string(newID), "fresh")
	select {
	case event := <-delivery.results:
		if event.SubscriptionID != newID || event.Context != "fresh" {
			t.Fatalf("delivery after restart = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh subscription was not delivered after restart")
	}
}

func newTestHub(t *testing.T, plugin *fakePlugin, delivery DeliveryPort) *Hub {
	t.Helper()
	service := New(delivery, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.AddPlugin("manual", plugin); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	t.Cleanup(func() {
		service.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		service.StopPlugins(ctx)
	})
	return service
}

type fakePlugin struct {
	mu           sync.Mutex
	handlers     PluginHandlers
	watchCalls   int
	watchStarted chan struct{}
	releaseWatch chan struct{}
	rejectWatch  bool
	emitOnAccept bool
}

func (p *fakePlugin) Start(_ context.Context, handlers PluginHandlers) error {
	p.mu.Lock()
	p.handlers = handlers
	p.mu.Unlock()
	handlers.Ready()
	return nil
}

func (p *fakePlugin) Watch(_ context.Context, request WatchRequest, activate func() error) error {
	p.mu.Lock()
	p.watchCalls++
	started := p.watchStarted
	release := p.releaseWatch
	rejected := p.rejectWatch
	emit := p.emitOnAccept
	handlers := p.handlers
	p.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if rejected {
		return &WatchError{Code: "watch_rejected", Message: "Plugin rejected the watch."}
	}
	if release != nil {
		<-release
	}
	if err := activate(); err != nil {
		return &WatchError{Code: "plugin_unavailable", Message: "Could not activate watch.", Unavailable: true, Cause: err}
	}
	if emit {
		handlers.Event(string(request.SubscriptionID), "immediate")
	}
	return nil
}

func (p *fakePlugin) Stop(context.Context) error { return nil }

type fakeDelivery struct{ results chan domain.Event }

func (d *fakeDelivery) Deliver(_ context.Context, event domain.Event) DeliveryResult {
	d.results <- event
	return DeliveryResult{Outcome: domain.DeliveryAccepted}
}

type blockingDelivery struct {
	calls   chan domain.Event
	started chan struct{}
	release chan struct{}
	count   atomic.Int32
}

func newBlockingDelivery(releaseManually bool) *blockingDelivery {
	delivery := &blockingDelivery{
		calls:   make(chan domain.Event, MaxDeliveries),
		started: make(chan struct{}, 1),
	}
	if releaseManually {
		delivery.release = make(chan struct{})
	}
	return delivery
}

func (d *blockingDelivery) Deliver(ctx context.Context, event domain.Event) DeliveryResult {
	d.calls <- event
	if d.count.Add(1) == 1 {
		d.started <- struct{}{}
		if d.release != nil {
			select {
			case <-d.release:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
		}
	}
	if ctx.Err() != nil {
		return DeliveryResult{Outcome: domain.DeliveryUnknown}
	}
	return DeliveryResult{Outcome: domain.DeliveryAccepted}
}

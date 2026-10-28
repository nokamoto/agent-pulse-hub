package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

const (
	cancellationTestA domain.SubscriptionID = "10000000-0000-4000-8000-000000000001"
	cancellationTestB domain.SubscriptionID = "10000000-0000-4000-8000-000000000002"
)

type cancellationTestInput struct {
	*MockPluginPort
	*MockCancellationPluginPort
	*MockRewaitPluginPort
	terminated atomic.Bool
}

type cancellationTestOutput struct {
	*MockPluginPort
	*MockguardedDeliveryPort
}

type cancellationTestStore struct {
	*MockRegistrationStore
	*MockRuntimeStateStore
	*MockCancellationStore
}

func newCancellationUnit(t *testing.T) (*Hub, *cancellationTestInput, *cancellationTestOutput, *cancellationTestStore) {
	t.Helper()
	ctrl := gomock.NewController(t)
	input := &cancellationTestInput{MockPluginPort: NewMockPluginPort(ctrl), MockCancellationPluginPort: NewMockCancellationPluginPort(ctrl), MockRewaitPluginPort: NewMockRewaitPluginPort(ctrl)}
	output := &cancellationTestOutput{NewMockPluginPort(ctrl), NewMockguardedDeliveryPort(ctrl)}
	store := &cancellationTestStore{NewMockRegistrationStore(ctrl), NewMockRuntimeStateStore(ctrl), NewMockCancellationStore(ctrl)}
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.store = store
	if err := h.AddPlugin("source", domain.InputRole, input); err != nil {
		t.Fatal(err)
	}
	if err := h.AddPlugin("sink", domain.OutputRole, output); err != nil {
		t.Fatal(err)
	}
	h.pluginReady("source", input, []string{"cancel_watch_v1"})
	h.pluginReady("sink", output, nil)
	input.MockCancellationPluginPort.EXPECT().InputTerminated().DoAndReturn(func() bool { return input.terminated.Load() }).AnyTimes()
	for _, id := range []domain.SubscriptionID{cancellationTestA, cancellationTestB} {
		registration := domain.Subscription{ID: id, InputPlugin: "source", OutputPlugin: "sink", WatchArgs: testArgs(t, `{"watch":"`+string(id)+`"}`), DestinationArgs: testArgs(t, `{}`), Active: true}
		h.registrations[id] = registration
		h.savedRegistrations[id] = registration
		h.retained[id] = struct{}{}
		h.activeByKey[registrationKey(registration.InputPlugin, registration.WatchArgs, registration.OutputPlugin, registration.DestinationArgs)] = id
	}
	t.Cleanup(h.Shutdown)
	return h, input, output, store
}

func cancellationAwait[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation unit coordination timed out")
		var zero T
		return zero
	}
}

func expectCancellationSuccess(input *cancellationTestInput, store *cancellationTestStore, id domain.SubscriptionID) {
	input.MockCancellationPluginPort.EXPECT().FenceWatch(id).Return(nil)
	store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(nil)
	input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), id).Return(nil)
	store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil)
}

func cancellationItem(h *Hub, input PluginPort, output PluginPort, id domain.SubscriptionID, eventID, context string) queuedEvent {
	r := h.registrations[id]
	return queuedEvent{event: domain.Event{ID: domain.DeliveryID(context), SubscriptionID: id, InputPlugin: r.InputPlugin, OutputPlugin: r.OutputPlugin, DestinationArgs: r.DestinationArgs, Context: context}, inputPort: input, outputPort: output, eventID: eventID}
}

func TestCancellationValidationUnknownAndUnsupportedDoNotMutate(t *testing.T) {
	for _, value := range []string{"", "not-id", "10000000-0000-4000-8000-00000000000A"} {
		t.Run(value, func(t *testing.T) {
			h, _, _, _ := newCancellationUnit(t)
			if err := h.Unsubscribe(value); err == nil || err.Code != "invalid_request" {
				t.Fatalf("validation = %v", err)
			}
			if len(h.cancellations) != 0 || !h.registrations[cancellationTestA].Active {
				t.Fatal("invalid request mutated route")
			}
		})
	}
	h, _, _, _ := newCancellationUnit(t)
	if err := h.Unsubscribe("20000000-0000-4000-8000-000000000000"); err == nil || err.Code != "unknown_subscription" {
		t.Fatalf("unknown = %v", err)
	}
	h.mu.Lock()
	h.plugins["source"].capabilities = nil
	h.mu.Unlock()
	if err := h.Unsubscribe(string(cancellationTestA)); err == nil || err.Code != "unsubscribe_unsupported" {
		t.Fatalf("unsupported = %v", err)
	}
	if len(h.cancellations) != 0 || !h.registrations[cancellationTestA].Active {
		t.Fatal("unsupported request mutated route")
	}
}

func TestCancellationBusyAndRejectedRetryPreserveFenceAndDurableIntent(t *testing.T) {
	h, input, _, store := newCancellationUnit(t)
	entered, release := make(chan struct{}), make(chan struct{})
	input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).Return(nil).Times(2)
	store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(nil).Times(2)
	input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).DoAndReturn(func(context.Context, domain.SubscriptionID) error {
		close(entered)
		<-release
		return &WatchError{Code: "cancellation_rejected", Message: "private-plugin-payload"}
	})
	result := make(chan *Error, 1)
	go func() { result <- h.Unsubscribe(string(cancellationTestA)) }()
	cancellationAwait(t, entered)
	if err := h.Unsubscribe(string(cancellationTestA)); err == nil || err.Code != "unsubscribe_busy" {
		t.Fatalf("overlap = %v", err)
	}
	r := h.registrations[cancellationTestA]
	if _, err := h.Register(r.InputPlugin, r.WatchArgs, r.OutputPlugin, r.DestinationArgs); err == nil || err.Code != "unsubscribe_busy" {
		t.Fatalf("equivalent registration = %v", err)
	}
	close(release)
	err := cancellationAwait(t, result)
	if err == nil || err.Code != "cancellation_rejected" || !strings.Contains(err.Message, "durable intent exists") || strings.Contains(err.Message, "private-plugin-payload") {
		t.Fatalf("negative result = %v", err)
	}
	if h.registrations[cancellationTestA].Active || h.cancellations[cancellationTestA] == nil || !h.registrations[cancellationTestB].Active {
		t.Fatal("failure lost target fence or affected unrelated route")
	}
	input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).Return(nil)
	store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil)
	if err := h.Unsubscribe(string(cancellationTestA)); err != nil {
		t.Fatal(err)
	}
	if err := h.Unsubscribe(string(cancellationTestA)); err == nil || err.Code != "unknown_subscription" {
		t.Fatalf("repeat = %v", err)
	}
}

func TestCancellationStorageStagesAndIntentHousekeeping(t *testing.T) {
	for _, test := range []struct {
		name                   string
		begin, remove, cleanup error
		code, text             string
	}{
		{"intent", errors.New("write staging denied"), nil, nil, "storage_error", "No durable intent exists"},
		{"removal", nil, errors.New("runtime entry is not regular"), nil, "storage_error", "A durable intent exists"},
		{"housekeeping", nil, nil, errors.New("intent housekeeping denied"), "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, input, _, store := newCancellationUnit(t)
			input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).Return(nil)
			store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(test.begin)
			if test.begin == nil {
				input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).Return(nil)
				store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(test.cleanup, test.remove)
			}
			err := h.Unsubscribe(string(cancellationTestA))
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := h.savedRegistrations[cancellationTestA]; exists {
					t.Fatal("housekeeping failure retained removed registration")
				}
			} else if err == nil || err.Code != test.code || !strings.Contains(err.Message, test.text) {
				t.Fatalf("storage failure = %v", err)
			}
			if !h.registrations[cancellationTestB].Active {
				t.Fatal("storage failure affected unrelated route")
			}
		})
	}
}

func TestCancellationGuardDropsDequeuedDeliveryAfterFenceHasBeenRemoved(t *testing.T) {
	h, input, output, store := newCancellationUnit(t)
	item := cancellationItem(h, input, output, cancellationTestA, "", "dequeued")
	entered, release := make(chan struct{}), make(chan struct{})
	var issued atomic.Bool
	output.MockguardedDeliveryPort.EXPECT().DeliverGuarded(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ domain.Event, admit func(func()) bool) DeliveryResult {
		close(entered)
		<-release
		admit(func() { issued.Store(true) })
		return DeliveryResult{Outcome: domain.DeliveryFailed}
	})
	h.mu.Lock()
	h.pendingEvents++
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.deliver(item); close(done) }()
	cancellationAwait(t, entered)
	expectCancellationSuccess(input, store, cancellationTestA)
	if err := h.Unsubscribe(string(cancellationTestA)); err != nil {
		t.Fatal(err)
	}
	close(release)
	cancellationAwait(t, done)
	if issued.Load() || h.pendingEvents != 0 {
		t.Fatal("dequeued request issued after completed cancellation or retained capacity")
	}
}

func TestCancellationRemovesQueuedTargetPreservesFIFOAndSendsDiscardNotice(t *testing.T) {
	h, input, output, store := newCancellationUnit(t)
	watch := &runtimeWatch{registration: h.registrations[cancellationTestA], inputName: "source", inputPort: input}
	h.runtimeWatches[cancellationTestA] = watch
	// Populate while holding the admission lock so the real worker cannot race
	// this deterministic queue construction. No wake is sent until cancellation.
	h.mu.Lock()
	h.queue <- cancellationItem(h, input, output, cancellationTestB, "", "B1")
	h.queue <- cancellationItem(h, input, output, cancellationTestA, "2", "A2")
	h.queue <- cancellationItem(h, input, output, cancellationTestB, "", "B2")
	h.pendingEvents = 3
	h.mu.Unlock()
	first, second := make(chan struct{}), make(chan struct{})
	gomock.InOrder(
		output.MockguardedDeliveryPort.EXPECT().DeliverGuarded(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, event domain.Event, admit func(func()) bool) DeliveryResult {
			if event.Context != "B1" {
				t.Errorf("first retained event = %s", event.Context)
			}
			admit(func() {})
			close(first)
			return DeliveryResult{Outcome: domain.DeliveryAccepted}
		}),
		output.MockguardedDeliveryPort.EXPECT().DeliverGuarded(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, event domain.Event, admit func(func()) bool) DeliveryResult {
			if event.Context != "B2" {
				t.Errorf("second retained event = %s", event.Context)
			}
			admit(func() {})
			close(second)
			return DeliveryResult{Outcome: domain.DeliveryAccepted}
		}),
	)
	input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).Return(nil)
	gomock.InOrder(
		store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(nil),
		input.MockPluginPort.EXPECT().NotifyDelivery(gomock.Any(), RuntimeNotice{SubscriptionID: cancellationTestA, EventID: "2", Status: "discarded"}).Return(nil),
		input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).Return(nil),
		store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil),
	)
	if err := h.Unsubscribe(string(cancellationTestA)); err != nil {
		t.Fatal(err)
	}
	cancellationAwait(t, first)
	cancellationAwait(t, second)
	waitRuntimeTest(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.pendingEvents == 0 })
}

func TestCancellationDrainsAdmittedSaveAndDiscardsCollectedLateSnapshot(t *testing.T) {
	h, input, _, store := newCancellationUnit(t)
	h.runtimeStore = store
	watch := &runtimeWatch{registration: h.registrations[cancellationTestA], inputName: "source", inputPort: input}
	h.runtimeWatches[cancellationTestA] = watch
	state := hubTestState(t, `{"schema":"v1","payload":{}}`)
	entered, release := make(chan struct{}), make(chan struct{})
	store.MockRuntimeStateStore.EXPECT().CommitRuntime(gomock.Any(), state).DoAndReturn(func(domain.Subscription, *jsonvalue.Value) (uint64, error) { close(entered); <-release; return 1, nil })
	saved := make(chan struct{})
	go func() { h.saveRuntimeState(watch, state); close(saved) }()
	cancellationAwait(t, entered)
	fenced := make(chan struct{})
	input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).DoAndReturn(func(domain.SubscriptionID) error { close(fenced); return nil })
	var saveFinished atomic.Bool
	store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).DoAndReturn(func(domain.Subscription) error {
		if !saveFinished.Load() {
			t.Error("intent overtook admitted save")
		}
		return nil
	})
	input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).Return(nil)
	store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil)
	result := make(chan *Error, 1)
	go func() { result <- h.Unsubscribe(string(cancellationTestA)) }()
	cancellationAwait(t, fenced)
	saveFinished.Store(true)
	close(release)
	cancellationAwait(t, saved)
	if err := cancellationAwait(t, result); err != nil {
		t.Fatal(err)
	}
	h.saveRuntimeState(watch, state)
	h.runRuntimeCheckpointBatchForWatches(context.Background(), false, []*runtimeWatch{watch})
	if _, exists := h.lifecycles[cancellationTestA]; exists {
		t.Fatal("late snapshot recreated removed lifecycle")
	}
	h.runtimeStore = nil
}

func TestCancellationStopWaitsForAdmittedTransactionAndRejectsNewCalls(t *testing.T) {
	h, input, _, store := newCancellationUnit(t)
	entered, release := make(chan struct{}), make(chan struct{})
	input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).Return(nil)
	store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(nil)
	input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).DoAndReturn(func(context.Context, domain.SubscriptionID) error { close(entered); <-release; return nil })
	store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil)
	result := make(chan *Error, 1)
	go func() { result <- h.Unsubscribe(string(cancellationTestA)) }()
	cancellationAwait(t, entered)
	stopped := make(chan struct{})
	go func() { h.Shutdown(); close(stopped) }()
	waitRuntimeTest(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.stopping })
	if err := h.Unsubscribe(string(cancellationTestB)); err == nil || err.Code != "plugin_unavailable" {
		t.Fatalf("stop admission = %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("stop finished before admitted cancellation")
	default:
	}
	close(release)
	if err := cancellationAwait(t, result); err != nil {
		t.Fatal(err)
	}
	cancellationAwait(t, stopped)
}

func TestCancellationSavedInactiveRouteRequiresConfirmedInputTermination(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream-only", true: "confirmed-cleanup"}[terminated], func(t *testing.T) {
			h, input, _, store := newCancellationUnit(t)
			h.mu.Lock()
			delete(h.registrations, cancellationTestA)
			h.plugins["source"].available = false
			h.mu.Unlock()
			input.terminated.Store(terminated)
			store.MockCancellationStore.EXPECT().BeginCancellation(gomock.Any()).Return(nil)
			if terminated {
				store.MockCancellationStore.EXPECT().RemoveCancelled(gomock.Any()).Return(nil, nil)
			} else {
				input.MockCancellationPluginPort.EXPECT().FenceWatch(cancellationTestA).Return(nil)
				input.MockCancellationPluginPort.EXPECT().CancelWatch(gomock.Any(), cancellationTestA).Return(&WatchError{Code: "plugin_unavailable", Unavailable: true})
			}
			err := h.Unsubscribe(string(cancellationTestA))
			if terminated && err != nil {
				t.Fatal(err)
			}
			if !terminated && (err == nil || err.Code != "plugin_unavailable") {
				t.Fatalf("unconfirmed termination = %v", err)
			}
		})
	}
}

func TestCancellationFreshEquivalentRegistrationUsesNewIDAndNoOldProgress(t *testing.T) {
	h, input, output, store := newCancellationUnit(t)
	original := h.registrations[cancellationTestA]
	expectCancellationSuccess(input, store, cancellationTestA)
	if err := h.Unsubscribe(string(cancellationTestA)); err != nil {
		t.Fatal(err)
	}
	output.MockPluginPort.EXPECT().ValidateDestination(gomock.Any(), gomock.Any()).Return(nil)
	input.MockPluginPort.EXPECT().Watch(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, request WatchRequest, activate func() error) error {
		if request.SubscriptionID == cancellationTestA || request.State != nil {
			t.Error("fresh equivalent watch reused cancelled ID or progress")
		}
		return activate()
	})
	store.MockRegistrationStore.EXPECT().Commit(gomock.Any()).DoAndReturn(func(registration domain.Subscription) error {
		if registration.ID == cancellationTestA {
			t.Error("registration reused cancelled UUID")
		}
		return nil
	})
	id, err := h.Register(original.InputPlugin, original.WatchArgs, original.OutputPlugin, original.DestinationArgs)
	if err != nil || id == cancellationTestA || !ValidSubscriptionID(string(id)) {
		t.Fatalf("fresh registration = %s, %v", id, err)
	}
}

func TestCancellationExpiresPendingRewaitButKeepsCompletedReceiptsAndLateActionOutcome(t *testing.T) {
	h, input, output, store := newCancellationUnit(t)
	h.mu.Lock()
	h.plugins["source"].capabilities["rewait_v1"] = true
	h.plugins["source"].capabilities["runtime_state_v2"] = true
	h.notificationWaits[cancellationTestA] = &notificationWait{generation: "old"}
	h.runtimeWatches[cancellationTestA] = &runtimeWatch{registration: h.registrations[cancellationTestA], inputName: "source", inputPort: input}
	h.sessionStartMillis = 1000
	h.rewaitNow = func() time.Time { return time.UnixMilli(2000) }
	h.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	input.MockRewaitPluginPort.EXPECT().ResumeWatch(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ ResumeWatchRequest) error {
		close(entered)
		<-ctx.Done()
		<-release
		return nil
	})
	operation := rewaitTestID(42)
	rewaitResult := make(chan *Error, 1)
	go func() { rewaitResult <- h.Rewait(string(cancellationTestA), operation, "position") }()
	cancellationAwait(t, entered)
	completed := rewaitTestID(43)
	done := make(chan struct{})
	close(done)
	h.mu.Lock()
	h.rewaitReceipts[completed] = &rewaitReceipt{subscriptionID: string(cancellationTestA), positionDigest: h.rewaitReceipts[operation].positionDigest, timestamp: 1500, done: done, completed: true}
	h.pendingEvents = 1
	h.mu.Unlock()
	late := cancellationItem(h, input, output, cancellationTestA, "1", "already-issued")
	expectCancellationSuccess(input, store, cancellationTestA)
	if err := h.Unsubscribe(string(cancellationTestA)); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := cancellationAwait(t, rewaitResult); err == nil || err.Code != "unknown_subscription" {
		t.Fatalf("pending rewait = %v", err)
	}
	if err := h.Rewait(string(cancellationTestA), completed, "position"); err != nil {
		t.Fatalf("completed receipt replay = %v", err)
	}
	if err := h.Rewait(string(cancellationTestA), operation, "position"); err == nil || err.Code != "unknown_subscription" {
		t.Fatalf("expired preparation replay = %v", err)
	}
	if err := h.Rewait(string(cancellationTestA), rewaitTestID(44), "position"); err == nil || err.Code != "unknown_subscription" {
		t.Fatalf("new rewait = %v", err)
	}
	h.finishRuntimeOutcome(late, "accepted")
	if h.pendingEvents != 0 || len(h.runtimeWatches) != 0 || !h.plugins["source"].available {
		t.Fatal("late action outcome revived target, leaked capacity, or invalidated shared input")
	}
}

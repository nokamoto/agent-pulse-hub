package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type testRuntimeStore struct {
	mu            sync.Mutex
	registrations []domain.Subscription
	states        map[domain.SubscriptionID]*jsonvalue.Value
	saves         map[domain.SubscriptionID]int
	failure       error
}

func newTestRuntimeStore() *testRuntimeStore {
	return &testRuntimeStore{states: make(map[domain.SubscriptionID]*jsonvalue.Value), saves: make(map[domain.SubscriptionID]int)}
}

func (s *testRuntimeStore) Commit(registration domain.Subscription) error {
	s.mu.Lock()
	s.registrations = append(s.registrations, registration)
	s.mu.Unlock()
	return nil
}

func (s *testRuntimeStore) LoadRuntime(registrations []domain.Subscription) (map[domain.SubscriptionID]*jsonvalue.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[domain.SubscriptionID]*jsonvalue.Value)
	for _, registration := range registrations {
		if state := s.states[registration.ID]; state != nil {
			states[registration.ID] = state
		}
	}
	return states, nil
}

func (s *testRuntimeStore) CommitRuntime(registration domain.Subscription, state *jsonvalue.Value) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return 0, s.failure
	}
	s.saves[registration.ID]++
	s.states[registration.ID] = state
	return uint64(s.saves[registration.ID]), nil
}

func (s *testRuntimeStore) saveCount(id domain.SubscriptionID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves[id]
}

func newRuntimeTestHub(t *testing.T, store *testRuntimeStore) (*Hub, *fakePlugin, *fakePlugin) {
	t.Helper()
	if store == nil {
		store = newTestRuntimeStore()
	}
	input := newFakePlugin("cursor", domain.InputRole)
	input.capabilities = []string{"runtime_state_v1"}
	output := newFakePlugin("sink", domain.OutputRole)
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range []*fakePlugin{input, output} {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.PrepareRestorations(nil); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	return service, input, output
}

func TestRuntimeStateNoticePrecedesPeriodicAndFinalCheckpoints(t *testing.T) {
	store := newTestRuntimeStore()
	service, input, output := newRuntimeTestHub(t, store)
	state := hubTestState(t, `{"schema":"probe-v1","payload":{"confirmed":["U1"]}}`)
	input.checkpoint = func(RuntimeCheckpointRequest) (*jsonvalue.Value, error) { return state, nil }
	id := register(t, service, "cursor", "sink", `{}`, `{}`)
	if got := input.emitState(string(id), "1", "U1"); got != "" {
		t.Fatalf("admitted event terminal result = %q", got)
	}
	waitRuntimeTest(t, func() bool {
		input.mu.Lock()
		defer input.mu.Unlock()
		return len(input.notices) == 1
	})
	output.mu.Lock()
	records := append([]domain.Event(nil), output.delivered...)
	output.mu.Unlock()
	if len(records) != 1 || records[0].Context != "U1" {
		t.Fatalf("delivered events = %+v", records)
	}
	input.mu.Lock()
	if notice := input.notices[0]; notice.EventID != "1" || notice.Status != string(domain.DeliveryAccepted) {
		input.mu.Unlock()
		t.Fatalf("terminal notice = %+v", notice)
	}
	input.mu.Unlock()

	service.runRuntimeCheckpointBatch(context.Background(), false)
	if count := store.saveCount(id); count != 1 {
		t.Fatalf("periodic runtime saves = %d, want 1", count)
	}
	service.Shutdown()
	input.mu.Lock()
	defer input.mu.Unlock()
	if len(input.checkpoints) != 2 || input.checkpoints[0].Quiesce || !input.checkpoints[1].Quiesce {
		t.Fatalf("checkpoint phases = %+v", input.checkpoints)
	}
	if count := store.saveCount(id); count != 2 {
		t.Fatalf("runtime saves after final checkpoint = %d, want 2", count)
	}
}

func TestRuntimeCheckpointSchedulesAreIndependentPerInput(t *testing.T) {
	store := newTestRuntimeStore()
	slow := newFakePlugin("slow-cursor", domain.InputRole)
	slow.capabilities = []string{"runtime_state_v1"}
	fast := newFakePlugin("fast-cursor", domain.InputRole)
	fast.capabilities = []string{"runtime_state_v1"}
	output := newFakePlugin("sink", domain.OutputRole)
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range []*fakePlugin{slow, fast, output} {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.PrepareRestorations(nil); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	service.StartRuntimeCheckpoints()

	state := hubTestState(t, `{"schema":"probe-v1","payload":{}}`)
	slowEntered, releaseSlow := make(chan struct{}), make(chan struct{})
	var slowBatches atomic.Int32
	slow.checkpoint = func(RuntimeCheckpointRequest) (*jsonvalue.Value, error) {
		if slowBatches.Add(1) == 1 {
			close(slowEntered)
			<-releaseSlow
		}
		return state, nil
	}
	fast.checkpoint = func(RuntimeCheckpointRequest) (*jsonvalue.Value, error) { return state, nil }
	slowID := register(t, service, "slow-cursor", "sink", `{}`, `{}`)
	fastID := register(t, service, "fast-cursor", "sink", `{}`, `{}`)

	deadline := time.Now().Add(12 * time.Second)
	for store.saveCount(fastID) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-slowEntered:
	default:
		t.Fatal("slow input did not enter its first periodic batch")
	}
	if got := store.saveCount(fastID); got < 2 {
		t.Fatalf("healthy input saves while another input is blocked = %d, want at least 2", got)
	}
	close(releaseSlow)
	deadline = time.Now().Add(2 * time.Second)
	for store.saveCount(slowID) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := store.saveCount(slowID); got < 1 {
		t.Fatalf("slow input did not save after release: %d", got)
	}
	service.Shutdown()
}

func TestRuntimeSaveFailureDoesNotStopMonitoring(t *testing.T) {
	store := newTestRuntimeStore()
	service, input, output := newRuntimeTestHub(t, store)
	state := hubTestState(t, `{"schema":"probe-v1","payload":{"confirmed":["U1","U2"]}}`)
	input.checkpoint = func(RuntimeCheckpointRequest) (*jsonvalue.Value, error) {
		return state, nil
	}
	id := register(t, service, "cursor", "sink", `{}`, `{}`)
	if got := input.emitState(string(id), "1", "U1"); got != "" {
		t.Fatalf("first event terminal result = %q", got)
	}
	waitRuntimeTest(t, func() bool {
		_, _, count := output.counts()
		return count == 1
	})
	store.mu.Lock()
	store.failure = errors.New("injected runtime save failure")
	store.mu.Unlock()
	service.runRuntimeCheckpointBatch(context.Background(), false)
	if !pluginAvailable(input) {
		t.Fatal("runtime save failure stopped the participating input")
	}
	if got := input.emitState(string(id), "2", "U2"); got != "" {
		t.Fatalf("second event terminal result = %q", got)
	}
	waitRuntimeTest(t, func() bool {
		_, _, count := output.counts()
		return count == 2
	})
	output.mu.Lock()
	delivered := append([]domain.Event(nil), output.delivered...)
	output.mu.Unlock()
	if delivered[1].Context != "U2" || !pluginAvailable(input) {
		t.Fatalf("monitoring after failed save: deliveries=%+v input_available=%t", delivered, pluginAvailable(input))
	}
}

func TestUnsentRuntimeOutcomesKeepEventCapacityReserved(t *testing.T) {
	service, input, output := newRuntimeTestHub(t, nil)
	input.notify = func(RuntimeNotice) error { return errors.New("injected notice send failure") }
	id := register(t, service, "cursor", "sink", `{}`, `{}`)
	for eventID := 1; eventID <= MaxDeliveries; eventID++ {
		if got := input.emitState(string(id), fmt.Sprint(eventID), "update"); got != "" {
			t.Fatalf("event %d terminal result = %q", eventID, got)
		}
	}
	waitRuntimeTest(t, func() bool {
		_, _, delivered := output.counts()
		service.mu.Lock()
		pending := service.pendingEvents
		service.mu.Unlock()
		return delivered == MaxDeliveries && pending == MaxDeliveries
	})
	if got := input.emitState(string(id), fmt.Sprint(MaxDeliveries+1), "overflow"); got != "discarded" {
		t.Fatalf("event beyond reserved capacity result = %q", got)
	}
	service.mu.Lock()
	watch := service.runtimeWatches[id]
	service.mu.Unlock()
	watch.mu.Lock()
	outcomes := len(watch.outcomes)
	watch.mu.Unlock()
	if outcomes != MaxDeliveries {
		t.Fatalf("unsent terminal outcomes = %d, want bounded at %d", outcomes, MaxDeliveries)
	}

	input.mu.Lock()
	input.notify = nil
	input.mu.Unlock()
	service.runRuntimeCheckpointBatch(context.Background(), false)
	service.mu.Lock()
	pending := service.pendingEvents
	service.mu.Unlock()
	if pending != 0 {
		t.Fatalf("successfully notified events still reserve capacity: %d", pending)
	}
}

func TestUnavailableInputReleasesUnsentRuntimeOutcomeCapacity(t *testing.T) {
	store := newTestRuntimeStore()
	input := newFakePlugin("cursor", domain.InputRole)
	input.capabilities = []string{"runtime_state_v1"}
	healthy := newFakePlugin("healthy-cursor", domain.InputRole)
	output := newFakePlugin("sink", domain.OutputRole)
	input.notify = func(RuntimeNotice) error { return errors.New("injected notice send failure") }
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range []*fakePlugin{input, healthy, output} {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.PrepareRestorations(nil); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	failedID := register(t, service, "cursor", "sink", `{"source":"failed"}`, `{}`)
	healthyID := register(t, service, "healthy-cursor", "sink", `{"source":"healthy"}`, `{}`)
	for eventID := 1; eventID <= MaxDeliveries; eventID++ {
		if got := input.emitState(string(failedID), fmt.Sprint(eventID), "update"); got != "" {
			t.Fatalf("event %d terminal result = %q", eventID, got)
		}
	}
	waitRuntimeTest(t, func() bool {
		_, _, delivered := output.counts()
		service.mu.Lock()
		pending := service.pendingEvents
		service.mu.Unlock()
		return delivered == MaxDeliveries && pending == MaxDeliveries
	})

	input.mu.Lock()
	input.notify = func(RuntimeNotice) error {
		return &RuntimeError{Code: "notice_send", Message: "input transport failed", Unavailable: true}
	}
	input.mu.Unlock()
	service.runRuntimeCheckpointBatch(context.Background(), false)
	service.mu.Lock()
	pending := service.pendingEvents
	failedAvailable := service.plugins["cursor"].available
	service.mu.Unlock()
	if pending != 0 || failedAvailable {
		t.Fatalf("failed input cleanup left capacity or input active: pending=%d available=%t", pending, failedAvailable)
	}

	healthy.emit(string(healthyID), "next event")
	waitRuntimeTest(t, func() bool {
		_, _, delivered := output.counts()
		return delivered == MaxDeliveries+1
	})
}

func TestRuntimeStopCutoffDowngradesLateAcceptanceToUnknown(t *testing.T) {
	service, input, output := newRuntimeTestHub(t, nil)
	deliverStarted, releaseDelivery := make(chan struct{}), make(chan struct{})
	output.deliver = func(domain.Event) DeliveryResult {
		close(deliverStarted)
		<-releaseDelivery
		return DeliveryResult{Outcome: domain.DeliveryAccepted}
	}
	id := register(t, service, "cursor", "sink", `{}`, `{}`)
	if got := input.emitState(string(id), "1", "late"); got != "" {
		t.Fatalf("admitted event terminal result = %q", got)
	}
	select {
	case <-deliverStarted:
	case <-time.After(time.Second):
		t.Fatal("delivery did not start")
	}
	shutdownDone := make(chan struct{})
	go func() {
		service.Shutdown()
		close(shutdownDone)
	}()
	waitRuntimeTest(t, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.stopping
	})
	close(releaseDelivery)
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after delivery cutoff")
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	if len(input.notices) != 1 || input.notices[0].Status != string(domain.DeliveryUnknown) {
		t.Fatalf("post-cutoff terminal notice = %+v", input.notices)
	}
	if len(input.checkpoints) != 1 || !input.checkpoints[0].Quiesce {
		t.Fatalf("final checkpoint = %+v", input.checkpoints)
	}
}

func TestRuntimeStateRequiresParticipantOnRestore(t *testing.T) {
	registration := domain.Subscription{
		ID: "8fc5db2a-77a0-4f60-bf92-bbe214a83a71", InputPlugin: "cursor", OutputPlugin: "sink",
		WatchArgs: hubTestState(t, `{}`), DestinationArgs: hubTestState(t, `{}`),
	}
	store := newTestRuntimeStore()
	store.states[registration.ID] = hubTestState(t, `{"schema":"probe-v1","payload":{}}`)
	input := newFakePlugin("cursor", domain.InputRole)
	output := newFakePlugin("sink", domain.OutputRole)
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	_ = service.AddPlugin(input.name, input.role, input)
	_ = service.AddPlugin(output.name, output.role, output)
	if err := service.PrepareRestorations([]domain.Subscription{registration}); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	defer func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	}()
	if err := service.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "requires input plugin") {
		t.Fatalf("nonparticipating restore result = %v", err)
	}
	if _, watches, _ := input.counts(); watches != 0 {
		t.Fatalf("incompatible restore dispatched %d watches", watches)
	}
}

func hubTestState(t *testing.T, raw string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func waitRuntimeTest(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime test condition did not become true")
}

func pluginAvailable(plugin *fakePlugin) bool {
	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	return plugin.available
}

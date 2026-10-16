package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type fakePlugin struct {
	mu sync.Mutex

	name      string
	role      domain.PluginRole
	handlers  PluginHandlers
	available bool
	validate  func(DestinationRequest) error
	watch     func(WatchRequest, func() error) error
	deliver   func(domain.Event) DeliveryResult

	validationRequests []DestinationRequest
	watchRequests      []WatchRequest
	delivered          []domain.Event
	deliverStarted     chan domain.Event
	emitOnAccept       bool
}

func newFakePlugin(name string, role domain.PluginRole) *fakePlugin {
	return &fakePlugin{name: name, role: role, available: true}
}

type testRegistrationStore struct {
	mu       sync.Mutex
	attempts int
	commits  []domain.Subscription
	failure  error
	entered  chan struct{}
	release  chan struct{}
}

func (s *testRegistrationStore) Commit(registration domain.Subscription) error {
	s.mu.Lock()
	s.attempts++
	entered, release, failure := s.entered, s.release, s.failure
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if failure != nil {
		return failure
	}
	s.mu.Lock()
	s.commits = append(s.commits, registration)
	s.mu.Unlock()
	return nil
}

func (s *testRegistrationStore) snapshot() (int, []domain.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts, append([]domain.Subscription(nil), s.commits...)
}

func newPersistentHub(t *testing.T, store *testRegistrationStore, plugins ...*fakePlugin) *Hub {
	t.Helper()
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range plugins {
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
	return service
}

func (p *fakePlugin) Start(_ context.Context, handlers PluginHandlers) error {
	p.mu.Lock()
	p.handlers = handlers
	p.mu.Unlock()
	if handlers.Ready != nil {
		handlers.Ready()
	}
	return nil
}

func (p *fakePlugin) ValidateDestination(_ context.Context, request DestinationRequest) error {
	p.mu.Lock()
	p.validationRequests = append(p.validationRequests, request)
	validate := p.validate
	p.mu.Unlock()
	if validate != nil {
		return validate(request)
	}
	return nil
}

func (p *fakePlugin) Watch(_ context.Context, request WatchRequest, activate func() error) error {
	p.mu.Lock()
	p.watchRequests = append(p.watchRequests, request)
	watch := p.watch
	p.mu.Unlock()
	if watch != nil {
		return watch(request, activate)
	}
	if err := activate(); err != nil {
		return err
	}
	p.mu.Lock()
	emitOnAccept := p.emitOnAccept
	p.mu.Unlock()
	if emitOnAccept {
		p.emit(string(request.SubscriptionID), "immediate")
	}
	return nil
}

func (p *fakePlugin) Deliver(_ context.Context, event domain.Event) DeliveryResult {
	p.mu.Lock()
	p.delivered = append(p.delivered, event)
	deliver := p.deliver
	started := p.deliverStarted
	p.mu.Unlock()
	if started != nil {
		started <- event
	}
	if deliver != nil {
		return deliver(event)
	}
	return DeliveryResult{Outcome: domain.DeliveryAccepted}
}

func (p *fakePlugin) Stop(context.Context) error {
	p.mu.Lock()
	p.available = false
	p.mu.Unlock()
	return nil
}

func (p *fakePlugin) emit(id, eventContext string) {
	p.mu.Lock()
	handlers := p.handlers
	available := p.available
	p.mu.Unlock()
	if available && handlers.Event != nil {
		handlers.Event(id, eventContext)
	}
}

func (p *fakePlugin) fail(err error) {
	p.mu.Lock()
	handler := p.handlers.Unavailable
	p.available = false
	p.mu.Unlock()
	if handler != nil {
		handler(err)
	}
}

func (p *fakePlugin) counts() (validations, watches, deliveries int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.validationRequests), len(p.watchRequests), len(p.delivered)
}

func (p *fakePlugin) lastDelivery() domain.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delivered[len(p.delivered)-1]
}

func testArgs(t *testing.T, value string) *jsonvalue.Value {
	t.Helper()
	parsed, err := jsonvalue.Parse([]byte(value), 0)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func newHub(t *testing.T, plugins ...*fakePlugin) *Hub {
	t.Helper()
	service := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, plugin := range plugins {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	service.StartPlugins(context.Background())
	t.Cleanup(func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	return service
}

func register(t *testing.T, service *Hub, input, output, watch, destination string) domain.SubscriptionID {
	t.Helper()
	id, err := service.Register(input, testArgs(t, watch), output, testArgs(t, destination))
	if err != nil {
		t.Fatalf("Register(%s -> %s): %v", input, output, err)
	}
	return id
}

func waitForDelivery(t *testing.T, output *fakePlugin) domain.Event {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		_, _, count := output.counts()
		if count > 0 {
			return output.lastDelivery()
		}
		select {
		case <-deadline:
			t.Fatal("event did not reach output plugin")
			return domain.Event{}
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRegisterValidatesOutputThenWatchesAndDeduplicatesCanonicalArguments(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	otherOutput := newFakePlugin("R", domain.OutputRole)
	service := newHub(t, input, output, otherOutput)
	watch := `{"nested":{"n":1.00},"items":["a","b"]}`
	watchEquivalent := `{"items":["a","b"],"nested":{"n":1}}`
	destination := `{"output_file":"/tmp/a.jsonl","meta":true}`
	destinationEquivalent := `{"meta":true,"output_file":"/tmp/a.jsonl"}`

	first := register(t, service, "P", "O", watch, destination)
	second := register(t, service, "P", "O", watchEquivalent, destinationEquivalent)
	if first == "" || first != second {
		t.Fatalf("equivalent IDs = %q, %q", first, second)
	}
	if valid, watches, _ := output.counts(); valid != 1 || watches != 0 {
		t.Fatalf("output calls = validation %d, watches %d; want one validation and no input watch", valid, watches)
	}
	if _, watches, _ := input.counts(); watches != 1 {
		t.Fatalf("input watch count = %d, want 1", watches)
	}

	changedDestination := register(t, service, "P", "O", watch, `{"output_file":"/tmp/b.jsonl"}`)
	changedOutput := register(t, service, "P", "R", watch, destination)
	if changedDestination == first || changedOutput == first || changedOutput == changedDestination {
		t.Fatalf("changed recipients reused an ID: original=%q destination=%q output=%q", first, changedDestination, changedOutput)
	}
	input.emit(string(first), "through A")
	if event := waitForDelivery(t, output); !bytes.Equal(event.DestinationArgs.Canonical(), testArgs(t, destination).Canonical()) {
		t.Fatalf("original event was retargeted: %+v", event)
	}
}

func TestPersistentRegistrationCommitsBeforeSuccessAndRetainsCapacityAfterInputExit(t *testing.T) {
	store := &testRegistrationStore{}
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := newPersistentHub(t, store, input, output)
	watchArgs := testArgs(t, `{"trigger":"x","number":9007199254740993}`)
	destinationArgs := testArgs(t, `{"output_file":"/tmp/a.jsonl"}`)
	id, err := service.Register("P", watchArgs, "O", destinationArgs)
	if err != nil || id == "" {
		t.Fatalf("persistent Register() = %q, %v", id, err)
	}
	attempts, records := store.snapshot()
	if attempts != 1 || len(records) != 1 || records[0].ID != id || records[0].Active || !records[0].WatchArgs.Equivalent(watchArgs) || !records[0].DestinationArgs.Equivalent(destinationArgs) {
		t.Fatalf("committed records = attempts %d, %+v", attempts, records)
	}
	service.mu.Lock()
	active := service.registrations[id].Active
	service.mu.Unlock()
	if !active {
		t.Fatal("registration was not active after commit")
	}
	duplicate, err := service.Register("P", watchArgs, "O", destinationArgs)
	if err != nil || duplicate != id {
		t.Fatalf("duplicate persistent registration = %q, %v", duplicate, err)
	}
	attempts, records = store.snapshot()
	if attempts != 1 || len(records) != 1 {
		t.Fatalf("duplicate registration committed again: attempts=%d records=%d", attempts, len(records))
	}
	if inputValidations, inputWatches, _ := input.counts(); inputValidations != 0 || inputWatches != 1 {
		t.Fatalf("duplicate repeated a plugin operation: input watch count=%d", inputWatches)
	}

	input.fail(errors.New("input exited after saved route"))
	if !waitForCondition(time.Second, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		_, exists := service.registrations[id]
		return !exists
	}) {
		t.Fatal("input failure did not clear its live route")
	}
	service.mu.Lock()
	count := service.registrationCountLocked()
	_, retained := service.retained[id]
	service.mu.Unlock()
	if !retained || count != 1 {
		t.Fatalf("saved registration was lost from retained capacity: retained=%v count=%d", retained, count)
	}
}

func TestPersistentSaveFailureIsRegistrationErrorAndLeavesInputHealthy(t *testing.T) {
	store := &testRegistrationStore{failure: errors.New("commit /state/registrations/id.json: injected failure")}
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := newPersistentHub(t, store, input, output)
	watchArgs, destinationArgs := testArgs(t, `{"trigger":"x"}`), testArgs(t, `{}`)
	if id, err := service.Register("P", watchArgs, "O", destinationArgs); id != "" || err == nil || err.Code != "storage_error" {
		t.Fatalf("save failure returned ID %q and error %v", id, err)
	}
	service.mu.Lock()
	var tombstone domain.Subscription
	for _, registration := range service.registrations {
		tombstone = registration
	}
	inputAvailable := service.plugins["P"].available
	count := service.registrationCountLocked()
	service.mu.Unlock()
	if tombstone.ID == "" || tombstone.Active || !inputAvailable || count != 1 {
		t.Fatalf("failed save state = %+v, input available=%v, count=%d", tombstone, inputAvailable, count)
	}
	if _, err := service.Register("P", watchArgs, "O", destinationArgs); err == nil || err.Code != "storage_error" {
		t.Fatalf("same accepted unsaved watch did not retain its failure: %v", err)
	}
	if _, watches, deliveries := input.counts(); watches != 1 || deliveries != 0 {
		t.Fatalf("input watches=%d, deliveries=%d after save failure", watches, deliveries)
	}
	input.emit(string(tombstone.ID), "discard unsaved route")
	if _, _, deliveries := output.counts(); deliveries != 0 {
		t.Fatalf("unsaved route delivered %d events", deliveries)
	}
	if attempts, records := store.snapshot(); attempts != 1 || len(records) != 0 {
		t.Fatalf("failed save snapshot: attempts=%d committed=%d", attempts, len(records))
	}
}

func TestShutdownSerializesWithRegistrationCommit(t *testing.T) {
	t.Run("commit already in progress is retained", func(t *testing.T) {
		store := &testRegistrationStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
		input, output := newFakePlugin("P", domain.InputRole), newFakePlugin("O", domain.OutputRole)
		service := newPersistentHub(t, store, input, output)
		result := make(chan struct {
			id  domain.SubscriptionID
			err *Error
		}, 1)
		go func() {
			id, err := service.Register("P", testArgs(t, `{}`), "O", testArgs(t, `{}`))
			result <- struct {
				id  domain.SubscriptionID
				err *Error
			}{id: id, err: err}
		}()
		select {
		case <-store.entered:
		case <-time.After(time.Second):
			t.Fatal("registration did not reach storage commit")
		}
		shutdownStarted, shutdownDone := make(chan struct{}), make(chan struct{})
		go func() {
			close(shutdownStarted)
			service.Shutdown()
			close(shutdownDone)
		}()
		<-shutdownStarted
		close(store.release)
		select {
		case got := <-result:
			if got.err != nil || got.id == "" {
				t.Fatalf("commit-winning registration = %q, %v", got.id, got.err)
			}
		case <-time.After(time.Second):
			t.Fatal("registration did not finish after commit")
		}
		select {
		case <-shutdownDone:
		case <-time.After(time.Second):
			t.Fatal("shutdown did not follow the serialized commit")
		}
		if attempts, records := store.snapshot(); attempts != 1 || len(records) != 1 {
			t.Fatalf("commit winner was not retained: attempts=%d records=%d", attempts, len(records))
		}
		service.StopPlugins(context.Background())
	})

	t.Run("shutdown before acceptance cancels without saving", func(t *testing.T) {
		store := &testRegistrationStore{}
		input, output := newFakePlugin("P", domain.InputRole), newFakePlugin("O", domain.OutputRole)
		watchStarted, releaseWatch := make(chan struct{}), make(chan struct{})
		input.watch = func(_ WatchRequest, activate func() error) error {
			close(watchStarted)
			<-releaseWatch
			return activate()
		}
		service := newPersistentHub(t, store, input, output)
		result := make(chan *Error, 1)
		go func() {
			_, err := service.Register("P", testArgs(t, `{}`), "O", testArgs(t, `{}`))
			result <- err
		}()
		select {
		case <-watchStarted:
		case <-time.After(time.Second):
			t.Fatal("watch did not start")
		}
		service.Shutdown()
		close(releaseWatch)
		select {
		case err := <-result:
			if err == nil || err.Code != "plugin_unavailable" {
				t.Fatalf("cancelled registration error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled registration did not finish")
		}
		if attempts, records := store.snapshot(); attempts != 0 || len(records) != 0 {
			t.Fatalf("cancelled registration was saved: attempts=%d records=%d", attempts, len(records))
		}
		service.StopPlugins(context.Background())
	})
}

func TestRestoreKeepsAdmissionClosedUntilEverySavedRouteSucceeds(t *testing.T) {
	store := &testRegistrationStore{}
	input := newFakePlugin("P", domain.InputRole)
	input.emitOnAccept = true
	firstOutput := newFakePlugin("O", domain.OutputRole)
	secondOutput := newFakePlugin("R", domain.OutputRole)
	secondOutput.validate = func(DestinationRequest) error {
		return &DestinationError{Code: "destination_rejected", Message: "fixture rejected destination"}
	}
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range []*fakePlugin{input, firstOutput, secondOutput} {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	first := domain.Subscription{
		ID: "11111111-1111-4111-8111-111111111111", InputPlugin: "P", OutputPlugin: "O",
		WatchArgs: testArgs(t, `{"route":1}`), DestinationArgs: testArgs(t, `{}`),
	}
	second := domain.Subscription{
		ID: "22222222-2222-4222-8222-222222222222", InputPlugin: "P", OutputPlugin: "R",
		WatchArgs: testArgs(t, `{"route":2}`), DestinationArgs: testArgs(t, `{}`),
	}
	if err := service.PrepareRestorations([]domain.Subscription{first, second}); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	if err := service.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "destination validation failed") {
		t.Fatalf("Restore() error = %v", err)
	}
	service.mu.Lock()
	open, registrations := service.admissionOpen, len(service.registrations)
	service.mu.Unlock()
	if open || registrations != 0 {
		t.Fatalf("partial restoration opened admission: open=%v registrations=%d", open, registrations)
	}
	input.emit(string(first.ID), "event before full restore")
	if _, _, deliveries := firstOutput.counts(); deliveries != 0 {
		t.Fatalf("staged route delivered before full restore: %d", deliveries)
	}
	service.Shutdown()
	service.StopPlugins(context.Background())
}

func TestRestoreCancellationBeforeAdmissionKeepsGateClosed(t *testing.T) {
	store := &testRegistrationStore{}
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := NewWithRegistrationStore(slog.New(slog.NewTextHandler(io.Discard, nil)), store)
	for _, plugin := range []*fakePlugin{input, output} {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	registration := domain.Subscription{
		ID: "11111111-1111-4111-8111-111111111111", InputPlugin: "P", OutputPlugin: "O",
		WatchArgs: testArgs(t, `{"route":1}`), DestinationArgs: testArgs(t, `{}`),
	}
	if err := service.PrepareRestorations([]domain.Subscription{registration}); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input.watch = func(WatchRequest, func() error) error {
		cancel()
		return nil
	}
	if err := service.Restore(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore() error = %v; want context cancellation", err)
	}
	service.mu.Lock()
	open, registrations := service.admissionOpen, len(service.registrations)
	service.mu.Unlock()
	if open || registrations != 0 {
		t.Fatalf("cancelled restoration opened admission: open=%v registrations=%d", open, registrations)
	}
	input.emit(string(registration.ID), "event after cancelled restore")
	if _, _, deliveries := output.counts(); deliveries != 0 {
		t.Fatalf("cancelled restoration delivered events: %d", deliveries)
	}
	service.Shutdown()
	service.StopPlugins(context.Background())
}

func waitForCondition(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return predicate()
}

func TestDestinationRejectionSendsNoWatch(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	output.validate = func(DestinationRequest) error {
		return &DestinationError{Code: "destination_rejected", Message: "bad path"}
	}
	service := newHub(t, input, output)
	_, err := service.Register("P", testArgs(t, `{}`), "O", testArgs(t, `{}`))
	if err == nil || err.Code != "destination_rejected" {
		t.Fatalf("Register error = %v", err)
	}
	if _, watches, _ := input.counts(); watches != 0 {
		t.Fatalf("watch count = %d after destination rejection", watches)
	}
}

func TestOutputFailureDuringIssuedWatchLeavesInactiveTombstone(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	watchStarted := make(chan struct{})
	releaseWatch := make(chan struct{})
	input.watch = func(_ WatchRequest, activate func() error) error {
		close(watchStarted)
		<-releaseWatch
		return activate()
	}
	service := newHub(t, input, output)
	result := make(chan *Error, 1)
	watchArgs := testArgs(t, `{"trigger":"x"}`)
	destinationArgs := testArgs(t, `{}`)
	go func() {
		_, err := service.Register("P", watchArgs, "O", destinationArgs)
		result <- err
	}()
	select {
	case <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("watch did not start")
	}
	output.fail(errors.New("output exited"))
	close(releaseWatch)
	if err := <-result; err == nil || err.Code != "plugin_unavailable" {
		t.Fatalf("registration result = %v", err)
	}
	service.mu.Lock()
	var tombstone domain.Subscription
	for _, registration := range service.registrations {
		tombstone = registration
	}
	service.mu.Unlock()
	if tombstone.ID == "" || tombstone.Active {
		t.Fatalf("inactive registration = %+v", tombstone)
	}
	input.emit(string(tombstone.ID), "discard me")
	if _, _, deliveries := output.counts(); deliveries != 0 {
		t.Fatalf("inactive route delivered %d events", deliveries)
	}
}

func TestInputFailureAfterOutputFailureCompletesPendingWatchOnlyOnce(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	watchStarted := make(chan struct{})
	releaseWatch := make(chan struct{})
	input.watch = func(_ WatchRequest, activate func() error) error {
		close(watchStarted)
		<-releaseWatch
		return activate()
	}
	service := newHub(t, input, output)
	result := make(chan *Error, 1)
	go func() {
		_, err := service.Register("P", testArgs(t, `{"trigger":"x"}`), "O", testArgs(t, `{}`))
		result <- err
	}()
	select {
	case <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("watch did not start")
	}

	output.fail(errors.New("output exited during watch"))
	input.fail(errors.New("input exited during watch"))
	close(releaseWatch)
	select {
	case err := <-result:
		if err == nil || err.Code != "plugin_unavailable" {
			t.Fatalf("registration result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("registration did not finish after both plugin failures")
	}
}

func TestPluginFailuresAreIsolatedAndEventsStayBoundToTheirInput(t *testing.T) {
	p := newFakePlugin("P", domain.InputRole)
	q := newFakePlugin("Q", domain.InputRole)
	o := newFakePlugin("O", domain.OutputRole)
	r := newFakePlugin("R", domain.OutputRole)
	service := newHub(t, p, q, o, r)
	pID := register(t, service, "P", "O", `{}`, `{"dest":"A"}`)
	qID := register(t, service, "Q", "R", `{}`, `{"dest":"B"}`)
	p.emit(string(qID), "foreign")
	p.emit(string(pID), "PULSE-P")
	if event := waitForDelivery(t, o); event.InputPlugin != "P" || event.Context != "PULSE-P" || event.SubscriptionID != pID {
		t.Fatalf("P delivery = %+v", event)
	}
	o.fail(errors.New("output O exited"))
	p.emit(string(pID), "O is unavailable")
	q.emit(string(qID), "PULSE-Q")
	if event := waitForDelivery(t, r); event.InputPlugin != "Q" || event.Context != "PULSE-Q" {
		t.Fatalf("Q route did not survive O failure: %+v", event)
	}
	p.fail(errors.New("input P exited"))
	if _, err := service.Register("P", testArgs(t, `{"new":true}`), "O", testArgs(t, `{}`)); err == nil || err.Code != "plugin_unavailable" {
		t.Fatalf("registration through failed P = %v", err)
	}
	q.emit(string(qID), "still alive")
	deadline := time.After(time.Second)
	for {
		_, _, count := r.counts()
		if count >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("unaffected Q->R route stopped")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestAdmittedEventSurvivesInputExitAndOutputResultIsNotRetried(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	output.deliverStarted = make(chan domain.Event, 1)
	releaseDelivery := make(chan struct{})
	output.deliver = func(domain.Event) DeliveryResult {
		<-releaseDelivery
		return DeliveryResult{Outcome: domain.DeliveryUnknown, Detail: "no confirmation"}
	}
	service := newHub(t, input, output)
	id := register(t, service, "P", "O", `{}`, `{}`)
	input.emit(string(id), "admitted")
	select {
	case event := <-output.deliverStarted:
		input.fail(errors.New("input exited after admission"))
		close(releaseDelivery)
		if event.Context != "admitted" {
			t.Fatalf("admitted event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not begin")
	}
	deadline := time.After(time.Second)
	for {
		_, _, count := output.counts()
		if count == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("delivery did not finish")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, _, count := output.counts(); count != 1 {
		t.Fatalf("delivery attempts = %d, want one", count)
	}
}

func TestRegistrationPayloadBoundRejectsBeforeEitherPluginOperation(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := newHub(t, input, output)
	large := jsonvalue.NewObject(map[string]*jsonvalue.Value{"data": jsonvalue.NewString(strings.Repeat("x", MaxPluginPayload))})
	_, err := service.Register("P", large, "O", jsonvalue.NewObject(nil))
	if err == nil || err.Code != "resource_exhausted" {
		t.Fatalf("oversize registration error = %v", err)
	}
	if validations, watches, _ := output.counts(); validations != 0 || watches != 0 {
		t.Fatalf("oversize request performed plugin work: validation=%d watch=%d", validations, watches)
	}
	if _, watches, _ := input.counts(); watches != 0 {
		t.Fatalf("oversize request sent %d watches", watches)
	}
}

func TestRegistrationCapacityIncludesRetainedSubscriptions(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := newHub(t, input, output)
	for i := 0; i < MaxRegistrations; i++ {
		args := jsonvalue.NewObject(map[string]*jsonvalue.Value{"n": jsonvalue.NewNumber(fmt.Sprint(i))})
		if _, err := service.Register("P", args, "O", jsonvalue.NewObject(nil)); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	args := jsonvalue.NewObject(map[string]*jsonvalue.Value{"n": jsonvalue.NewNumber("1024")})
	if _, err := service.Register("P", args, "O", jsonvalue.NewObject(nil)); err == nil || err.Code != "resource_exhausted" {
		t.Fatalf("registration beyond capacity = %v", err)
	}
	if _, watches, _ := input.counts(); watches != MaxRegistrations {
		t.Fatalf("watch count = %d; want %d", watches, MaxRegistrations)
	}
}

func TestRegisterDistinguishesUnknownAndWrongRolePlugins(t *testing.T) {
	input := newFakePlugin("P", domain.InputRole)
	output := newFakePlugin("O", domain.OutputRole)
	service := newHub(t, input, output)
	for _, test := range []struct{ input, output, want string }{
		{"unknown", "O", "unknown_plugin"},
		{"O", "O", "wrong_role"},
		{"P", "missing", "unknown_plugin"},
		{"P", "P", "wrong_role"},
	} {
		if _, err := service.Register(test.input, jsonvalue.NewObject(nil), test.output, jsonvalue.NewObject(nil)); err == nil || err.Code != test.want {
			t.Errorf("Register(%q,%q) error = %v, want %s", test.input, test.output, err, test.want)
		}
	}
}

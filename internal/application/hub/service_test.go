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

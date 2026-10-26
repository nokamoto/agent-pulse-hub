package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

type activationLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *activationLog) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *activationLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func newActivationTestHub(t *testing.T, store RegistrationStore, saved []domain.Subscription, plugins ...*fakePlugin) (*Hub, *activationLog) {
	t.Helper()
	logs := &activationLog{}
	service := NewWithRegistrationStore(slog.New(slog.NewJSONHandler(logs, nil)), store)
	for _, plugin := range plugins {
		if err := service.AddPlugin(plugin.name, plugin.role, plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.PrepareRestorations(saved); err != nil {
		t.Fatal(err)
	}
	service.StartPlugins(context.Background())
	t.Cleanup(func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	return service, logs
}

func awaitActivation[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("activation coordination did not complete")
		var zero T
		return zero
	}
}

func activationSavedRoutes(t *testing.T, inputName string) []domain.Subscription {
	t.Helper()
	return []domain.Subscription{
		{ID: "11111111-1111-4111-8111-111111111111", InputPlugin: inputName, OutputPlugin: "sink", WatchArgs: testArgs(t, `{"route":1}`), DestinationArgs: testArgs(t, `{}`)},
		{ID: "22222222-2222-4222-8222-222222222222", InputPlugin: inputName, OutputPlugin: "sink", WatchArgs: testArgs(t, `{"route":2}`), DestinationArgs: testArgs(t, `{}`)},
	}
}

func TestV2RestoreGrantsOnlyAfterCompleteCommitAndReadiness(t *testing.T) {
	for _, outcome := range []string{"accepted", "rejected", "stop"} {
		t.Run(outcome, func(t *testing.T) {
			store := newTestRuntimeStore()
			input := newFakePlugin("cursor", domain.InputRole)
			input.capabilities = []string{"runtime_state_v2"}
			output := newFakePlugin("sink", domain.OutputRole)
			saved := activationSavedRoutes(t, input.name)
			store.states[saved[0].ID] = hubTestState(t, `{"schema":"v1","payload":{"confirmed":["U1"]}}`)
			service, logs := newActivationTestHub(t, store, saved, input, output)
			laterPrepared := make(chan struct{})
			releaseLater := make(chan struct{})
			input.watch = func(request WatchRequest, accepted func() error) error {
				if !request.RuntimeState {
					return errors.New("v2 input received ordinary watch")
				}
				if request.SubscriptionID == saved[0].ID {
					if request.State == nil || !request.State.Equivalent(store.states[saved[0].ID]) {
						return errors.New("saved state did not survive v2 negotiation")
					}
				} else {
					close(laterPrepared)
					<-releaseLater
					if outcome == "rejected" {
						return &WatchError{Code: "watch_rejected", Message: "later watch rejected"}
					}
				}
				return accepted()
			}
			grants := make(chan error, 2)
			input.activateWatch = func(_ context.Context, id domain.SubscriptionID) error {
				service.mu.Lock()
				committed := service.admissionOpen && len(service.registrations) == 2 && len(service.runtimeWatches) == 2
				service.mu.Unlock()
				if !committed || !strings.Contains(logs.String(), `"msg":"daemon_ready"`) {
					grants <- fmt.Errorf("grant %s preceded complete startup commit/readiness", id)
				} else {
					grants <- nil
				}
				return nil
			}
			result := make(chan error, 1)
			go func() { result <- service.Restore(context.Background()) }()
			awaitActivation(t, laterPrepared)
			input.mu.Lock()
			premature := len(input.activationRequests)
			input.mu.Unlock()
			if premature != 0 || strings.Contains(logs.String(), `"msg":"daemon_ready"`) {
				t.Fatal("staged restoration emitted a grant or readiness")
			}
			if status := input.emitState(string(saved[0].ID), "1", "early"); status != "discarded" {
				t.Fatalf("staged route accepted event: %s", status)
			}
			if outcome == "stop" {
				service.Shutdown()
			}
			close(releaseLater)
			err := awaitActivation(t, result)
			if outcome == "accepted" {
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := awaitActivation(t, grants); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err == nil || len(grants) != 0 || strings.Contains(logs.String(), `"msg":"daemon_ready"`) {
					t.Fatalf("failed precommit startup: error=%v grants=%d logs=%s", err, len(grants), logs.String())
				}
			}
		})
	}
}

type activationCommitStore struct {
	*testRuntimeStore
	commit func(domain.Subscription) error
}

func (s *activationCommitStore) Commit(registration domain.Subscription) error {
	return s.commit(registration)
}

func TestV2RegistrationCommitFailureStaysDormantAndDuplicateSendsOneGrant(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(storageFailure), func(t *testing.T) {
			input := newFakePlugin("cursor", domain.InputRole)
			input.capabilities = []string{"runtime_state_v2"}
			output := newFakePlugin("sink", domain.OutputRole)
			entered, release := make(chan struct{}), make(chan struct{})
			store := &activationCommitStore{testRuntimeStore: newTestRuntimeStore()}
			store.commit = func(registration domain.Subscription) error {
				close(entered)
				<-release
				if storageFailure {
					return errors.New("injected registration save failure")
				}
				return store.testRuntimeStore.Commit(registration)
			}
			service, _ := newActivationTestHub(t, store, nil, input, output)
			if err := service.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			granted := make(chan domain.SubscriptionID, 1)
			input.activateWatch = func(_ context.Context, id domain.SubscriptionID) error { granted <- id; return nil }
			type registrationResult struct {
				id  domain.SubscriptionID
				err *Error
			}
			result := make(chan registrationResult, 1)
			go func() {
				id, err := service.Register(input.name, testArgs(t, `{}`), output.name, testArgs(t, `{}`))
				result <- registrationResult{id, err}
			}()
			awaitActivation(t, entered)
			input.mu.Lock()
			premature := len(input.activationRequests)
			input.mu.Unlock()
			if premature != 0 {
				t.Fatal("grant preceded registration commit")
			}
			close(release)
			registered := awaitActivation(t, result)
			if storageFailure {
				if registered.err == nil || registered.err.Code != "storage_error" || len(granted) != 0 {
					t.Fatalf("failed commit activated: %+v grants=%d", registered, len(granted))
				}
				return
			}
			if registered.err != nil || awaitActivation(t, granted) != registered.id {
				t.Fatalf("committed route activation = %+v", registered)
			}
			duplicate, err := service.Register(input.name, testArgs(t, `{}`), output.name, testArgs(t, `{}`))
			if err != nil || duplicate != registered.id {
				t.Fatalf("equivalent active registration = %s, %v", duplicate, err)
			}
			input.mu.Lock()
			count := len(input.activationRequests)
			input.mu.Unlock()
			if count != 1 {
				t.Fatalf("duplicate active registration dispatched %d grants", count)
			}
		})
	}
}

func TestActivationBatchesShareDeadlineAndDifferentInputsDispatchConcurrently(t *testing.T) {
	blocked := newFakePlugin("blocked", domain.InputRole)
	healthy := newFakePlugin("healthy", domain.InputRole)
	for _, input := range []*fakePlugin{blocked, healthy} {
		input.capabilities = []string{"runtime_state_v2"}
	}
	output := newFakePlugin("sink", domain.OutputRole)
	service, _ := newActivationTestHub(t, newTestRuntimeStore(), nil, blocked, healthy, output)
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadlines := make(chan time.Time, 2)
	release := make(chan struct{})
	blocked.activateWatch = func(ctx context.Context, _ domain.SubscriptionID) error {
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	healthyGrant := make(chan struct{}, 1)
	healthy.activateWatch = func(context.Context, domain.SubscriptionID) error { healthyGrant <- struct{}{}; return nil }
	register(t, service, blocked.name, output.name, `{"route":1}`, `{}`)
	firstDeadline := awaitActivation(t, deadlines)
	register(t, service, blocked.name, output.name, `{"route":2}`, `{}`)
	register(t, service, healthy.name, output.name, `{}`, `{}`)
	awaitActivation(t, healthyGrant)
	close(release)
	if secondDeadline := awaitActivation(t, deadlines); !secondDeadline.Equal(firstDeadline) {
		t.Fatalf("queued grant reset batch deadline: first=%s second=%s", firstDeadline, secondDeadline)
	}
}

func TestActivationFailureAfterCommitRetainsRecordsAndHealthyRoutes(t *testing.T) {
	bad := newFakePlugin("bad", domain.InputRole)
	healthy := newFakePlugin("healthy", domain.InputRole)
	for _, input := range []*fakePlugin{bad, healthy} {
		input.capabilities = []string{"runtime_state_v2"}
	}
	output := newFakePlugin("sink", domain.OutputRole)
	store := newTestRuntimeStore()
	service, logs := newActivationTestHub(t, store, nil, bad, healthy, output)
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, releaseFailure := make(chan struct{}), make(chan struct{})
	bad.activateWatch = func(context.Context, domain.SubscriptionID) error {
		close(started)
		<-releaseFailure
		return errors.New("injected grant send failure")
	}
	healthyGrant := make(chan struct{}, 1)
	healthy.activateWatch = func(context.Context, domain.SubscriptionID) error { healthyGrant <- struct{}{}; return nil }
	badID := register(t, service, bad.name, output.name, `{"route":1}`, `{}`)
	awaitActivation(t, started)
	queuedID := register(t, service, bad.name, output.name, `{"route":2}`, `{}`)
	healthyID := register(t, service, healthy.name, output.name, `{}`, `{}`)
	awaitActivation(t, healthyGrant)
	close(releaseFailure)
	waitRuntimeTest(t, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		return !service.plugins[bad.name].available
	})
	service.mu.Lock()
	_, retained := service.retained[badID]
	_, queuedRetained := service.retained[queuedID]
	open := service.admissionOpen
	active := service.registrations[healthyID].Active
	service.mu.Unlock()
	store.mu.Lock()
	saved := len(store.registrations)
	store.mu.Unlock()
	bad.mu.Lock()
	badGrants := len(bad.activationRequests)
	bad.mu.Unlock()
	if !retained || !queuedRetained || !open || !active || saved != 3 || badGrants != 1 || strings.Count(logs.String(), `"msg":"daemon_ready"`) != 1 {
		t.Fatalf("postcommit failure rolled back state: retained=%v admission=%v healthy=%v saved=%d", retained, open, active, saved)
	}
	if got := healthy.emitState(string(healthyID), "1", "fresh"); got != "" {
		t.Fatalf("healthy route rejected after other input failure: %s", got)
	}
}

func TestStopCancelsPendingActivationBatchWithoutSendingRemainingGrant(t *testing.T) {
	input := newFakePlugin("cursor", domain.InputRole)
	input.capabilities = []string{"runtime_state_v2"}
	output := newFakePlugin("sink", domain.OutputRole)
	service, _ := newActivationTestHub(t, newTestRuntimeStore(), nil, input, output)
	if err := service.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	input.activateWatch = func(ctx context.Context, _ domain.SubscriptionID) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	register(t, service, input.name, output.name, `{"route":1}`, `{}`)
	awaitActivation(t, started)
	register(t, service, input.name, output.name, `{"route":2}`, `{}`)
	service.Shutdown()
	input.mu.Lock()
	count := len(input.activationRequests)
	input.mu.Unlock()
	service.mu.Lock()
	pending := len(service.activationBatches)
	service.mu.Unlock()
	if count != 1 || pending != 0 {
		t.Fatalf("stop did not remove unsent grants: dispatched=%d batches=%d", count, pending)
	}
}

package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func TestRegistrationRejectsInvalidIdentityAndUnavailablePlugin(t *testing.T) {
	for _, test := range []struct {
		name, plugin, session, code string
		args                        *jsonvalue.Value
		configure                   bool
		startupErr                  error
	}{
		{name: "empty plugin", session: testSessionID, args: jsonvalue.NewObject(nil), code: "invalid_request"},
		{name: "null arguments", plugin: "manual", session: testSessionID, code: "invalid_request"},
		{name: "array arguments", plugin: "manual", session: testSessionID, args: jsonvalue.NewArray(), code: "invalid_request"},
		{name: "invalid session", plugin: "manual", session: "invalid", args: jsonvalue.NewObject(nil), code: "missing_session"},
		{name: "unknown plugin", plugin: "manual", session: testSessionID, args: jsonvalue.NewObject(nil), code: "unknown_plugin"},
		{name: "unavailable plugin", plugin: "manual", session: testSessionID, args: jsonvalue.NewObject(nil), configure: true, startupErr: errors.New("failed to launch"), code: "plugin_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			t.Cleanup(h.Shutdown)
			if test.configure {
				plugin := NewMockPluginPort(gomock.NewController(t))
				plugin.EXPECT().Start(gomock.Any(), gomock.Any()).Return(test.startupErr)
				plugin.EXPECT().Stop(gomock.Any()).Return(nil).AnyTimes()
				if err := h.AddPlugin("manual", plugin); err != nil {
					t.Fatal(err)
				}
				h.StartPlugins(context.Background())
			}
			id, err := h.Register(test.plugin, test.session, test.args)
			if id != "" || err == nil || err.Code != test.code {
				t.Fatalf("id %q error %v want %s", id, err, test.code)
			}
		})
	}
}

func TestShutdownDuringPendingWatchReturnsFailureWithoutOrphan(t *testing.T) {
	ctrl := gomock.NewController(t)
	plugin := NewMockPluginPort(ctrl)
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(h.Shutdown)
	plugin.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, handlers PluginHandlers) error { handlers.Ready(); return nil })
	started := make(chan struct{})
	release := make(chan struct{})
	plugin.EXPECT().Watch(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ WatchRequest, activate func() error) error {
		close(started)
		<-release
		if err := activate(); err == nil {
			t.Error("shutdown pending watch activated")
		}
		return &WatchError{Unavailable: true, Code: "plugin_unavailable"}
	})
	plugin.EXPECT().Stop(gomock.Any()).Return(nil).AnyTimes()
	if err := h.AddPlugin("manual", plugin); err != nil {
		t.Fatal(err)
	}
	h.StartPlugins(context.Background())
	result := make(chan *Error, 1)
	go func() { _, err := h.Register("manual", testSessionID, jsonvalue.NewObject(nil)); result <- err }()
	<-started
	h.Shutdown()
	close(release)
	if err := <-result; err == nil || err.Code != "plugin_unavailable" {
		t.Fatalf("result %v", err)
	}
	if len(h.registrations) != 0 || len(h.pendingByKey) != 0 {
		t.Fatal("shutdown preserved pending registration")
	}
}

func TestPendingRegistrationsCountTowardCapacity(t *testing.T) {
	plugin := &fakePlugin{}
	h := newTestHub(t, plugin, &fakeDelivery{results: make(chan domain.Event, 1)})
	for index := 0; index < MaxRegistrations; index++ {
		h.pendingByKey[string(rune(index))] = &pendingRegistration{done: make(chan struct{})}
	}
	_, err := h.Register("manual", testSessionID, jsonvalue.NewObject(nil))
	if err == nil || err.Code != "resource_exhausted" || plugin.watchCalls != 0 {
		t.Fatalf("error %v watches %d", err, plugin.watchCalls)
	}
}

func TestPartialStartupLeavesHealthyPluginAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	failed, healthy := NewMockPluginPort(ctrl), NewMockPluginPort(ctrl)
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h.Shutdown()
	failed.EXPECT().Start(gomock.Any(), gomock.Any()).Return(errors.New("startup failed"))
	failed.EXPECT().Stop(gomock.Any()).Return(nil).AnyTimes()
	healthy.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, handlers PluginHandlers) error { handlers.Ready(); return nil })
	healthy.EXPECT().Watch(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ WatchRequest, activate func() error) error { return activate() })
	if err := h.AddPlugin("failed", failed); err != nil {
		t.Fatal(err)
	}
	if err := h.AddPlugin("healthy", healthy); err != nil {
		t.Fatal(err)
	}
	h.StartPlugins(context.Background())
	if _, err := h.Register("failed", testSessionID, jsonvalue.NewObject(nil)); err == nil || err.Code != "plugin_unavailable" {
		t.Fatalf("failed plugin %v", err)
	}
	if id, err := h.Register("healthy", testSessionID, jsonvalue.NewObject(nil)); err != nil || id == "" {
		t.Fatalf("healthy ID %q error %v", id, err)
	}
}

func TestAdmittedEventsKeepDestinationAfterPluginFailureWithoutRetry(t *testing.T) {
	for _, outcome := range []domain.DeliveryOutcome{domain.DeliveryAccepted, domain.DeliveryFailed, domain.DeliveryUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			plugin, delivery := NewMockPluginPort(ctrl), NewMockDeliveryPort(ctrl)
			h := New(delivery, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var handlers PluginHandlers
			plugin.EXPECT().Start(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, value PluginHandlers) error { handlers = value; value.Ready(); return nil })
			plugin.EXPECT().Watch(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ WatchRequest, activate func() error) error { return activate() })
			plugin.EXPECT().Stop(gomock.Any()).Return(nil).AnyTimes()
			started, release := make(chan struct{}), make(chan struct{})
			calls := make(chan domain.Event, 2)
			count := 0
			delivery.EXPECT().Deliver(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, event domain.Event) DeliveryResult {
				count++
				if count == 1 {
					close(started)
					<-release
				}
				calls <- event
				return DeliveryResult{Outcome: outcome}
			}).Times(2)
			if err := h.AddPlugin("manual", plugin); err != nil {
				t.Fatal(err)
			}
			h.StartPlugins(context.Background())
			id, err := h.Register("manual", testSessionID, jsonvalue.NewObject(nil))
			if err != nil {
				t.Fatal(err)
			}
			handlers.Event(string(id), "first")
			<-started
			handlers.Event(string(id), "second")
			handlers.Unavailable(errors.New("exited"))
			handlers.Event(string(id), "late")
			close(release)
			first, second := <-calls, <-calls
			h.Shutdown()
			if first.Context != "first" || second.Context != "second" || first.SessionID != testSessionID || second.SessionID != testSessionID || first.ID == second.ID {
				t.Fatalf("captured events %#v %#v", first, second)
			}
		})
	}
}

//go:build windows

package controlpipe

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsidentity"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const controlPipeTestSessionID = "11111111-1111-4111-8111-111111111111"

func TestServerSameUserRegistrationSecondDaemonAndLostResponse(t *testing.T) {
	plugin := &controlPipeTestPlugin{
		watchStarted: make(chan hub.WatchRequest, 2),
		releaseWatch: make(chan struct{}),
		active:       make(chan string, 2),
	}
	service := hub.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

	server, err := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancelServe()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("serve control pipe: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("control server did not stop")
		}
	})

	secondServer, err := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		_ = secondServer.Close()
		t.Fatal("second daemon acquired the active user's control pipe")
	}

	sid, err := windowsidentity.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	watchArgs := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"marker": jsonvalue.NewString("response-loss"),
	})

	connection, err := winio.DialPipeContext(context.Background(), PipeName(sid))
	if err != nil {
		t.Fatalf("connect as current user: %v", err)
	}
	request, err := protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":    jsonvalue.NewNumber("1"),
		"op":         jsonvalue.NewString("register"),
		"plugin":     jsonvalue.NewString("manual"),
		"session_id": jsonvalue.NewString(controlPipeTestSessionID),
		"watch_args": watchArgs,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAll(connection, request); err != nil {
		_ = connection.Close()
		t.Fatalf("send first registration: %v", err)
	}
	var first hub.WatchRequest
	select {
	case first = <-plugin.watchStarted:
	case <-time.After(3 * time.Second):
		_ = connection.Close()
		t.Fatal("server did not begin the first registration")
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("close client before registration response: %v", err)
	}
	close(plugin.releaseWatch)
	select {
	case activeID := <-plugin.active:
		if activeID != string(first.SubscriptionID) {
			t.Fatalf("activated ID = %q, want %q", activeID, first.SubscriptionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not commit registration after client response loss")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, _, err := register(ctx, "manual", controlPipeTestSessionID, watchArgs, PipeName(sid), sid)
	if err != nil {
		t.Fatalf("repeat same-user registration after lost response: %v", err)
	}
	if !response.Success || response.SubscriptionID != string(first.SubscriptionID) {
		t.Fatalf("re-registration response = %#v, want original subscription %q", response, first.SubscriptionID)
	}
	if got := plugin.watchCalls.Load(); got != 1 {
		t.Fatalf("watch calls after repeat registration = %d, want 1", got)
	}
}

type controlPipeTestPlugin struct {
	watchStarted chan hub.WatchRequest
	releaseWatch chan struct{}
	active       chan string
	watchCalls   atomic.Int32
}

func (*controlPipeTestPlugin) Start(_ context.Context, handlers hub.PluginHandlers) error {
	handlers.Ready()
	return nil
}

func (p *controlPipeTestPlugin) Watch(_ context.Context, request hub.WatchRequest, activate func() error) error {
	p.watchCalls.Add(1)
	p.watchStarted <- request
	<-p.releaseWatch
	if err := activate(); err != nil {
		return err
	}
	p.active <- string(request.SubscriptionID)
	return nil
}

func (*controlPipeTestPlugin) Stop(context.Context) error { return nil }

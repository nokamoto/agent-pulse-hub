//go:build windows

package controlpipe

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
)

func TestNamedPipeAllowsCurrentUserAndRejectsSecondDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serverDone := make(chan error, 1)
	serverReady := make(chan struct{})
	go func() {
		serverDone <- Serve(ctx, logger, func(context.Context, protocol.ControlRequest) protocol.ControlResponse {
			return protocol.ControlResponse{Version: 1, OK: true, SubscriptionID: "01a11c69-4d5f-7bf1-9506-c872a3543cf4"}
		}, func() { close(serverReady) })
	}()
	select {
	case <-serverReady:
	case serveErr := <-serverDone:
		t.Fatalf("Serve() failed before readiness: %v", serveErr)
	case <-time.After(2 * time.Second):
		t.Fatal("named pipe server did not become ready")
	}
	request := protocol.ControlRequest{
		Version:   1,
		Operation: "register",
		Plugin:    "manual",
		SessionID: "01a11c69-4d5f-7bf1-9506-c872a3543cf4",
		WatchArgs: json.RawMessage(`{}`),
	}
	connectContext, stopConnect := context.WithTimeout(context.Background(), 5*time.Second)
	response, err := Register(connectContext, request)
	stopConnect()
	if err != nil {
		select {
		case serveErr := <-serverDone:
			t.Fatalf("Register() error = %v; Serve() error = %v", err, serveErr)
		default:
			t.Fatalf("Register() error = %v", err)
		}
	}
	if !response.OK || response.SubscriptionID != "01a11c69-4d5f-7bf1-9506-c872a3543cf4" {
		t.Fatalf("response = %+v", response)
	}

	secondContext, stopSecond := context.WithTimeout(context.Background(), time.Second)
	defer stopSecond()
	if err := Serve(secondContext, logger, func(context.Context, protocol.ControlRequest) protocol.ControlResponse {
		return protocol.ControlResponse{}
	}, nil); err == nil {
		t.Fatal("second daemon reserved an already-owned pipe")
	}
	cancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("named pipe server did not stop")
	}
}

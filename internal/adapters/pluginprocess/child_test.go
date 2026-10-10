package pluginprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/config"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	application "github.com/nokamoto/agent-pulse-hub/internal/application/pulse"
	domain "github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

func TestChildStartsAcceptsWatchRoutesEventAndStops(t *testing.T) {
	t.Setenv("AGENT_PULSE_PLUGIN_HELPER", "1")
	t.Setenv("AGENT_PULSE_PLUGIN_HELPER_INVALID_UTF8", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan domain.Event, 1)
	exits := make(chan string, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	child, err := Start(context.Background(), config.Plugin{
		Name:       "fixture",
		Executable: executable,
		Args:       config.StringArgs{"-test.run=TestPluginChildHelper"},
	}, logger, func(_ string, event domain.Event) bool {
		events <- event
		return true
	}, func(_ string, reason string) {
		exits <- reason
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	watchResult := child.Watch(context.Background(), domain.WatchRequest{
		RequestID:      "request-1",
		SubscriptionID: "subscription-1",
		WatchArgs:      json.RawMessage(`{}`),
	}, func() bool { return true })
	if watchResult.Err != nil || watchResult.Uncertain {
		t.Fatalf("Watch() result = %+v", watchResult)
	}
	select {
	case event := <-events:
		if event.SubscriptionID != "subscription-1" || event.Context != "ready immediately" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plugin event was not delivered")
	}
	child.Stop("test shutdown")
	if child.Available() {
		t.Fatal("child remained available after Stop")
	}
	select {
	case <-exits:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin exit was not observed")
	}
}

func TestChildWatchWaitsForAcceptanceHandlingAfterContextExpires(t *testing.T) {
	t.Setenv("AGENT_PULSE_PLUGIN_HELPER", "1")
	t.Setenv("AGENT_PULSE_PLUGIN_HELPER_INVALID_UTF8", "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	child, err := Start(context.Background(), config.Plugin{
		Name:       "fixture",
		Executable: executable,
		Args:       config.StringArgs{"-test.run=TestPluginChildHelper"},
	}, logger, func(string, domain.Event) bool { return true }, nil)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { child.Stop("test cleanup") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acceptanceStarted := make(chan struct{})
	continueAcceptance := make(chan struct{})
	watchDone := make(chan application.WatchResult, 1)
	go func() {
		watchDone <- child.Watch(ctx, domain.WatchRequest{
			RequestID:      "request-2",
			SubscriptionID: "subscription-2",
			WatchArgs:      json.RawMessage(`{}`),
		}, func() bool {
			close(acceptanceStarted)
			<-continueAcceptance
			return true
		})
	}()
	select {
	case <-acceptanceStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin acceptance callback did not start")
	}
	cancel()
	select {
	case result := <-watchDone:
		close(continueAcceptance)
		t.Fatalf("Watch() returned before acceptance handling completed: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueAcceptance)
	select {
	case result := <-watchDone:
		if result.Err != nil || result.Uncertain {
			t.Fatalf("Watch() result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Watch() did not return after acceptance handling completed")
	}
	if !child.Available() {
		t.Fatal("child became unavailable after a resolved watch acceptance")
	}
}

func TestPluginChildHelper(t *testing.T) {
	if os.Getenv("AGENT_PULSE_PLUGIN_HELPER") != "1" {
		return
	}
	if err := runHelperProtocol(os.Stdin, os.Stdout); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func runHelperProtocol(input io.Reader, output io.Writer) error {
	writer := bufio.NewWriter(output)
	send := func(value any) error {
		line, err := protocol.EncodeLine(value)
		if err != nil {
			return err
		}
		if _, err := writer.Write(line); err != nil {
			return err
		}
		return writer.Flush()
	}
	if os.Getenv("AGENT_PULSE_PLUGIN_HELPER_INVALID_UTF8") == "1" {
		if _, err := writer.Write([]byte{0xff, '\n'}); err != nil {
			return err
		}
	}
	if err := send(protocol.PluginReply{Version: 1, Type: "ready"}); err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	for {
		line, err := protocol.ReadFrame(reader, domain.MaxFrameBytes)
		if err != nil {
			return err
		}
		frame, err := protocol.DecodeDaemonFrame(line)
		if err != nil {
			return err
		}
		if frame.Type == "shutdown" {
			return nil
		}
		accepted := true
		if err := send(protocol.PluginReply{Version: 1, Type: "watch_result", RequestID: frame.RequestID, Accepted: &accepted}); err != nil {
			return err
		}
		if err := send(protocol.PluginReply{Version: 1, Type: "event", SubscriptionID: frame.SubscriptionID, Context: "ready immediately"}); err != nil {
			return err
		}
	}
}

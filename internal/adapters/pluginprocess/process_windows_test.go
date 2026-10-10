//go:build windows

package pluginprocess

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"golang.org/x/sys/windows"
)

const (
	pluginHelperEnvironment         = "AGENT_PULSE_PLUGINPROCESS_TEST_HELPER"
	pluginChildEnvironment          = "AGENT_PULSE_PLUGINPROCESS_TEST_CHILD"
	pluginChildPIDFileEnvironment   = "AGENT_PULSE_PLUGINPROCESS_TEST_CHILD_PID_FILE"
	pluginSpawnChildEnvironment     = "AGENT_PULSE_PLUGINPROCESS_TEST_SPAWN_CHILD"
	pluginSkipReadyEnvironment      = "AGENT_PULSE_PLUGINPROCESS_TEST_SKIP_READY"
	malformedFrameHelperEnvironment = "AGENT_PULSE_PLUGINPROCESS_TEST_MALFORMED_FRAME"
	oversizedFrameHelperEnvironment = "AGENT_PULSE_PLUGINPROCESS_TEST_OVERSIZED_FRAME"
)

func TestPluginProcessHelper(t *testing.T) {
	if os.Getenv(pluginChildEnvironment) == "1" {
		pidFile := os.Getenv(pluginChildPIDFileEnvironment)
		if pidFile == "" || os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			os.Exit(2)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv(pluginHelperEnvironment) != "1" {
		return
	}
	if os.Getenv(pluginSpawnChildEnvironment) == "1" {
		child := exec.Command(os.Args[0], "-test.run=^TestPluginProcessHelper$")
		child.Env = append(os.Environ(), pluginChildEnvironment+"=1", pluginChildPIDFileEnvironment+"="+os.Getenv(pluginChildPIDFileEnvironment))
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
	}
	if os.Getenv(malformedFrameHelperEnvironment) == "1" {
		_, _ = io.WriteString(os.Stdout, `{"type":"ready","extra":true}`+"\n")
	}
	if os.Getenv(oversizedFrameHelperEnvironment) == "1" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", protocol.MaxFrameBytes+1)+"\n")
	}
	if os.Getenv(pluginSkipReadyEnvironment) != "1" {
		if err := pluginprotocol.Write(os.Stdout, pluginprotocol.Frame{Type: "ready"}); err != nil {
			os.Exit(2)
		}
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := protocol.ReadFrame(reader, protocol.MaxFrameBytes)
		if err != nil {
			os.Exit(2)
		}
		value, err := protocol.ParseFrame(line)
		if err != nil {
			os.Exit(2)
		}
		frame, err := pluginprotocol.Decode(value, pluginprotocol.DaemonToPlugin)
		if err != nil {
			os.Exit(2)
		}
		switch frame.Type {
		case "watch":
			accepted := true
			if err := pluginprotocol.Write(os.Stdout, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: &accepted}); err != nil {
				os.Exit(2)
			}
			if err := pluginprotocol.Write(os.Stdout, pluginprotocol.Frame{Type: "event", SubscriptionID: frame.SubscriptionID, Context: "helper event"}); err != nil {
				os.Exit(2)
			}
		case "shutdown":
			os.Exit(0)
		default:
			os.Exit(2)
		}
	}
}

func TestNormalPluginShutdownTerminatesDescendantProcess(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(pluginHelperEnvironment, "1")
	t.Setenv(pluginSpawnChildEnvironment, "1")
	t.Setenv(pluginChildPIDFileEnvironment, childPIDFile)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process := New(Config{Name: "tree", Executable: executable, Args: []string{"-test.run=^TestPluginProcessHelper$"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := process.Start(ctx, hub.PluginHandlers{}); err != nil {
		t.Fatal(err)
	}
	childHandle := waitForChildProcess(t, childPIDFile)
	defer windows.CloseHandle(childHandle)
	if state, err := windows.WaitForSingleObject(childHandle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("descendant process state before shutdown = (%d, %v), want running", state, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer stopCancel()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(childHandle, 3_000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant process state after shutdown = (%d, %v), want terminated", state, err)
	}
}

func TestPluginStartupCancellationTerminatesDescendantProcess(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(pluginHelperEnvironment, "1")
	t.Setenv(pluginSpawnChildEnvironment, "1")
	t.Setenv(pluginSkipReadyEnvironment, "1")
	t.Setenv(pluginChildPIDFileEnvironment, childPIDFile)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process := New(Config{Name: "tree", Executable: executable, Args: []string{"-test.run=^TestPluginProcessHelper$"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- process.Start(ctx, hub.PluginHandlers{}) }()
	childHandle := waitForChildProcess(t, childPIDFile)
	defer windows.CloseHandle(childHandle)
	if state, err := windows.WaitForSingleObject(childHandle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("descendant process state before cancellation = (%d, %v), want running", state, err)
	}
	cancel()
	select {
	case err := <-started:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plugin startup did not stop after context cancellation")
	}
	if state, err := windows.WaitForSingleObject(childHandle, 3_000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant process state after cancellation = (%d, %v), want terminated", state, err)
	}
}

func waitForChildProcess(t *testing.T, pidFile string) windows.Handle {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if parseErr != nil {
				t.Fatalf("invalid child PID %q: %v", data, parseErr)
			}
			handle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if openErr != nil {
				t.Fatalf("open descendant process %d: %v", pid, openErr)
			}
			return handle
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process did not write its PID to %s", pidFile)
	return 0
}

func TestProcessProtocolLifecycle(t *testing.T) {
	t.Setenv(pluginHelperEnvironment, "1")
	t.Setenv(malformedFrameHelperEnvironment, "1")
	ready := make(chan struct{}, 1)
	events := make(chan string, 1)
	unavailable := make(chan error, 1)
	var activated atomic.Bool
	process := New(Config{
		Name:       "fixture",
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestPluginProcessHelper$"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := process.Start(ctx, hub.PluginHandlers{
		Ready:       func() { ready <- struct{}{} },
		Event:       func(_ string, contextText string) { events <- contextText },
		Unavailable: func(err error) { unavailable <- err },
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("plugin readiness callback was not invoked")
	}
	args := jsonvalue.NewObject(map[string]*jsonvalue.Value{})
	if err := process.Watch(ctx, hub.WatchRequest{
		RequestID: "request-1", SubscriptionID: domain.SubscriptionID("subscription-1"), Arguments: args,
	}, func() error {
		activated.Store(true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got != "helper event" {
			t.Fatalf("event context = %q", got)
		}
	case err := <-unavailable:
		t.Fatalf("plugin became unavailable: %v", err)
	case <-ctx.Done():
		t.Fatal("plugin event was not delivered")
	}
	if !activated.Load() {
		t.Fatal("watch activation callback did not run before event delivery")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer stopCancel()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.doneCh:
	case <-stopCtx.Done():
		t.Fatal("plugin process did not exit after shutdown")
	}
	if got := strings.TrimSpace(process.failureText()); got != "unknown process failure" && got != "" {
		t.Fatalf("graceful shutdown recorded an unexpected failure: %q", got)
	}
}

func TestIndependentPowerShellPluginLifecycle(t *testing.T) {
	executable, err := exec.LookPath("pwsh.exe")
	if err != nil {
		executable, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell is not installed")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "independent_plugin.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	events := make(chan string, 1)
	unavailable := make(chan error, 1)
	activated := make(chan struct{}, 1)
	process := New(Config{
		Name:       "powershell",
		Executable: executable,
		Args:       []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := process.Stop(stopCtx); err != nil {
			t.Errorf("stop independent PowerShell plugin: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := process.Start(ctx, hub.PluginHandlers{
		Ready:       func() { ready <- struct{}{} },
		Event:       func(_ string, contextText string) { events <- contextText },
		Unavailable: func(err error) { unavailable <- err },
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case err := <-unavailable:
		t.Fatalf("PowerShell plugin became unavailable before readiness: %v", err)
	case <-ctx.Done():
		t.Fatal("PowerShell plugin did not become ready")
	}
	if err := process.Watch(ctx, hub.WatchRequest{
		RequestID: "opaque-request-1", SubscriptionID: domain.SubscriptionID("opaque-subscription-1"), Arguments: jsonvalue.NewObject(nil),
	}, func() error {
		activated <- struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-activated:
	case <-ctx.Done():
		t.Fatal("PowerShell watch was not activated")
	}
	select {
	case got := <-events:
		if got != "independent PowerShell event" {
			t.Fatalf("PowerShell event context = %q", got)
		}
	case err := <-unavailable:
		t.Fatalf("PowerShell plugin became unavailable: %v", err)
	case <-ctx.Done():
		t.Fatal("PowerShell event was not delivered")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer stopCancel()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedPluginFrameFailsProcessWithoutDelivery(t *testing.T) {
	t.Setenv(pluginHelperEnvironment, "1")
	t.Setenv(oversizedFrameHelperEnvironment, "1")
	ready := make(chan struct{}, 1)
	events := make(chan string, 1)
	unavailable := make(chan error, 1)
	process := New(Config{
		Name:       "fixture",
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestPluginProcessHelper$"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := process.Stop(stopCtx); err != nil {
			t.Errorf("stop oversized-frame fixture: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	err := process.Start(ctx, hub.PluginHandlers{
		Ready:       func() { ready <- struct{}{} },
		Event:       func(_ string, contextText string) { events <- contextText },
		Unavailable: func(err error) { unavailable <- err },
	})
	if err == nil {
		t.Fatal("plugin process accepted an oversized frame")
	}
	select {
	case <-unavailable:
	case <-ctx.Done():
		t.Fatal("oversized frame did not mark the plugin unavailable")
	}
	select {
	case <-ready:
		t.Fatal("plugin became ready after an oversized frame")
	default:
	}
	select {
	case event := <-events:
		t.Fatalf("oversized frame caused an event: %q", event)
	default:
	}
}

func TestDrainStderrLogsBoundedSanitizedDiagnostics(t *testing.T) {
	var output bytes.Buffer
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &Process{
		config: Config{Name: "manual"},
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
	}
	diagnostic := "claimed file CLAIMED_PATH could not be read \x1b[31m" + strings.Repeat("x", maxPluginDiagnosticBytes+32) + "\n"
	if _, err := io.WriteString(writer, diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	process.drainStderr(reader)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	logged := output.String()
	if !strings.Contains(logged, "CLAIMED_PATH") || !strings.Contains(logged, `"truncated":true`) {
		t.Fatalf("plugin diagnostic was not logged with its truncation state: %s", logged)
	}
	if strings.Contains(logged, `\u001b`) || strings.Contains(logged, "\x1b") {
		t.Fatalf("plugin diagnostic retained a terminal control sequence: %s", logged)
	}
}

func TestAcceptedWatchWinsContextCancellationDuringActivation(t *testing.T) {
	t.Setenv(pluginHelperEnvironment, "1")
	events := make(chan string, 1)
	process := New(Config{
		Name:       "fixture",
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestPluginProcessHelper$"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	processCtx, cancelProcess := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancelProcess()
	if err := process.Start(processCtx, hub.PluginHandlers{
		Event: func(_ string, contextText string) { events <- contextText },
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := process.Stop(stopCtx); err != nil {
			t.Errorf("stop fixture process: %v", err)
		}
	}()

	watchCtx, cancelWatch := context.WithCancel(processCtx)
	defer cancelWatch()
	activationStarted := make(chan struct{})
	releaseActivation := make(chan struct{})
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- process.Watch(watchCtx, hub.WatchRequest{
			RequestID: "request-race", SubscriptionID: domain.SubscriptionID("subscription-race"),
			Arguments: jsonvalue.NewObject(map[string]*jsonvalue.Value{}),
		}, func() error {
			close(activationStarted)
			cancelWatch()
			<-releaseActivation
			return nil
		})
	}()
	select {
	case <-activationStarted:
	case <-processCtx.Done():
		t.Fatal("plugin did not accept the watch")
	}
	close(releaseActivation)
	select {
	case err := <-watchResult:
		if err != nil {
			t.Fatalf("accepted watch was lost to context cancellation: %v", err)
		}
	case <-processCtx.Done():
		t.Fatal("watch did not return after acceptance")
	}
	select {
	case contextText := <-events:
		if contextText != "helper event" {
			t.Fatalf("event context = %q", contextText)
		}
	case <-processCtx.Done():
		t.Fatal("accepted watch event was not delivered")
	}
}

func TestInterruptedAcceptedWatchWaitsForOutcomePastDeadline(t *testing.T) {
	process := &Process{
		pending: make(map[string]*watchPending),
		doneCh:  make(chan struct{}),
	}
	pending := &watchPending{
		deadline: time.Now().Add(-time.Second),
		result:   make(chan watchOutcome),
	}
	watchResult := make(chan error, 1)
	go func() {
		watchResult <- process.interruptWatch("request-accepted", pending, context.Canceled, "interrupted")
	}()
	select {
	case pending.result <- watchOutcome{accepted: true}:
	case <-time.After(time.Second):
		t.Fatal("accepted watch outcome was not awaited after cancellation")
	}
	select {
	case err := <-watchResult:
		if err != nil {
			t.Fatalf("accepted watch returned an error after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not return after receiving its outcome")
	}
}

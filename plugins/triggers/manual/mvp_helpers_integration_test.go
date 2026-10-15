//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc"
)

type manualIntegrationReporter interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
	Errorf(string, ...any)
	Logf(string, ...any)
}

type manualReceive struct {
	frame pluginprotocol.Frame
	err   error
}

type manualPluginHost struct {
	client  *plugin.Client
	cmd     *exec.Cmd
	stream  pluginprotocol.SessionClient
	frames  chan manualReceive
	ctx     context.Context
	cancel  context.CancelFunc
	log     string
	logFile *os.File
}

var (
	manualBinary         string
	manualSuiteDirectory string
)

var _ = BeforeSuite(func() {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		Fail(fmt.Sprintf("resolve repository root: %v", err))
	}
	manualSuiteDirectory, err = os.MkdirTemp("", "manual-plugin-mvp-suite-")
	if err != nil {
		Fail(fmt.Sprintf("create suite directory: %v", err))
	}
	manualBinary = filepath.Join(manualSuiteDirectory, "manual-plugin"+manualExeSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", manualBinary, "./plugins/triggers/manual")
	command.Dir = root
	started := time.Now()
	output, err := command.CombinedOutput()
	if err != nil {
		Fail(fmt.Sprintf("build model input plugin: elapsed=%s error=%v\n%s", time.Since(started), err, output))
	}
	GinkgoT().Logf("manual plugin build: OS=%s arch=%s elapsed=%s", runtime.GOOS, runtime.GOARCH, time.Since(started))
})

var _ = AfterSuite(func() {
	if manualSuiteDirectory != "" {
		_ = os.RemoveAll(manualSuiteDirectory)
	}
})

func manualExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func startManualPluginHost(t manualIntegrationReporter, directory string) *manualPluginHost {
	t.Helper()
	logPath := filepath.Join(directory, "manual-plugin.stderr")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create manual plugin log: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.Command(manualBinary)
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  pluginprocess.HandshakeConfig,
		Plugins:          plugin.PluginSet{pluginprocess.PluginKey(string(domain.InputRole)): &pluginprocess.FrameworkPlugin{}},
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     5 * time.Second,
		Logger:           hclog.NewNullLogger(),
		Stderr:           logFile,
	})
	started := time.Now()
	rpcClient, err := client.Client()
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("start standalone manual plugin: %v", err)
	}
	value, err := rpcClient.Dispense(pluginprocess.PluginKey(string(domain.InputRole)))
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("dispense input role: %v", err)
	}
	connection, ok := value.(grpc.ClientConnInterface)
	if !ok {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatal("framework did not return a gRPC connection")
	}
	stream, err := pluginprotocol.OpenSession(ctx, connection)
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("open standalone input Session: %v", err)
	}
	readyPayload := make(chan manualReceive, 1)
	go func() {
		payload, receiveErr := stream.Recv()
		if receiveErr != nil {
			readyPayload <- manualReceive{err: receiveErr}
			return
		}
		frame, parseErr := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		readyPayload <- manualReceive{frame: frame, err: parseErr}
	}()
	select {
	case ready := <-readyPayload:
		if ready.err != nil || ready.frame.Type != "ready" || ready.frame.Role != "input" {
			cancel()
			client.Kill()
			_ = logFile.Close()
			t.Fatalf("standalone input readiness = %+v, %v", ready.frame, ready.err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatal("standalone input did not announce readiness")
	}
	GinkgoT().Logf("standalone input ready in %s", time.Since(started))
	host := &manualPluginHost{client: client, cmd: cmd, stream: stream, frames: make(chan manualReceive, 16), ctx: ctx, cancel: cancel, log: logPath, logFile: logFile}
	go host.receive()
	return host
}

func (h *manualPluginHost) receive() {
	for {
		payload, err := h.stream.Recv()
		if err != nil {
			h.frames <- manualReceive{err: err}
			return
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		h.frames <- manualReceive{frame: frame, err: err}
		if err != nil {
			return
		}
	}
}

func (h *manualPluginHost) send(t manualIntegrationReporter, frame pluginprotocol.Frame) {
	t.Helper()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatalf("encode plugin request: %v", err)
	}
	if err := h.stream.Send(payload); err != nil {
		t.Fatalf("send plugin request: %v", err)
	}
}

func (h *manualPluginHost) watch(t manualIntegrationReporter, requestID, subscriptionID, path string) pluginprotocol.Frame {
	t.Helper()
	h.send(t, pluginprotocol.Frame{
		Type: "watch", RequestID: requestID, SubscriptionID: subscriptionID,
		WatchArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"trigger_file": jsonvalue.NewString(path)}),
	})
	result := h.receiveType(t, "watch_result", requestID, 2*time.Second)
	return result
}

func (h *manualPluginHost) receiveType(t manualIntegrationReporter, frameType, requestID string, timeout time.Duration) pluginprotocol.Frame {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case result := <-h.frames:
			if result.err != nil {
				t.Fatalf("receive plugin frame: %v", result.err)
			}
			if result.frame.Type == frameType && (requestID == "" || result.frame.RequestID == requestID) {
				return result.frame
			}
		case <-deadline.C:
			t.Fatalf("did not receive %s request_id=%s within %s", frameType, requestID, timeout)
		}
	}
}

func (h *manualPluginHost) expectEvent(t manualIntegrationReporter, subscriptionID, expected string) {
	t.Helper()
	frame := h.receiveType(t, "event", "", 2*time.Second)
	if frame.SubscriptionID != subscriptionID || frame.Context != expected {
		t.Fatalf("event = %+v; want subscription=%q context bytes=%d", frame, subscriptionID, len(expected))
	}
}

func (h *manualPluginHost) assertNoFrame(t manualIntegrationReporter, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-h.frames:
		t.Fatalf("unexpected plugin frame during invalid input: %+v", result)
	case <-timer.C:
	}
}

func (h *manualPluginHost) cleanup() {
	if h == nil {
		return
	}
	_ = h.stream.CloseSend()
	h.cancel()
	done := make(chan struct{})
	go func() { h.client.Kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = h.cmd.Process.Kill()
	}
	_ = h.logFile.Close()
}

func writeManualTrigger(t manualIntegrationReporter, target string, content []byte) {
	t.Helper()
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create trigger parent: %v", err)
	}
	file, err := os.CreateTemp(parent, ".publish-*")
	if err != nil {
		t.Fatalf("create trigger sibling: %v", err)
	}
	name := file.Name()
	written, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != len(content) {
		_ = os.Remove(name)
		t.Fatalf("write trigger: bytes=%d expected=%d error=%v close=%v", written, len(content), writeErr, closeErr)
	}
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)
		t.Fatalf("publish trigger: %v", err)
	}
}

func writeManualWatchPath(t manualIntegrationReporter, directory, name, path string) string {
	t.Helper()
	args, err := json.Marshal(map[string]string{"trigger_file": path})
	if err != nil {
		t.Fatalf("encode watch arguments: %v", err)
	}
	file := filepath.Join(directory, name)
	if err := os.WriteFile(file, args, 0o600); err != nil {
		t.Fatalf("write watch arguments: %v", err)
	}
	return file
}

func manualPluginLogContains(path, marker string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), marker)
}

func waitManualLog(t manualIntegrationReporter, path, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if manualPluginLogContains(path, marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("manual plugin did not log %q: %s", marker, readManualFile(path))
}

func readManualFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err.Error()
	}
	return string(data)
}

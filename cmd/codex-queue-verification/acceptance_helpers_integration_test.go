//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc"
)

type acceptanceBinaries struct {
	root   string
	goexe  string
	action string
	hub    string
	manual string
	fake   string
}

var builtAcceptance acceptanceBinaries

type acceptanceT interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

var _ = BeforeSuite(func() {
	builtAcceptance = buildAcceptanceBinaries(GinkgoT())
})

var _ = AfterSuite(func() {
	if builtAcceptance.root != "" {
		_ = os.RemoveAll(builtAcceptance.root)
	}
})

func buildAcceptanceBinaries(t acceptanceT) acceptanceBinaries {
	t.Helper()
	moduleFile := runGoEnv(t, "GOMOD")
	root := filepath.Dir(moduleFile)
	goexe := runGoEnv(t, "GOEXE")
	buildRoot, err := os.MkdirTemp("", "codex-queue-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	result := acceptanceBinaries{root: buildRoot, goexe: goexe}
	for _, item := range []struct {
		name   string
		source string
		dest   *string
	}{
		{name: "action", source: "./plugins/actions/codex-queue", dest: &result.action},
		{name: "hub", source: "./cmd/agent-pulse-hub", dest: &result.hub},
		{name: "manual", source: "./plugins/triggers/manual", dest: &result.manual},
		{name: "fake", source: "./cmd/codex-queue-verification/testdata/fake-process.go", dest: &result.fake},
	} {
		output := filepath.Join(buildRoot, item.name+goexe)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		command := exec.CommandContext(ctx, "go", "build", "-o", output, item.source)
		command.Dir = root
		command.WaitDelay = time.Second
		var stdout, stderr limitedBuffer
		stdout.limit, stderr.limit = 64*1024, 64*1024
		command.Stdout, command.Stderr = &stdout, &stderr
		startErr := command.Start()
		if startErr != nil {
			cancel()
			t.Fatalf("start build %s: %v", item.name, startErr)
		}
		buildErr := command.Wait()
		cancel()
		if buildErr != nil || stdout.Overflowed() || stderr.Overflowed() {
			t.Fatalf("build %s failed: %v\nstdout=%s\nstderr=%s", item.name, buildErr, stdout.String(), stderr.String())
		}
		*item.dest = output
	}
	return result
}

func runGoEnv(t acceptanceT, key string) string {
	t.Helper()
	output, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" && key != "GOEXE" {
		t.Fatalf("go env %s returned an empty value", key)
	}
	return value
}

type limitedBuffer struct {
	mu       sync.Mutex
	value    bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.value.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = b.value.Write(data[:remaining])
	}
	if remaining < len(data) {
		b.overflow = true
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value.String()
}

func (b *limitedBuffer) Overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

type commandResult struct {
	started        bool
	exited         bool
	exitCode       int
	stdout         string
	stderr         string
	stdoutOverflow bool
	stderrOverflow bool
	err            error
}

func runCommand(t acceptanceT, timeout time.Duration, executable string, arguments []string, environment []string) commandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = environment
	command.WaitDelay = time.Second
	stdout, stderr := &limitedBuffer{limit: 64 * 1024}, &limitedBuffer{limit: 64 * 1024}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return commandResult{exitCode: -1, err: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	err := command.Wait()
	result := commandResult{
		started: true, exited: command.ProcessState != nil, exitCode: -1,
		stdout: stdout.String(), stderr: stderr.String(),
		stdoutOverflow: stdout.Overflowed(), stderrOverflow: stderr.Overflowed(), err: err,
	}
	if command.ProcessState != nil {
		result.exitCode = command.ProcessState.ExitCode()
	}
	if ctx.Err() != nil && result.err == nil {
		result.err = ctx.Err()
	}
	if result.stdoutOverflow || result.stderrOverflow {
		t.Fatalf("process output exceeded 64 KiB: %s %q", executable, arguments)
	}
	return result
}

func environmentWith(values map[string]string, unset ...string) []string {
	blocked := make(map[string]bool, len(values)+len(unset))
	for key := range values {
		blocked[strings.ToUpper(key)] = true
	}
	for _, key := range unset {
		blocked[strings.ToUpper(key)] = true
	}
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !blocked[strings.ToUpper(key)] {
			environment = append(environment, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func fixtureEnvironment(controlPath, journalPath string) []string {
	return environmentWith(map[string]string{
		"APH_CQ_FIXTURE_CONTROL": controlPath,
		"APH_CQ_FIXTURE_JOURNAL": journalPath,
	})
}

func writeJSON(t acceptanceT, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJournal(t acceptanceT, path string) []map[string]json.RawMessage {
	t.Helper()
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 1_024), 64*1_024)
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("decode journal row: %v", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func copyFile(t acceptanceT, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t acceptanceT, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

type publicOutput struct{ plugin.NetRPCUnsupportedPlugin }

func (publicOutput) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return errors.New("verification host does not serve plugins")
}

func (publicOutput) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, connection *grpc.ClientConn) (any, error) {
	return connection, nil
}

type publicActionSession struct {
	client *plugin.Client
	stream pluginprotocol.SessionClient
	closed bool
}

func startPublicAction(t acceptanceT, actionPath, codexPath string, environment []string) *publicActionSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	command := exec.CommandContext(ctx, actionPath, "--codex-executable", codexPath)
	command.Env = environment
	command.WaitDelay = time.Second
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  pluginprocess.HandshakeConfig,
		Plugins:          plugin.PluginSet{"output": publicOutput{}},
		Cmd:              command,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     10 * time.Second,
		Logger:           hclog.NewNullLogger(),
		Stderr:           io.Discard,
	})
	cleanup := &publicActionSession{client: client}
	t.Cleanup(func() {
		cancel()
		cleanup.close(t)
	})
	rpc, err := client.Client()
	if err != nil {
		t.Fatalf("start actual action through go-plugin: %v", err)
	}
	value, err := rpc.Dispense("output")
	if err != nil {
		t.Fatalf("dispense output role: %v", err)
	}
	connection, ok := value.(*grpc.ClientConn)
	if !ok {
		t.Fatalf("dispensed output connection has type %T", value)
	}
	stream, err := pluginprotocol.OpenSession(ctx, connection)
	if err != nil {
		t.Fatalf("open public v2 Session: %v", err)
	}
	cleanup.stream = stream
	ready := receiveActionFrame(t, stream)
	if ready.Type != "ready" || ready.Role != "output" {
		t.Fatalf("readiness frame=%+v", ready)
	}
	return cleanup
}

func (s *publicActionSession) send(t acceptanceT, frame pluginprotocol.Frame) {
	t.Helper()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stream.Send(payload); err != nil {
		t.Fatal(err)
	}
}

func (s *publicActionSession) receive(t acceptanceT) pluginprotocol.Frame {
	t.Helper()
	return receiveActionFrame(t, s.stream)
}

func (s *publicActionSession) close(t acceptanceT) {
	t.Helper()
	if s.closed {
		return
	}
	s.closed = true
	if s.stream != nil {
		_ = s.stream.CloseSend()
	}
	s.client.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for !s.client.Exited() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.client.Exited() {
		t.Fatal("configured action process did not exit after standard go-plugin Kill")
	}
}

func receiveActionFrame(t acceptanceT, stream pluginprotocol.SessionClient) pluginprotocol.Frame {
	t.Helper()
	payload, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func destinationArgs(t acceptanceT, raw string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func queueRequest(requestID, threadID, contextText string) pluginprotocol.Frame {
	external := true
	return pluginprotocol.Frame{
		Type: "deliver", RequestID: requestID, DeliveryID: "delivery-" + requestID,
		InputPlugin: "manual 日本語", SubscriptionID: "sub-α",
		DestinationArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"thread_id": jsonvalue.NewString(threadID)}),
		Context:         contextText, ExternalData: &external, PermissionNotice: pluginprotocol.PermissionNotice,
	}
}

func queueArgv(t acceptanceT, row map[string]json.RawMessage) []string {
	t.Helper()
	var kind string
	if err := json.Unmarshal(row["kind"], &kind); err != nil || kind != "codex" {
		t.Fatalf("journal kind=%q error=%v", kind, err)
	}
	var argv []string
	if err := json.Unmarshal(row["argv"], &argv); err != nil {
		t.Fatal(err)
	}
	return argv
}

type runningDaemon struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	logs    *limitedBuffer
	hub     string
	address string
	closed  bool
}

func startDaemon(t acceptanceT, hub, config, address string, environment []string) *runningDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, hub, "daemon", "--config", config, "--address", address)
	command.Env = environment
	command.WaitDelay = time.Second
	logs := &limitedBuffer{limit: 64 * 1024}
	command.Stdout, command.Stderr = logs, logs
	if err := command.Start(); err != nil {
		cancel()
		t.Fatalf("start actual hub daemon: %v", err)
	}
	daemon := &runningDaemon{command: command, cancel: cancel, done: make(chan struct{}), logs: logs, hub: hub, address: address}
	go func() {
		daemon.err = command.Wait()
		close(daemon.done)
	}()
	t.Cleanup(func() {
		if !daemon.closed {
			_ = runCommand(t, 5*time.Second, hub, []string{"stop", "--address", address}, os.Environ())
			cancel()
			select {
			case <-daemon.done:
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				<-daemon.done
			}
		}
	})
	return daemon
}

func (d *runningDaemon) waitText(t acceptanceT, text string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(d.logs.String(), text) {
			if d.logs.Overflowed() {
				t.Fatal("daemon log exceeded 64 KiB")
			}
			return
		}
		select {
		case <-d.done:
			t.Fatalf("daemon exited before %q: %v\n%s", text, d.err, d.logs.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for daemon log %q\n%s", text, d.logs.String())
}

func (d *runningDaemon) stop(t acceptanceT) {
	t.Helper()
	result := runCommand(t, 5*time.Second, d.hub, []string{"stop", "--address", d.address}, os.Environ())
	if result.exitCode != 0 || result.stdout == "" || !strings.Contains(result.stdout, `"ok":true`) {
		t.Fatalf("public hub stop failed: exit=%d stdout=%q stderr=%q err=%v", result.exitCode, result.stdout, result.stderr, result.err)
	}
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after public stop")
	}
	if d.err != nil {
		t.Fatalf("daemon shutdown failed: %v\n%s", d.err, d.logs.String())
	}
	if d.logs.Overflowed() {
		t.Fatal("daemon log exceeded 64 KiB")
	}
	if !strings.Contains(d.logs.String(), `"plugin":"manual"`) || !strings.Contains(d.logs.String(), `"plugin":"codex"`) || strings.Count(d.logs.String(), `"exited":true`) < 2 {
		t.Fatalf("configured plugin exits were not both observed:\n%s", d.logs.String())
	}
	d.closed = true
}

func freeLoopbackAddress(t acceptanceT) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func assertEmptyDirectory(t acceptanceT, path, description string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("%s remains: %v", description, names)
	}
}

func requireOneShotSuccess(t acceptanceT, result commandResult, label string) {
	t.Helper()
	if !result.started || !result.exited || result.exitCode != 0 || result.err != nil {
		t.Fatalf("%s failed: started=%t exited=%t code=%d err=%v stdout=%q stderr=%q", label, result.started, result.exited, result.exitCode, result.err, result.stdout, result.stderr)
	}
}

func requireJournalCount(t acceptanceT, path string, count int) []map[string]json.RawMessage {
	t.Helper()
	rows := readJournal(t, path)
	if len(rows) != count {
		t.Fatalf("journal rows=%d want=%d: %v", len(rows), count, rows)
	}
	return rows
}

func mustReadJSON(t acceptanceT, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(fmt.Errorf("decode JSON field: %w", err))
	}
}

func assertHeldProcessStopped(t acceptanceT, row map[string]json.RawMessage) {
	t.Helper()
	var address string
	mustReadJSON(t, row["probe_address"], &address)
	if address == "" {
		t.Fatal("held process did not record its liveness probe")
	}
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("held child still accepts connections at %s after its result returned", address)
	}
}

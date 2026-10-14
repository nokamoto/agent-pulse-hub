//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type mvpReporter interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
	Errorf(string, ...any)
	Logf(string, ...any)
}

type mvpPluginConfig struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

type mvpConfig struct {
	Plugins []mvpPluginConfig `json:"plugins"`
}

type mvpCLIResult struct {
	stdout string
	stderr string
	err    error
}

func (r mvpCLIResult) code() int {
	if r.err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(r.err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type mvpAsyncCLI struct {
	cmd    *exec.Cmd
	stdout *strings.Builder
	stderr *strings.Builder
	done   chan mvpCLIResult
}

type mvpDaemon struct {
	cmd      *exec.Cmd
	hub      string
	address  string
	logPath  string
	stdout   string
	done     chan struct{}
	mu       sync.Mutex
	waitErr  error
	started  time.Time
	config   string
	workRoot string
}

type mvpCaseEnv struct {
	root       string
	workspace  string
	hub        string
	manual     string
	model      string
	python     string
	fixture    string
	buildTimes map[string]time.Duration
}

func prepareMVPSuite(t mvpReporter) *mvpCaseEnv {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	workspace, err := os.MkdirTemp("", "agent-pulse-hub-mvp-suite-")
	if err != nil {
		t.Fatalf("create MVP suite directory: %v", err)
	}
	env := &mvpCaseEnv{root: root, workspace: workspace, fixture: filepath.Join(root, "cmd", "agent-pulse-hub", "testdata", "mvp-plugin.py"), buildTimes: make(map[string]time.Duration)}
	for _, item := range []struct {
		name   string
		target *string
	}{
		{name: "agent-pulse-hub", target: &env.hub},
		{name: "manual-plugin", target: &env.manual},
		{name: "model-output-plugin", target: &env.model},
	} {
		path := filepath.Join(workspace, item.name+exeSuffix())
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		command := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/"+item.name)
		command.Dir = root
		started := time.Now()
		output, buildErr := command.CombinedOutput()
		elapsed := time.Since(started)
		cancel()
		if buildErr != nil {
			t.Fatalf("build %s: elapsed=%s error=%v\n%s", item.name, elapsed, buildErr, output)
		}
		*item.target = path
		env.buildTimes[item.name] = elapsed
	}
	python := os.Getenv("APH_PLUGIN_PYTHON")
	if python == "" {
		python, err = exec.LookPath("python")
		if err != nil {
			python, err = exec.LookPath("python3")
		}
	}
	if err != nil || python == "" {
		t.Fatalf("locate qualified Python interpreter: %v", err)
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatalf("resolve Python interpreter: %v", err)
	}
	env.python = python
	t.Logf("suite build evidence: OS=%s arch=%s hub=%s manual=%s model-output=%s", runtime.GOOS, runtime.GOARCH, env.buildTimes["agent-pulse-hub"], env.buildTimes["manual-plugin"], env.buildTimes["model-output-plugin"])
	return env
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func (e *mvpCaseEnv) cleanup() {
	if e != nil && e.workspace != "" {
		_ = os.RemoveAll(e.workspace)
	}
}

func (e *mvpCaseEnv) newCase(t mvpReporter) string {
	t.Helper()
	root, err := os.MkdirTemp(e.workspace, "case-")
	if err != nil {
		t.Fatalf("create case directory: %v", err)
	}
	return root
}

func (e *mvpCaseEnv) pythonPlugin(t mvpReporter, directory, name, role string) mvpPluginConfig {
	t.Helper()
	pluginDir := filepath.Join(directory, name)
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("create %s fixture directory: %v", name, err)
	}
	return mvpPluginConfig{Name: name, Role: role, Executable: e.python, Args: []string{"-B", e.fixture, pluginDir, "--role", role}}
}

func (e *mvpCaseEnv) modelInput() mvpPluginConfig {
	return mvpPluginConfig{Name: "manual", Role: "input", Executable: e.manual, Args: []string{}}
}

func (e *mvpCaseEnv) modelOutput(name string) mvpPluginConfig {
	return mvpPluginConfig{Name: name, Role: "output", Executable: e.model, Args: []string{}}
}

func writeMVPConfig(t mvpReporter, directory string, config mvpConfig) string {
	t.Helper()
	path := filepath.Join(directory, "config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeMVPObject(t mvpReporter, directory, name string, value any) string {
	t.Helper()
	path := filepath.Join(directory, name)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func freeMVPAddress(t mvpReporter) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("select loopback port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release selected loopback port: %v", err)
	}
	return address
}

func (e *mvpCaseEnv) startDaemon(t mvpReporter, directory, configPath, address string, readyPlugins int) *mvpDaemon {
	t.Helper()
	logPath := filepath.Join(directory, "daemon.jsonl")
	stdoutPath := filepath.Join(directory, "daemon.stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create daemon stdout: %v", err)
	}
	stderr, err := os.Create(logPath)
	if err != nil {
		stdout.Close()
		t.Fatalf("create daemon log: %v", err)
	}
	command := exec.Command(e.hub, "daemon", "--config", configPath, "--address", address)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		t.Fatalf("start daemon: %v", err)
	}
	_ = stdout.Close()
	_ = stderr.Close()
	child := &mvpDaemon{cmd: command, hub: e.hub, address: address, logPath: logPath, stdout: stdoutPath, done: make(chan struct{}), started: time.Now(), config: configPath, workRoot: directory}
	go func() {
		err := command.Wait()
		child.mu.Lock()
		child.waitErr = err
		child.mu.Unlock()
		close(child.done)
	}()
	if !waitMVP(t, 10*time.Second, func() bool { return countMVPLog(child.logPath, "plugin_ready") >= readyPlugins || !child.alive() }) {
		child.cleanup()
		t.Fatalf("daemon did not report readiness: %s", readMVPFile(child.logPath))
	}
	if !child.alive() {
		t.Fatalf("daemon exited during startup: error=%v log=%s", child.error(), readMVPFile(child.logPath))
	}
	return child
}

func (d *mvpDaemon) alive() bool {
	select {
	case <-d.done:
		return false
	default:
		return true
	}
}

func (d *mvpDaemon) error() error {
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waitErr
}

func (d *mvpDaemon) stop(t mvpReporter, hubBinary string) {
	t.Helper()
	if d.alive() {
		result := runMVPCommand(t, 8*time.Second, hubBinary, "stop", "--address", d.address)
		if result.code() != 0 {
			t.Fatalf("stop daemon: code=%d stdout=%q stderr=%q", result.code(), result.stdout, result.stderr)
		}
	}
	select {
	case <-d.done:
	case <-time.After(8 * time.Second):
		d.cleanup()
		t.Fatalf("daemon did not exit after stop; log=%s", readMVPFile(d.logPath))
	}
	if err := d.error(); err != nil {
		t.Fatalf("daemon exit = %v; log=%s", err, readMVPFile(d.logPath))
	}
}

func (d *mvpDaemon) cleanup() {
	if d == nil || d.cmd == nil || !d.alive() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	command := exec.CommandContext(ctx, d.hub, "stop", "--address", d.address)
	_ = command.Run()
	cancel()
	select {
	case <-d.done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = d.cmd.Process.Kill()
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
	}
}

func (e *mvpCaseEnv) runCLI(t mvpReporter, timeout time.Duration, args ...string) mvpCLIResult {
	t.Helper()
	return runMVPCommand(t, timeout, e.hub, args...)
}

func runMVPCommand(t mvpReporter, timeout time.Duration, binary string, args ...string) mvpCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("command exceeded %s: %w", timeout, ctx.Err())
	}
	return mvpCLIResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func startMVPCommand(t mvpReporter, binary string, args ...string) *mvpAsyncCLI {
	t.Helper()
	command := exec.Command(binary, args...)
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start asynchronous command %v: %v", args, err)
	}
	async := &mvpAsyncCLI{cmd: command, stdout: stdout, stderr: stderr, done: make(chan mvpCLIResult, 1)}
	go func() {
		err := command.Wait()
		async.done <- mvpCLIResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
	}()
	return async
}

func (e *mvpCaseEnv) register(t mvpReporter, timeout time.Duration, address, input, output string, watchArgs, destinationArgs any) mvpCLIResult {
	t.Helper()
	root := e.newCase(t)
	watchPath := writeMVPObject(t, root, "watch.json", watchArgs)
	destinationPath := writeMVPObject(t, root, "destination.json", destinationArgs)
	return e.runCLI(t, timeout, "register", "--input-plugin", input, "--watch-args-file", watchPath,
		"--output-plugin", output, "--destination-args-file", destinationPath, "--address", address)
}

func parseMVPResponse(t mvpReporter, result mvpCLIResult) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &value); err != nil {
		t.Fatalf("parse registration stdout %q: %v; stderr=%q code=%d", result.stdout, err, result.stderr, result.code())
	}
	return value
}

func writeFixtureCommand(t mvpReporter, directory string, number int, command any) {
	t.Helper()
	name := fmt.Sprintf("command-%d.json", number)
	path := filepath.Join(directory, name)
	data, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("encode fixture command %s: %v", name, err)
	}
	temporary, err := os.CreateTemp(directory, name+".tmp-")
	if err != nil {
		t.Fatalf("create temporary fixture command %s: %v", name, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		t.Fatalf("write fixture command %s: %v", name, err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatalf("close fixture command %s: %v", name, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		t.Fatalf("publish fixture command %s: %v", name, err)
	}
}

func readMVPJournal(path string) []map[string]any {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	rows := make([]map[string]any, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4_096), 256*1024)
	for scanner.Scan() {
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func mvpRowsOfKind(path, kind string) []map[string]any {
	rows := make([]map[string]any, 0)
	for _, row := range readMVPJournal(path) {
		if row["kind"] == kind {
			rows = append(rows, row)
		}
	}
	return rows
}

func mvpRowsOfMessage(path, message string) []map[string]any {
	rows := make([]map[string]any, 0)
	for _, row := range readMVPJournal(path) {
		if row["msg"] == message {
			rows = append(rows, row)
		}
	}
	return rows
}

func countMVPLog(path, message string) int {
	count := 0
	for _, row := range readMVPJournal(path) {
		if row["msg"] == message {
			count++
		}
	}
	return count
}

func waitMVP(t mvpReporter, duration time.Duration, ready func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(duration)
	for {
		if ready() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func awaitMVPJournal(t mvpReporter, path, kind string, count int, timeout time.Duration) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if !waitMVP(t, timeout, func() bool {
		rows = mvpRowsOfKind(path, kind)
		return len(rows) >= count
	}) {
		t.Fatalf("journal %s did not reach %d %s rows; rows=%v", path, count, kind, readMVPJournal(path))
	}
	return rows
}

func readMVPFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func writeTrigger(t mvpReporter, path, text string) {
	t.Helper()
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("create trigger parent: %v", err)
	}
	temp, err := os.CreateTemp(parent, ".publish-*")
	if err != nil {
		t.Fatalf("create sibling trigger: %v", err)
	}
	name := temp.Name()
	_, writeErr := temp.WriteString(text)
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(name)
		t.Fatalf("write trigger: write=%v close=%v", writeErr, closeErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(name)
		t.Fatalf("trigger target is not absent before publish: %v", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		t.Fatalf("publish trigger by rename: %v", err)
	}
}

func modelRecords(t mvpReporter, path string) []map[string]any {
	t.Helper()
	rows := make([]map[string]any, 0)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rows
		}
		t.Fatalf("open model output: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4_096), 64*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("parse model output line: %v", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read model output: %v", err)
	}
	return rows
}

func writeFixtureEvent(t mvpReporter, directory string, number int, subscriptionID, context string) {
	t.Helper()
	writeFixtureCommand(t, directory, number, map[string]any{
		"version": 2, "type": "event", "subscription_id": subscriptionID, "context": context,
	})
}

func parseObjectValue(t mvpReporter, text string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(text), 0)
	if err != nil || value.Kind() != jsonvalue.Object {
		t.Fatalf("parse JSON object: %v", err)
	}
	return value
}

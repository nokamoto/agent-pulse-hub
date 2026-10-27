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
	root      string
	workspace string
	hub       string
	model     string
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

func (e *mvpCaseEnv) startDaemonWithState(t mvpReporter, directory, configPath, address, stateDirectory string) *mvpDaemon {
	child := e.launchDaemonWithState(t, directory, configPath, address, stateDirectory)
	if !waitMVP(t, 10*time.Second, func() bool { return countMVPLog(child.logPath, "daemon_ready") > 0 || !child.alive() }) {
		child.cleanup()
		t.Fatalf("daemon did not complete its startup barrier: %s", readMVPFile(child.logPath))
	}
	if !child.alive() {
		t.Fatalf("daemon exited during startup: error=%v log=%s", child.error(), readMVPFile(child.logPath))
	}
	return child
}

func (e *mvpCaseEnv) launchDaemonWithState(t mvpReporter, directory, configPath, address, stateDirectory string) *mvpDaemon {
	t.Helper()
	logPath := filepath.Join(directory, fmt.Sprintf("daemon-%d.jsonl", time.Now().UnixNano()))
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
	args := []string{"daemon", "--config", configPath, "--address", address}
	if stateDirectory != "" {
		args = append(args, "--state-dir", stateDirectory)
	}
	command := exec.Command(e.hub, args...)
	command.Env = gpwEnvironment(gpwDaemonEnvironment)
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

func waitMVPDaemonExit(t mvpReporter, daemon *mvpDaemon, timeout time.Duration) int {
	t.Helper()
	select {
	case <-daemon.done:
	case <-time.After(timeout):
		daemon.cleanup()
		t.Fatalf("daemon did not exit within %s: %s", timeout, readMVPFile(daemon.logPath))
	}
	err := daemon.error()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("daemon wait failed: %v", err)
	return -1
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

func readMVPFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
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

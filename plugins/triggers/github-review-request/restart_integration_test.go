//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type grrPluginConfig struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

type grrDaemonConfig struct {
	Plugins []grrPluginConfig `json:"plugins"`
}

type grrDaemon struct {
	command   *exec.Cmd
	address   string
	logPath   string
	logFile   *os.File
	done      chan struct{}
	waitError error
}

func runGRRRestartAcceptance(t grrReporter) {
	t.Helper()
	caseDirectory, err := os.MkdirTemp(grrSuiteDirectory, "restart-case-")
	if err != nil {
		t.Fatalf("create restart case directory: %v", err)
	}
	fixture := newGRRFixture(t, map[string]uint64{"hub-token": 91})
	urlA := "https://github.com/owner-a/repo-a/pull/31"
	urlB := "https://github.com/owner-b/repo-b/pull/32"
	urlC := "https://github.com/owner-c/repo-c/pull/33"
	fixture.setSearch([]grrItem{{ID: 301, URL: urlA}}, "complete", "1")
	configPath := writeGRRDaemonConfig(t, caseDirectory)
	stateDirectory := filepath.Join(caseDirectory, "state")
	address := freeGRRAddress(t)
	destinationA := filepath.Join(caseDirectory, "action-a.jsonl")
	destinationB := filepath.Join(caseDirectory, "action-b.jsonl")
	watchArgs := map[string]any{"host": "github.com", "poll_interval": "100ms"}

	first := startGRRDaemon(t, caseDirectory, configPath, address, stateDirectory, fixture)
	firstA := registerGRR(t, caseDirectory, address, destinationA, watchArgs)
	firstB := registerGRR(t, caseDirectory, address, destinationB, watchArgs)
	if firstA == "" || firstB == "" || firstA == firstB {
		t.Fatalf("registration IDs were not isolated: A=%q B=%q", firstA, firstB)
	}
	if !waitGRR(t, 10*time.Second, func() bool { return len(readGRRRecords(destinationA)) == 1 && len(readGRRRecords(destinationB)) == 1 }) {
		t.Fatalf("initial requests were not delivered independently: A=%v B=%v hub=%s", readGRRRecords(destinationA), readGRRRecords(destinationB), readGRRFile(first.logPath))
	}
	assertGRRRecord(t, destinationA, 0, firstA, urlA)
	assertGRRRecord(t, destinationB, 0, firstB, urlA)
	if err := first.stop(t); err != nil {
		t.Fatalf("normal stop failed: %v; log=%s", err, readGRRFile(first.logPath))
	}
	assertGRRCheckpoint(t, stateDirectory, firstA, "301", 2)
	assertGRRCheckpoint(t, stateDirectory, firstB, "301", 2)

	fixture.setSearch([]grrItem{{ID: 301, URL: urlA}, {ID: 302, URL: urlB}}, "complete", "1")
	second := startGRRDaemon(t, caseDirectory, configPath, freeGRRAddress(t), stateDirectory, fixture)
	if !waitGRR(t, 10*time.Second, func() bool { return len(readGRRRecords(destinationA)) == 2 && len(readGRRRecords(destinationB)) == 2 }) {
		t.Fatalf("restored registrations did not emit only the new request: A=%v B=%v hub=%s", readGRRRecords(destinationA), readGRRRecords(destinationB), readGRRFile(second.logPath))
	}
	assertGRRRecord(t, destinationA, 1, firstA, urlB)
	assertGRRRecord(t, destinationB, 1, firstB, urlB)
	if len(readGRRRecords(destinationA)) != 2 || len(readGRRRecords(destinationB)) != 2 {
		t.Fatal("restored request was emitted more than once")
	}
	if err := second.stop(t); err != nil {
		t.Fatalf("second normal stop failed: %v; log=%s", err, readGRRFile(second.logPath))
	}
	assertGRRCheckpoint(t, stateDirectory, firstA, "301", 2)
	assertGRRCheckpoint(t, stateDirectory, firstA, "302", 2)
	assertGRRCheckpoint(t, stateDirectory, firstB, "301", 2)
	assertGRRCheckpoint(t, stateDirectory, firstB, "302", 2)

	corruptGRRCheckpoint(t, stateDirectory, firstA)
	fixture.setSearch([]grrItem{{ID: 301, URL: urlA}, {ID: 302, URL: urlB}, {ID: 303, URL: urlC}}, "complete", "1")
	invalid := launchGRRDaemon(t, caseDirectory, configPath, freeGRRAddress(t), stateDirectory, fixture)
	if !waitGRR(t, 10*time.Second, func() bool {
		return !invalid.alive() || strings.Contains(readGRRFile(invalid.logPath), "watch_rejected") || strings.Contains(readGRRFile(invalid.logPath), "incompatible with this host")
	}) {
		invalid.cleanup()
		t.Fatalf("invalid restored checkpoint was not reported: %s", readGRRFile(invalid.logPath))
	}
	if invalid.alive() {
		_ = invalid.stop(t)
	}
	if recordsA := readGRRRecords(destinationA); len(recordsA) != 2 || recordsA[0]["context"] != urlA || recordsA[1]["context"] != urlB {
		t.Fatalf("incompatible state fell back to fresh delivery for the affected registration: A=%v", recordsA)
	}
	invalidLog := readGRRFile(invalid.logPath)
	for _, marker := range []string{"hub-token", urlA, urlB, urlC} {
		if strings.Contains(invalidLog, marker) {
			t.Errorf("invalid-state diagnostics contain %q", marker)
		}
	}
}

func writeGRRDaemonConfig(t grrReporter, directory string) string {
	t.Helper()
	config := grrDaemonConfig{Plugins: []grrPluginConfig{
		{Name: "github-review-request", Role: "input", Executable: grrPluginBinary, Args: []string{}},
		{Name: "model", Role: "output", Executable: grrModelBinary, Args: []string{}},
	}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode daemon configuration: %v", err)
	}
	path := filepath.Join(directory, "daemon-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write daemon configuration: %v", err)
	}
	return path
}

func freeGRRAddress(t grrReporter) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("select control address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release control address: %v", err)
	}
	return address
}

func startGRRDaemon(t grrReporter, directory, configPath, address, stateDirectory string, fixture *grrFixture) *grrDaemon {
	t.Helper()
	daemon := launchGRRDaemon(t, directory, configPath, address, stateDirectory, fixture)
	if !waitGRR(t, 10*time.Second, func() bool { return strings.Contains(readGRRFile(daemon.logPath), "daemon_ready") || !daemon.alive() }) {
		daemon.cleanup()
		t.Fatalf("daemon did not become ready: %s", readGRRFile(daemon.logPath))
	}
	if !daemon.alive() {
		t.Fatalf("daemon exited during startup: %s", readGRRFile(daemon.logPath))
	}
	return daemon
}

func launchGRRDaemon(t grrReporter, directory, configPath, address, stateDirectory string, fixture *grrFixture) *grrDaemon {
	t.Helper()
	logPath := filepath.Join(directory, fmt.Sprintf("hub-%d.jsonl", time.Now().UnixNano()))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}
	command := exec.Command(grrHubBinary, "daemon", "--config", configPath, "--address", address, "--state-dir", stateDirectory)
	command.Env = grrEnvironment(grrTestEnvironment(fixture, map[string]string{"GH_TOKEN": "hub-token", "GITHUB_TOKEN": ""}))
	command.Stderr = logFile
	command.Stdout = ioDiscard{}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start hub daemon: %v", err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatalf("close daemon log handle: %v", err)
	}
	daemon := &grrDaemon{command: command, address: address, logPath: logPath, done: make(chan struct{})}
	go func() {
		daemon.waitError = command.Wait()
		close(daemon.done)
	}()
	return daemon
}

type ioDiscard struct{}

func (ioDiscard) Write(data []byte) (int, error) { return len(data), nil }

func (d *grrDaemon) alive() bool {
	select {
	case <-d.done:
		return false
	default:
		return true
	}
}

func (d *grrDaemon) stop(t grrReporter) error {
	t.Helper()
	if d.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, grrHubBinary, "stop", "--address", d.address)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("stop command: %w: %s", err, output)
		}
	}
	select {
	case <-d.done:
		return d.waitError
	case <-time.After(8 * time.Second):
		d.cleanup()
		return fmt.Errorf("daemon did not stop: %s", readGRRFile(d.logPath))
	}
}

func (d *grrDaemon) cleanup() {
	if d == nil || d.command == nil || !d.alive() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = exec.CommandContext(ctx, grrHubBinary, "stop", "--address", d.address).Run()
	cancel()
	select {
	case <-d.done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = d.command.Process.Kill()
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
	}
}

func registerGRR(t grrReporter, directory, address, outputFile string, watchArgs map[string]any) string {
	t.Helper()
	watchPath := filepath.Join(directory, fmt.Sprintf("watch-%d.json", time.Now().UnixNano()))
	destinationPath := filepath.Join(directory, fmt.Sprintf("destination-%d.json", time.Now().UnixNano()))
	watchData, err := json.Marshal(watchArgs)
	if err != nil {
		t.Fatalf("encode watch arguments: %v", err)
	}
	destinationData, err := json.Marshal(map[string]string{"output_file": outputFile})
	if err != nil {
		t.Fatalf("encode destination arguments: %v", err)
	}
	if err := os.WriteFile(watchPath, watchData, 0o600); err != nil {
		t.Fatalf("write watch arguments: %v", err)
	}
	if err := os.WriteFile(destinationPath, destinationData, 0o600); err != nil {
		t.Fatalf("write destination arguments: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, grrHubBinary,
		"register", "--input-plugin", "github-review-request", "--watch-args-file", watchPath,
		"--output-plugin", "model", "--destination-args-file", destinationPath, "--address", address,
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("register route: %v output=%s", err, output)
	}
	var response struct {
		SubscriptionID string `json:"subscription_id"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.SubscriptionID == "" {
		t.Fatalf("registration response %s error=%v", output, err)
	}
	return response.SubscriptionID
}

func readGRRRecords(path string) []map[string]any {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	records := make([]map[string]any, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) == nil {
			records = append(records, record)
		}
	}
	return records
}

func assertGRRRecord(t grrReporter, path string, index int, subscriptionID, contextText string) {
	t.Helper()
	records := readGRRRecords(path)
	if len(records) <= index || records[index]["subscription_id"] != subscriptionID || records[index]["context"] != contextText {
		t.Fatalf("action record %d at %s = %v; want subscription=%s context=%s", index, path, records, subscriptionID, contextText)
	}
}

func assertGRRCheckpoint(t grrReporter, stateDirectory, subscriptionID, issueID string, status int) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stateDirectory, "runtime", subscriptionID, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("runtime checkpoint for %s missing: files=%v error=%v", subscriptionID, files, err)
	}
	var latest map[string]json.RawMessage
	data, err := os.ReadFile(files[len(files)-1])
	if err != nil || json.Unmarshal(data, &latest) != nil {
		t.Fatalf("read runtime record for %s: %v", subscriptionID, err)
	}
	var stateText string
	if err := json.Unmarshal(latest["state"], &stateText); err == nil {
		t.Fatalf("runtime record state unexpectedly encoded as string: %s", stateText)
	}
	var state map[string]any
	if err := json.Unmarshal(latest["state"], &state); err != nil {
		t.Fatalf("decode runtime state: %v", err)
	}
	value := grrParseJSON(t, string(latest["state"]))
	if _, err := jsonvalue.Parse([]byte(value.Marshal()), 32_768); err != nil {
		t.Fatalf("saved state is invalid JSON: %v", err)
	}
	encoded := string(value.Marshal())
	want := fmt.Sprintf(`["%s",%d]`, issueID, status)
	if !strings.Contains(encoded, want) || !strings.Contains(encoded, `"schema":"github-review-request-v1"`) || strings.Contains(encoded, "hub-token") {
		t.Fatalf("saved state for %s = %s, missing %s", subscriptionID, encoded, want)
	}
}

func corruptGRRCheckpoint(t grrReporter, stateDirectory, subscriptionID string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stateDirectory, "runtime", subscriptionID, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("runtime checkpoint for corruption missing: files=%v error=%v", files, err)
	}
	var record map[string]any
	data, err := os.ReadFile(files[len(files)-1])
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatalf("read runtime checkpoint: %v", err)
	}
	record["state"] = map[string]any{"schema": "unsupported-v99", "payload": map[string]any{"private": "hub-token"}}
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatalf("encode incompatible checkpoint: %v", err)
	}
	if err := os.WriteFile(files[len(files)-1], data, 0o600); err != nil {
		t.Fatalf("write incompatible checkpoint: %v", err)
	}
}

func waitGRR(t grrReporter, timeout time.Duration, ready func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
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

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
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
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
	command     *exec.Cmd
	address     string
	logPath     string
	journalPath string
	logFile     *os.File
	done        chan struct{}
	waitError   error
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
	urlD := "https://github.com/owner-d/repo-d/pull/34"
	fixture.setSearch([]grrItem{{ID: 301, URL: urlA}}, "complete", "1")
	configPath := writeGRRDaemonConfig(t, caseDirectory)
	stateDirectory := filepath.Join(caseDirectory, "state")
	address := freeGRRAddress(t)
	destinationA := filepath.Join(caseDirectory, "action-a.jsonl")
	destinationB := filepath.Join(caseDirectory, "action-b.jsonl")
	watchArgs := map[string]any{"host": "github.com", "poll_interval": "100ms"}

	first := startGRRDaemon(t, caseDirectory, configPath, address, stateDirectory, fixture)
	firstA := registerGRR(t, caseDirectory, address, destinationA, watchArgs)
	awaitGRRFirstObservation(t, first, firstA, true, 1)
	if records := readGRRRecords(destinationA); len(records) != 0 {
		t.Fatalf("first baseline emitted output: %v", records)
	}
	fixture.setSearch([]grrItem{{ID: 301, URL: urlA}, {ID: 302, URL: urlB}}, "complete", "1")
	if !waitGRR(t, 5*time.Second, func() bool { return len(readGRRRecords(destinationA)) == 1 }) {
		t.Fatalf("later request was not delivered: %s", readGRRFile(first.logPath))
	}
	assertGRRRecord(t, destinationA, 0, firstA, urlB)
	awaitGRRAcceptance(t, first, firstA, destinationA, 0)

	secondWatchArgs := map[string]any{"host": "github.com", "poll_interval": "120ms"}
	firstB := registerGRR(t, caseDirectory, address, destinationB, secondWatchArgs)
	if firstA == "" || firstB == "" || firstA == firstB {
		t.Fatalf("registration IDs were not isolated: A=%q B=%q", firstA, firstB)
	}
	awaitGRRFirstObservation(t, first, firstB, true, 2)
	if err := first.stop(t); err != nil {
		t.Fatalf("normal stop failed: %v; log=%s", err, readGRRFile(first.logPath))
	}
	if a, b := readGRRRecords(destinationA), readGRRRecords(destinationB); len(a) != 1 || len(b) != 0 {
		t.Fatalf("baselines did not stay silent: first=%v second=%v", a, b)
	}
	assertGRRCheckpoint(t, stateDirectory, firstA, "301", 3)
	assertGRRCheckpoint(t, stateDirectory, firstA, "302", 2)
	assertGRRCheckpoint(t, stateDirectory, firstB, "301", 3)
	assertGRRCheckpoint(t, stateDirectory, firstB, "302", 3)

	abc := []grrItem{{ID: 301, URL: urlA}, {ID: 302, URL: urlB}, {ID: 303, URL: urlC}}
	fixture.setSearch(abc, "complete", "1")
	savedFiles, err := filepath.Glob(filepath.Join(stateDirectory, "registrations", "*.json"))
	if err != nil || len(savedFiles) != 2 {
		t.Fatalf("saved registration order missing: files=%v error=%v", savedFiles, err)
	}
	firstRestoredID := strings.TrimSuffix(filepath.Base(savedFiles[0]), ".json")
	laterValidation, release := fixture.holdSecondRestoredIdentity()
	releaseValidation := sync.OnceFunc(func() { close(release) })
	defer releaseValidation()
	second := launchGRRDaemon(t, caseDirectory, configPath, freeGRRAddress(t), stateDirectory, fixture)
	select {
	case <-laterValidation:
	case <-time.After(4 * time.Second):
		t.Fatalf("later watch validation did not reach its barrier: %s", readGRRFile(second.logPath))
	}
	if !waitGRR(t, time.Second, func() bool { return grrAcceptedWatchIndex(second.journalPath, firstRestoredID) >= 0 }) {
		t.Fatalf("first restored watch did not reach its positive-result journal barrier: %s", readGRRFile(second.journalPath))
	}
	window, cancelWindow := context.WithTimeout(context.Background(), 500*time.Millisecond)
	select {
	case request := <-fixture.searchCalls:
		cancelWindow()
		t.Fatalf("watch searched before complete restoration: %+v journal=%s", request, readGRRFile(second.journalPath))
	case <-window.Done():
	}
	cancelWindow()
	if fixture.searchCountSnapshot() != 0 || strings.Contains(readGRRFile(second.journalPath), `"type":"activate_watch"`) || strings.Contains(readGRRFile(second.logPath), "daemon_ready") {
		t.Fatalf("restoration crossed its barrier early: searches=%d journal=%s hub=%s", fixture.searchCountSnapshot(), readGRRFile(second.journalPath), readGRRFile(second.logPath))
	}
	releaseValidation()
	awaitGRRFirstObservation(t, second, firstA, false, 3)
	awaitGRRFirstObservation(t, second, firstB, false, 3)
	if !waitGRR(t, 5*time.Second, func() bool { return len(readGRRRecords(destinationA)) == 2 && len(readGRRRecords(destinationB)) == 1 }) {
		t.Fatalf("restored registrations did not emit only the new request: A=%v B=%v hub=%s", readGRRRecords(destinationA), readGRRRecords(destinationB), readGRRFile(second.logPath))
	}
	assertGRRRecord(t, destinationA, 1, firstA, urlC)
	assertGRRRecord(t, destinationB, 0, firstB, urlC)
	awaitGRRAcceptance(t, second, firstA, destinationA, 1)
	awaitGRRAcceptance(t, second, firstB, destinationB, 0)
	assertGRRGrantOrder(t, second.journalPath, firstA, firstB)
	if err := second.stop(t); err != nil {
		t.Fatalf("second normal stop failed: %v; log=%s", err, readGRRFile(second.logPath))
	}
	assertGRRCheckpoint(t, stateDirectory, firstA, "301", 3)
	assertGRRCheckpoint(t, stateDirectory, firstA, "302", 2)
	assertGRRCheckpoint(t, stateDirectory, firstA, "303", 2)
	assertGRRCheckpoint(t, stateDirectory, firstB, "301", 3)
	assertGRRCheckpoint(t, stateDirectory, firstB, "302", 3)
	assertGRRCheckpoint(t, stateDirectory, firstB, "303", 2)

	corruptGRRCheckpoint(t, stateDirectory, firstA)
	fixture.setSearch(append(abc, grrItem{ID: 304, URL: urlD}), "complete", "1")
	beforeInvalidSearches := fixture.searchCountSnapshot()
	invalid := launchGRRDaemon(t, caseDirectory, configPath, freeGRRAddress(t), stateDirectory, fixture)
	if !waitGRR(t, 5*time.Second, func() bool { return !invalid.alive() }) {
		invalid.cleanup()
		t.Fatalf("incompatible restore did not stop startup: %s", readGRRFile(invalid.logPath))
	}
	if invalid.waitError == nil || fixture.searchCountSnapshot() != beforeInvalidSearches ||
		len(readGRRRecords(destinationA)) != 2 || len(readGRRRecords(destinationB)) != 1 {
		t.Fatalf("incompatible restore started polling or changed delivery: %s", readGRRFile(invalid.logPath))
	}
	invalidLog := readGRRFile(invalid.logPath)
	if !strings.Contains(invalidLog, "saved GitHub review progress is incompatible") {
		t.Fatalf("restore rejection reason missing: %s", invalidLog)
	}
	for _, marker := range []string{"hub-token", urlA, urlB, urlC, urlD} {
		if strings.Contains(invalidLog, marker) {
			t.Errorf("invalid-state diagnostics contain %q", marker)
		}
	}

	// Preserve the stopped watch's whole progress directory outside the runtime
	// tree. Keeping its registration lets the daemon restore it with null state.
	if err := os.Rename(filepath.Join(stateDirectory, "runtime", firstA), filepath.Join(caseDirectory, "reset-backup")); err != nil {
		t.Fatalf("reset saved progress for %s: %v", firstA, err)
	}
	reset := startGRRDaemon(t, caseDirectory, configPath, freeGRRAddress(t), stateDirectory, fixture)
	awaitGRRFirstObservation(t, reset, firstA, true, 4)
	if !waitGRR(t, 5*time.Second, func() bool { return len(readGRRRecords(destinationB)) == 2 }) {
		t.Fatalf("the other registration did not deliver D: %s", readGRRFile(reset.logPath))
	}
	assertGRRRecord(t, destinationB, 1, firstB, urlD)
	awaitGRRAcceptance(t, reset, firstB, destinationB, 1)
	if err := reset.stop(t); err != nil {
		t.Fatalf("normal stop after reset failed: %v", err)
	}
	if a, b := readGRRRecords(destinationA), readGRRRecords(destinationB); len(a) != 2 || len(b) != 2 {
		t.Fatalf("unexpected delivery after reset: first=%v second=%v", a, b)
	}
	for _, issueID := range []string{"301", "302", "303", "304"} {
		assertGRRCheckpoint(t, stateDirectory, firstA, issueID, 3)
	}
	assertGRRCheckpoint(t, stateDirectory, firstB, "304", 2)
}

func awaitGRRFirstObservation(t grrReporter, daemon *grrDaemon, subscriptionID string, baselineCreated bool, matchCount int) {
	t.Helper()
	if !waitGRR(t, 5*time.Second, func() bool {
		for _, line := range strings.Split(readGRRFile(daemon.logPath), "\n") {
			var record struct {
				Message string `json:"message"`
			}
			var diagnostic struct {
				Name            string `json:"msg"`
				RegistrationID  string `json:"registration_id"`
				BaselineCreated *bool  `json:"baseline_created"`
				MatchCount      int    `json:"match_count"`
			}
			if json.Unmarshal([]byte(line), &record) == nil &&
				json.Unmarshal([]byte(record.Message), &diagnostic) == nil &&
				diagnostic.Name == "github_review_request_first_complete_observation" &&
				diagnostic.RegistrationID == subscriptionID &&
				diagnostic.BaselineCreated != nil && *diagnostic.BaselineCreated == baselineCreated &&
				diagnostic.MatchCount == matchCount {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("first complete observation for %s was not recorded: %s", subscriptionID, readGRRFile(daemon.logPath))
	}
}

func awaitGRRAcceptance(t grrReporter, daemon *grrDaemon, subscriptionID, outputFile string, recordIndex int) {
	t.Helper()
	records := readGRRRecords(outputFile)
	if len(records) <= recordIndex {
		t.Fatalf("missing output record %d for %s", recordIndex, subscriptionID)
	}
	deliveryID, _ := records[recordIndex]["delivery_id"].(string)
	if deliveryID == "" {
		t.Fatalf("output record %d for %s has no delivery ID", recordIndex, subscriptionID)
	}
	if !waitGRR(t, 5*time.Second, func() bool {
		for _, record := range readGRRRecords(daemon.logPath) {
			if record["msg"] == "delivery_result" && record["subscription_id"] == subscriptionID &&
				record["delivery_id"] == deliveryID && record["outcome"] == "accepted" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("delivery %s for %s was not accepted: %s", deliveryID, subscriptionID, readGRRFile(daemon.logPath))
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
	journalPath := logPath + ".protocol.jsonl"
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}
	command := exec.Command(grrHubBinary, "daemon", "--config", configPath, "--address", address, "--state-dir", stateDirectory)
	command.Env = grrEnvironment(grrTestEnvironment(fixture, map[string]string{"GH_TOKEN": "hub-token", "GITHUB_TOKEN": "", "APH_GRR_PROTOCOL_JOURNAL": journalPath}))
	command.Stderr = logFile
	command.Stdout = ioDiscard{}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start hub daemon: %v", err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatalf("close daemon log handle: %v", err)
	}
	daemon := &grrDaemon{command: command, address: address, logPath: logPath, journalPath: journalPath, done: make(chan struct{})}
	go func() {
		daemon.waitError = command.Wait()
		close(daemon.done)
	}()
	DeferCleanup(daemon.cleanup)
	return daemon
}

func grrAcceptedWatchIndex(journalPath, subscriptionID string) int {
	requestID := ""
	for index, record := range readGRRRecords(journalPath) {
		if record["type"] == "state_watch" && record["subscription_id"] == subscriptionID {
			requestID, _ = record["request_id"].(string)
		}
		if requestID != "" && record["type"] == "watch_result" && record["request_id"] == requestID && record["accepted"] == true {
			return index
		}
	}
	return -1
}

func assertGRRGrantOrder(t grrReporter, journalPath string, subscriptionIDs ...string) {
	t.Helper()
	lastAccepted := -1
	for _, id := range subscriptionIDs {
		index := grrAcceptedWatchIndex(journalPath, id)
		if index < 0 {
			t.Fatalf("missing positive watch result for %s: %s", id, readGRRFile(journalPath))
		}
		if index > lastAccepted {
			lastAccepted = index
		}
	}
	grants := make(map[string]int)
	for index, record := range readGRRRecords(journalPath) {
		if record["type"] == "activate_watch" {
			id, _ := record["subscription_id"].(string)
			grants[id]++
			if index <= lastAccepted {
				t.Fatalf("grant preceded full restoration: %s", readGRRFile(journalPath))
			}
		}
	}
	for _, id := range subscriptionIDs {
		if grants[id] != 1 {
			t.Fatalf("grant count for %s = %d, want one", id, grants[id])
		}
	}
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
	if !strings.Contains(encoded, want) || !strings.Contains(encoded, `"schema":"github-review-request-v2"`) ||
		!strings.Contains(encoded, `"baseline_established":true`) || strings.Contains(encoded, "hub-token") {
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

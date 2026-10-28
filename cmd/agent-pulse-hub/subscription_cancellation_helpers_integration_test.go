//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func scInput(t mvpReporter, root, name string) (mvpPluginConfig, string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return mvpPluginConfig{Name: name, Role: "input", Executable: mvpEnv.python, Args: []string{"-B", filepath.Join(mvpEnv.root, "cmd", "agent-pulse-hub", "testdata", "subscription-cancellation-plugin.py"), directory}}, directory
}

func scRegister(t mvpReporter, daemon *mvpDaemon, input, output string, watch, destination any) string {
	t.Helper()
	result := mvpEnv.register(t, 5*time.Second, daemon.address, input, output, watch, destination)
	if result.code() != 0 {
		t.Fatalf("SC registration: %+v; daemon=%s", result, readMVPFile(daemon.logPath))
	}
	id, _ := parseMVPResponse(t, result)["subscription_id"].(string)
	if id == "" {
		t.Fatal("registration omitted subscription ID")
	}
	return id
}

func scCancel(t mvpReporter, daemon *mvpDaemon, id string) mvpCLIResult {
	t.Helper()
	return mvpEnv.runCLI(t, 13*time.Second, "unsubscribe", "--subscription-id", id, "--address", daemon.address)
}

func scSuccess(t mvpReporter, result mvpCLIResult, id string) {
	t.Helper()
	if result.code() != 0 {
		t.Fatalf("unsubscribe failed: %+v", result)
	}
	response := parseMVPResponse(t, result)
	if response["ok"] != true || response["subscription_id"] != id || len(response) != 2 {
		t.Fatalf("unsubscribe response=%v", response)
	}
}

func scError(t mvpReporter, result mvpCLIResult, code string) {
	t.Helper()
	var response map[string]any
	if result.code() == 0 || json.Unmarshal([]byte(result.stdout), &response) != nil || response["ok"] != false || response["code"] != code {
		t.Fatalf("want %s failure: %+v", code, result)
	}
	message, _ := response["message"].(string)
	if len(response) != 3 || message == "" {
		t.Fatalf("invalid error envelope: %v", response)
	}
}

func scAbsent(t mvpReporter, state, id string) {
	t.Helper()
	for _, path := range []string{filepath.Join(state, "registrations", id+".json"), filepath.Join(state, "runtime", id), filepath.Join(state, "cancellations", id+".json")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled state remains: %s %v", path, err)
		}
	}
}

func scAwaitRows(t mvpReporter, journal, kind, id string, count int) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if !waitMVP(t, 3*time.Second, func() bool {
		rows = runtimeRowsForSubscription(mvpRowsOfKind(journal, kind), id)
		return len(rows) >= count
	}) {
		t.Fatalf("missing %s for %s: %v", kind, id, readMVPJournal(journal))
	}
	return rows
}

func scStop(t mvpReporter, daemon *mvpDaemon, processes int) {
	t.Helper()
	daemon.stop(t, mvpEnv.hub)
	awaitMVPLogCount(t, daemon.logPath, "plugin_process_exit", processes, 3*time.Second)
}

func runSCSelective() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	input, inputDirectory := scInput(t, root, "cursor")
	output := mvpEnv.pythonPlugin(t, root, "action", "output")
	outputDirectory := filepath.Join(root, "action")
	inputJournal, outputJournal := filepath.Join(inputDirectory, "journal.jsonl"), filepath.Join(outputDirectory, "journal.jsonl")
	gate := filepath.Join(outputDirectory, "release-a")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
	state := filepath.Join(root, "state")
	daemon := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	DeferCleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	sourceA, sourceB := filepath.Join(root, "a.jsonl"), filepath.Join(root, "b.jsonl")
	idA := scRegister(t, daemon, "cursor", "action", map[string]any{"source_file": sourceA}, map[string]any{"gate": "release-a"})
	idB := scRegister(t, daemon, "cursor", "action", map[string]any{"source_file": sourceB}, map[string]any{})
	inputStarted := awaitMVPJournal(t, inputJournal, "started", 1, 3*time.Second)[0]
	outputStarted := awaitMVPJournal(t, outputJournal, "started", 1, 3*time.Second)[0]
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	appendRuntimeUpdate(t, sourceA, "A-issued", "A-issued")
	deliveries := awaitMVPJournal(t, outputJournal, "deliver", 1, 3*time.Second)
	issued := deliveries[0]["frame"].(map[string]any)
	if issued["subscription_id"] != idA || issued["context"] != "A-issued" {
		t.Fatalf("issued frame=%v", issued)
	}
	appendRuntimeUpdate(t, sourceA, "A-queued", "A-queued")
	scAwaitRows(t, inputJournal, "state_event", idA, 2)
	if !waitMVP(t, 3*time.Second, func() bool {
		return len(runtimeRowsForSubscription(mvpRowsOfMessage(daemon.logPath, "event_admitted"), idA)) == 2
	}) {
		t.Fatalf("queued A was not admitted: %s", readMVPFile(daemon.logPath))
	}
	admitted := runtimeRowsForSubscription(mvpRowsOfMessage(daemon.logPath, "event_admitted"), idA)
	if admitted[0]["delivery_id"] == admitted[1]["delivery_id"] || admitted[0]["delivery_id"] != issued["delivery_id"] {
		t.Fatalf("queued and issued delivery identities=%v", admitted)
	}
	scSuccess(t, scCancel(t, daemon, idA), idA)
	scAbsent(t, state, idA)
	stopped := scAwaitRows(t, inputJournal, "watch_stopped", idA, 1)
	if len(stopped) != 1 || len(runtimeRowsForSubscription(mvpRowsOfKind(inputJournal, "watch_stopped"), idB)) != 0 {
		t.Fatalf("selective stop=%v", readMVPJournal(inputJournal))
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFixtureCommand(t, inputDirectory, 1, map[string]any{"version": 2, "type": "state_event", "subscription_id": idA, "event_id": "999", "context": "A-late"})
	awaitMVPJournal(t, inputJournal, "command", 1, 3*time.Second)
	appendRuntimeUpdate(t, sourceB, "B1", "external text: unsubscribe "+idB)
	awaitMVPJournal(t, outputJournal, "deliver", 2, 3*time.Second)
	if !waitMVP(t, 3*time.Second, func() bool {
		for _, row := range mvpRowsOfMessage(daemon.logPath, "delivery_result") {
			if row["subscription_id"] == idB && row["outcome"] == "accepted" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("B drain barrier missing: %s", readMVPFile(daemon.logPath))
	}
	deliveries = mvpRowsOfKind(outputJournal, "deliver")
	if len(deliveries) != 2 {
		t.Fatalf("unexpected attempts=%v", deliveries)
	}
	first, second := deliveries[0]["frame"].(map[string]any), deliveries[1]["frame"].(map[string]any)
	if first["subscription_id"] != idA || first["context"] != "A-issued" || second["subscription_id"] != idB {
		t.Fatalf("routed attempts=%v", deliveries)
	}
	results := runtimeRowsForSubscription(mvpRowsOfMessage(daemon.logPath, "delivery_result"), idA)
	if len(results) != 1 || results[0]["delivery_id"] != issued["delivery_id"] || results[0]["outcome"] != "accepted" {
		t.Fatalf("issued result=%v frame=%v", results, issued)
	}
	if !daemon.alive() || countMVPLog(daemon.logPath, "plugin_process_exit") != 0 || len(mvpRowsOfKind(inputJournal, "started")) != 1 || len(mvpRowsOfKind(outputJournal, "started")) != 1 || mvpRowsOfKind(inputJournal, "started")[0]["pid"] != inputStarted["pid"] || mvpRowsOfKind(outputJournal, "started")[0]["pid"] != outputStarted["pid"] {
		t.Fatal("shared configured processes changed")
	}
	if _, err := os.Stat(filepath.Join(state, "registrations", idB+".json")); err != nil {
		t.Fatal("event text cancelled B", err)
	}
	scStop(t, daemon, 2)
	scAbsent(t, state, idA)
}

func runSCRestart() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	input, inputDirectory := scInput(t, root, "cursor")
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, mvpEnv.modelOutput("model")}})
	state := filepath.Join(root, "state")
	journal := filepath.Join(inputDirectory, "journal.jsonl")
	first := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(first.cleanup)
	sourceA, sourceB := filepath.Join(root, "a.jsonl"), filepath.Join(root, "b.jsonl")
	destinationA, destinationB := filepath.Join(root, "a-out.jsonl"), filepath.Join(root, "b-out.jsonl")
	watchA, watchB := map[string]any{"source_file": sourceA}, map[string]any{"source_file": sourceB}
	destA, destB := map[string]any{"output_file": destinationA}, map[string]any{"output_file": destinationB}
	idA := scRegister(t, first, "cursor", "model", watchA, destA)
	idB := scRegister(t, first, "cursor", "model", watchB, destB)
	appendRuntimeUpdate(t, sourceA, "A-U1", "A-U1")
	appendRuntimeUpdate(t, sourceB, "B-U1", "B-U1")
	scAwaitRows(t, journal, "delivery_notice", idA, 1)
	scAwaitRows(t, journal, "delivery_notice", idB, 1)
	if !waitMVP(t, 8*time.Second, func() bool {
		return runtimeRecordHas(filepath.Join(state, "runtime", idA), idA, "cursor", []string{"A-U1"}) && runtimeRecordHas(filepath.Join(state, "runtime", idB), idB, "cursor", []string{"B-U1"})
	}) {
		t.Fatal("distinct progress did not commit")
	}
	registrationB, err := os.ReadFile(filepath.Join(state, "registrations", idB+".json"))
	if err != nil {
		t.Fatal(err)
	}
	scSuccess(t, scCancel(t, first, idA), idA)
	scAbsent(t, state, idA)
	scStop(t, first, 2)
	scAbsent(t, state, idA)
	assertRuntimeRecord(t, filepath.Join(state, "runtime", idB), idB, "cursor", []string{"B-U1"})
	second := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(second.cleanup)
	ready := mvpRowsOfMessage(second.logPath, "daemon_ready")
	if len(ready) != 1 || ready[0]["restored_registrations"] != float64(1) {
		t.Fatalf("restart readiness=%v", ready)
	}
	if !waitMVP(t, 3*time.Second, func() bool { return hasSuppressed(mvpRowsOfKind(journal, "suppressed"), idB, "B-U1") }) {
		t.Fatal("B lost restored progress")
	}
	if len(runtimeRowsForSubscription(mvpRowsOfKind(journal, "state_watch"), idA)) != 1 {
		t.Fatal("cancelled A restored")
	}
	after, err := os.ReadFile(filepath.Join(state, "registrations", idB+".json"))
	if err != nil || string(after) != string(registrationB) {
		t.Fatalf("B registration changed: %v", err)
	}
	appendRuntimeUpdate(t, sourceB, "B-U2", "B-U2")
	if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destinationB)) == 2 }) {
		t.Fatal("B-U2 was not delivered")
	}
	newA := scRegister(t, second, "cursor", "model", watchA, destA)
	if newA == idA || newA == idB || !hasStateWatch(mvpRowsOfKind(journal, "state_watch"), newA, nil) {
		t.Fatalf("recreated A did not start fresh: %s", newA)
	}
	if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destinationA)) == 2 }) {
		t.Fatal("fresh A-U1 was not delivered")
	}
	rows := modelRecords(t, destinationA)
	if rows[1]["context"] != "A-U1" || rows[1]["subscription_id"] != newA {
		t.Fatalf("fresh output=%v", rows)
	}
	scStop(t, second, 2)
	scAbsent(t, state, idA)
	assertRuntimeRecord(t, filepath.Join(state, "runtime", idB), idB, "cursor", []string{"B-U1", "B-U2"})
}

// scObstructCancellation holds the input result so the real durable intent is
// observable before replacing the target-owned runtime path with a regular file.
func scObstructCancellation(t mvpReporter, daemon *mvpDaemon, state, inputDirectory, id string) (string, string) {
	t.Helper()
	directory := filepath.Join(state, "runtime", id)
	if !waitMVP(t, 7*time.Second, func() bool { entries, err := os.ReadDir(directory); return err == nil && len(entries) > 0 }) {
		t.Fatalf("runtime generation missing before obstruction: %s", directory)
	}
	gate := filepath.Join(inputDirectory, "release-"+id)
	writeMVPObject(t, inputDirectory, "cancel-control.json", map[string]any{"subscription_id": id, "release_file": gate})
	cli := startMVPCommand(t, mvpEnv.hub, "unsubscribe", "--subscription-id", id, "--address", daemon.address)
	finished := false
	DeferCleanup(func() {
		_ = os.WriteFile(gate, nil, 0o600)
		if finished {
			return
		}
		select {
		case <-cli.done:
		default:
			_ = cli.cmd.Process.Kill()
			select {
			case <-cli.done:
			case <-time.After(3 * time.Second):
			}
		}
	})
	intent := filepath.Join(state, "cancellations", id+".json")
	if !waitMVP(t, 3*time.Second, func() bool { _, err := os.Stat(intent); return err == nil }) {
		t.Fatalf("durable intent not committed: %s", readMVPFile(daemon.logPath))
	}
	scAwaitRows(t, filepath.Join(inputDirectory, "journal.jsonl"), "cancel_held", id, 1)
	preserved := filepath.Join(filepath.Dir(state), "preserved-"+id)
	if err := os.Rename(directory, preserved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory, []byte("controlled non-directory obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	DeferCleanup(func() { daemon.cleanup(); restoreRuntimeObstruction(state, id, preserved) })
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-cli.done:
		finished = true
		scError(t, result, "storage_error")
		message, _ := parseMVPResponse(t, result)["message"].(string)
		if !strings.Contains(message, "persistent removal") || !strings.Contains(message, id) || !strings.Contains(message, "durable intent") || !strings.Contains(message, "restart") {
			t.Fatalf("storage failure omitted stage/target/recovery: %s", message)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("obstructed cancellation did not finish")
	}
	if _, err := os.Stat(intent); err != nil {
		t.Fatal("failed removal lost intent", err)
	}
	return directory, preserved
}

func runSCFailure() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	inputA, dirA := scInput(t, root, "cursor-a")
	inputB, dirB := scInput(t, root, "cursor-b")
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{inputA, inputB, mvpEnv.modelOutput("model")}})
	state := filepath.Join(root, "state")
	journalA, journalB := filepath.Join(dirA, "journal.jsonl"), filepath.Join(dirB, "journal.jsonl")
	daemon := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	sourceA, sourceB := filepath.Join(root, "a.jsonl"), filepath.Join(root, "b.jsonl")
	destA, destB := filepath.Join(root, "a-out.jsonl"), filepath.Join(root, "b-out.jsonl")
	watchA, destinationA := map[string]any{"source_file": sourceA}, map[string]any{"output_file": destA}
	idA := scRegister(t, daemon, "cursor-a", "model", watchA, destinationA)
	idB := scRegister(t, daemon, "cursor-b", "model", map[string]any{"source_file": sourceB}, map[string]any{"output_file": destB})
	appendRuntimeUpdate(t, sourceA, "A-U1", "A-U1")
	appendRuntimeUpdate(t, sourceB, "B-U1", "B-U1")
	scAwaitRows(t, journalA, "delivery_notice", idA, 1)
	scAwaitRows(t, journalB, "delivery_notice", idB, 1)
	registrationsBefore := readStateFiles(t, filepath.Join(state, "registrations"))
	for _, args := range [][]string{{"unsubscribe", "--address", daemon.address}, {"unsubscribe", "--subscription-id", "malformed", "--address", daemon.address}} {
		if result := mvpEnv.runCLI(t, 3*time.Second, args...); result.code() == 0 {
			t.Fatalf("invalid input succeeded: %+v", result)
		}
	}
	scError(t, scCancel(t, daemon, "ffffffff-ffff-4fff-8fff-ffffffffffff"), "unknown_subscription")
	if !sameStateFiles(registrationsBefore, readStateFiles(t, filepath.Join(state, "registrations"))) || len(mvpRowsOfKind(journalA, "cancel_watch")) != 0 || len(mvpRowsOfKind(journalB, "cancel_watch")) != 0 {
		t.Fatal("invalid/unknown cancellation changed subscriptions")
	}
	writeMVPObject(t, dirA, "cancel-control.json", map[string]any{"subscription_id": idA, "reject": true})
	scError(t, scCancel(t, daemon, idA), "cancellation_rejected")
	appendRuntimeUpdate(t, sourceA, "A-fenced", "A-fenced")
	appendRuntimeUpdate(t, sourceB, "B-U2", "B-U2")
	scAwaitRows(t, journalB, "delivery_notice", idB, 2)
	if len(modelRecords(t, destA)) != 1 || len(modelRecords(t, destB)) != 2 {
		t.Fatalf("rejection fence/isolation A=%v B=%v", modelRecords(t, destA), modelRecords(t, destB))
	}
	gate := filepath.Join(dirA, "release-timeout")
	writeMVPObject(t, dirA, "cancel-control.json", map[string]any{"subscription_id": idA, "release_file": gate})
	DeferCleanup(func() { _ = os.WriteFile(gate, nil, 0o600) })
	started := time.Now()
	scError(t, scCancel(t, daemon, idA), "plugin_unavailable")
	if elapsed := time.Since(started); elapsed < 9*time.Second {
		t.Fatalf("actual cancellation timeout was not exercised: %s", elapsed)
	}
	if countMVPLog(daemon.logPath, "plugin_process_exit") != 0 {
		t.Fatal("timeout killed a healthy input")
	}
	if len(modelRecords(t, destA)) != 1 || len(modelRecords(t, destB)) != 2 {
		t.Fatal("timeout changed target fence or unrelated delivery")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	scAwaitRows(t, journalA, "watch_stopped", idA, 1)
	scSuccess(t, scCancel(t, daemon, idA), idA)
	scAbsent(t, state, idA)
	scError(t, scCancel(t, daemon, idA), "unknown_subscription")
	assertRuntimeRecord(t, filepath.Join(state, "runtime", idB), idB, "cursor-b", []string{"B-U1", "B-U2"})
	// A distinct fresh registration exercises explicit retry after a real
	// filesystem obstruction, independently of the following restart branch.
	retryID := scRegister(t, daemon, "cursor-a", "model", watchA, destinationA)
	directory, preserved := scObstructCancellation(t, daemon, state, dirA, retryID)
	current, err := os.ReadFile(filepath.Join(state, "registrations", idB+".json"))
	if err != nil || string(current) != string(registrationsBefore[idB+".json"]) {
		t.Fatal("storage failure changed B registration", err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(preserved, directory); err != nil {
		t.Fatal(err)
	}
	scSuccess(t, scCancel(t, daemon, retryID), retryID)
	scAbsent(t, state, retryID)
	restartID := scRegister(t, daemon, "cursor-a", "model", watchA, destinationA)
	_, preserved = scObstructCancellation(t, daemon, state, dirA, restartID)
	scStop(t, daemon, 3)
	restoreRuntimeObstruction(state, restartID, preserved)
	second := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(second.cleanup)
	scAbsent(t, state, restartID)
	ready := mvpRowsOfMessage(second.logPath, "daemon_ready")
	if len(ready) != 1 || ready[0]["restored_registrations"] != float64(1) {
		t.Fatalf("intent startup restored wrong routes: %v", ready)
	}
	if !waitMVP(t, 3*time.Second, func() bool {
		return hasSuppressed(mvpRowsOfKind(journalB, "suppressed"), idB, "B-U1") && hasSuppressed(mvpRowsOfKind(journalB, "suppressed"), idB, "B-U2")
	}) {
		t.Fatal("B progress not preserved through cancellation recovery")
	}
	assertRuntimeRecord(t, filepath.Join(state, "runtime", idB), idB, "cursor-b", []string{"B-U1", "B-U2"})
	inactive := scRegister(t, second, "cursor-a", "model", watchA, destinationA)
	writeFixtureCommand(t, dirA, 1, map[string]any{"exit": true})
	if !waitMVP(t, 4*time.Second, func() bool {
		for _, row := range mvpRowsOfMessage(second.logPath, "plugin_process_exit") {
			if row["plugin"] == "cursor-a" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("input termination not confirmed: %s", readMVPFile(second.logPath))
	}
	scSuccess(t, scCancel(t, second, inactive), inactive)
	scAbsent(t, state, inactive)
	appendRuntimeUpdate(t, sourceB, "B-U3", "B-U3")
	if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destB)) == 3 }) {
		t.Fatalf("unrelated B did not continue: %s", readMVPFile(second.logPath))
	}
	if rows := modelRecords(t, destB); rows[2]["subscription_id"] != idB || rows[2]["context"] != "B-U3" {
		t.Fatalf("B routing changed: %v", rows)
	}
	scStop(t, second, 3)
}

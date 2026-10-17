//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// Governed by docs/design/trigger-runtime-state.md. These cases exercise the
// assembled daemon, configured processes, public control client and real state
// files. The source fixture owns only its logical progress, never file storage.
var _ = Describe("Trigger runtime state command acceptance", Serial, func() {
	It("restores confirmed progress in isolation and starts recreated registrations fresh", Label("case:TRS-V01-RESTART-ISOLATION"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input, inputDirectory := mvpEnv.runtimeStateInput(t, root)
		output := mvpEnv.modelOutput("model")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
		stateDirectory := filepath.Join(root, "state")
		address := freeMVPAddress(t)
		sourceA, sourceB := filepath.Join(root, "source-a.jsonl"), filepath.Join(root, "source-b.jsonl")
		destinationA, destinationB := filepath.Join(root, "action-a.jsonl"), filepath.Join(root, "action-b.jsonl")
		inputJournal := filepath.Join(inputDirectory, "journal.jsonl")
		first := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(first.cleanup)

		watchA, destinationArgsA := map[string]any{"source_file": sourceA}, map[string]any{"output_file": destinationA}
		watchB, destinationArgsB := map[string]any{"source_file": sourceB}, map[string]any{"output_file": destinationB}
		registrationA := mvpEnv.register(t, 5*time.Second, address, "cursor", "model", watchA, destinationArgsA)
		registrationB := mvpEnv.register(t, 5*time.Second, address, "cursor", "model", watchB, destinationArgsB)
		idA := fmt.Sprint(parseMVPResponse(t, registrationA)["subscription_id"])
		idB := fmt.Sprint(parseMVPResponse(t, registrationB)["subscription_id"])
		if registrationA.code() != 0 || registrationB.code() != 0 || idA == "" || idB == "" || idA == idB {
			t.Fatalf("participating registrations = %v / %v", registrationA, registrationB)
		}
		duplicate := mvpEnv.register(t, 5*time.Second, address, "cursor", "model", watchA, destinationArgsA)
		if duplicate.code() != 0 || parseMVPResponse(t, duplicate)["subscription_id"] != idA {
			t.Fatalf("equivalent registration = %v", duplicate)
		}
		if started := len(runtimeRowsForSubscription(mvpRowsOfKind(inputJournal, "watch_started"), idA)); started != 1 {
			t.Fatalf("equivalent registration started A %d watches", started)
		}

		awaitMVPLogCount(t, first.logPath, "runtime_state_saved", 2, 8*time.Second)
		periodicBoundary := time.Now()
		appendRuntimeUpdate(t, sourceA, "A-U1", "A-U1")
		appendRuntimeUpdate(t, sourceB, "B-V1", "B-V1")
		if !waitMVP(t, 3*time.Second, func() bool {
			return len(modelRecords(t, destinationA)) == 1 && len(modelRecords(t, destinationB)) == 1
		}) {
			t.Fatalf("first accepted updates were not routed independently: A=%v B=%v log=%s", modelRecords(t, destinationA), modelRecords(t, destinationB), readMVPFile(first.logPath))
		}
		notices := awaitRuntimeJournal(t, inputJournal, "delivery_notice", 2, 3*time.Second)
		if !hasRuntimeNotice(notices, idA, "A-U1", "accepted") || !hasRuntimeNotice(notices, idB, "B-V1", "accepted") {
			t.Fatalf("first terminal notices = %v", notices)
		}
		periodic := checkpointRows(mvpRowsOfKind(inputJournal, "checkpoint"), false)
		if len(runtimeRowsForSubscription(periodic, idA)) != 1 || len(runtimeRowsForSubscription(periodic, idB)) != 1 {
			t.Fatalf("unexpected periodic snapshot crossed the final checkpoint boundary: %v", periodic)
		}
		if elapsed := time.Since(periodicBoundary); elapsed >= 5*time.Second {
			t.Fatalf("first accepted updates and stop preparation crossed the next periodic boundary: %s", elapsed)
		}
		first.stop(t, mvpEnv.hub)
		if !waitMVP(t, 3*time.Second, func() bool { return countMVPLog(first.logPath, "plugin_process_exit") == 2 }) {
			t.Fatalf("configured process exits missing after final checkpoint: %s", readMVPFile(first.logPath))
		}
		periodic = checkpointRows(mvpRowsOfKind(inputJournal, "checkpoint"), false)
		if len(runtimeRowsForSubscription(periodic, idA)) != 1 || len(runtimeRowsForSubscription(periodic, idB)) != 1 {
			t.Fatalf("an extra periodic snapshot crossed shutdown: %v", periodic)
		}
		assertFinalCheckpoint(t, inputJournal, idA, "A-U1")
		assertFinalCheckpoint(t, inputJournal, idB, "B-V1")
		assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", idA), idA, "cursor", []string{"A-U1"})
		assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", idB), idB, "cursor", []string{"B-V1"})

		second := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), stateDirectory)
		DeferCleanup(second.cleanup)
		awaitRuntimeJournal(t, inputJournal, "watch_started", 4, 3*time.Second)
		awaitRuntimeJournal(t, inputJournal, "suppressed", 2, 3*time.Second)
		if !hasSuppressed(mvpRowsOfKind(inputJournal, "suppressed"), idA, "A-U1") || !hasSuppressed(mvpRowsOfKind(inputJournal, "suppressed"), idB, "B-V1") {
			t.Fatalf("restored progress did not suppress its own source IDs: %v", mvpRowsOfKind(inputJournal, "suppressed"))
		}
		appendRuntimeUpdate(t, sourceA, "A-U2", "A-U2")
		appendRuntimeUpdate(t, sourceB, "B-V2", "B-V2")
		if !waitMVP(t, 3*time.Second, func() bool {
			return len(modelRecords(t, destinationA)) == 2 && len(modelRecords(t, destinationB)) == 2
		}) {
			t.Fatalf("fresh restored updates were not delivered once: A=%v B=%v log=%s", modelRecords(t, destinationA), modelRecords(t, destinationB), readMVPFile(second.logPath))
		}
		secondNotices := awaitRuntimeJournal(t, inputJournal, "delivery_notice", 4, 3*time.Second)
		if !hasRuntimeNotice(secondNotices, idA, "A-U2", "accepted") || !hasRuntimeNotice(secondNotices, idB, "B-V2", "accepted") {
			t.Fatalf("restored accepted update notices = %v", secondNotices)
		}
		second.stop(t, mvpEnv.hub)
		assertFinalCheckpoint(t, inputJournal, idA, "A-U2")
		assertFinalCheckpoint(t, inputJournal, idB, "B-V2")
		assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", idA), idA, "cursor", []string{"A-U1", "A-U2"})
		assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", idB), idB, "cursor", []string{"B-V1", "B-V2"})

		backup := filepath.Join(root, "preserved-state")
		copyStateDirectory(t, stateDirectory, backup)
		if err := os.Remove(filepath.Join(stateDirectory, "registrations", idA+".json")); err != nil {
			t.Fatalf("offline-delete A registration: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(stateDirectory, "runtime", idA)); err != nil {
			t.Fatalf("offline-delete A runtime state: %v", err)
		}
		if err := os.WriteFile(sourceA, []byte("{\"id\":\"A-U1\",\"context\":\"A-U1\"}\n"), 0o600); err != nil {
			t.Fatalf("prepare deliberate A reset source: %v", err)
		}
		third := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), stateDirectory)
		DeferCleanup(third.cleanup)
		awaitRuntimeJournal(t, inputJournal, "watch_started", 5, 3*time.Second)
		awaitRuntimeJournal(t, inputJournal, "suppressed", 4, 3*time.Second)
		recreated := mvpEnv.register(t, 5*time.Second, third.address, "cursor", "model", watchA, destinationArgsA)
		newIDA := fmt.Sprint(parseMVPResponse(t, recreated)["subscription_id"])
		if recreated.code() != 0 || newIDA == "" || newIDA == idA || newIDA == idB {
			t.Fatalf("recreated A registration ID = %q, response=%v", newIDA, recreated)
		}
		if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destinationA)) == 3 }) {
			t.Fatalf("deliberately reset A did not resume its initial behavior: %v", modelRecords(t, destinationA))
		}
		recordsA := modelRecords(t, destinationA)
		if recordsA[2]["context"] != "A-U1" || recordsA[2]["subscription_id"] != newIDA || len(modelRecords(t, destinationB)) != 2 {
			t.Fatalf("reset/recreation changed routing or retained B progress: A=%v B=%v", recordsA, modelRecords(t, destinationB))
		}
		if !hasStateWatch(mvpRowsOfKind(inputJournal, "state_watch"), newIDA, nil) {
			t.Fatalf("recreated A did not receive null initial state: %v", mvpRowsOfKind(inputJournal, "state_watch"))
		}
		third.stop(t, mvpEnv.hub)
		assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", idB), idB, "cursor", []string{"B-V1", "B-V2"})
		if _, err := os.Stat(filepath.Join(backup, "runtime", idA)); err != nil {
			t.Fatalf("pre-deletion state copy was not preserved: %v", err)
		}
	})

	It("blocks invalid state before watch startup and permits only deliberate reset", Label("case:TRS-V02-RESTORE-RECOVERY"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input, inputDirectory := mvpEnv.runtimeStateInput(t, root)
		output := mvpEnv.modelOutput("model")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
		stateDirectory := filepath.Join(root, "valid-state")
		address := freeMVPAddress(t)
		source := filepath.Join(root, "source.jsonl")
		destination := filepath.Join(root, "action.jsonl")
		watchArgs := map[string]any{"source_file": source}
		destinationArgs := map[string]any{"output_file": destination}
		journal := filepath.Join(inputDirectory, "journal.jsonl")
		initial := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(initial.cleanup)
		registered := mvpEnv.register(t, 5*time.Second, address, "cursor", "model", watchArgs, destinationArgs)
		id := fmt.Sprint(parseMVPResponse(t, registered)["subscription_id"])
		if registered.code() != 0 || id == "" {
			t.Fatalf("initial participating registration = %v", registered)
		}
		appendRuntimeUpdate(t, source, "R-U1", "R-U1")
		if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destination)) == 1 }) {
			t.Fatalf("initial state update was not accepted: %s", readMVPFile(initial.logPath))
		}
		awaitRuntimeJournal(t, journal, "delivery_notice", 1, 3*time.Second)
		initial.stop(t, mvpEnv.hub)
		awaitMVPLogCount(t, initial.logPath, "runtime_state_saved", 1, 2*time.Second)
		runtimeOwnerDirectory := filepath.Join("runtime", id)
		latest := latestRuntimeFile(t, filepath.Join(stateDirectory, runtimeOwnerDirectory))
		baselineRegistrations := readStateFiles(t, filepath.Join(stateDirectory, "registrations"))

		variants := []struct {
			name         string
			incompatible bool
		}{
			{name: "corrupt-latest"},
			{name: "incompatible-schema", incompatible: true},
		}
		for _, variant := range variants {
			variant := variant
			variantState := filepath.Join(root, variant.name+"-state")
			copyStateDirectory(t, stateDirectory, variantState)
			variantLatest := filepath.Join(variantState, runtimeOwnerDirectory, filepath.Base(latest))
			if variant.incompatible {
				var record map[string]any
				data, err := os.ReadFile(variantLatest)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				record["state"] = map[string]any{"schema": "unsupported-v9", "payload": map[string]any{"sentinel": "PRIVATE-STATE-MUST-NOT-LEAK"}}
				data, err = json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(variantLatest, data, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(variantLatest, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			failedBytes, err := os.ReadFile(variantLatest)
			if err != nil {
				t.Fatal(err)
			}
			startedBefore := len(runtimeRowsForSubscription(mvpRowsOfKind(journal, "watch_started"), id))
			outputBeforeVariant := len(modelRecords(t, destination))
			runDirectory := filepath.Join(root, variant.name+"-run")
			if err := os.Mkdir(runDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			failed := mvpEnv.launchDaemonWithState(t, runDirectory, config, freeMVPAddress(t), variantState)
			if code := waitMVPDaemonExit(t, failed, 10*time.Second); code != 1 {
				t.Fatalf("%s startup exit = %d; log=%s", variant.name, code, readMVPFile(failed.logPath))
			}
			diagnostic := readMVPFile(failed.logPath)
			if countMVPLog(failed.logPath, "daemon_ready") != 0 || !strings.Contains(diagnostic, "cursor") || !strings.Contains(diagnostic, id) || strings.Contains(diagnostic, "PRIVATE-STATE-MUST-NOT-LEAK") {
				t.Fatalf("%s fail-closed startup diagnostic = %s", variant.name, diagnostic)
			}
			if after, err := os.ReadFile(variantLatest); err != nil || string(after) != string(failedBytes) {
				t.Fatalf("%s failed startup changed its state file: err=%v", variant.name, err)
			}
			if registrations := readStateFiles(t, filepath.Join(variantState, "registrations")); !sameStateFiles(registrations, baselineRegistrations) {
				t.Fatalf("%s failed startup changed registrations: %v", variant.name, registrations)
			}
			if len(modelRecords(t, destination)) != outputBeforeVariant || len(runtimeRowsForSubscription(mvpRowsOfKind(journal, "watch_started"), id)) != startedBefore {
				t.Fatalf("%s started an affected watch or delivered on failure", variant.name)
			}
			if variant.incompatible {
				if !strings.Contains(diagnostic, "incompatible probe state schema") || len(runtimeRowsForSubscription(mvpRowsOfKind(journal, "watch_rejected"), id)) == 0 {
					t.Fatalf("schema application failure was not identified: %s", diagnostic)
				}
			}

			if err := os.RemoveAll(filepath.Join(variantState, runtimeOwnerDirectory)); err != nil {
				t.Fatalf("explicit offline reset of %s: %v", variant.name, err)
			}
			reset := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), variantState)
			DeferCleanup(reset.cleanup)
			ready := mvpRowsOfMessage(reset.logPath, "daemon_ready")
			if len(ready) != 1 || ready[0]["restored_registrations"] != float64(1) {
				t.Fatalf("explicit reset readiness = %v; log=%s", ready, readMVPFile(reset.logPath))
			}
			if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destination)) == outputBeforeVariant+1 }) {
				t.Fatalf("reset did not permit initial source behavior: %s", readMVPFile(reset.logPath))
			}
			if records := modelRecords(t, destination); records[len(records)-1]["subscription_id"] != id {
				t.Fatalf("explicit reset changed retained registration identity: %v", records)
			}
			if !hasStateWatch(mvpRowsOfKind(journal, "state_watch"), id, nil) {
				t.Fatalf("explicit reset did not send null state: %v", mvpRowsOfKind(journal, "state_watch"))
			}
			reset.stop(t, mvpEnv.hub)
		}
	})

	It("reports failed saving while monitoring and fresh notifications continue", Label("case:TRS-V03-SAVE-FAILURE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input, inputDirectory := mvpEnv.runtimeStateInput(t, root)
		output := mvpEnv.modelOutput("model")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
		stateDirectory := filepath.Join(root, "state")
		address := freeMVPAddress(t)
		source := filepath.Join(root, "source.jsonl")
		destination := filepath.Join(root, "action.jsonl")
		journal := filepath.Join(inputDirectory, "journal.jsonl")
		daemon := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		var obstructedID, preservedDirectory string
		obstructed := false
		DeferCleanup(func() {
			daemon.cleanup()
			if obstructed {
				restoreRuntimeObstruction(stateDirectory, obstructedID, preservedDirectory)
			}
		})
		registered := mvpEnv.register(t, 5*time.Second, address, "cursor", "model", map[string]any{"source_file": source}, map[string]any{"output_file": destination})
		id := fmt.Sprint(parseMVPResponse(t, registered)["subscription_id"])
		if registered.code() != 0 || id == "" {
			t.Fatalf("participating registration = %v", registered)
		}
		appendRuntimeUpdate(t, source, "F-U1", "F-U1")
		if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destination)) == 1 }) {
			t.Fatalf("initial accepted update was not delivered: %s", readMVPFile(daemon.logPath))
		}
		awaitRuntimeJournal(t, journal, "delivery_notice", 1, 3*time.Second)
		awaitMVPLogCount(t, daemon.logPath, "runtime_state_saved", 1, 8*time.Second)
		runtimeDirectory := filepath.Join(stateDirectory, "runtime", id)
		if !waitMVP(t, 7*time.Second, func() bool { return runtimeRecordHas(runtimeDirectory, id, "cursor", []string{"F-U1"}) }) {
			t.Fatalf("no successful checkpoint contained F-U1: %s", readMVPFile(daemon.logPath))
		}
		successfulSaves := countMVPLog(daemon.logPath, "runtime_state_saved")
		obstructedID = id
		preservedDirectory = filepath.Join(root, "preserved-runtime-id")
		if err := os.Rename(runtimeDirectory, preservedDirectory); err != nil {
			t.Fatalf("prepare controlled runtime-save obstruction: %v", err)
		}
		obstructed = true
		if err := os.WriteFile(runtimeDirectory, []byte("controlled save obstruction"), 0o600); err != nil {
			t.Fatalf("create runtime-save obstruction: %v", err)
		}
		if !waitMVP(t, 8*time.Second, func() bool {
			for _, row := range mvpRowsOfMessage(daemon.logPath, "runtime_state_persistence_error") {
				if row["subscription_id"] == id && row["operation"] == "save" && strings.Contains(fmt.Sprint(row["message"]), "runtime continuity at next restart cannot be guaranteed") {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("periodic runtime save failure was not diagnosed: %s", readMVPFile(daemon.logPath))
		}
		appendRuntimeUpdate(t, source, "F-U2", "F-U2")
		if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, destination)) == 2 }) {
			t.Fatalf("input stopped monitoring after runtime save failure: %s", readMVPFile(daemon.logPath))
		}
		awaitRuntimeJournal(t, journal, "delivery_notice", 2, 3*time.Second)
		if !daemon.alive() || countMVPLog(daemon.logPath, "plugin_unavailable") != 0 {
			t.Fatalf("save failure invalidated the healthy trigger: %s", readMVPFile(daemon.logPath))
		}
		if countMVPLog(daemon.logPath, "runtime_state_saved") != successfulSaves {
			t.Fatalf("failed save was reported as committed: %s", readMVPFile(daemon.logPath))
		}
		if records := modelRecords(t, destination); records[0]["context"] != "F-U1" || records[1]["context"] != "F-U2" || records[1]["subscription_id"] != id {
			t.Fatalf("notifications after failed save = %v", records)
		}
		daemon.stop(t, mvpEnv.hub)
		if err := os.Remove(runtimeDirectory); err != nil {
			t.Fatalf("remove controlled obstruction after stop: %v", err)
		}
		if err := os.Rename(preservedDirectory, runtimeDirectory); err != nil {
			t.Fatalf("restore saved runtime directory after stop: %v", err)
		}
		obstructed = false
		assertRuntimeRecord(t, runtimeDirectory, id, "cursor", []string{"F-U1"})
	})
})

func checkpointRows(rows []map[string]any, quiesce bool) []map[string]any {
	selected := make([]map[string]any, 0)
	for _, row := range rows {
		if value, _ := row["quiesce"].(bool); value == quiesce {
			selected = append(selected, row)
		}
	}
	return selected
}

func hasRuntimeNotice(rows []map[string]any, subscriptionID, updateID, status string) bool {
	for _, row := range rows {
		if row["subscription_id"] == subscriptionID && row["update_id"] == updateID && row["status"] == status {
			return true
		}
	}
	return false
}

func hasSuppressed(rows []map[string]any, subscriptionID, updateID string) bool {
	for _, row := range rows {
		if row["subscription_id"] == subscriptionID && row["update_id"] == updateID {
			return true
		}
	}
	return false
}

func hasStateWatch(rows []map[string]any, subscriptionID string, state any) bool {
	for _, row := range rows {
		if row["subscription_id"] == subscriptionID && sameJSONValue(row["state"], state) {
			return true
		}
	}
	return false
}

func assertFinalCheckpoint(t mvpReporter, journal, subscriptionID, updateID string) {
	t.Helper()
	rows := readMVPJournal(journal)
	noticeIndex, finalIndex := -1, -1
	var finalCheckpoint map[string]any
	for index, row := range rows {
		if row["subscription_id"] != subscriptionID {
			continue
		}
		if row["kind"] == "delivery_notice" && row["update_id"] == updateID && row["status"] == "accepted" {
			noticeIndex = index
		}
		if row["kind"] == "checkpoint" {
			quiesce, _ := row["quiesce"].(bool)
			if quiesce {
				finalIndex = index
				finalCheckpoint = row
			}
		}
	}
	if noticeIndex < 0 || finalIndex < 0 || noticeIndex >= finalIndex {
		t.Fatalf("accepted notice was not observed before final quiesce checkpoint for %s: %v", subscriptionID, rows)
	}
	state, _ := finalCheckpoint["state"].(map[string]any)
	payload, _ := state["payload"].(map[string]any)
	if !containsString(payload["confirmed"], updateID) {
		t.Fatalf("final quiesce snapshot for %s omitted %s: %v", subscriptionID, updateID, finalCheckpoint)
	}
}

func assertRuntimeRecord(t mvpReporter, directory, subscriptionID, inputPlugin string, confirmed []string) {
	t.Helper()
	path := latestRuntimeFile(t, directory)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed runtime record %s: %v", path, err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode committed runtime record %s: %v", path, err)
	}
	if record["subscription_id"] != subscriptionID || record["input_plugin"] != inputPlugin {
		t.Fatalf("runtime record owner = %v", record)
	}
	state, _ := record["state"].(map[string]any)
	payload, _ := state["payload"].(map[string]any)
	if state["schema"] != "probe-v1" || !sameJSONValue(payload["confirmed"], stringsToAny(confirmed)) {
		t.Fatalf("runtime record state = %v, want confirmed %v", state, confirmed)
	}
}

func runtimeRecordHas(directory, subscriptionID, inputPlugin string, confirmed []string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	if err != nil {
		return false
	}
	var record map[string]any
	if json.Unmarshal(data, &record) != nil || record["subscription_id"] != subscriptionID || record["input_plugin"] != inputPlugin {
		return false
	}
	state, _ := record["state"].(map[string]any)
	payload, _ := state["payload"].(map[string]any)
	return state["schema"] == "probe-v1" && sameJSONValue(payload["confirmed"], stringsToAny(confirmed))
}

func latestRuntimeFile(t mvpReporter, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		t.Fatalf("runtime generation directory %s = %v, %v", directory, entries, err)
	}
	return filepath.Join(directory, entries[0].Name())
}

func containsString(value any, expected string) bool {
	items, _ := value.([]any)
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}

func stringsToAny(values []string) []any {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return items
}

func sameJSONValue(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func sameStateFiles(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if string(value) != string(right[name]) {
			return false
		}
	}
	return true
}

func restoreRuntimeObstruction(stateDirectory, subscriptionID, preservedDirectory string) {
	obstructed := filepath.Join(stateDirectory, "runtime", subscriptionID)
	if info, err := os.Lstat(obstructed); err == nil && info.Mode().IsRegular() {
		_ = os.Remove(obstructed)
	}
	if _, err := os.Stat(preservedDirectory); err == nil {
		_ = os.Rename(preservedDirectory, obstructed)
	}
}

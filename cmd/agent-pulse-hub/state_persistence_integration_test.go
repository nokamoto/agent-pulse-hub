//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// These cases are governed by docs/design/state-persistence.md.
var _ = Describe("Local registration persistence command acceptance", Serial, func() {
	It("restores saved routing values and deduplicates the restored registration", Label("case:SP-V01-RESTORE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		trigger := filepath.Join(root, "trigger.txt")
		destinationA := filepath.Join(root, "destination-a.jsonl")
		destinationB := filepath.Join(root, "destination-b.jsonl")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{mvpEnv.modelInput(), mvpEnv.modelOutput("model")}})
		address := freeMVPAddress(t)
		first := mvpEnv.startDaemon(t, root, config, address, 2)
		DeferCleanup(first.cleanup)
		stateDirectory := filepath.Clean(config) + ".state"
		if _, err := os.Stat(stateDirectory); err != nil {
			t.Fatalf("default state directory was not created: %v", err)
		}
		ready := mvpRowsOfMessage(first.logPath, "daemon_ready")
		if len(ready) != 1 || ready[0]["restored_registrations"] != float64(0) {
			t.Fatalf("fresh readiness = %v", ready)
		}
		watchArgs := map[string]any{"trigger_file": trigger}
		destinationArgs := map[string]any{"output_file": destinationA}
		watchPath := writeMVPObject(t, root, "watch.json", watchArgs)
		destinationPath := writeMVPObject(t, root, "destination.json", destinationArgs)
		registered := runMVPCommand(t, 5*time.Second, mvpEnv.hub, "register", "--input-plugin", "manual", "--watch-args-file", watchPath,
			"--output-plugin", "model", "--destination-args-file", destinationPath, "--address", address)
		if registered.code() != 0 {
			t.Fatalf("register route: code=%d stdout=%q stderr=%q", registered.code(), registered.stdout, registered.stderr)
		}
		subscriptionID, _ := parseMVPResponse(t, registered)["subscription_id"].(string)
		if subscriptionID == "" {
			t.Fatal("registration returned no subscription ID")
		}
		savedPath := filepath.Join(stateDirectory, "registrations", subscriptionID+".json")
		savedBytes, err := os.ReadFile(savedPath)
		if err != nil {
			t.Fatalf("read committed registration: %v", err)
		}
		var saved map[string]any
		if err := json.Unmarshal(savedBytes, &saved); err != nil {
			t.Fatalf("decode committed registration: %v", err)
		}
		watch, watchOK := saved["watch_args"].(map[string]any)
		destination, destinationOK := saved["destination_args"].(map[string]any)
		if len(saved) != 6 || saved["version"] != float64(1) || saved["subscription_id"] != subscriptionID || saved["input_plugin"] != "manual" || saved["output_plugin"] != "model" || !watchOK || watch["trigger_file"] != trigger || !destinationOK || destination["output_file"] != destinationA {
			t.Fatalf("committed registration changed values: %v", saved)
		}
		if len(readStateRegistrationFiles(t, filepath.Join(stateDirectory, "registrations"))) != 1 {
			t.Fatal("fresh registration created more than one record")
		}
		if err := os.Remove(watchPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(destinationPath); err != nil {
			t.Fatal(err)
		}
		first.stop(t, mvpEnv.hub)
		if !waitMVP(t, 3*time.Second, func() bool { return countMVPLog(first.logPath, "plugin_process_exit") == 2 }) {
			t.Fatalf("configured plugin exits missing: %s", readMVPFile(first.logPath))
		}
		if _, err := os.Stat(filepath.Join(stateDirectory, ".lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("normal stop retained state lock: %v", err)
		}

		second := mvpEnv.startDaemon(t, root, config, address, 2)
		DeferCleanup(second.cleanup)
		ready = mvpRowsOfMessage(second.logPath, "daemon_ready")
		if len(ready) != 1 || ready[0]["restored_registrations"] != float64(1) {
			t.Fatalf("restored readiness = %v; log=%s", ready, readMVPFile(second.logPath))
		}
		if _, err := os.Stat(watchPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restart depended on original watch arguments: %v", err)
		}
		if _, err := os.Stat(destinationPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restart depended on original destination arguments: %v", err)
		}
		contextOne := "PULSE-RESTORED-日本語-1"
		writeTrigger(t, trigger, contextOne)
		if !waitMVP(t, 4*time.Second, func() bool { return len(modelRecords(t, destinationA)) == 1 }) {
			t.Fatalf("restored route did not deliver without registration: %s", readMVPFile(second.logPath))
		}
		watchPath = writeMVPObject(t, root, "watch.json", map[string]any{"trigger_file": trigger})
		destinationPath = writeMVPObject(t, root, "destination.json", map[string]any{"output_file": destinationA})
		duplicate := runMVPCommand(t, 5*time.Second, mvpEnv.hub, "register", "--input-plugin", "manual", "--watch-args-file", watchPath,
			"--output-plugin", "model", "--destination-args-file", destinationPath, "--address", address)
		duplicateResponse := parseMVPResponse(t, duplicate)
		if duplicate.code() != 0 || duplicateResponse["subscription_id"] != subscriptionID {
			t.Fatalf("equivalent restored registration = code %d response %v", duplicate.code(), duplicateResponse)
		}
		if len(readStateRegistrationFiles(t, filepath.Join(stateDirectory, "registrations"))) != 1 || len(modelRecords(t, destinationA)) != 1 {
			t.Fatal("duplicate registration added a record or caused delivery")
		}
		if _, err := os.Stat(destinationB); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restored route wrote unregistered destination B: %v", err)
		}
		contextTwo := "PULSE-RESTORED-日本語-2"
		writeTrigger(t, trigger, contextTwo)
		if !waitMVP(t, 4*time.Second, func() bool { return len(modelRecords(t, destinationA)) == 2 }) {
			t.Fatalf("second fresh event was not delivered: %s", readMVPFile(second.logPath))
		}
		records := modelRecords(t, destinationA)
		if len(records) != 2 || records[0]["context"] != contextOne || records[1]["context"] != contextTwo || records[0]["subscription_id"] != subscriptionID || records[1]["subscription_id"] != subscriptionID || records[0]["input_plugin"] != "manual" || records[1]["input_plugin"] != "manual" || records[0]["external_data"] != true || records[1]["external_data"] != true {
			t.Fatalf("restored output observations = %v", records)
		}
		second.stop(t, mvpEnv.hub)
	})

	It("rejects registration when the local record cannot be committed", Label("case:SP-V02-SAVE-FAILURE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		invalidWatch := filepath.Join(root, "already-exists.txt")
		if err := os.WriteFile(invalidWatch, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
		trigger := filepath.Join(root, "valid-trigger.txt")
		destination := filepath.Join(root, "destination.jsonl")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{mvpEnv.modelInput(), mvpEnv.modelOutput("model")}})
		stateDirectory := filepath.Join(root, "selected-state")
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(daemon.cleanup)
		ready := mvpRowsOfMessage(daemon.logPath, "daemon_ready")
		if len(ready) != 1 || ready[0]["restored_registrations"] != float64(0) {
			t.Fatalf("fresh state readiness = %v", ready)
		}
		badDestination := mvpEnv.register(t, 5*time.Second, address, "manual", "model", map[string]any{"trigger_file": trigger}, map[string]any{})
		badDestinationResponse := parseMVPResponse(t, badDestination)
		if badDestination.code() != 1 || badDestinationResponse["code"] != "destination_rejected" {
			t.Fatalf("invalid destination = code %d response %v", badDestination.code(), badDestinationResponse)
		}
		badWatch := mvpEnv.register(t, 5*time.Second, address, "manual", "model", map[string]any{"trigger_file": invalidWatch}, map[string]any{"output_file": destination})
		badWatchResponse := parseMVPResponse(t, badWatch)
		if badWatch.code() != 1 || badWatchResponse["code"] != "watch_rejected" {
			t.Fatalf("existing trigger watch = code %d response %v", badWatch.code(), badWatchResponse)
		}
		registrationDirectory := filepath.Join(stateDirectory, "registrations")
		if len(readStateRegistrationFiles(t, registrationDirectory)) != 0 {
			t.Fatal("rejected destination or watch created a committed record")
		}
		if err := os.Remove(invalidWatch); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(registrationDirectory); err != nil {
			t.Fatalf("prepare deterministic storage obstruction: %v", err)
		}
		if err := os.WriteFile(registrationDirectory, []byte("obstruction"), 0o600); err != nil {
			t.Fatal(err)
		}
		storageFailure := mvpEnv.register(t, 5*time.Second, address, "manual", "model", map[string]any{"trigger_file": trigger}, map[string]any{"output_file": destination})
		storageResponse := parseMVPResponse(t, storageFailure)
		message, _ := storageResponse["message"].(string)
		if storageFailure.code() != 1 || storageResponse["code"] != "storage_error" || !strings.Contains(message, stateDirectory) || len(message) > 1_024 {
			t.Fatalf("storage failure response = code %d, %v", storageFailure.code(), storageResponse)
		}
		if !daemon.alive() || countMVPLog(daemon.logPath, "plugin_unavailable") != 0 {
			t.Fatalf("storage failure stopped a healthy shared plugin: %s", readMVPFile(daemon.logPath))
		}
		writeTrigger(t, trigger, "PULSE-UNSAVED")
		if !waitMVP(t, 3*time.Second, func() bool { return countMVPLog(daemon.logPath, "event_rejected") >= 1 }) {
			t.Fatalf("accepted but unsaved watch event was not discarded: %s", readMVPFile(daemon.logPath))
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsaved route delivered to the destination: %v", err)
		}
		if data, err := os.ReadFile(registrationDirectory); err != nil || string(data) != "obstruction" {
			t.Fatalf("storage obstruction changed: data=%q err=%v", data, err)
		}
		daemon.stop(t, mvpEnv.hub)
		if !waitMVP(t, 3*time.Second, func() bool { return countMVPLog(daemon.logPath, "plugin_process_exit") == 2 }) {
			t.Fatalf("configured plugin exits missing: %s", readMVPFile(daemon.logPath))
		}
		if err := os.Remove(registrationDirectory); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(registrationDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(trigger)
		second := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(second.cleanup)
		ready = mvpRowsOfMessage(second.logPath, "daemon_ready")
		if len(ready) != 1 || ready[0]["restored_registrations"] != float64(0) || len(readStateRegistrationFiles(t, registrationDirectory)) != 0 {
			t.Fatalf("repaired empty state restarted with a route: ready=%v records=%v", ready, readStateRegistrationFiles(t, registrationDirectory))
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsaved event replayed after restart: %v", err)
		}
		second.stop(t, mvpEnv.hub)
	})

	It("fails startup without partial admission when saved state cannot restore", Label("case:SP-V03-STARTUP-FAILURE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		triggerOne, triggerTwo := filepath.Join(root, "trigger-one.txt"), filepath.Join(root, "trigger-two.txt")
		destinationOne, destinationTwo := filepath.Join(root, "destination-one.jsonl"), filepath.Join(root, "destination-two.jsonl")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{mvpEnv.modelInput(), mvpEnv.modelOutput("model")}})
		stateDirectory := filepath.Join(root, "valid-state")
		address := freeMVPAddress(t)
		initial := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(initial.cleanup)
		firstResult := mvpEnv.register(t, 5*time.Second, address, "manual", "model", map[string]any{"trigger_file": triggerOne}, map[string]any{"output_file": destinationOne})
		secondResult := mvpEnv.register(t, 5*time.Second, address, "manual", "model", map[string]any{"trigger_file": triggerTwo}, map[string]any{"output_file": destinationTwo})
		firstID := fmt.Sprint(parseMVPResponse(t, firstResult)["subscription_id"])
		secondID := fmt.Sprint(parseMVPResponse(t, secondResult)["subscription_id"])
		if firstResult.code() != 0 || secondResult.code() != 0 || firstID == "" || secondID == "" || firstID == secondID {
			t.Fatalf("saved routes = %v / %v", firstResult, secondResult)
		}
		initial.stop(t, mvpEnv.hub)
		registrationDirectory := filepath.Join(stateDirectory, "registrations")
		entries, err := os.ReadDir(registrationDirectory)
		if err != nil || len(entries) != 2 {
			t.Fatalf("saved state entries = %v, %v", entries, err)
		}
		laterName := entries[len(entries)-1].Name()
		laterPath := filepath.Join(registrationDirectory, laterName)
		baseBytes, err := os.ReadFile(laterPath)
		if err != nil {
			t.Fatal(err)
		}
		var laterRecord map[string]any
		if err := json.Unmarshal(baseBytes, &laterRecord); err != nil {
			t.Fatal(err)
		}
		watch, _ := laterRecord["watch_args"].(map[string]any)
		laterTrigger, _ := watch["trigger_file"].(string)
		if laterTrigger == "" {
			t.Fatalf("later record has no trigger path: %v", laterRecord)
		}

		malformedState := filepath.Join(root, "malformed-state")
		copyStateDirectory(t, stateDirectory, malformedState)
		malformedPath := filepath.Join(malformedState, "registrations", laterName)
		if err := os.WriteFile(malformedPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		malformedBefore := readStateFiles(t, filepath.Join(malformedState, "registrations"))
		malformedRun := filepath.Join(root, "malformed-run")
		if err := os.Mkdir(malformedRun, 0o700); err != nil {
			t.Fatal(err)
		}
		malformed := mvpEnv.launchDaemonWithState(t, malformedRun, config, freeMVPAddress(t), malformedState)
		if code := waitMVPDaemonExit(t, malformed, 8*time.Second); code != 1 {
			t.Fatalf("malformed state exit code = %d; log=%s", code, readMVPFile(malformed.logPath))
		}
		if countMVPLog(malformed.logPath, "daemon_ready") != 0 || !strings.Contains(readMVPFile(malformed.logPath), laterName) {
			t.Fatalf("malformed state startup diagnostics = %s", readMVPFile(malformed.logPath))
		}
		if _, err := os.Stat(filepath.Join(malformedState, ".lock")); !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(malformedBefore, readStateFiles(t, filepath.Join(malformedState, "registrations"))) {
			t.Fatalf("malformed startup changed records or retained lock: %v", err)
		}

		missingPluginState := filepath.Join(root, "missing-plugin-state")
		copyStateDirectory(t, stateDirectory, missingPluginState)
		missingPluginPath := filepath.Join(missingPluginState, "registrations", laterName)
		if err := json.Unmarshal(baseBytes, &laterRecord); err != nil {
			t.Fatal(err)
		}
		laterRecord["input_plugin"] = "not-configured"
		changed, err := json.Marshal(laterRecord)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(missingPluginPath, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		missingBefore := readStateFiles(t, filepath.Join(missingPluginState, "registrations"))
		missingRun := filepath.Join(root, "missing-plugin-run")
		if err := os.Mkdir(missingRun, 0o700); err != nil {
			t.Fatal(err)
		}
		missingPlugin := mvpEnv.launchDaemonWithState(t, missingRun, config, freeMVPAddress(t), missingPluginState)
		if code := waitMVPDaemonExit(t, missingPlugin, 8*time.Second); code != 1 {
			t.Fatalf("missing plugin state exit code = %d; log=%s", code, readMVPFile(missingPlugin.logPath))
		}
		if countMVPLog(missingPlugin.logPath, "daemon_ready") != 0 || countMVPLog(missingPlugin.logPath, "plugin_process_exit") != 0 || !strings.Contains(readMVPFile(missingPlugin.logPath), "not-configured") {
			t.Fatalf("missing plugin startup diagnostics = %s", readMVPFile(missingPlugin.logPath))
		}
		if _, err := os.Stat(filepath.Join(missingPluginState, ".lock")); !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(missingBefore, readStateFiles(t, filepath.Join(missingPluginState, "registrations"))) {
			t.Fatalf("missing plugin startup changed records or retained lock: %v", err)
		}

		watchFailureState := filepath.Join(root, "watch-failure-state")
		copyStateDirectory(t, stateDirectory, watchFailureState)
		if err := os.WriteFile(laterTrigger, []byte("preexisting trigger"), 0o600); err != nil {
			t.Fatal(err)
		}
		watchBefore := readStateFiles(t, filepath.Join(watchFailureState, "registrations"))
		watchRun := filepath.Join(root, "watch-failure-run")
		if err := os.Mkdir(watchRun, 0o700); err != nil {
			t.Fatal(err)
		}
		watchFailure := mvpEnv.launchDaemonWithState(t, watchRun, config, freeMVPAddress(t), watchFailureState)
		if code := waitMVPDaemonExit(t, watchFailure, 10*time.Second); code != 1 {
			t.Fatalf("rejected watch startup exit code = %d; log=%s", code, readMVPFile(watchFailure.logPath))
		}
		if countMVPLog(watchFailure.logPath, "daemon_ready") != 0 || countMVPLog(watchFailure.logPath, "plugin_process_exit") != 2 || !strings.Contains(readMVPFile(watchFailure.logPath), "restore registration") {
			t.Fatalf("rejected watch startup diagnostics = %s", readMVPFile(watchFailure.logPath))
		}
		if _, err := os.Stat(filepath.Join(watchFailureState, ".lock")); !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(watchBefore, readStateFiles(t, filepath.Join(watchFailureState, "registrations"))) {
			t.Fatalf("rejected watch startup changed records or retained lock: %v", err)
		}
		if _, err := os.Stat(destinationOne); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partially restored route delivered before startup failed: %v", err)
		}
		if _, err := os.Stat(destinationTwo); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed restore route delivered: %v", err)
		}
		if err := os.Remove(laterTrigger); err != nil {
			t.Fatal(err)
		}

		firstOwner := mvpEnv.startDaemonWithState(t, root, config, address, stateDirectory)
		DeferCleanup(firstOwner.cleanup)
		ready := mvpRowsOfMessage(firstOwner.logPath, "daemon_ready")
		if len(ready) != 1 || ready[0]["restored_registrations"] != float64(2) {
			t.Fatalf("valid owner readiness = %v", ready)
		}
		secondRun := filepath.Join(root, "second-owner-run")
		if err := os.Mkdir(secondRun, 0o700); err != nil {
			t.Fatal(err)
		}
		secondOwner := mvpEnv.launchDaemonWithState(t, secondRun, config, freeMVPAddress(t), stateDirectory)
		if code := waitMVPDaemonExit(t, secondOwner, 5*time.Second); code != 1 {
			t.Fatalf("second state owner exit code = %d; log=%s", code, readMVPFile(secondOwner.logPath))
		}
		if !strings.Contains(readMVPFile(secondOwner.logPath), "acquire state directory lock") || countMVPLog(secondOwner.logPath, "daemon_ready") != 0 || !firstOwner.alive() {
			t.Fatalf("second owner affected first or missed lock failure: %s", readMVPFile(secondOwner.logPath))
		}
		if _, err := os.Stat(filepath.Join(stateDirectory, ".lock")); err != nil {
			t.Fatalf("second owner removed first owner's lock: %v", err)
		}
		writeTrigger(t, triggerOne, "PULSE-FIRST-OWNER-STILL-ROUTES")
		if !waitMVP(t, 4*time.Second, func() bool { return len(modelRecords(t, destinationOne)) == 1 }) {
			t.Fatalf("first owner stopped routing after second-owner rejection: %s", readMVPFile(firstOwner.logPath))
		}
		if records := modelRecords(t, destinationOne); records[0]["subscription_id"] != firstID || records[0]["context"] != "PULSE-FIRST-OWNER-STILL-ROUTES" {
			t.Fatalf("first owner route changed after second-owner rejection: %v", records)
		}
		firstOwner.stop(t, mvpEnv.hub)
	})
})

func readStateRegistrationFiles(t mvpReporter, directory string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read state registrations %s: %v", directory, err)
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("read registration %s: %v", entry.Name(), err)
		}
		var row map[string]any
		if err := json.Unmarshal(data, &row); err != nil {
			t.Fatalf("decode registration %s: %v", entry.Name(), err)
		}
		rows = append(rows, row)
	}
	return rows
}

func readStateFiles(t mvpReporter, directory string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read state directory %s: %v", directory, err)
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("state entry %s is not a readable file: %v", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read state file %s: %v", path, err)
		}
		files[entry.Name()] = data
	}
	return files
}

func awaitMVPLogCount(t mvpReporter, path, message string, count int, timeout time.Duration) {
	t.Helper()
	if !waitMVP(t, timeout, func() bool { return countMVPLog(path, message) >= count }) {
		t.Fatalf("daemon log %s did not reach %d %s rows: %s", path, count, message, readMVPFile(path))
	}
}

// This probe qualifies local file mechanisms only, outside TestIntegration and
// all product acceptance reports. It does not implement or verify persistence.
func TestStatePersistenceFileFeasibility(t *testing.T) {
	started := time.Now()
	root := t.TempDir()
	lock := filepath.Join(root, ".lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second owner: %v", err)
	}
	staging, records := filepath.Join(root, "staging"), filepath.Join(root, "registrations")
	for _, directory := range []string{staging, records} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte(`{"version":1,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71","watch_args":{"number":9007199254740993,"text":"日本語"}}`)
	stage := filepath.Join(staging, "probe.tmp")
	file, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	n, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || n != len(payload) {
		t.Fatalf("write/close: n=%d write=%v close=%v", n, writeErr, closeErr)
	}
	final := filepath.Join(records, "8fc5db2a-77a0-4f60-bf92-bbe214a83a71.json")
	if err := os.Rename(stage, final); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(final)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("committed bytes: %q %v", got, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
	if err := os.Remove(final); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(records); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(records, []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, "another.json"), payload, 0o600); err == nil {
		t.Fatal("regular-file parent allowed a record write")
	}
	for _, path := range []string{records, staging, lock} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: entries=%v error=%v", entries, err)
	}
	t.Logf("OS=%s arch=%s; lock exclusion, exclusive staging write/close, sibling rename, exact bytes, regular-file obstruction and cleanup; zero children; elapsed=%s; feasibility only", runtime.GOOS, runtime.GOARCH, time.Since(started))
}

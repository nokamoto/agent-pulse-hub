//go:build integration

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// Governed by docs/design/trigger-runtime-state.md. This case uses the actual
// daemon/client, model action, storage, and configured-process cleanup.
var _ = Describe("Runtime-state v2 activation", Serial, func() {
	It("waits for complete restoration before granting prepared watches", Label("case:TRS-V04-ACTIVATION"), func() {
		t := GinkgoT()
		started := time.Now()
		root := mvpEnv.newCase(t)
		input, inputDirectory := mvpEnv.runtimeStateInput(t, root)
		input.Args = append(input.Args, "--activation-v2")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, mvpEnv.modelOutput("model")}})
		stateDirectory := filepath.Join(root, "state")
		journal := filepath.Join(inputDirectory, "journal.jsonl")
		first := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), stateDirectory)
		DeferCleanup(first.cleanup)
		routes := make(map[string]activationRoute)
		for _, prefix := range []string{"A", "B"} {
			route := activationRoute{source: filepath.Join(root, prefix+"-source.jsonl"), destination: filepath.Join(root, prefix+"-output.jsonl"), prefix: prefix}
			registered := mvpEnv.register(t, 5*time.Second, first.address, "cursor", "model", map[string]any{"source_file": route.source}, map[string]any{"output_file": route.destination})
			id := fmt.Sprint(parseMVPResponse(t, registered)["subscription_id"])
			if registered.code() != 0 || id == "" || id == "<nil>" {
				t.Fatalf("initial v2 registration = %v", registered)
			}
			routes[id] = route
			appendRuntimeUpdate(t, route.source, prefix+"-U1", prefix+"-U1")
		}
		if len(routes) != 2 || !waitMVP(t, 3*time.Second, func() bool {
			for _, route := range routes {
				if len(modelRecords(t, route.destination)) != 1 {
					return false
				}
			}
			return len(mvpRowsOfKind(journal, "delivery_notice")) == 2
		}) {
			t.Fatalf("initial v2 routes did not complete delivery: journal=%v log=%s", readMVPJournal(journal), readMVPFile(first.logPath))
		}
		first.stop(t, mvpEnv.hub)
		assertActivationProcessExits(t, first)
		for id, route := range routes {
			assertFinalCheckpoint(t, journal, id, route.prefix+"-U1")
			assertRuntimeRecord(t, filepath.Join(stateDirectory, "runtime", id), id, "cursor", []string{route.prefix + "-U1"})
		}
		preserved := filepath.Join(root, "preserved-state")
		copyStateDirectory(t, stateDirectory, preserved)
		preservedFiles := activationSavedFiles(t, preserved)
		entries, err := os.ReadDir(filepath.Join(stateDirectory, "registrations"))
		if err != nil || len(entries) != 2 {
			t.Fatalf("retained registration filenames = %v, %v", entries, err)
		}
		firstID := strings.TrimSuffix(entries[0].Name(), ".json")
		laterID := strings.TrimSuffix(entries[1].Name(), ".json")
		for _, reject := range []bool{false, true} {
			name, runState := "success", stateDirectory
			if reject {
				name, runState = "rejection", preserved
			}
			release := filepath.Join(root, name+"-release")
			writeMVPObject(t, inputDirectory, "activation-control.json", map[string]any{"held_subscription_id": laterID, "release_file": release, "reject": reject})
			journalStart := len(readMVPJournal(journal))
			beforeOutputs := make(map[string]int)
			for id, route := range routes {
				beforeOutputs[id] = len(modelRecords(t, route.destination))
			}
			daemon := mvpEnv.launchDaemonWithState(t, root, config, freeMVPAddress(t), runState)
			DeferCleanup(daemon.cleanup)
			if !waitMVP(t, 3*time.Second, func() bool {
				return len(runtimeRowsForSubscription(activationRows(journal, journalStart, "watch_prepared"), firstID)) == 1 && len(runtimeRowsForSubscription(activationRows(journal, journalStart, "watch_result_held"), laterID)) == 1
			}) {
				t.Fatalf("%s did not reach prepared-first/held-later barrier: journal=%v log=%s", name, activationRows(journal, journalStart, ""), readMVPFile(daemon.logPath))
			}
			if !reject {
				for _, route := range routes {
					appendRuntimeUpdate(t, route.source, route.prefix+"-U2", route.prefix+"-U2")
				}
			}
			assertActivationDormant(t, journal, journalStart, daemon, routes, beforeOutputs)
			if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
				t.Fatalf("release %s watch validation: %v", name, err)
			}
			if reject {
				if code := waitMVPDaemonExit(t, daemon, 5*time.Second); code != 1 {
					t.Fatalf("rejected later restoration exit = %d: %s", code, readMVPFile(daemon.logPath))
				}
				assertActivationDormant(t, journal, journalStart, daemon, routes, beforeOutputs)
				assertActivationProcessExits(t, daemon)
				diagnostic := readMVPFile(daemon.logPath)
				if !strings.Contains(diagnostic, laterID) || !strings.Contains(diagnostic, "cursor") || !strings.Contains(diagnostic, "controlled later watch rejection") {
					t.Fatalf("later-watch rejection diagnostic omitted identity/reason: %s", diagnostic)
				}
				if !sameStateFiles(activationSavedFiles(t, preserved), preservedFiles) {
					t.Fatal("failed restoration changed retained registrations or runtime generations")
				}
				continue
			}
			if !waitMVP(t, 3*time.Second, func() bool { return len(activationRows(journal, journalStart, "delivery_notice")) == 2 }) {
				t.Fatalf("released watches did not deliver fresh observations: journal=%v log=%s", activationRows(journal, journalStart, ""), readMVPFile(daemon.logPath))
			}
			ready := mvpRowsOfMessage(daemon.logPath, "daemon_ready")
			if len(ready) != 1 || ready[0]["restored_registrations"] != float64(2) {
				t.Fatalf("successful restoration readiness = %v", ready)
			}
			readyTime, err := time.Parse(time.RFC3339Nano, fmt.Sprint(ready[0]["time"]))
			if err != nil {
				t.Fatalf("parse startup commit diagnostic time: %v", err)
			}
			prepared := make(map[string]bool)
			for _, row := range activationRows(journal, journalStart, "") {
				if row["kind"] == "watch_prepared" {
					prepared[fmt.Sprint(row["subscription_id"])] = true
				}
				if row["kind"] == "activate_watch" && len(prepared) != 2 {
					t.Fatalf("activation preceded complete restoration: %v", activationRows(journal, journalStart, ""))
				}
			}
			for id, route := range routes {
				grants := runtimeRowsForSubscription(activationRows(journal, journalStart, "activate_watch"), id)
				reads := runtimeRowsForSubscription(activationRows(journal, journalStart, "source_read"), id)
				records := modelRecords(t, route.destination)
				if len(grants) != 1 || len(reads) != 1 || len(records) != beforeOutputs[id]+1 || records[len(records)-1]["context"] != route.prefix+"-U2" || records[len(records)-1]["subscription_id"] != id {
					t.Fatalf("retained route %s activation/delivery = grants %v reads %v output %v", id, grants, reads, records)
				}
				grantTime, valid := grants[0]["time"].(float64)
				if !valid || grantTime < float64(readyTime.UnixNano())/float64(time.Second) {
					t.Fatalf("retained route %s grant preceded startup commit diagnostic: ready=%v grant=%v", id, ready, grants)
				}
				if !hasRuntimeNotice(activationRows(journal, journalStart, "delivery_notice"), id, route.prefix+"-U2", "accepted") {
					t.Fatalf("retained route %s lacks accepted fresh-update notice", id)
				}
			}
			daemon.stop(t, mvpEnv.hub)
			assertActivationProcessExits(t, daemon)
		}
		if elapsed := time.Since(started); elapsed > 30*time.Second {
			t.Fatalf("activation scenario exceeded 30-second budget: %s", elapsed)
		}
		t.Logf("TRS-V04 scenario elapsed=%s; three daemon launches, peak four direct children", time.Since(started))
	})
})

type activationRoute struct{ source, destination, prefix string }

func activationRows(journal string, start int, kind string) []map[string]any {
	all := readMVPJournal(journal)
	if start > len(all) {
		return nil
	}
	var rows []map[string]any
	for _, row := range all[start:] {
		if kind == "" || row["kind"] == kind {
			rows = append(rows, row)
		}
	}
	return rows
}

func assertActivationDormant(t mvpReporter, journal string, start int, daemon *mvpDaemon, routes map[string]activationRoute, outputs map[string]int) {
	t.Helper()
	for _, kind := range []string{"activate_watch", "watch_started", "source_read", "state_event", "delivery_notice"} {
		if rows := activationRows(journal, start, kind); len(rows) != 0 {
			t.Fatalf("uncommitted restoration performed %s: %v", kind, rows)
		}
	}
	if countMVPLog(daemon.logPath, "daemon_ready") != 0 {
		t.Fatalf("uncommitted restoration reported readiness: %s", readMVPFile(daemon.logPath))
	}
	for id, route := range routes {
		if records := modelRecords(t, route.destination); len(records) != outputs[id] {
			t.Fatalf("uncommitted restoration delivered to %s: %v", id, records)
		}
	}
}

func assertActivationProcessExits(t mvpReporter, daemon *mvpDaemon) {
	t.Helper()
	if countMVPLog(daemon.logPath, "plugin_process_exit") != 2 {
		t.Fatalf("configured-process cleanup incomplete: %s", readMVPFile(daemon.logPath))
	}
}

func activationSavedFiles(t mvpReporter, directory string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	for _, name := range []string{"registrations", "runtime"} {
		err := filepath.WalkDir(filepath.Join(directory, name), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(directory, path)
			if err != nil {
				return err
			}
			files[relative] = data
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot saved %s files: %v", name, err)
		}
	}
	return files
}

//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func signalGrantNext(t mvpReporter, h *gpwHost, id string, event map[string]any, generation int) {
	h.send(t, map[string]any{"type": "delivery_notice", "subscription_id": id, "event_id": event["event_id"], "status": "accepted"})
	request := fmt.Sprintf("grant-%d", generation)
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": id, "generation": request})
}

func signalObservationCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	f := newGPWFixture(t)
	f.changeSignals(func(s *signalFixtureState) { s.CI = 26 })
	h := startGPWHost(t, directory, "github.com", f, nil)
	if h.watch(t, "initial", "github.com")["accepted"] != true {
		t.Fatal("watch rejected")
	}
	if state := gpwState(t, h.checkpoint(t, "initial", "dormant")); state["baseline"] != false || state["signal_version"] != float64(1) {
		t.Fatal(state)
	}
	f.changeSignals(func(s *signalFixtureState) { s.Failure = "check-page" })
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "initial", "generation": "initial-1"})
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(h.log), "service") }) {
		t.Fatal("initial failed page not observed")
	}
	if state := gpwState(t, h.checkpoint(t, "initial", "failed-baseline")); state["baseline"] != false || state["sequence"] != float64(0) {
		t.Fatal("partial baseline committed", state)
	}
	f.changeSignals(func(s *signalFixtureState) { s.Failure = "" })
	signalWaitState(t, h, "initial", func(s map[string]any) bool { return s["baseline"] == true })
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	h.close()
	for _, category := range []string{"ci_check", "ci_status", "signal_conflict", "signal_readiness"} {
		f.changeSignals(func(s *signalFixtureState) {
			*s = signalFixtureState{CI: 1, Base: 1, Draft: true, Conflict: "MERGEABLE"}
		})
		h = startGPWHost(t, gpwSuite.newCase(t), "github.com", f, nil)
		if h.watch(t, "branch", "github.com")["accepted"] != true {
			t.Fatal("branch watch rejected")
		}
		h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "branch", "generation": "branch-1"})
		signalWaitState(t, h, "branch", func(s map[string]any) bool { return s["baseline"] == true })
		h.noEvent(t)
		if category == "ci_check" {
			f.changeSignals(func(s *signalFixtureState) { s.OldHead = true })
			if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(h.log), "observation_changed_during_acquisition") }) {
				t.Fatal("old-head result not rejected")
			}
			h.noEvent(t)
			f.changeSignals(func(s *signalFixtureState) { s.OldHead = false; s.Failed = true; s.Failure = "status-page" })
		} else if category == "signal_conflict" {
			f.changeSignals(func(s *signalFixtureState) { s.Base = 2; s.Conflict = "UNKNOWN" })
			f.waitRounds(t, f.count()+2)
			h.noEvent(t)
			f.changeSignals(func(s *signalFixtureState) { s.Conflict = "CONFLICTING"; s.Failure = "pr-read" })
		} else {
			f.changeSignals(func(s *signalFixtureState) {
				if category == "ci_status" {
					s.StatusFailed = true
				} else {
					s.Draft = false
				}
			})
		}
		if category == "ci_check" || category == "signal_conflict" {
			if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(h.log), "service") }) {
				t.Fatal("required acquisition failure missing")
			}
			if state := gpwState(t, h.checkpoint(t, "branch", "failed-change")); state["sequence"] != float64(1) {
				t.Fatal("failed acquisition advanced", state)
			}
			h.noEvent(t)
			f.changeSignals(func(s *signalFixtureState) { s.Failure = "" })
		}
		event := h.receive(t, "state_event", "")
		context := gpwContext(t, event["context"])
		changes := context["changes"].([]any)
		if len(changes) != 1 || changes[0].(map[string]any)["category"] != category {
			t.Fatal("category not independent", context)
		}
		gpwRequireContains(t, context, "PR17", signalHead(), "current", "waiting_reason")
		signalGrantNext(t, h, "branch", event, 2)
		f.waitRounds(t, f.count()+2)
		h.noEvent(t)
		gpwRequireNoSecrets(t, h.log, "private-status-marker", "private-conflict-marker", "private-token-marker", "private-check-marker", "Bearer high")
		h.close()
	}
	for _, request := range f.calls() {
		if request.Host != "api.github.com" || request.Token != "high" {
			t.Fatal("signal request escaped the selected origin or token precedence")
		}
	}
}

func signalSavedChunks(t mvpReporter, manifest string) ([]any, []byte) {
	t.Helper()
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if json.Unmarshal(raw, &meta) != nil {
		t.Fatal("invalid manifest")
	}
	count := 0
	_, _ = fmt.Sscan(meta["chunk_count"].(string), &count)
	chunks := []any{}
	total := []byte{}
	for i := 0; i < count; i++ {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(manifest), fmt.Sprintf("%020d.json", i)))
		if err != nil || len(data) > 32768 {
			t.Fatal(err, len(data))
		}
		var v any
		if json.Unmarshal(data, &v) != nil {
			t.Fatal("bad object")
		}
		chunks = append(chunks, v)
		total = append(total, data...)
	}
	return chunks, total
}

func signalRestartCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	f := newGPWFixture(t)
	f.changeSignals(func(s *signalFixtureState) {
		s.CI = 128
		s.NameBytes = 256
		s.Failed = true
		s.StatusFailed = true
		s.Conflict = "CONFLICTING"
		s.Draft = false
	})
	daemon, output, other, state := gpwNewDaemon(t, f, directory)
	id := gpwRegister(t, daemon, output, gpwArgs("github.com"))
	f.waitRounds(t, f.count()+2)
	if len(modelRecords(t, output)) != 0 {
		t.Fatal("existing signals woke baseline")
	}
	f.changeSignals(func(s *signalFixtureState) {
		s.Failed = false
		s.StatusFailed = false
		s.Conflict = "MERGEABLE"
		s.Draft = true
	})
	f.waitRounds(t, f.count()+2)
	if len(modelRecords(t, output)) != 0 {
		t.Fatal("recovery woke wait")
	}
	noticeGate := filepath.Join(directory, "gate-delivery_notice-1")
	grantGate := filepath.Join(directory, "gate-activate_watch-3")
	for _, gate := range []string{noticeGate, grantGate} {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.changeSignals(func(s *signalFixtureState) { s.Comment = 1 })
	gpwRecords(t, output, 1)
	journal := filepath.Join(directory, "protocol.jsonl")
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(journal), "delivery_notice_held") }) {
		t.Fatal("seed terminal notice was not held")
	}
	for cycle := 0; cycle < 3; cycle++ {
		f.changeSignals(func(s *signalFixtureState) {
			s.Failed = true
			s.Attempt = cycle + 1
			s.Base = cycle + 2
			s.Conflict = "CONFLICTING"
			s.Draft = false
		})
		f.waitRounds(t, f.count()+2)
		f.changeSignals(func(s *signalFixtureState) { s.Failed = false; s.Conflict = "MERGEABLE"; s.Draft = true })
		f.waitRounds(t, f.count()+2)
	}
	if len(modelRecords(t, output)) != 1 {
		t.Fatal("consumed wait emitted")
	}
	var frozen signalFixtureState
	f.changeSignals(func(s *signalFixtureState) {
		frozen = *s
		s.Failure = "pr-read"
	})
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(daemon.logPath), "service") }) {
		t.Fatal("signal fixture did not freeze comparison")
	}
	if err := os.Remove(noticeGate); err != nil {
		t.Fatal(err)
	}
	rows := gpwRecords(t, output, 2)
	prefix := gpwContext(t, rows[1]["context"])
	continuation := prefix["continuation"].(map[string]any)
	if len(prefix["changes"].([]any)) != 8 || continuation["remaining_changes"] != float64(1) || continuation["has_more"] != true || continuation["through_sequence"] != float64(10) || continuation["observed_through_sequence"] != float64(11) || continuation["operation"] != nil {
		t.Fatal("wrong prefix", prefix)
	}
	if prefix["current"].(map[string]any)["is_draft"] != true {
		t.Fatal("recovery comparison missing")
	}
	// A normal stop fences acceptance and commits a quiescent immutable cut.
	if !waitMVP(t, time.Second, func() bool {
		count := 0
		for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
			if row["type"] == "delivery_notice" && row["subscription_id"] == id && row["status"] == "accepted" {
				count++
			}
		}
		return count >= 2
	}) {
		t.Fatal("prefix acceptance missing")
	}
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(journal), "activate_watch_held") }) {
		t.Fatal("suffix grant was not held")
	}
	daemon.stop(t, gpwSuite.hub)
	if err := os.Remove(grantGate); err != nil {
		t.Fatal(err)
	}
	files := gpwSavedFiles(t, state, id)
	if len(files) != 1 {
		t.Fatal(files)
	}
	chunks, data := signalSavedChunks(t, files[0])
	header := chunks[0].(map[string]any)
	if len(data) <= 32768 || header["version"] != float64(4) || header["signal_version"] != float64(1) || header["confirmed"] != float64(10) {
		t.Fatal("invalid v4 saved cut", len(data), header)
	}
	nonce := header["nonce"]
	legacyState := filepath.Join(gpwSuite.newCase(t), "state")
	signalCopyTree(t, state, legacyState)
	relative, err := filepath.Rel(state, files[0])
	if err != nil {
		t.Fatal(err)
	}
	signalWriteLegacyV3(t, filepath.Join(legacyState, relative), header)
	f.changeSignals(func(s *signalFixtureState) { *s = frozen })
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	rows = gpwRecords(t, output, 3)
	suffix := gpwContext(t, rows[2]["context"])
	if len(suffix["changes"].([]any)) != 1 || suffix["continuation"].(map[string]any)["through_sequence"] != float64(11) || suffix["continuation"].(map[string]any)["has_more"] != false {
		t.Fatal("lost or repeated suffix", suffix)
	}
	f.changeSignals(func(s *signalFixtureState) { s.Failure = "" })
	f.waitRounds(t, f.count()+2)
	if len(modelRecords(t, output)) != 3 {
		t.Fatal("covered suffix repeated")
	}
	f.changeSignals(func(s *signalFixtureState) {
		s.Failed = true
		s.Attempt = 5
		s.Base = 9
		s.Conflict = "CONFLICTING"
		s.Draft = false
	})
	rows = gpwRecords(t, output, 4)
	if len(gpwContext(t, rows[3]["context"])["changes"].([]any)) != 3 {
		t.Fatal("fresh episodes suppressed")
	}
	if len(modelRecords(t, other)) != 0 {
		t.Fatal("destination B touched")
	}
	unrelatedArgs := gpwArgs("github.com")
	unrelatedArgs["waiting_reason"] = "unrelated retained route"
	unrelatedID := gpwRegister(t, daemon, other, unrelatedArgs)
	f.waitRounds(t, f.count()+2)
	daemon.stop(t, gpwSuite.hub)
	files = gpwSavedFiles(t, state, id)
	newChunks, _ := signalSavedChunks(t, files[0])
	if newChunks[0].(map[string]any)["nonce"] != nonce {
		t.Fatal("registration nonce changed")
	}
	// Both legacy service schemas must reject before activation, with their
	// original opaque bytes still owned by the stopped core generation.
	for _, schema := range []string{"github-pr-watch-v1", "github-pr-watch-v2"} {
		branchDirectory := gpwSuite.newCase(t)
		branchState := filepath.Join(branchDirectory, "state")
		signalCopyTree(t, state, branchState)
		relative, err := filepath.Rel(state, files[0])
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(branchState, relative)
		originalManifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		_ = json.Unmarshal(originalManifest, &manifest)
		manifest["schema"] = schema
		payload := signalLegacyPayload(newChunks[0].(map[string]any), schema)
		chunkDirectory := filepath.Dir(manifestPath)
		entries, err := os.ReadDir(chunkDirectory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "manifest.json" {
				if err := os.Remove(filepath.Join(chunkDirectory, entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
		payloadBytes, _ := json.Marshal(payload)
		chunkPath := filepath.Join(chunkDirectory, "00000000000000000000.json")
		if err := os.WriteFile(chunkPath, payloadBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest["chunk_count"] = "1"
		manifest["data_bytes"] = fmt.Sprint(len(payloadBytes))
		encoded, _ := json.Marshal(manifest)
		if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		unrelatedBefore := signalTreeBytes(t, filepath.Join(branchState, "runtime", unrelatedID))
		registrationBefore := readMVPFile(filepath.Join(branchState, "registrations", unrelatedID+".json"))
		failedDaemon := gpwSuite.launchDaemonWithState(t, branchDirectory, daemon.config, freeMVPAddress(t), branchState)
		DeferCleanup(failedDaemon.cleanup)
		if code := waitMVPDaemonExit(t, failedDaemon, 3*time.Second); code == 0 || !strings.Contains(readMVPFile(failedDaemon.logPath), "signal_state_incompatible") {
			t.Fatal("old schema not rejected", code, readMVPFile(failedDaemon.logPath))
		}
		if len(modelRecords(t, output)) != 4 {
			t.Fatal("old state emitted")
		}
		actual, err := os.ReadFile(manifestPath)
		if err != nil || string(actual) != string(encoded) {
			t.Fatal("incompatible state overwritten", err)
		}
		if readMVPFile(chunkPath) != string(payloadBytes) || signalTreeBytes(t, filepath.Join(branchState, "runtime", unrelatedID)) != unrelatedBefore || readMVPFile(filepath.Join(branchState, "registrations", unrelatedID+".json")) != registrationBefore {
			t.Fatal("restoration changed saved or unrelated state")
		}
	}
	f.changeSignals(func(s *signalFixtureState) { *s = frozen })
	migrated := gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), legacyState)
	DeferCleanup(migrated.cleanup)
	rows = gpwRecords(t, output, 5)
	migratedContext := gpwContext(t, rows[4]["context"])
	if migratedContext["confirmation_position"] != suffix["confirmation_position"] || len(migratedContext["changes"].([]any)) != 1 {
		t.Fatal("legacy attempted prefix replayed or retained suffix changed", migratedContext)
	}
	migrated.stop(t, gpwSuite.hub)
	migratedChunks, _ := signalSavedChunks(t, gpwSavedFiles(t, legacyState, id)[0])
	migratedHeader := migratedChunks[0].(map[string]any)
	if migratedHeader["version"] != float64(4) || migratedHeader["nonce"] != nonce || migratedHeader["selected"] != float64(11) {
		t.Fatal("v3 migration did not preserve identity and selected coverage", migratedHeader)
	}
	for _, row := range readMVPJournal(journal) {
		if size, ok := row["wire_bytes"].(float64); ok && size > 65536 {
			t.Fatal("signal session exceeded the wire frame bound", size)
		}
		if size, ok := row["chunk_bytes"].(float64); ok && size > 32768 {
			t.Fatal("signal checkpoint exceeded the object bound", size)
		}
	}
}

func signalWriteLegacyV3(t mvpReporter, path string, current map[string]any) {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal([]byte(readMVPFile(path)), &manifest); err != nil {
		t.Fatal(err)
	}
	header := make(map[string]any, len(current))
	for key, value := range current {
		header[key] = value
	}
	header["version"] = 3
	header["attempted"] = header["selected"]
	header["confirmed"], header["comparison"] = 2, 2
	delete(header, "selected")
	delete(header, "profile")
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	headerPath := filepath.Join(filepath.Dir(path), "00000000000000000000.json")
	old, err := os.ReadFile(headerPath)
	if err != nil {
		t.Fatal(err)
	}
	total, err := strconv.Atoi(manifest["data_bytes"].(string))
	if err != nil {
		t.Fatal(err)
	}
	manifest["schema"] = "github-pr-watch-v3"
	manifest["data_bytes"] = strconv.Itoa(total - len(old) + len(headerBytes))
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headerPath, headerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func signalLegacyPayload(header map[string]any, schema string) map[string]any {
	if schema == "github-pr-watch-v1" {
		return map[string]any{"scope": header["scope"], "nonce": header["nonce"], "sequence": 1, "baseline": true, "fingerprints": []any{}, "journal": []any{}, "positions": []any{}, "aggregate": header["aggregate"], "metadata": []any{}, "started_at": header["started_at"], "ended_at": header["ended_at"], "confirmed": 1, "attempted": 0}
	}
	result := map[string]any{}
	for key, value := range header {
		result[key] = value
	}
	delete(result, "signal_version")
	result["version"] = 2
	result["sequence"] = 1
	result["observation_sequence"] = 1
	result["confirmed"] = 1
	result["attempted"] = 0
	result["positions"] = []any{}
	result["comparison"] = nil
	result["item_count"] = 0
	result["change_count"] = 0
	result["first_change_sequence"] = 0
	result["last_change_sequence"] = 0
	return result
}

func signalCopyTree(t mvpReporter, source, target string) {
	t.Helper()
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected fixture file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

func signalTreeBytes(t mvpReporter, root string) string {
	t.Helper()
	var all strings.Builder
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			all.WriteString(path)
			all.Write(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return all.String()
}

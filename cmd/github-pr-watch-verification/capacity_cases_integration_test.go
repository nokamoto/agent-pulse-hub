//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func gpwSavedHeader(t mvpReporter, manifest string) map[string]any {
	t.Helper()
	path := filepath.Join(filepath.Dir(manifest), "00000000000000000000.json")
	var header map[string]any
	if err := json.Unmarshal([]byte(readMVPFile(path)), &header); err != nil {
		t.Fatalf("read stream header %s: %v", path, err)
	}
	return header
}

func gpwLargeCase() {
	t := GinkgoT()
	started := time.Now()
	directory := gpwSuite.newCase(t)
	f := newGPWFixture(t)
	f.capacity = 1000
	h := startGPWHost(t, directory, "github.com", f, nil)
	if h.watch(t, "large", "github.com")["accepted"] != true {
		t.Fatal("large watch rejected")
	}
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "large", "generation": "large-1"})
	f.waitRounds(t, 2)
	baseline := h.checkpoint(t, "large", "large-baseline")
	baselineHeader := gpwState(t, baseline)
	if baselineHeader["item_count"] != float64(1000) {
		t.Fatalf("incomplete thousand-item baseline: %v", baselineHeader)
	}
	gpwRequireReply(t, baseline["chunks"].([]any))
	before := baselineHeader["sequence"]
	if before != float64(1) {
		t.Fatalf("baseline sequence %v", before)
	}
	h.noEvent(t)
	for i, mode := range []string{"failed-comments", "failed-thread"} {
		f.set(1, mode)
		calls := len(f.calls())
		if !waitMVP(t, time.Second, func() bool {
			for _, call := range f.calls()[calls:] {
				if mode == "failed-comments" && strings.Contains(call.Query, "PullRequestComments") && call.Cursor != "" {
					return true
				}
				if mode == "failed-thread" && strings.Contains(call.Query, "PullRequestThreadComments") && call.Cursor == "thread-next" {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("failure stimulus %s not reached", mode)
		}
		state := gpwState(t, h.checkpoint(t, "large", fmt.Sprintf("large-failed-%d", i)))
		if state["sequence"] != before {
			t.Fatalf("failed round advanced %v", state)
		}
		h.noEvent(t)
	}
	f.set(1, "complete")
	event := h.receive(t, "state_event", "")
	context := gpwContext(t, event["context"])
	changes := context["changes"].([]any)
	if len(changes) != 4 {
		t.Fatalf("expected four exact transitions: %v", changes)
	}
	got := map[string]string{}
	for _, value := range changes {
		c := value.(map[string]any)
		got[c["item"].(map[string]any)["id"].(string)] = c["kind"].(string)
	}
	want := map[string]string{"C0001": "edited", "C0002": "disappeared", "C1001": "created", "PR17": "head_changed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions %v want %v", got, want)
	}
	h.send(t, map[string]any{"type": "delivery_notice", "subscription_id": "large", "event_id": event["event_id"], "status": "accepted"})
	snapshot := h.checkpoint(t, "large", "large-save")
	chunks := snapshot["chunks"].([]any)
	bytes := 0
	for _, chunk := range chunks {
		raw, _ := json.Marshal(chunk)
		bytes += len(raw)
	}
	if bytes <= 32768 || len(chunks) < 2 {
		t.Fatalf("snapshot did not exercise streaming: %d bytes/%d chunks", bytes, len(chunks))
	}
	gpwRequireReply(t, chunks)
	h.close()
	h = startGPWHost(t, directory, "github.com", f, nil)
	roundsBeforeRestore := f.count()
	if h.restore(t, "large", "github.com", snapshot["schema"].(string), chunks)["accepted"] != true {
		t.Fatal("stream restore rejected")
	}
	rounds := f.count()
	if f.count() != roundsBeforeRestore {
		t.Fatal("dormant restore observed source")
	}
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "large", "generation": "large-restored"})
	f.waitRounds(t, rounds+2)
	h.noEvent(t)
	f.set(2, "complete")
	context = gpwContext(t, h.receive(t, "state_event", "")["context"])
	changes = context["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["item"].(map[string]any)["id"] != "C0003" {
		t.Fatalf("old item continuity lost: %v", context)
	}
	h.close()
	t.Logf("GPW-I04 execution: %s, snapshot %d bytes/%d chunks", time.Since(started), bytes, len(chunks))
}

func gpwCheckPrefix(t mvpReporter, context map[string]any, next *uint64, seen map[string]bool) (string, bool) {
	t.Helper()
	changes := context["changes"].([]any)
	if len(changes) == 0 || len(changes) > 8 {
		t.Fatalf("invalid prefix size %d", len(changes))
	}
	for _, raw := range changes {
		c := raw.(map[string]any)
		sequence := uint64(c["sequence"].(float64))
		if sequence != *next {
			t.Fatalf("gap/duplicate: got %d want %d", sequence, *next)
		}
		*next++
		item := c["item"].(map[string]any)
		id := item["id"].(string)
		if seen[id] {
			t.Fatalf("repeated transition %s", id)
		}
		seen[id] = true
		if id == "D01" && (c["body_status"] != "shortened" || c["digest"] == "" || item["url"] == "") {
			t.Fatalf("missing body limitation: %v", c)
		}
		if id == "D02" && (c["body_status"] != "empty" || item["body"] != "") {
			t.Fatalf("empty body confused: %v", c)
		}
	}
	continuation := context["continuation"].(map[string]any)
	if uint64(continuation["through_sequence"].(float64)) != *next-1 || continuation["observed_through_sequence"] != float64(42) {
		t.Fatalf("bad continuation %v", continuation)
	}
	more := continuation["has_more"].(bool)
	if int(continuation["remaining_changes"].(float64)) != 40-len(seen) || (more && continuation["operation"] != "rewait") {
		t.Fatalf("suffix lost: %v", continuation)
	}
	return context["confirmation_position"].(string), more
}

func gpwStreamFiles(t mvpReporter, state, id string) (string, []string) {
	t.Helper()
	manifests := gpwSavedFiles(t, state, id)
	if len(manifests) != 1 || filepath.Base(manifests[0]) != "manifest.json" {
		t.Fatalf("expected one committed stream: %v", manifests)
	}
	manifest := manifests[0]
	var record map[string]any
	if json.Unmarshal([]byte(readMVPFile(manifest)), &record) != nil || record["schema"] != "github-pr-watch-v2" || record["subscription_id"] != id {
		t.Fatalf("bad manifest %s", readMVPFile(manifest))
	}
	count, err := strconv.ParseUint(record["chunk_count"].(string), 10, 64)
	if err != nil || count < 2 {
		t.Fatalf("not multi-chunk %v", record)
	}
	files := []string{}
	for i := uint64(0); i < count; i++ {
		path := filepath.Join(filepath.Dir(manifest), fmt.Sprintf("%020d.json", i))
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) > 32768 {
			t.Fatalf("bad committed chunk %s: %v", path, err)
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			t.Fatal("invalid committed object")
		}
		files = append(files, path)
	}
	return manifest, files
}

func gpwContinuationCase() {
	t := GinkgoT()
	started := time.Now()
	directory := gpwSuite.newCase(t)
	f := newGPWFixture(t)
	f.capacity = 100
	daemon, output, _, state := gpwNewDaemon(t, f, directory)
	id := gpwRegister(t, daemon, output, gpwArgs("github.com"))
	f.waitRounds(t, 2)
	f.set(1, "complete")
	seed := gpwContext(t, gpwRecords(t, output, 1)[0]["context"])
	p0 := seed["confirmation_position"].(string)
	gpwWaitAccepted(t, directory, id)
	f.set(2, "failed-comments")
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(daemon.logPath), "service") }) {
		t.Fatal("missing interrupted acquisition diagnostic")
	}
	if len(modelRecords(t, output)) != 1 {
		t.Fatal("failed round notified")
	}
	f.set(2, "complete")
	f.waitRounds(t, f.count()+2)
	_, result := gpwRewaitEventually(t, daemon, id, p0)
	if result.code() != 0 {
		t.Fatalf("first continuation %s", result.stdout)
	}
	next := uint64(3)
	seen := map[string]bool{}
	position, more := gpwCheckPrefix(t, gpwContext(t, gpwRecords(t, output, 2)[1]["context"]), &next, seen)
	if !more {
		t.Fatal("first prefix lost continuation")
	}
	gpwWaitAcceptedCount(t, directory, id, 2)
	daemon.stop(t, gpwSuite.hub)
	manifest, files := gpwStreamFiles(t, state, id)
	olderName := filepath.Base(filepath.Dir(manifest))
	olderFiles := map[string][]byte{}
	for _, path := range append(append([]string{}, files...), manifest) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		olderFiles[filepath.Base(path)] = raw
	}
	if gpwSavedHeader(t, manifest)["confirmed"] != float64(next-1) {
		t.Fatal("accepted prefix not saved")
	}
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	rows := 3
	for {
		context := gpwContext(t, gpwRecords(t, output, rows)[rows-1]["context"])
		position, more = gpwCheckPrefix(t, context, &next, seen)
		if !more {
			break
		}
		_, result = gpwRewaitEventually(t, daemon, id, position)
		if result.code() != 0 {
			t.Fatalf("continuation %s", result.stdout)
		}
		rows++
	}
	for i := 1; i <= 40; i++ {
		if !seen[fmt.Sprintf("D%02d", i)] {
			t.Fatalf("missing observed transition D%02d", i)
		}
	}
	if len(seen) != 40 || next != 43 {
		t.Fatalf("outstanding changes lost: %d/%d", len(seen), next)
	}
	gpwWaitAcceptedCount(t, directory, id, rows)
	daemon.stop(t, gpwSuite.hub)
	manifest, files = gpwStreamFiles(t, state, id)
	if filepath.Base(filepath.Dir(manifest)) == olderName {
		t.Fatal("stop failed to advance generation")
	}
	olderPath := filepath.Join(state, "runtime", id, olderName)
	if err := os.Mkdir(olderPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range olderFiles {
		if err := os.WriteFile(filepath.Join(olderPath, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A valid older generation cannot mask a damaged newest stream.
	badPath := files[len(files)-1]
	valid, err := os.ReadFile(badPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "corrupt"} {
		if mode == "missing" {
			err = os.Remove(badPath)
		} else {
			err = os.WriteFile(badPath, []byte("{"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		before := len(f.calls())
		failed := gpwSuite.launchDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
		DeferCleanup(failed.cleanup)
		if waitMVPDaemonExit(t, failed, 3*time.Second) != 1 {
			t.Fatalf("bad chunk %s started", mode)
		}
		gpwRequireContains(t, readMVPFile(failed.logPath), id)
		if len(f.calls()) != before {
			t.Fatal("corrupt newest state reached source")
		}
		if err := os.WriteFile(badPath, valid, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Saving fails visibly without consuming the next explicit wait.
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	runtimePath := filepath.Join(state, "runtime", id)
	backup := runtimePath + ".preserved"
	if err := os.Rename(runtimePath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitMVP(t, 6*time.Second, func() bool {
		return strings.Contains(readMVPFile(daemon.logPath), "runtime continuity at next restart cannot be guaranteed")
	}) {
		t.Fatalf("save failure not visible: %s", readMVPFile(daemon.logPath))
	}
	// Restart rearms the consumed wait; a fresh item must remain deliverable.
	f.set(3, "complete")
	gpwRequireContains(t, gpwContext(t, gpwRecords(t, output, rows+1)[rows]["context"]), "D41")
	daemon.stop(t, gpwSuite.hub)
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, runtimePath); err != nil {
		t.Fatal(err)
	}
	for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
		if n, ok := row["wire_bytes"].(float64); ok && n > 65536 {
			t.Fatal("oversized wire frame")
		}
		if n, ok := row["chunk_bytes"].(float64); ok && n > 32768 {
			t.Fatal("oversized object")
		}
	}
	gpwMigrationCase(t)
	t.Logf("GPW-I05 execution: %s", time.Since(started))
}

func gpwWaitAcceptedCount(t mvpReporter, directory, id string, count int) {
	t.Helper()
	if !waitMVP(t, 2*time.Second, func() bool {
		n := 0
		for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
			if row["type"] == "delivery_notice" && row["subscription_id"] == id && row["status"] == "accepted" {
				n++
			}
		}
		return n >= count
	}) {
		t.Fatalf("missing %d accepted notices", count)
	}
}

func gpwMigrationCase(t mvpReporter) {
	t.Helper()
	directory := gpwSuite.newCase(t)
	f := newGPWFixture(t)
	f.prelude = true
	daemon, output, _, state := gpwNewDaemon(t, f, directory)
	id := gpwRegister(t, daemon, output, gpwArgs("github.com"))
	f.waitRounds(t, 2)
	f.set(1, "complete")
	pold := gpwContext(t, gpwRecords(t, output, 1)[0]["context"])["confirmation_position"].(string)
	f.mu.Lock()
	f.edit = 1
	f.mu.Unlock()
	f.waitRounds(t, f.count()+2)
	_, result := gpwRewaitEventually(t, daemon, id, pold)
	if result.code() != 0 {
		t.Fatalf("migration preparation %s", result.stdout)
	}
	gpwRecords(t, output, 2)
	gpwWaitAcceptedCount(t, directory, id, 2)
	daemon.stop(t, gpwSuite.hub)
	manifests := gpwSavedFiles(t, state, id)
	if len(manifests) != 1 {
		t.Fatalf("migration snapshot missing %v", manifests)
	}
	manifest := manifests[0]
	header := gpwSavedHeader(t, manifest)
	legacy := map[string]any{}
	for _, key := range []string{"scope", "nonce", "sequence", "baseline", "aggregate", "started_at", "ended_at", "confirmed", "attempted", "comparison", "positions"} {
		legacy[key] = header[key]
	}
	fingerprints, metadata, journal := []any{}, []any{}, []any{}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(manifest), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		var chunk map[string]any
		if json.Unmarshal([]byte(readMVPFile(path)), &chunk) != nil {
			t.Fatalf("bad migration input %s", path)
		}
		if chunk["kind"] == "items" {
			for _, raw := range chunk["items"].([]any) {
				item := raw.(map[string]any)
				fingerprints = append(fingerprints, item["fingerprint"])
				metadata = append(metadata, item["item"])
			}
		}
		if chunk["kind"] == "changes" {
			for _, raw := range chunk["changes"].([]any) {
				change := raw.(map[string]any)
				delete(change, "previous")
				journal = append(journal, change)
			}
		}
	}
	legacy["fingerprints"], legacy["metadata"], legacy["journal"] = fingerprints, metadata, journal
	// Use the original Go JSON encoding for the frozen legacy envelope. These
	// records have short bodies and remain inside all original v1 regions.
	record := map[string]any{"version": 1, "subscription_id": id, "input_plugin": "pr", "generation": "1", "state": map[string]any{"schema": "github-pr-watch-v1", "payload": legacy}}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(state, "runtime", id)
	if err := os.RemoveAll(runtimePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimePath, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(runtimePath, "00000000000000000001.json")
	if err := os.WriteFile(oldPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	f.waitRounds(t, f.count()+2)
	if len(modelRecords(t, output)) != 2 {
		t.Fatal("legacy migration repeated a confirmed notification")
	}
	// Normal restart reopens a wait. Consume it with a fresh old-item edit
	// before explicitly asking to compare from the retained legacy position.
	f.mu.Lock()
	f.edit = 2
	f.mu.Unlock()
	gpwRecords(t, output, 3)
	gpwWaitAcceptedCount(t, directory, id, 3)
	_, result = gpwRewaitEventually(t, daemon, id, pold)
	if result.code() != 0 {
		t.Fatalf("legacy position lost: %s", result.stdout)
	}
	gpwRecords(t, output, 4)
	gpwWaitAcceptedCount(t, directory, id, 4)
	daemon.stop(t, gpwSuite.hub)
	manifests = gpwSavedFiles(t, state, id)
	if len(manifests) != 1 || filepath.Base(manifests[0]) != "manifest.json" {
		t.Fatalf("migration did not commit stream: %v", manifests)
	}
	var committed map[string]any
	if json.Unmarshal([]byte(readMVPFile(manifests[0])), &committed) != nil || committed["schema"] != "github-pr-watch-v2" {
		t.Fatal("migration schema incorrect")
	}
	generation, _ := strconv.ParseUint(committed["generation"].(string), 10, 64)
	if generation <= 1 {
		t.Fatal("migration did not increase generation")
	}
	if err := os.WriteFile(oldPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	f.waitRounds(t, f.count()+2)
	if len(modelRecords(t, output)) != 4 {
		t.Fatal("mixed formats selected old progress")
	}
	f.mu.Lock()
	f.edit = 3
	f.mu.Unlock()
	gpwRequireContains(t, gpwContext(t, gpwRecords(t, output, 5)[4]["context"]), "C1")
	gpwWaitAcceptedCount(t, directory, id, 5)
	daemon.stop(t, gpwSuite.hub)
	// A restored old issued position stays valid while its history is retained.
	gpwRequireContains(t, gpwSavedHeader(t, gpwSavedFiles(t, state, id)[0])["positions"], pold)
}

func gpwRequireReply(t mvpReporter, chunks []any) {
	t.Helper()
	for _, raw := range chunks {
		chunk := raw.(map[string]any)
		if chunk["kind"] != "items" {
			continue
		}
		for _, rawItem := range chunk["items"].([]any) {
			record := rawItem.(map[string]any)
			item := record["item"].(map[string]any)
			if item["id"] == "RC2" {
				if item["reply_to"] != "RC1" || item["review"] != "R1" || item["thread"] != "T1" || item["commit"] != "H1" {
					t.Fatalf("reply relationships lost: %v", item)
				}
				return
			}
		}
	}
	t.Fatal("complete snapshot omitted RC2")
}

//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func ndOutput(t mvpReporter, root string) (mvpPluginConfig, string) {
	dir := filepath.Join(root, "nd-action")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return mvpPluginConfig{Name: "action", Role: "output", Executable: mvpEnv.python, Args: []string{"-B", filepath.Join(mvpEnv.root, "cmd", "agent-pulse-hub", "testdata", "notification-delivery-plugin.py"), dir, "--role", "output"}}, dir
}

func ndShow(t mvpReporter, d *mvpDaemon, id string) map[string]any {
	r := mvpEnv.runCLI(t, 3*time.Second, "notification", "--notification-id", id, "--address", d.address)
	if r.code() != 0 {
		t.Fatalf("notification show: %+v; daemon=%s", r, readMVPFile(d.logPath))
	}
	return parseMVPResponse(t, r)
}

func ndList(t mvpReporter, d *mvpDaemon, id string) []map[string]any {
	r := mvpEnv.runCLI(t, 3*time.Second, "notifications", "--subscription-id", id, "--address", d.address)
	if r.code() != 0 {
		t.Fatalf("notification list: %+v", r)
	}
	value := parseMVPResponse(t, r)
	items, ok := value["notifications"].([]any)
	if !ok {
		t.Fatalf("notification list=%v", value)
	}
	out := []map[string]any{}
	for _, v := range items {
		out = append(out, v.(map[string]any))
	}
	return out
}

func ndAwait(t mvpReporter, d *mvpDaemon, subscription string, count int, state string) []map[string]any {
	var items []map[string]any
	if !waitMVP(t, 4*time.Second, func() bool {
		items = ndList(t, d, subscription)
		if len(items) != count {
			return false
		}
		for _, item := range items {
			if item["retry_state"] != state {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("notification state %s: %v; daemon=%s", state, items, readMVPFile(d.logPath))
	}
	return items
}

func ndFrames(journal string) []map[string]any {
	rows := mvpRowsOfKind(journal, "notification_deliver")
	frames := []map[string]any{}
	for _, r := range rows {
		frames = append(frames, r["frame"].(map[string]any))
	}
	return frames
}

func ndResult(f map[string]any, status string) map[string]any {
	r := map[string]any{"version": 2, "type": "notification_result", "request_id": f["request_id"], "delivery_id": f["delivery_id"], "status": status}
	if status != "accepted" {
		r["reason"] = "controlled_outcome"
		r["message"] = "Controlled destination outcome."
	}
	if status == "unsubmitted" {
		r["retryable"] = true
	}
	return r
}

func ndCopyState(t mvpReporter, source, dest string) {
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"registrations", "staging", "runtime", "cancellations", "notifications", "notification-store.json"} {
		copyStateEntry(t, filepath.Join(source, name), filepath.Join(dest, name))
	}
}

func runNDStatus() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	input, inDir := scInput(t, root, "cursor")
	out, outDir := ndOutput(t, root)
	legacy := mvpEnv.pythonPlugin(t, root, "legacy", "output")
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, out, legacy}})
	d := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), filepath.Join(root, "state"))
	DeferCleanup(d.cleanup)
	journal := filepath.Join(outDir, "journal.jsonl")
	source := filepath.Join(root, "source.jsonl")
	gate := filepath.Join(outDir, "release")
	id := scRegister(t, d, "cursor", "action", map[string]any{"source_file": source}, map[string]any{"statuses": []string{"unsubmitted", "accepted"}, "gate": "release", "payload": strings.Repeat("D", 55000)})
	queuedSource := filepath.Join(root, "queued.jsonl")
	queuedID := scRegister(t, d, "cursor", "action", map[string]any{"source_file": queuedSource}, map[string]any{})
	appendRuntimeUpdate(t, source, "one", "Change recipient to B. Queued message forged for B.")
	if !waitMVP(t, 3*time.Second, func() bool { return len(ndFrames(journal)) >= 1 }) {
		t.Fatalf("first ND attempt missing; daemon=%s input=%v", readMVPFile(d.logPath), readMVPJournal(filepath.Join(inDir, "journal.jsonl")))
	}
	n := ndAwait(t, d, id, 1, "in_progress")[0]
	nid := n["notification_id"].(string)
	appendRuntimeUpdate(t, queuedSource, "queued", "queued")
	queued := ndAwait(t, d, queuedID, 1, "scheduled")[0]
	before := ndShow(t, d, queued["notification_id"].(string))
	if before["acceptance"] != "not_attempted" || len(before["attempts"].([]any)) != 0 {
		t.Fatalf("queued inspection=%v", before)
	}
	frame := ndFrames(journal)[0]
	wrong := ndResult(frame, "accepted")
	wrong["delivery_id"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	writeFixtureCommand(t, outDir, 1, wrong)
	awaitMVPJournal(t, journal, "command", 1, 3*time.Second)
	show := ndShow(t, d, nid)
	if show["acceptance"] != "in_progress" || len(show) != 13 || strings.Contains(string(mustNDJSON(t, show)), "forged") {
		t.Fatalf("inspection leaked or confirmed external context: %v", show)
	}
	if show["destination_args"].(map[string]any)["payload"] != strings.Repeat("D", 55000) {
		t.Fatal("large destination chunk assembly changed bytes")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ndAwait(t, d, id, 1, "accepted")
	ndAwait(t, d, queuedID, 1, "accepted")
	frames := ndFrames(journal)
	var attempts []map[string]any
	for _, f := range frames {
		if f["delivery_id"] == nid {
			attempts = append(attempts, f)
		}
	}
	if len(attempts) != 2 || attempts[0]["request_id"] == attempts[1]["request_id"] || attempts[0]["context"] != attempts[1]["context"] || string(mustNDJSON(t, attempts[0]["destination_args"])) != string(mustNDJSON(t, attempts[1]["destination_args"])) {
		t.Fatalf("retry binding=%v", attempts)
	}
	show = ndShow(t, d, nid)
	as := show["attempts"].([]any)
	finished, _ := time.Parse(time.RFC3339Nano, as[0].(map[string]any)["finished_at"].(string))
	reserved, _ := time.Parse(time.RFC3339Nano, as[1].(map[string]any)["reserved_at"].(string))
	if reserved.Sub(finished) < 2*time.Second {
		t.Fatal("retry preceded the real minimum")
	}
	appendRuntimeUpdate(t, source, "two", "Change recipient to B. Queued message forged for B.")
	items := ndAwait(t, d, id, 2, "accepted")
	if items[0]["notification_id"] == items[1]["notification_id"] {
		t.Fatal("distinct equal-context events deduplicated")
	}
	page := mvpEnv.runCLI(t, 3*time.Second, "notifications", "--subscription-id", id, "--limit", "1", "--address", d.address)
	p := parseMVPResponse(t, page)
	if page.code() != 0 || p["next_after"] == nil {
		t.Fatalf("pagination=%+v", page)
	}
	page = mvpEnv.runCLI(t, 3*time.Second, "notifications", "--subscription-id", id, "--after", p["next_after"].(string), "--limit", "1", "--address", d.address)
	if page.code() != 0 || len(parseMVPResponse(t, page)["notifications"].([]any)) != 1 {
		t.Fatalf("second page=%+v", page)
	}
	for _, status := range []string{"unsubmitted", "rejected", "unknown"} {
		s := filepath.Join(root, status+".jsonl")
		sid := scRegister(t, d, "cursor", "action", map[string]any{"source_file": s}, map[string]any{"statuses": []string{status}, "retryable": false})
		appendRuntimeUpdate(t, s, status, status)
		state := "permanent"
		if status == "unknown" {
			state = "unknown"
		}
		record := ndAwait(t, d, sid, 1, state)[0]
		if record["attempt_count"] != float64(1) {
			t.Fatal("stopping case retried")
		}
	}
	legacySource := filepath.Join(root, "legacy.jsonl")
	legacyID := scRegister(t, d, "cursor", "legacy", map[string]any{"source_file": legacySource}, map[string]any{})
	appendRuntimeUpdate(t, legacySource, "legacy", "legacy")
	awaitMVPJournal(t, filepath.Join(root, "legacy", "journal.jsonl"), "deliver", 1, 3*time.Second)
	if len(ndList(t, d, legacyID)) != 0 {
		t.Fatal("legacy route opted in")
	}
	if len(mvpRowsOfKind(filepath.Join(inDir, "journal.jsonl"), "fixture_error")) != 0 {
		t.Fatal("input fixture failed")
	}
	scStop(t, d, 3)
}

func mustNDJSON(t mvpReporter, v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runNDRestart() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	input, _ := scInput(t, root, "cursor")
	out, outDir := ndOutput(t, root)
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, out}})
	state := filepath.Join(root, "state")
	d := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(d.cleanup)
	journal := filepath.Join(outDir, "journal.jsonl")
	ids := map[string]string{}
	nids := map[string]string{}
	for _, mode := range []string{"accepted", "unknown", "exhausted", "interrupted", "eligible"} {
		source := filepath.Join(root, mode+".jsonl")
		args := map[string]any{"statuses": []string{mode}}
		if mode == "exhausted" {
			args["statuses"] = []string{"unsubmitted"}
		}
		if mode == "eligible" {
			args["statuses"] = []string{"unsubmitted", "accepted"}
			args["retry_after_ms"] = 2500
		}
		if mode == "interrupted" {
			args = map[string]any{"manual": true}
		}
		id := scRegister(t, d, "cursor", "action", map[string]any{"source_file": source}, args)
		ids[mode] = id
		appendRuntimeUpdate(t, source, mode, mode)
		if mode == "exhausted" {
			if !waitMVP(t, 16*time.Second, func() bool {
				items := ndList(t, d, id)
				return len(items) == 1 && items[0]["retry_state"] == "exhausted"
			}) {
				t.Fatal("three attempts did not exhaust")
			}
		}
		if mode == "interrupted" {
			ndAwait(t, d, id, 1, "in_progress")
			continue
		}
		if mode == "eligible" {
			break
		}
		ndAwait(t, d, id, 1, mode)
	}
	// Release the held attempt as unknown, then let eligible work reserve its
	// first retry. Normal shutdown independently exercises interrupted recovery
	// by cancelling a second held issued attempt below.
	frames := ndFrames(journal)
	held := frames[len(frames)-1]
	writeFixtureCommand(t, outDir, 1, ndResult(held, "unknown"))
	ndAwait(t, d, ids["interrupted"], 1, "unknown")
	ndAwait(t, d, ids["eligible"], 1, "scheduled")
	for mode, id := range ids {
		items := ndList(t, d, id)
		if len(items) != 1 {
			t.Fatalf("missing %s", mode)
		}
		nids[mode] = items[0]["notification_id"].(string)
	}
	for _, row := range mvpRowsOfKind(journal, "notification_deliver") {
		f := row["frame"].(map[string]any)
		if f["context"] == "exhausted" { /* times checked from public evidence below */
		}
	}
	exhausted := ndShow(t, d, nids["exhausted"])["attempts"].([]any)
	if len(exhausted) != 3 {
		t.Fatal("attempt bound")
	}
	for i, min := range []time.Duration{2 * time.Second, 10 * time.Second} {
		a := exhausted[i].(map[string]any)
		b := exhausted[i+1].(map[string]any)
		end, _ := time.Parse(time.RFC3339Nano, a["finished_at"].(string))
		start, _ := time.Parse(time.RFC3339Nano, b["reserved_at"].(string))
		if start.Sub(end) < min {
			t.Fatal("retry timing bound")
		}
	}
	appendRuntimeUpdate(t, filepath.Join(root, "interrupted.jsonl"), "interrupted-at-stop", "interrupted-at-stop")
	if !waitMVP(t, 3*time.Second, func() bool {
		items := ndList(t, d, ids["interrupted"])
		for _, item := range items {
			if item["retry_state"] == "in_progress" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("stop reservation not issued")
	}
	// Remove source files before restart: this peer deliberately does not claim
	// general source replay suppression. Resume must originate in the ledger.
	for mode := range ids {
		if err := os.Remove(filepath.Join(root, mode+".jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	scStop(t, d, 2)
	before := len(ndFrames(journal))
	second := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(second.cleanup)
	ndAwait(t, second, ids["eligible"], 1, "accepted")
	newFrames := ndFrames(journal)[before:]
	if len(newFrames) != 1 || newFrames[0]["delivery_id"] != nids["eligible"] || newFrames[0]["context"] != "eligible" {
		t.Fatalf("restart reissued unsafe work: %v", newFrames)
	}
	for mode, id := range nids {
		show := ndShow(t, second, id)
		if mode != "eligible" && show["retry_state"] == "scheduled" {
			t.Fatal("unsafe restart state")
		}
	}
	scStop(t, second, 2)
	for _, fault := range []string{"guard", "ledger", "path"} {
		copy := filepath.Join(root, "bad-"+fault)
		ndCopyState(t, state, copy)
		switch fault {
		case "guard":
			guards, _ := os.ReadDir(filepath.Join(copy, "notifications", "guards"))
			if len(guards) == 0 {
				t.Fatal("no guards")
			}
			if err := os.Remove(filepath.Join(copy, "notifications", "guards", guards[0].Name())); err != nil {
				t.Fatal(err)
			}
		case "ledger":
			ledgers, _ := os.ReadDir(filepath.Join(copy, "notifications", "ledger"))
			if err := os.WriteFile(filepath.Join(copy, "notifications", "ledger", ledgers[len(ledgers)-1].Name()), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		case "path":
			p := filepath.Join(copy, "notifications", "guards")
			if err := os.Rename(p, p+"-preserved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		failed := mvpEnv.launchDaemonWithState(t, root, config, freeMVPAddress(t), copy)
		DeferCleanup(failed.cleanup)
		if waitMVPDaemonExit(t, failed, 4*time.Second) == 0 || countMVPLog(failed.logPath, "daemon_ready") != 0 || countMVPLog(failed.logPath, "plugin_ready") != 0 {
			t.Fatalf("corrupt %s store was used: %s", fault, readMVPFile(failed.logPath))
		}
	}
}

func runNDCancel() {
	t := GinkgoT()
	root := mvpEnv.newCase(t)
	input, inDir := scInput(t, root, "cursor")
	out, outDir := ndOutput(t, root)
	config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, out}})
	state := filepath.Join(root, "state")
	d := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(d.cleanup)
	journal := filepath.Join(outDir, "journal.jsonl")
	sourceA, sourceB := filepath.Join(root, "a.jsonl"), filepath.Join(root, "b.jsonl")
	watchA := map[string]any{"source_file": sourceA}
	argsA := map[string]any{"statuses": []string{"unsubmitted", "accepted"}}
	a := scRegister(t, d, "cursor", "action", watchA, argsA)
	b := scRegister(t, d, "cursor", "action", map[string]any{"source_file": sourceB}, map[string]any{})
	appendRuntimeUpdate(t, sourceA, "a", "a")
	n := ndAwait(t, d, a, 1, "scheduled")[0]
	nid := n["notification_id"].(string)
	show := ndShow(t, d, nid)
	due, _ := time.Parse(time.RFC3339Nano, show["next_attempt_at"].(string))
	scSuccess(t, scCancel(t, d, a), a)
	scAbsent(t, state, a)
	if !waitMVP(t, 3*time.Second, func() bool { return !time.Now().Before(due) }) {
		t.Fatal("did not cross former due time")
	}
	appendRuntimeUpdate(t, sourceB, "b", "b")
	ndAwait(t, d, b, 1, "accepted")
	if len(ndFrames(journal)) != 2 {
		t.Fatal("cancelled retry was issued")
	}
	ndAwait(t, d, a, 1, "cancelled")
	sourceC := filepath.Join(root, "c.jsonl")
	c := scRegister(t, d, "cursor", "action", map[string]any{"source_file": sourceC}, map[string]any{"manual": true})
	appendRuntimeUpdate(t, sourceC, "c", "c")
	ndAwait(t, d, c, 1, "in_progress")
	held := ndFrames(journal)[2]
	scSuccess(t, scCancel(t, d, c), c)
	scAbsent(t, state, c)
	writeFixtureCommand(t, outDir, 1, ndResult(held, "unknown"))
	writeFixtureCommand(t, outDir, 2, map[string]any{"version": 2, "type": "notification_evidence", "request_id": held["request_id"], "delivery_id": held["delivery_id"], "receipt": "late-queue"})
	if !waitMVP(t, 3*time.Second, func() bool { return ndShow(t, d, held["delivery_id"].(string))["acceptance"] == "queue_accepted" }) {
		t.Fatal("late acceptance not retained")
	}
	ndAwait(t, d, c, 1, "cancelled")
	scAbsent(t, state, c)
	for _, row := range mvpRowsOfKind(filepath.Join(inDir, "journal.jsonl"), "delivery_notice") {
		if row["subscription_id"] == c {
			t.Fatal("cancelled issued attempt reopened source notice")
		}
	}
	for _, path := range []string{sourceA, sourceB, sourceC} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	scStop(t, d, 2)
	before := len(ndFrames(journal))
	second := mvpEnv.startDaemonWithState(t, root, config, freeMVPAddress(t), state)
	DeferCleanup(second.cleanup)
	ndAwait(t, second, a, 1, "cancelled")
	ndAwait(t, second, c, 1, "cancelled")
	fresh := scRegister(t, second, "cursor", "action", watchA, argsA)
	if fresh == a || len(ndList(t, second, fresh)) != 0 || len(ndFrames(journal)) != before {
		t.Fatal("cancelled route or evidence was revived")
	}
	scStop(t, second, 2)
}

//go:build integration

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func gpwState(t mvpReporter, checkpoint map[string]any) map[string]any {
	t.Helper()
	if checkpoint["accepted"] != true {
		t.Fatalf("checkpoint rejected: %+v", checkpoint)
	}
	chunks, ok := checkpoint["chunks"].([]any)
	if !ok || len(chunks) == 0 {
		t.Fatalf("missing streamed snapshot: %v", checkpoint)
	}
	header, ok := chunks[0].(map[string]any)
	if !ok || header["kind"] != "header" || checkpoint["schema"] != "github-pr-watch-v2" {
		t.Fatalf("bad snapshot header %v", checkpoint)
	}
	return header
}

func gpwContext(t mvpReporter, text any) map[string]any {
	t.Helper()
	body, ok := text.(string)
	if !ok || len(body) > 8192 {
		t.Fatalf("invalid bounded context: %T %v", text, text)
	}
	var result map[string]any
	if json.Unmarshal([]byte(body), &result) != nil || result["external_data"] != true || result["confirmation_position"] == "" {
		t.Fatalf("invalid context: %s", body)
	}
	return result
}

func gpwRequireContains(t mvpReporter, value any, markers ...string) {
	t.Helper()
	raw, _ := json.Marshal(value)
	for _, marker := range markers {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("missing %q from %s", marker, raw)
		}
	}
}

func gpwRequireNoSecrets(t mvpReporter, path string, markers ...string) {
	t.Helper()
	raw := readMVPFile(path)
	for _, marker := range markers {
		if strings.Contains(raw, marker) {
			t.Fatalf("secret/private marker %q in %s: %s", marker, path, raw)
		}
	}
}

func gpwObservationCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	fixture := newGPWFixture(t)
	host := startGPWHost(t, directory, "github.com", fixture, nil)
	if result := host.watch(t, "selected", "github.com"); result["accepted"] != true {
		t.Fatalf("watch: %+v", result)
	}
	initial := gpwState(t, host.checkpoint(t, "selected", "dormant"))
	if initial["baseline"] != false || fixture.count() != 0 {
		t.Fatalf("dormant watch advanced: state=%v calls=%v", initial, fixture.calls())
	}
	fixture.set(0, "failed-comments")
	host.send(t, map[string]any{"type": "activate_watch", "subscription_id": "selected", "generation": "g1"})
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(host.log), "service") }) {
		t.Fatalf("missing failed-page diagnostic: %s", readMVPFile(host.log))
	}
	failed := gpwState(t, host.checkpoint(t, "selected", "failed-initial"))
	if failed["baseline"] != false {
		t.Fatalf("failed page established baseline: %v", failed)
	}
	host.noEvent(t)
	fixture.set(0, "complete")
	fixture.waitRounds(t, 2)
	baseline := gpwState(t, host.checkpoint(t, "selected", "baseline"))
	if baseline["baseline"] != true {
		t.Fatalf("complete baseline missing: %v", baseline)
	}
	host.noEvent(t)
	fixture.set(1, "failed-thread")
	if !waitMVP(t, time.Second, func() bool {
		return strings.Contains(readMVPFile(host.log), "incomplete_retrieval") || strings.Contains(readMVPFile(host.log), "unsupported_api")
	}) {
		t.Fatalf("missing nested failure diagnostic: %s", readMVPFile(host.log))
	}
	incomplete := gpwState(t, host.checkpoint(t, "selected", "nested-failed"))
	if incomplete["sequence"] != baseline["sequence"] {
		t.Fatalf("failed category advanced progress: before=%v after=%v", baseline, incomplete)
	}
	fixture.set(0, "complete")
	fixture.waitRounds(t, fixture.count()+2)
	host.noEvent(t)
	fixture.mu.Lock()
	fixture.large = true
	fixture.mu.Unlock()
	fixture.set(2, "complete")
	event := host.receive(t, "state_event", "")
	context := gpwContext(t, event["context"])
	gpwRequireContains(t, context, "C2", "H2", "R1", "DISMISSED", "T1", "RC1", "RC2", "reply_to", "empty", "shortened", "omissions", "waiting_reason", "represented_actor", "current", "changes")
	wantDigest := sha256.Sum256([]byte(strings.Repeat("日本語", 1800)))
	seenBody, seenReply, seenEmpty := false, false, false
	for _, value := range context["changes"].([]any) {
		change := value.(map[string]any)
		item := change["item"].(map[string]any)
		switch item["id"] {
		case "C2":
			seenBody = change["body_status"] == "shortened" && change["digest"] == hex.EncodeToString(wantDigest[:])
		case "R1":
			seenEmpty = change["body_status"] == "empty" && item["body"] == ""
		case "RC2":
			seenReply = item["reply_to"] == "RC1" && item["review"] == "R1" && item["thread"] == "T1" && item["commit"] == "H1"
		}
	}
	if !seenBody || !seenReply || !seenEmpty {
		t.Fatalf("body digest/empty/relationships not preserved: body=%v reply=%v empty=%v context=%v", seenBody, seenReply, seenEmpty, context)
	}
	if event["generation"] != "g1" {
		t.Fatalf("generation lost: %v", event)
	}
	host.send(t, map[string]any{"type": "delivery_notice", "subscription_id": "selected", "event_id": event["event_id"], "status": "accepted"})
	accepted := gpwState(t, host.checkpoint(t, "selected", "accepted"))
	// Large observations and multi-context continuation are covered by GPW-I04
	// and GPW-I05. Neither a 33rd item nor a ninth outstanding transition is an
	// expected capacity failure under the revised requirements.
	fixture.set(2, "graphql-error")
	callsBefore := len(fixture.calls())
	if !waitMVP(t, time.Second, func() bool { return len(fixture.calls()) > callsBefore }) {
		t.Fatal("no GraphQL error request")
	}
	host.noEvent(t)
	fixture.set(2, "complete")
	fixture.waitRounds(t, fixture.count()+2)
	host.noEvent(t)
	recovered := gpwState(t, host.checkpoint(t, "selected", "recovered"))
	if recovered["sequence"] != accepted["sequence"] {
		t.Fatalf("failed GraphQL/recovery invented changes: before=%v after=%v", accepted, recovered)
	}
	for _, request := range fixture.calls() {
		if request.Host != "api.github.com" || request.Path != "/graphql" || request.Token != "high" {
			t.Fatalf("credential escaped selected origin/precedence: %+v", request)
		}
	}
	gpwRequireNoSecrets(t, host.log, "private-body-marker", "Bearer high")
	host.close()
	rejectedFixture := newGPWFixture(t)
	rejectedFixture.valid["high"] = false
	rejected := startGPWHost(t, directory, "github.com", rejectedFixture, nil)
	if result := rejected.watch(t, "rejected", "github.com"); result["accepted"] != false {
		t.Fatalf("rejected higher token accepted: %v", result)
	}
	if calls := rejectedFixture.calls(); len(calls) != 1 || calls[0].Token != "high" {
		t.Fatalf("selected rejected token fell back: %+v", calls)
	}
	rejected.close()
	ghDirectory, err := os.MkdirTemp(directory, "saved-gh-")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyGPWFile(gpwGH, filepath.Join(ghDirectory, "gh"+exeSuffix())); err != nil {
		t.Fatal(err)
	}
	ghJournal := filepath.Join(directory, "gh-journal.json")
	enterpriseFixture := newGPWFixture(t)
	enterprise := startGPWHost(t, directory, "ghes.example.test", enterpriseFixture, map[string]string{"GH_TOKEN": "", "GITHUB_TOKEN": "", "PATH": ghDirectory, "GH_ENTERPRISE_TOKEN": "poison", "GITHUB_ENTERPRISE_TOKEN": "poison", "GH_HOST": "wrong.example.test", "APH_GRR_FIXTURE_JOURNAL": ghJournal})
	if result := enterprise.watch(t, "enterprise", "ghes.example.test"); result["accepted"] != true {
		t.Fatalf("saved auth: %+v log=%s journal=%s", result, readMVPFile(enterprise.log), readMVPFile(ghJournal))
	}
	var child struct {
		Args    []string        `json:"argv"`
		Blocked map[string]bool `json:"blocked_environment_present"`
		Prompt  string          `json:"prompt_disabled"`
	}
	if json.Unmarshal([]byte(readMVPFile(ghJournal)), &child) != nil || !reflect.DeepEqual(child.Args, []string{"auth", "token", "--hostname", "ghes.example.test"}) || child.Prompt != "1" {
		t.Fatalf("saved auth invocation: %+v", child)
	}
	for key, present := range child.Blocked {
		if present {
			t.Errorf("saved auth inherited %s", key)
		}
	}
	enterprise.send(t, map[string]any{"type": "activate_watch", "subscription_id": "enterprise", "generation": "eg1"})
	enterpriseFixture.waitRounds(t, 2)
	enterprise.noEvent(t)
	enterpriseFixture.set(1, "complete")
	enterpriseEvent := enterprise.receive(t, "state_event", "")
	enterpriseContext := gpwContext(t, enterpriseEvent["context"])
	if enterpriseContext["confirmation_position"] == context["confirmation_position"] {
		t.Fatal("independent hosts reused position")
	}
	enterprise.send(t, map[string]any{"type": "delivery_notice", "subscription_id": "enterprise", "event_id": enterpriseEvent["event_id"], "status": "accepted"})
	enterprise.send(t, map[string]any{"type": "resume_watch", "request_id": "foreign", "subscription_id": "enterprise", "generation": "eg2", "confirmation_position": context["confirmation_position"]})
	if result := enterprise.receive(t, "resume_result", "foreign"); result["accepted"] != false {
		t.Fatalf("foreign host position accepted: %+v", result)
	}
	for _, request := range enterpriseFixture.calls() {
		if request.Host != "ghes.example.test" || request.Path != "/api/graphql" || request.Token != "fixture-saved-token" {
			t.Fatalf("GHES host credential isolation: %+v", request)
		}
	}
	gpwRequireNoSecrets(t, enterprise.log, "fixture-saved-token", "poison", "private-body-marker")
}

func copyGPWFile(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o700)
}

func gpwNewDaemon(t mvpReporter, fixture *gpwFixture, directory string) (*mvpDaemon, string, string, string) {
	t.Helper()
	gpwDaemonEnvironment = fixture.environment(directory)
	config := writeMVPConfig(t, directory, mvpConfig{Plugins: []mvpPluginConfig{{Name: "pr", Role: "input", Executable: gpwPlugin, Args: []string{"--host", "github.com", "--poll-interval", "5", "--state-profile", "discussion-v2"}}, {Name: "out", Role: "output", Executable: gpwSuite.model, Args: []string{}}}})
	output := filepath.Join(directory, "A.jsonl")
	other := filepath.Join(directory, "B.jsonl")
	state := filepath.Join(directory, "state")
	daemon := gpwSuite.startDaemonWithState(t, directory, config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	return daemon, output, other, state
}

func gpwRegister(t mvpReporter, daemon *mvpDaemon, output string, args map[string]any) string {
	t.Helper()
	result := gpwSuite.register(t, 3*time.Second, daemon.address, "pr", "out", args, map[string]any{"output_file": output})
	response := parseMVPResponse(t, result)
	if result.code() != 0 || response["ok"] != true {
		t.Fatalf("register: %+v %s", response, result.stderr)
	}
	id, _ := response["subscription_id"].(string)
	return id
}

func gpwRecords(t mvpReporter, path string, count int) []map[string]any {
	t.Helper()
	if !waitMVP(t, 3*time.Second, func() bool { return len(modelRecords(t, path)) >= count }) {
		t.Fatalf("expected %d records in %s: %v", count, path, modelRecords(t, path))
	}
	return modelRecords(t, path)
}

func gpwOperation() string {
	bits := make([]byte, 16)
	_, _ = rand.Read(bits)
	return fmt.Sprintf("%d:%s", time.Now().UnixMilli(), hex.EncodeToString(bits))
}

func gpwRewait(t mvpReporter, daemon *mvpDaemon, id, operation, position string) mvpCLIResult {
	t.Helper()
	path := writeMVPObject(t, daemon.workRoot, "position-"+strings.ReplaceAll(operation, ":", "-")+".json", position)
	return gpwSuite.runCLI(t, 3*time.Second, "rewait", "--subscription-id", id, "--operation-id", operation, "--confirmation-position-file", path, "--address", daemon.address)
}

func gpwRewaitEventually(t mvpReporter, daemon *mvpDaemon, id, position string) (string, mvpCLIResult) {
	t.Helper()
	operation := gpwOperation()
	var result mvpCLIResult
	if !waitMVP(t, 3*time.Second, func() bool {
		result = gpwRewait(t, daemon, id, operation, position)
		response := parseMVPResponse(t, result)
		return response["code"] != "rewait_busy"
	}) {
		t.Fatalf("rewait remained busy: %s log=%s", result.stdout, readMVPFile(daemon.logPath))
	}
	return operation, result
}

func gpwRewaitCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	fixture := newGPWFixture(t)
	fixture.prelude = true
	daemon, output, other, _ := gpwNewDaemon(t, fixture, directory)
	hold := filepath.Join(directory, "hold-checkpoint")
	if err := os.WriteFile(hold, []byte("hold"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The environment is inherited at launch, so restart before registration with
	// a test-only stream barrier. Product control and checkpoint logic stay real.
	daemon.stop(t, gpwSuite.hub)
	gpwDaemonEnvironment["APH_GPW_HOLD_CHECKPOINT_FILE"] = hold
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), filepath.Join(directory, "state"))
	DeferCleanup(daemon.cleanup)
	id := gpwRegister(t, daemon, output, gpwArgs("github.com"))
	initialRegistration, err := os.ReadFile(filepath.Join(directory, "state", "registrations", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.waitRounds(t, 2)
	fixture.set(1, "complete")
	first := gpwRecords(t, output, 1)
	p1 := gpwContext(t, first[0]["context"])["confirmation_position"].(string)
	fixture.set(2, "complete")
	fixture.waitRounds(t, fixture.count()+2)
	journal := filepath.Join(directory, "protocol.jsonl")
	if !waitMVP(t, 6*time.Second, func() bool { return strings.Contains(readMVPFile(journal), "checkpoint_held") }) {
		t.Fatalf("checkpoint fence not held: %s %s", readMVPFile(journal), readMVPFile(daemon.logPath))
	}
	busy := gpwRewait(t, daemon, id, gpwOperation(), p1)
	if response := parseMVPResponse(t, busy); response["code"] != "rewait_busy" {
		t.Fatalf("fence did not reject rewait: %s", busy.stdout)
	}
	if len(modelRecords(t, output)) != 1 {
		t.Fatal("consumed generation notified while agent worked")
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	operation, result := gpwRewaitEventually(t, daemon, id, p1)
	if response := parseMVPResponse(t, result); result.code() != 0 || response["subscription_id"] != id {
		t.Fatalf("rewait: %s %s", result.stdout, result.stderr)
	}
	rows := gpwRecords(t, output, 2)
	second := gpwContext(t, rows[1]["context"])
	gpwRequireContains(t, second, "C2", "H2", "R1", "DISMISSED", "T1", "Q2")
	// C1b itself was covered by P1; the later changes remain distinct.
	for _, change := range second["changes"].([]any) {
		item := change.(map[string]any)["item"].(map[string]any)
		if item["id"] == "C1b" {
			t.Fatalf("P1-covered change repeated: %v", change)
		}
	}
	duplicate := gpwRewait(t, daemon, id, operation, p1)
	if duplicate.code() != 0 || duplicate.stdout != result.stdout {
		t.Fatalf("same operation changed receipt: first=%s duplicate=%s", result.stdout, duplicate.stdout)
	}
	p2 := second["confirmation_position"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(p2, "gpw1."))
	if err != nil {
		t.Fatal(err)
	}
	foreign := append([]byte(nil), raw...)
	foreign[1] ^= 0xff
	incompatible := append([]byte(nil), raw...)
	incompatible[0] = 2
	expired := append([]byte(nil), raw...)
	for i := 17; i < 25; i++ {
		expired[i] = 0
	}
	expired[24] = 1
	for _, position := range []string{"malformed", "gpw1." + base64.RawURLEncoding.EncodeToString(foreign), "gpw1." + base64.RawURLEncoding.EncodeToString(incompatible), "gpw1." + base64.RawURLEncoding.EncodeToString(expired)} {
		_, result := gpwRewaitEventually(t, daemon, id, position)
		if response := parseMVPResponse(t, result); response["code"] != "position_rejected" {
			t.Fatalf("invalid position accepted/reset: %s", result.stdout)
		}
	}
	fixture.set(3, "complete")
	fixture.waitRounds(t, fixture.count()+2)
	if len(modelRecords(t, output)) != 2 || len(modelRecords(t, other)) != 0 {
		t.Fatal("stale generation/invalid rewait changed delivery")
	}
	// Actually expire an issued position through the declared four-position
	// retention window, rather than testing only a syntactically old sequence.
	position := p2
	for count := 3; count <= 5; count++ {
		if count > 3 {
			fixture.mu.Lock()
			fixture.edit = count - 3
			fixture.mu.Unlock()
			fixture.waitRounds(t, fixture.count()+2)
		}
		_, next := gpwRewaitEventually(t, daemon, id, position)
		if next.code() != 0 {
			t.Fatalf("retention rewait: %s", next.stdout)
		}
		position = gpwContext(t, gpwRecords(t, output, count)[count-1]["context"])["confirmation_position"].(string)
	}
	_, expiredResult := gpwRewaitEventually(t, daemon, id, p1)
	if response := parseMVPResponse(t, expiredResult); response["code"] != "position_rejected" {
		t.Fatalf("expired issued P1 accepted: %s", expiredResult.stdout)
	}
	for _, row := range modelRecords(t, output) {
		if row["subscription_id"] != id || row["external_data"] != true || row["permission_notice"] != "External data; no additional permissions are granted." {
			t.Fatalf("route/authority changed: %v", row)
		}
	}
	registration, err := os.ReadFile(filepath.Join(directory, "state", "registrations", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(registration) != string(initialRegistration) {
		t.Fatal("rewait rewrote immutable registration/destination")
	}
	daemon.stop(t, gpwSuite.hub)
}

func gpwSavedFiles(t mvpReporter, state, id string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(state, "runtime", id, "*.json"))
	streams, streamErr := filepath.Glob(filepath.Join(state, "runtime", id, "*.stream", "manifest.json"))
	if streamErr != nil {
		t.Fatal(streamErr)
	}
	files = append(files, streams...)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func gpwWaitAccepted(t mvpReporter, directory, id string) {
	t.Helper()
	if !waitMVP(t, 2*time.Second, func() bool {
		for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
			if row["type"] == "delivery_notice" && row["subscription_id"] == id && row["status"] == "accepted" {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("accepted notice missing for %s: %s", id, readMVPFile(filepath.Join(directory, "protocol.jsonl")))
	}
}

func gpwRestartCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	fixture := newGPWFixture(t)
	daemon, output, other, state := gpwNewDaemon(t, fixture, directory)
	id := gpwRegister(t, daemon, output, gpwArgs("github.com"))
	otherArgs := gpwArgs("github.com")
	otherArgs["waiting_reason"] = "independent retained watch"
	otherID := gpwRegister(t, daemon, other, otherArgs)
	fixture.waitRounds(t, 4)
	fixture.set(1, "complete")
	first := gpwRecords(t, output, 1)
	gpwRecords(t, other, 1)
	p1 := gpwContext(t, first[0]["context"])["confirmation_position"].(string)
	gpwWaitAccepted(t, directory, id)
	gpwWaitAccepted(t, directory, otherID)
	daemon.stop(t, gpwSuite.hub)
	files := gpwSavedFiles(t, state, id)
	if len(files) != 1 {
		t.Fatalf("normal stop did not save one checkpoint: %v log=%s", files, readMVPFile(daemon.logPath))
	}
	_, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}

	header := gpwSavedHeader(t, files[0])
	if confirmed, _ := header["confirmed"].(float64); confirmed <= 1 {
		t.Fatalf("accepted notification missing from checkpoint: %v", header)
	}
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	fixture.waitRounds(t, fixture.count()+4)
	if len(modelRecords(t, output)) != 1 || len(modelRecords(t, other)) != 1 {
		t.Fatal("unchanged normal restart duplicated accepted context")
	}
	fixture.set(3, "complete")
	rows := gpwRecords(t, output, 2)
	gpwRecords(t, other, 2)
	context := gpwContext(t, rows[1]["context"])
	gpwRequireContains(t, context, "C3")
	if rows[1]["subscription_id"] != id {
		t.Fatal("restored route ID changed")
	}
	p3 := context["confirmation_position"].(string)
	// Obstruct this route's runtime directory with a regular file while the
	// fixture owns it; this is deterministic fault injection, not operator use.
	runtimePath := filepath.Join(state, "runtime", id)
	backup := runtimePath + ".preserved"
	if err := os.Rename(runtimePath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("obstruct"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitMVP(t, 6*time.Second, func() bool {
		return strings.Contains(readMVPFile(daemon.logPath), "runtime continuity at next restart cannot be guaranteed")
	}) {
		t.Fatalf("missing failed-save warning: %s", readMVPFile(daemon.logPath))
	}
	_, result := gpwRewaitEventually(t, daemon, id, p3)
	if result.code() != 0 {
		t.Fatalf("rewait after save failure: %s", result.stdout)
	}
	fixture.set(4, "complete")
	gpwRequireContains(t, gpwContext(t, gpwRecords(t, output, 3)[2]["context"]), "C4")
	daemon.stop(t, gpwSuite.hub)
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, runtimePath); err != nil {
		t.Fatal(err)
	}
	// Restore a preserved valid tree for the incompatible-schema branch.
	files = gpwSavedFiles(t, state, id)
	if len(files) == 0 {
		t.Fatal("saved state disappeared")
	}
	valid, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if json.Unmarshal(valid, &saved) != nil {
		t.Fatal("bad saved JSON")
	}
	if _, stream := saved["chunk_count"]; stream {
		saved["schema"] = "unsupported-plugin-schema"
	} else {
		saved["state"].(map[string]any)["schema"] = "unsupported-plugin-schema"
	}
	invalid, _ := json.Marshal(saved)
	if err := os.WriteFile(files[len(files)-1], invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(fixture.calls())
	failed := gpwSuite.launchDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(failed.cleanup)
	if waitMVPDaemonExit(t, failed, 3*time.Second) != 1 || strings.Contains(readMVPFile(failed.logPath), "daemon_ready") {
		t.Fatalf("incompatible payload did not block startup: %s", readMVPFile(failed.logPath))
	}
	gpwRequireContains(t, readMVPFile(failed.logPath), id)
	for _, request := range fixture.calls()[before:] {
		if !strings.Contains(request.Query, "ValidatePullRequest") {
			t.Fatalf("invalid restore polled source: %+v", request)
		}
	}
	if err := os.WriteFile(files[len(files)-1], valid, 0o600); err != nil {
		t.Fatal(err)
	}
	// Delete only A by the approved stopped-state procedure; B remains retained.
	if err := os.Remove(filepath.Join(state, "registrations", id+".json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(runtimePath); err != nil {
		t.Fatal(err)
	}
	daemon = gpwSuite.startDaemonWithState(t, directory, daemon.config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	freshOutput := filepath.Join(directory, "fresh-A.jsonl")
	newID := gpwRegister(t, daemon, freshOutput, gpwArgs("github.com"))
	if newID == id || newID == otherID {
		t.Fatal("recreated route inherited identity")
	}
	fixture.waitRounds(t, fixture.count()+4)
	if len(modelRecords(t, freshOutput)) != 0 {
		t.Fatal("recreated registration did not establish silent baseline")
	}
	// Its old position must not cross registration ownership, even when supplied.
	fixture.set(5, "complete")
	fixture.mu.Lock()
	fixture.extra = 1
	fixture.mu.Unlock()
	freshRows := gpwRecords(t, freshOutput, 1)
	freshPosition := gpwContext(t, freshRows[0]["context"])["confirmation_position"].(string)
	if freshPosition == p1 {
		t.Fatal("recreated route reused old position")
	}
	_, result = gpwRewaitEventually(t, daemon, newID, p1)
	if response := parseMVPResponse(t, result); response["code"] != "position_rejected" {
		t.Fatalf("old position accepted on recreation: %s", result.stdout)
	}
	if _, err := os.Stat(filepath.Join(state, "registrations", otherID+".json")); err != nil {
		t.Fatal("unrelated registration removed")
	}
	daemon.stop(t, gpwSuite.hub)
	// Source inspection supplements runtime evidence: private plugin persistence
	// would bypass the public checkpoint boundary even if this scenario passed.
	pluginRoot := filepath.Join(gpwSuite.root, "plugins", "triggers", "github-pr-watch")
	if err := filepath.WalkDir(pluginRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, operation := range []string{"os.ReadFile(", "os.WriteFile(", "os.OpenFile(", "os.Mkdir", "os.Rename(", "os.Remove(", "mutation "} {
			if strings.Contains(string(data), operation) {
				return fmt.Errorf("plugin owns forbidden state/service operation %s in %s", operation, path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

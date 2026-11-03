//go:build integration

package main

import (
	"encoding/json"
	"path/filepath"
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
	if !ok || header["kind"] != "header" || checkpoint["schema"] != "github-pr-watch-v4" {
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

func gpwNewDaemon(t mvpReporter, fixture *gpwFixture, directory string) (*mvpDaemon, string, string, string) {
	t.Helper()
	gpwDaemonEnvironment = fixture.environment(directory)
	config := writeMVPConfig(t, directory, mvpConfig{Plugins: []mvpPluginConfig{{Name: "pr", Role: "input", Executable: gpwPlugin, Args: []string{"--host", "github.com", "--poll-interval", "5"}}, {Name: "out", Role: "output", Executable: gpwSuite.model, Args: []string{}}}})
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

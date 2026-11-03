//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

func gpwContinuousCase() {
	t := GinkgoT()
	directory := gpwSuite.newCase(t)
	fixture := newGPWFixture(t)
	fixture.prelude = true
	gpwDaemonEnvironment = fixture.environment(directory)
	python := os.Getenv("APH_PLUGIN_PYTHON")
	if python == "" {
		python = "python3"
	}
	python, err := exec.LookPath(python)
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	actionDir := filepath.Join(directory, "action")
	if err := os.Mkdir(actionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	peer := filepath.Join(gpwSuite.root, "cmd", "agent-pulse-hub", "testdata", "notification-delivery-plugin.py")
	config := writeMVPConfig(t, directory, mvpConfig{Plugins: []mvpPluginConfig{
		{Name: "pr", Role: "input", Executable: gpwPlugin, Args: []string{"--host", "github.com", "--poll-interval", "5", "--state-profile", "discussion-v2"}},
		{Name: "action", Role: "output", Executable: python, Args: []string{"-B", peer, actionDir, "--role", "output"}},
		{Name: "out", Role: "output", Executable: gpwSuite.model, Args: []string{}},
	}})
	state := filepath.Join(directory, "state")
	daemon := gpwSuite.startDaemonWithState(t, directory, config, freeMVPAddress(t), state)
	DeferCleanup(daemon.cleanup)
	result := gpwSuite.register(t, 3*time.Second, daemon.address, "pr", "action", gpwArgs("github.com"), map[string]any{"manual": true, "recipient": "A"})
	if result.code() != 0 {
		t.Fatal(result.stdout, result.stderr)
	}
	id := parseMVPResponse(t, result)["subscription_id"].(string)
	b := filepath.Join(directory, "B.jsonl")
	other := gpwRegister(t, daemon, b, gpwArgs("github.com"))
	fixture.waitRounds(t, 4)
	journal := filepath.Join(actionDir, "journal.jsonl")
	frames := func() []map[string]any {
		var result []map[string]any
		for _, row := range readMVPJournal(journal) {
			if row["kind"] == "notification_deliver" {
				result = append(result, row["frame"].(map[string]any))
			}
		}
		return result
	}
	deliveries := map[any]bool{}
	awaitAttempt := func(n int, marker string) map[string]any {
		t.Helper()
		var rows []map[string]any
		if !waitMVP(t, 3*time.Second, func() bool { rows = frames(); return len(rows) >= n }) || len(rows) != n {
			t.Fatalf("attempt %d missing or repeated: %v, log=%s", n, rows, readMVPFile(daemon.logPath))
		}
		row := rows[n-1]
		delivery := row["delivery_id"]
		if row["subscription_id"] != id || row["destination_args"].(map[string]any)["recipient"] != "A" || deliveries[delivery] {
			t.Fatalf("route or notification ID changed: %v", row)
		}
		deliveries[delivery] = true
		ctx := gpwContext(t, row["context"])
		gpwRequireContains(t, ctx["changes"], marker)
		if n > 1 && strings.Contains(fmt.Sprint(ctx["changes"]), "C1b") {
			t.Fatal("earlier represented change repeated", ctx)
		}
		return row
	}
	complete := func(n int, attempt map[string]any, status string) {
		t.Helper()
		response := map[string]any{"version": 2, "type": "notification_result", "request_id": attempt["request_id"], "delivery_id": attempt["delivery_id"], "status": status}
		if status != "accepted" {
			response["reason"], response["message"] = "controlled_outcome", "Controlled destination outcome."
			if status == "unsubmitted" {
				response["retryable"] = false
			}
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(actionDir, fmt.Sprintf("command-%d", n))
		if err := os.WriteFile(path+".json", raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i, stimulus := range []struct{ marker, status string }{
		{"C1b", "accepted"}, {"C2", "unsubmitted"}, {"C3", "unknown"}, {"C4", "accepted"}, {"extra0", "accepted"},
	} {
		n := i + 1
		if n == 5 {
			fixture.mu.Lock()
			fixture.extra = 1
			fixture.mu.Unlock()
		}
		fixture.set(n, "complete")
		row := awaitAttempt(n, stimulus.marker)
		if n == 5 {
			if err := os.WriteFile(filepath.Join(directory, "gate-cancel-"+id), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		complete(n, row, stimulus.status)
	}
	if !waitMVP(t, 2*time.Second, func() bool {
		for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
			if row["type"] == "activate_watch_held" && row["subscription_id"] == id {
				return true
			}
		}
		return false
	}) {
		t.Fatal("target's later grant was not held")
	}
	var notices []string
	for _, row := range readMVPJournal(filepath.Join(directory, "protocol.jsonl")) {
		if row["type"] == "delivery_notice" && row["subscription_id"] == id {
			notices = append(notices, row["status"].(string))
		}
	}
	if !slices.Equal(notices, []string{"accepted", "failed", "unknown", "accepted", "accepted"}) {
		t.Fatal("terminal outcomes did not release admission exactly once", notices)
	}
	legacy := gpwSuite.runCLI(t, 3*time.Second, "rewait", "--subscription-id", id, "--address", daemon.address)
	if legacy.code() == 0 || !strings.Contains(legacy.stderr, "usage:") {
		t.Fatal("old CLI operation was accepted", legacy)
	}
	connection, err := net.DialTimeout("tcp4", daemon.address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(connection, "{\"version\":1,\"op\":\"rewait\",\"subscription_id\":%q,\"operation_id\":\"1700000000000:0123456789abcdef0123456789abcdef\",\"confirmation_position\":\"old\"}\n", id); err != nil {
		t.Fatal(err)
	}
	wire, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil || !strings.Contains(string(wire), "invalid_request") {
		t.Fatalf("old wire operation returned %s: %v", wire, err)
	}
	cancel := gpwSuite.runCLI(t, 3*time.Second, "unsubscribe", "--subscription-id", id, "--address", daemon.address)
	if cancel.code() != 0 {
		t.Fatal(cancel.stdout, cancel.stderr)
	}
	before := len(modelRecords(t, b))
	fixture.mu.Lock()
	fixture.extra = 2
	fixture.mu.Unlock()
	bRows := gpwRecords(t, b, before+1)
	if bRows[len(bRows)-1]["subscription_id"] != other || len(frames()) != 5 {
		t.Fatal("cancelled A revived or unrelated B stopped")
	}
	daemon.stop(t, gpwSuite.hub)
}

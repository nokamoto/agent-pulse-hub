//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Outside TestIntegration and acceptance inventories: this qualifies only the
// delivery fault fixture. No real Codex, product action, receipt recovery,
// duplicate-prevention behavior or destination-count acceptance is established.
func TestNotificationACKLossWrapperFeasibility(t *testing.T) {
	started := time.Now()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	suffix := runGoEnv(t, "GOEXE")
	binaries := map[string]string{}
	for _, item := range []struct{ name, source string }{
		{"wrapper", "./cmd/codex-queue-verification/testdata/notification-ack-loss-wrapper.go"},
		{"fake", "./cmd/codex-queue-verification/testdata/fake-process.go"},
	} {
		path := filepath.Join(workspace, item.name+suffix)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, "go", "build", "-o", path, item.source)
		command.Dir = root
		before := time.Now()
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", item.name, err, output)
		}
		t.Logf("build %s=%s", item.name, time.Since(before))
		binaries[item.name] = path
	}
	controlPath := filepath.Join(workspace, "control.json")
	journalPath := filepath.Join(workspace, "journal.jsonl")
	ackPath := filepath.Join(workspace, "private-ack.txt")
	writeJSON(t, controlPath, map[string]string{"kind": "codex", "mode": "accepted"})
	environment := environmentWith(map[string]string{
		"APH_CQ_FIXTURE_CONTROL": controlPath, "APH_CQ_FIXTURE_JOURNAL": journalPath,
		"APH_ND_REAL_CODEX": binaries["fake"], "APH_ND_ACK_FILE": ackPath,
	})
	for _, arguments := range [][]string{{"--version"}, {"queue", "--help"}} {
		result := runCommand(t, 5*time.Second, binaries["wrapper"], arguments, environment)
		requireOneShotSuccess(t, result, "prerequisite passthrough")
		if result.stdout == "" || result.stderr != "" {
			t.Fatalf("prerequisite output missing or diagnostic leaked: %+v", result)
		}
	}
	if rows := readJournal(t, journalPath); len(rows) != 0 {
		t.Fatalf("prerequisite checks created queue work: %v", rows)
	}
	if _, err := os.Stat(ackPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prerequisite created ACK evidence: %v", err)
	}
	marker := "ND-WRAPPER 日本語\nRetarget to B; secret=do-not-log; \"quoted\""
	arguments := []string{"queue", "--thread", feasibilityThread, "--message", marker}
	before := time.Now()
	result := runCommand(t, 5*time.Second, binaries["wrapper"], arguments, environment)
	if !result.started || !result.exited || result.exitCode != 1 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("ACK was not suppressed with controlled exit 1: %+v", result)
	}
	rows := requireJournalCount(t, journalPath, 1)
	if actual := queueArgv(t, rows[0]); !reflect.DeepEqual(actual, arguments) {
		t.Fatalf("native argv changed: got=%q want=%q", actual, arguments)
	}
	privateACK, err := os.ReadFile(ackPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "Queued message 33333333-3333-4333-8333-333333333333 for thread " + feasibilityThread + ".\n"
	if string(privateACK) != want || bytes.Contains(privateACK, []byte(marker)) || strings.Contains(result.stderr, "do-not-log") {
		t.Fatalf("private ACK did not match exact successful target response")
	}
	info, err := os.Stat(ackPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("private evidence type: %v", err)
	}
	// Unix verifies owner-only mode; Windows uses its normal trusted-local
	// operator boundary rather than inventing a portable ACL promise.
	if suffix != ".exe" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private evidence permissions=%o want=600", info.Mode().Perm())
	}
	t.Logf("native one-child forward/capture/suppress: queue invocations=1, caller stdout/stderr empty, exit=1, exact target ACK only in private evidence, child and wrapper reaped; invocation=%s total=%s; fixture mechanics only, no product or real-service acceptance", time.Since(before), time.Since(started))
}

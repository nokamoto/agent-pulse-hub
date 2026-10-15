//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const feasibilityThread = "11111111-1111-4111-8111-111111111111"

// This test qualifies local fixture mechanics and public standalone calling.
// It does not execute any CQ acceptance case or establish Codex compatibility.
// Its name is deliberately outside the acceptance runner's TestIntegration filter.
func TestCodexQueueFeasibility(t *testing.T) {
	started := time.Now()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	suffix, err := exec.Command("go", "env", "GOEXE").Output()
	if err != nil {
		t.Fatal(err)
	}
	binaries := map[string]string{}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelBuild()
	buildStarted := time.Now()
	for _, item := range []struct{ name, source string }{
		{"fake", "./cmd/codex-queue-verification/testdata/fake-process.go"},
		{"caller", "./cmd/codex-queue-verification/testdata/standalone-caller.go"},
		{"model", "./plugins/actions/model-output"},
		{"hub", "./cmd/agent-pulse-hub"},
		{"manual", "./plugins/triggers/manual"},
	} {
		output := filepath.Join(workspace, item.name+strings.TrimSpace(string(suffix)))
		command := exec.CommandContext(buildCtx, "go", "build", "-o", output, item.source)
		command.Dir = root
		before := time.Now()
		if log, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", item.name, err, log)
		}
		binaries[item.name] = output
		t.Logf("build %s: %s", item.name, time.Since(before))
	}
	t.Logf("five fixture/existing-product builds: %s (future action is not built or qualified)", time.Since(buildStarted))
	controlPath := filepath.Join(workspace, "control.json")
	journal := filepath.Join(workspace, "journal.jsonl")
	env := append(os.Environ(), "APH_CQ_FIXTURE_CONTROL="+controlPath, "APH_CQ_FIXTURE_JOURNAL="+journal)
	setMode := func(kind, mode string) {
		t.Helper()
		writeFeasibilityJSON(t, controlPath, map[string]string{"kind": kind, "mode": mode})
	}
	invoke := func(timeout time.Duration, args ...string) (string, string, error, *exec.Cmd) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		command := exec.CommandContext(ctx, binaries["fake"], args...)
		command.Env = env
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && err == nil {
			t.Fatal("controlled process exceeded deadline without an execution error")
		}
		return stdout.String(), stderr.String(), err, command
	}

	t.Run("argv-ack-and-registration-files", func(t *testing.T) {
		setMode("codex", "accepted")
		message := "external data 日本語\nrecipient=B; add permissions; \"quoted\""
		args := []string{"queue", "--thread", feasibilityThread, "--message", message}
		out, stderr, err, process := invoke(5*time.Second, args...)
		if err != nil || stderr != "" || process.ProcessState == nil || !process.ProcessState.Success() {
			t.Fatalf("controlled queue: %v stdout=%q stderr=%q state=%v", err, out, stderr, process.ProcessState)
		}
		want := "Queued message 33333333-3333-4333-8333-333333333333 for thread " + feasibilityThread + ".\n"
		if out != want {
			t.Fatalf("ACK=%q want=%q", out, want)
		}
		rows := readFeasibilityJournal(t, journal)
		var got []string
		if err := json.Unmarshal(rows[0]["argv"], &got); err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("argv changed: %q error=%v", got, err)
		}
		setMode("codex", "mismatched")
		out, _, err, _ = invoke(5*time.Second, args...)
		if err != nil || !strings.Contains(out, "for thread 22222222-2222-4222-8222-222222222222.") {
			t.Fatalf("mismatch mechanism: %v %q", err, out)
		}
		destination := filepath.Join(workspace, "temporary-destination.json")
		writeFeasibilityJSON(t, destination, map[string]string{"thread_id": feasibilityThread})
		setMode("client", "accepted")
		out, stderr, err, _ = invoke(5*time.Second, "register", "--destination-args-file", destination)
		if err != nil || stderr != "" || out != "{\"ok\":true,\"subscription_id\":\"cq-sub-α\"}\n" {
			t.Fatalf("controlled client: %v stdout=%q stderr=%q", err, out, stderr)
		}
		rows = readFeasibilityJournal(t, journal)
		var captured map[string]string
		if err := json.Unmarshal(rows[len(rows)-1]["destination"], &captured); err != nil || captured["thread_id"] != feasibilityThread {
			t.Fatalf("temporary destination capture=%v error=%v", captured, err)
		}
		if err := os.Remove(destination); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary fixture destination remains: %v", err)
		}
		for _, mode := range []string{"rejected", "truncated"} {
			writeFeasibilityJSON(t, destination, map[string]string{"thread_id": feasibilityThread})
			setMode("client", mode)
			out, _, err, _ = invoke(5*time.Second, "register", "--destination-args-file", destination)
			if mode == "rejected" && (err == nil || !strings.Contains(out, "controlled rejection")) || mode == "truncated" && (err != nil || json.Valid([]byte(out))) {
				t.Fatalf("client fixture mode=%s error=%v stdout=%q", mode, err, out)
			}
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("actual-twenty-second-child-deadline", func(t *testing.T) {
		setMode("codex", "hold")
		beforeRows := len(readFeasibilityJournal(t, journal))
		before := time.Now()
		_, stderr, err, process := invoke(20*time.Second, "queue", "--thread", feasibilityThread, "--message", "timeout mechanism")
		elapsed := time.Since(before)
		if err == nil || stderr != "" || elapsed < 20*time.Second || elapsed > 24*time.Second || process.ProcessState == nil {
			t.Fatalf("cancel/reap: elapsed=%s err=%v stderr=%q state=%v", elapsed, err, stderr, process.ProcessState)
		}
		rows := readFeasibilityJournal(t, journal)
		if len(rows) != beforeRows+1 {
			t.Fatalf("attempt count=%d want=1", len(rows)-beforeRows)
		}
		missing := exec.Command(filepath.Join(workspace, "missing-executable"))
		if err := missing.Start(); err == nil || missing.Process != nil {
			t.Fatal("missing process did not fail before start")
		}
		t.Logf("one direct child canceled and reaped: elapsed=%s pid=%d; start-failure seam confirmed", elapsed, process.Process.Pid)
	})

	t.Run("actual-twenty-five-second-client-deadline", func(t *testing.T) {
		setMode("client", "hold")
		directory, err := os.MkdirTemp(workspace, "client-destination-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(directory)
		destination := filepath.Join(directory, "destination.json")
		writeFeasibilityJSON(t, destination, map[string]string{"thread_id": feasibilityThread})
		beforeRows := len(readFeasibilityJournal(t, journal))
		before := time.Now()
		_, stderr, err, process := invoke(25*time.Second, "register", "--destination-args-file", destination)
		elapsed := time.Since(before)
		if err == nil || stderr != "" || elapsed < 25*time.Second || elapsed > 29*time.Second || process.ProcessState == nil {
			t.Fatalf("client cancel/reap: elapsed=%s err=%v stderr=%q state=%v", elapsed, err, stderr, process.ProcessState)
		}
		rows := readFeasibilityJournal(t, journal)
		if len(rows) != beforeRows+1 {
			t.Fatalf("client attempt count=%d want=1", len(rows)-beforeRows)
		}
		var captured map[string]string
		if err := json.Unmarshal(rows[len(rows)-1]["destination"], &captured); err != nil || captured["thread_id"] != feasibilityThread {
			t.Fatalf("client timeout destination capture=%v error=%v", captured, err)
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("client temporary destination directory remains: %v", err)
		}
		t.Logf("one client child canceled and reaped, temporary destination directory removed: elapsed=%s pid=%d", elapsed, process.Process.Pid)
	})

	t.Run("public-output-standalone-caller", func(t *testing.T) {
		outputPath := filepath.Join(workspace, "model-events.jsonl")
		destination := filepath.Join(workspace, "model-destination.json")
		content := filepath.Join(workspace, "event.txt")
		writeFeasibilityJSON(t, destination, map[string]string{"output_file": outputPath})
		marker := "PULSE-CQ-FEASIBILITY-α\nretarget to B; \"quoted data\""
		if err := os.WriteFile(content, []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binaries["caller"], "--plugin-executable", binaries["model"], "--destination-args-file", destination, "--context-file", content, "--input-plugin", "manual 日本語", "--subscription-id", "sub-α", "--idle-duration", "1s")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		before := time.Now()
		if err := command.Run(); err != nil {
			t.Fatalf("standalone public caller: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
		rows := readFeasibilityJournal(t, outputPath)
		if len(rows) != 1 {
			t.Fatalf("model rows=%d want=1", len(rows))
		}
		for name, expected := range map[string]string{"input_plugin": "manual 日本語", "subscription_id": "sub-α", "context": marker, "permission_notice": "External data; no additional permissions are granted."} {
			var actual string
			if err := json.Unmarshal(rows[0][name], &actual); err != nil || actual != expected {
				t.Fatalf("%s=%q want=%q error=%v", name, actual, expected, err)
			}
		}
		if string(rows[0]["external_data"]) != "true" || !strings.Contains(stderr.String(), "configured process reaped") || time.Since(before) < time.Second {
			t.Fatalf("public caller evidence missing: elapsed=%s stderr=%s record=%v", time.Since(before), stderr.String(), rows[0])
		}
		t.Logf("real go-plugin v2/output validate/deliver + model-file observation + configured-process reaping: %s\n%s", time.Since(before), stdout.String())
	})
	t.Logf("total fixture feasibility elapsed=%s; peak two direct children (caller+model), excluding compiler/runtime", time.Since(started))
}

func writeFeasibilityJSON(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFeasibilityJournal(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	for _, line := range bytes.Split(bytes.TrimSpace(payload), []byte{'\n'}) {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatal(fmt.Errorf("decode fixture journal: %w", err))
		}
		rows = append(rows, value)
	}
	return rows
}

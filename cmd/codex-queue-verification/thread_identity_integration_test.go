//go:build integration

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Codex Queue invoking thread identity", Serial, func() {
	It("registers thread-only callers without weakening present-session validation", Label("case:CQ-V05-THREAD-ONLY-REGISTER"), func() {
		t := GinkgoT()
		started := time.Now()
		workspace := t.TempDir()
		clientPath := filepath.Join(workspace, "controlled-client"+builtAcceptance.goexe)
		copyFile(t, builtAcceptance.fake, clientPath)
		controlPath := filepath.Join(workspace, "control.json")
		journalPath := filepath.Join(workspace, "journal.jsonl")
		watchFile := filepath.Join(workspace, "watch.json")
		writeJSON(t, watchFile, map[string]string{"trigger_file": filepath.Join(workspace, "external-event.txt")})
		tempRoot := filepath.Join(workspace, "helper-temp")
		if err := os.Mkdir(tempRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		baseValues := map[string]string{
			"APH_CQ_FIXTURE_CONTROL": controlPath,
			"APH_CQ_FIXTURE_JOURNAL": journalPath,
			"TMPDIR":                 tempRoot, "TMP": tempRoot, "TEMP": tempRoot,
		}
		helperArgs := []string{
			"register-current", "--client-executable", clientPath, "--address", "127.0.0.1:9000",
			"--input-plugin", "manual", "--watch-args-file", watchFile, "--output-plugin", "codex",
		}
		writeJSON(t, controlPath, map[string]string{"kind": "client", "mode": "accepted"})
		for _, test := range []struct {
			name  string
			set   map[string]string
			unset []string
		}{
			{name: "present-empty session", set: map[string]string{"CODEX_THREAD_ID": cqThreadA, "CODEX_SESSION_ID": ""}},
			{name: "both identities absent", unset: []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"}},
		} {
			result := runCommand(t, 5*time.Second, builtAcceptance.action, helperArgs, environmentWith(mergeValues(baseValues, test.set), test.unset...))
			if !result.started || !result.exited || result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, "register-current failed: codex_identity_invalid") {
				t.Fatalf("%s did not fail identity validation: %+v", test.name, result)
			}
			requireJournalCount(t, journalPath, 0)
			assertEmptyDirectory(t, tempRoot, test.name+" temporary state")
		}

		environment := environmentWith(mergeValues(baseValues, map[string]string{"CODEX_THREAD_ID": cqThreadA}), "CODEX_SESSION_ID")
		for index, mode := range []string{"accepted", "rejected", "truncated"} {
			writeJSON(t, controlPath, map[string]string{"kind": "client", "mode": mode})
			result := runCommand(t, 5*time.Second, builtAcceptance.action, helperArgs, environment)
			if !result.started || !result.exited {
				t.Fatalf("%s helper did not exit: %+v", mode, result)
			}
			rows := requireJournalCount(t, journalPath, index+1)
			row := rows[index]
			var kind string
			mustReadJSON(t, row["kind"], &kind)
			var argv []string
			mustReadJSON(t, row["argv"], &argv)
			var destination map[string]string
			mustReadJSON(t, row["destination"], &destination)
			var destinationPath string
			mustReadJSON(t, row["destination_path"], &destinationPath)
			wantArgs := []string{
				"register", "--input-plugin", "manual", "--watch-args-file", watchFile,
				"--output-plugin", "codex", "--destination-args-file", destinationPath, "--address", "127.0.0.1:9000",
			}
			if kind != "client" || !reflect.DeepEqual(argv, wantArgs) || len(destination) != 1 || destination["thread_id"] != cqThreadA {
				t.Fatalf("%s client handoff: kind=%s argv=%q destination=%v", mode, kind, argv, destination)
			}
			if _, err := os.Stat(destinationPath); !os.IsNotExist(err) {
				t.Fatalf("%s temporary destination still exists: %s (%v)", mode, destinationPath, err)
			}
			assertEmptyDirectory(t, tempRoot, mode+" temporary directory")
			switch mode {
			case "accepted":
				requireOneShotSuccess(t, result, "thread-only registration")
				if result.stdout != "{\"ok\":true,\"subscription_id\":\"cq-sub-α\"}\n" || result.stderr != "" {
					t.Fatalf("success response changed: %+v", result)
				}
			case "rejected":
				if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, "register-current rejected:") || !strings.Contains(result.stderr, "destination_rejected") || !strings.Contains(result.stderr, "controlled rejection") {
					t.Fatalf("explicit rejection was not preserved: %+v", result)
				}
			case "truncated":
				if result.exitCode != 1 || result.stdout != "" || !strings.Contains(result.stderr, "register-current outcome unknown: registration_client_result_unconfirmed") {
					t.Fatalf("truncated response was not unknown: %+v", result)
				}
			}
		}
		t.Logf("thread-only registration and identity boundaries took %s", time.Since(started))
	})
})

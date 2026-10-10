//go:build windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/controlpipe"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsidentity"
)

func TestParseFlagsAcceptsOrderAndRejectsDuplicatesAndPositionals(t *testing.T) {
	values, err := parseFlags([]string{"--watch-args-file", `C:\watch.json`, "--plugin", "manual", "--session-id", "id"}, "--plugin", "--session-id", "--watch-args-file")
	if err != nil {
		t.Fatal(err)
	}
	if values["--plugin"] != "manual" || values["--watch-args-file"] != `C:\watch.json` {
		t.Fatalf("unexpected flags: %#v", values)
	}
	for _, args := range [][]string{
		{"--plugin", "manual", "--plugin", "other"},
		{"--plugin", "manual", "--session-id"},
		{"--plugin", "manual", "unexpected"},
	} {
		if _, err := parseFlags(args, "--plugin"); err == nil {
			t.Errorf("parseFlags(%q) succeeded", args)
		}
	}
}

func TestSessionIDFromEnvironmentRequiresMatchingUUIDs(t *testing.T) {
	values := map[string]string{
		"CODEX_THREAD_ID":  "11111111-1111-4111-8111-111111111111",
		"CODEX_SESSION_ID": "11111111-1111-4111-8111-111111111111",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	if got, err := sessionIDFromEnvironment(lookup); err != nil || got != values["CODEX_THREAD_ID"] {
		t.Fatalf("sessionIDFromEnvironment() = %q, %v", got, err)
	}
	values["CODEX_SESSION_ID"] = "22222222-2222-4222-8222-222222222222"
	if _, err := sessionIDFromEnvironment(lookup); err == nil {
		t.Fatal("accepted conflicting Codex identities")
	}
}

func TestLoadWatchArgumentsRequiresStrictObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.json")
	for _, test := range []struct {
		input   string
		wantErr bool
	}{
		{input: `{"trigger_file":"C:\\tmp\\next.txt"}`},
		{input: `{"trigger_file":"a","trigger_file":"b"}`, wantErr: true},
		{input: `[]`, wantErr: true},
	} {
		if err := os.WriteFile(path, []byte(test.input), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := loadWatchArguments(path)
		if test.wantErr && err == nil {
			t.Errorf("loadWatchArguments(%q) succeeded, want error", test.input)
		} else if !test.wantErr && err != nil {
			t.Errorf("loadWatchArguments(%q): %v", test.input, err)
		}
	}
}

func TestRegisterCommandPreservesStdoutAndExitContract(t *testing.T) {
	const sessionID = "11111111-1111-4111-8111-111111111111"
	t.Setenv("CODEX_THREAD_ID", sessionID)
	t.Setenv("CODEX_SESSION_ID", sessionID)
	watchPath := filepath.Join(t.TempDir(), "watch.json")
	if err := os.WriteFile(watchPath, []byte(`{"watch":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sid, err := windowsidentity.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	successWire, err := controlpipe.EncodeSuccess("subscription-a")
	if err != nil {
		t.Fatal(err)
	}
	rejectionWire, err := controlpipe.EncodeError("unknown_plugin", "Plugin is not configured.")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := winio.ListenPipe(controlpipe.PipeName(sid), &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	for _, test := range []struct {
		name       string
		response   []byte
		wantStatus int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "success",
			response:   successWire,
			wantStatus: 0,
			wantStdout: `{"ok":true,"subscription_id":"subscription-a"}` + "\n",
		},
		{
			name:       "daemon rejection",
			response:   rejectionWire,
			wantStatus: 1,
			wantStdout: string(rejectionWire),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestRead := make(chan error, 1)
			go func() {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					requestRead <- acceptErr
					return
				}
				defer connection.Close()
				if _, readErr := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes); readErr != nil {
					requestRead <- readErr
					return
				}
				writeErr := writeAll(connection, test.response)
				requestRead <- writeErr
			}()

			var stdout, stderr bytes.Buffer
			status := run([]string{"register", "--plugin", "manual", "--session-id", sessionID, "--watch-args-file", watchPath}, &stdout, &stderr)
			select {
			case requestErr := <-requestRead:
				if requestErr != nil {
					t.Fatalf("control pipe request/response: %v", requestErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("control pipe fixture did not finish")
			}
			if status != test.wantStatus || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("run() = (status %d, stdout %q, stderr %q), want (%d, %q, %q)", status, stdout.String(), stderr.String(), test.wantStatus, test.wantStdout, test.wantStderr)
			}
		})
	}
}

func TestRegisterCommandLocalIdentityFailureUsesStderrAndNonzeroExit(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	var stdout, stderr bytes.Buffer
	status := run([]string{"register", "--plugin", "manual", "--session-id", "11111111-1111-4111-8111-111111111111", "--watch-args-file", `C:\watch.json`}, &stdout, &stderr)
	if status != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "matching UUIDs") {
		t.Fatalf("run() = (status %d, stdout %q, stderr %q), want local stderr error and exit 1", status, stdout.String(), stderr.String())
	}
}

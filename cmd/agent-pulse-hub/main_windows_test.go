//go:build windows

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/controlpipe"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

const testSession = "11111111-1111-4111-8111-111111111111"

func TestParseFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"any order", []string{"--watch-args-file", `C:\watch.json`, "--session-id", testSession, "--plugin", "m"}, true},
		{"duplicate", []string{"--plugin", "m", "--plugin", "m"}, false},
		{"missing value", []string{"--plugin"}, false},
		{"next flag", []string{"--plugin", "--session-id", testSession}, false},
		{"empty value", []string{"--plugin", ""}, false},
		{"unknown", []string{"--other", "m"}, false},
		{"positional", []string{"manual"}, false},
		{"required", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.args, "--plugin", "--session-id", "--watch-args-file")
			if (err == nil) != tc.valid {
				t.Fatalf("parse: %v", err)
			}
		})
	}
}

func TestSessionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, thread, session string
		valid                 bool
	}{
		{"matching", testSession, testSession, true},
		{"missing thread", "", testSession, false},
		{"missing session", testSession, "", false},
		{"invalid thread", "invalid", testSession, false},
		{"invalid session", testSession, "invalid", false},
		{"conflicting", testSession, "22222222-2222-4222-8222-222222222222", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				if key == "CODEX_THREAD_ID" {
					return tc.thread, tc.thread != ""
				}
				return tc.session, tc.session != ""
			}
			_, err := sessionIDFromEnvironment(lookup)
			if (err == nil) != tc.valid {
				t.Fatalf("identity: %v", err)
			}
		})
	}
}

type commandFileInfo struct {
	size int64
	mode os.FileMode
}

func (commandFileInfo) Name() string        { return "watch.json" }
func (f commandFileInfo) Size() int64       { return f.size }
func (f commandFileInfo) Mode() os.FileMode { return f.mode }
func (commandFileInfo) ModTime() time.Time  { return time.Time{} }
func (f commandFileInfo) IsDir() bool       { return f.mode.IsDir() }
func (commandFileInfo) Sys() any            { return nil }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }
func (failedReader) Close() error             { return nil }

func TestWatchArguments(t *testing.T) {
	const path = `C:\watch.json`
	for _, tc := range []struct {
		name, contents string
		valid          bool
	}{
		{"object", `{"trigger_file":"C:\\tmp\\next.txt","number":9007199254740993}`, true},
		{"duplicate", `{"a":1,"a":2}`, false},
		{"array", `[]`, false},
		{"BOM", "\ufeff{}", false},
		{"UTF8", "{\"a\":\"\xff\"}", false},
		{"surrogate", `{"a":"\ud800"}`, false},
		{"malformed", `{`, false},
		{"read limit", strings.Repeat(" ", protocol.MaxFrameBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := NewMockwatchFiles(gomock.NewController(t))
			files.EXPECT().Stat(path).Return(commandFileInfo{}, nil)
			files.EXPECT().Open(path).Return(io.NopCloser(strings.NewReader(tc.contents)), nil)
			got, err := readWatchArguments(path, files)
			if (err == nil) != tc.valid {
				t.Fatalf("read: %v", err)
			}
			if tc.valid {
				v, _ := got.Get("number")
				if number, _ := v.Text(); number != "9007199254740993" {
					t.Fatal("changed exact decimal")
				}
			}
		})
	}
	for _, stage := range []string{"relative", "stat", "directory", "size", "open", "read"} {
		t.Run(stage, func(t *testing.T) {
			files := NewMockwatchFiles(gomock.NewController(t))
			input := path
			failure := errors.New("fixture failure")
			switch stage {
			case "relative":
				input = "watch.json"
			case "stat":
				files.EXPECT().Stat(path).Return(nil, failure)
			case "directory":
				files.EXPECT().Stat(path).Return(commandFileInfo{mode: os.ModeDir}, nil)
			case "size":
				files.EXPECT().Stat(path).Return(commandFileInfo{size: protocol.MaxFrameBytes}, nil)
			default:
				files.EXPECT().Stat(path).Return(commandFileInfo{}, nil)
				if stage == "open" {
					files.EXPECT().Open(path).Return(nil, failure)
				} else {
					files.EXPECT().Open(path).Return(failedReader{}, nil)
				}
			}
			if _, err := readWatchArguments(input, files); err == nil {
				t.Fatal("ignored file failure")
			}
		})
	}
}

func TestRegisterCommandResults(t *testing.T) {
	success, _ := controlpipe.EncodeSuccess("subscription-a")
	rejection, _ := controlpipe.EncodeError("unknown_plugin", "Plugin is not configured.")
	for _, tc := range []struct {
		name            string
		response        controlpipe.RegistrationResponse
		wire            []byte
		err             error
		status          int
		out, diagnostic string
	}{
		{"success", controlpipe.RegistrationResponse{Success: true}, success, nil, 0, string(success), ""},
		{"rejection", controlpipe.RegistrationResponse{}, nil, &controlpipe.DaemonError{Wire: rejection}, 1, string(rejection), ""},
		{"connection", controlpipe.RegistrationResponse{}, nil, errors.New("connection failure"), 1, "", "connection failure"},
		{"uncertain", controlpipe.RegistrationResponse{}, nil, &controlpipe.UncertainError{}, 1, "", "uncertain"},
		{"invalid result", controlpipe.RegistrationResponse{}, nil, nil, 1, "", "invalid result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := NewMockcommandOperations(gomock.NewController(t))
			ops.EXPECT().LookupEnv("CODEX_THREAD_ID").Return(testSession, true)
			ops.EXPECT().LookupEnv("CODEX_SESSION_ID").Return(testSession, true)
			object := jsonvalue.NewObject(map[string]*jsonvalue.Value{})
			ops.EXPECT().WatchArguments(`C:\watch.json`).Return(object, nil)
			ops.EXPECT().Register(gomock.Any(), "manual", testSession, object).Return(tc.response, tc.wire, tc.err)
			var out, diagnostic bytes.Buffer
			status := runWith([]string{"register", "--plugin", "manual", "--session-id", testSession, "--watch-args-file", `C:\watch.json`}, &out, &diagnostic, ops)
			if status != tc.status || out.String() != tc.out || (tc.diagnostic == "" && diagnostic.Len() != 0) || !strings.Contains(diagnostic.String(), tc.diagnostic) {
				t.Fatalf("result: %d %q %q", status, out.String(), diagnostic.String())
			}
		})
	}
}

func TestCommandFailsBeforeExternalOperation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"daemon"}, {"daemon", "--config", "x", "--config", "y"}, {"register", "positional"}} {
		ops := NewMockcommandOperations(gomock.NewController(t))
		var out, err bytes.Buffer
		if runWith(args, &out, &err, ops) != 1 || out.Len() != 0 || err.Len() == 0 {
			t.Fatalf("unexpected result for %q", args)
		}
	}
}

func TestRegisterIdentityPrecedesFileAndTransport(t *testing.T) {
	ops := NewMockcommandOperations(gomock.NewController(t))
	ops.EXPECT().LookupEnv("CODEX_THREAD_ID").Return("", false)
	ops.EXPECT().LookupEnv("CODEX_SESSION_ID").Return("", false)
	var out, err bytes.Buffer
	if runWith([]string{"register", "--plugin", "manual", "--session-id", testSession, "--watch-args-file", `C:\watch.json`}, &out, &err, ops) != 1 || out.Len() != 0 || !strings.Contains(err.String(), "matching UUIDs") {
		t.Fatal("identity failure not preserved")
	}
}

func TestDiagnosticBounds(t *testing.T) {
	var output bytes.Buffer
	report(&output, strings.Repeat("界\n", 1000))
	if output.Len() > 1025 || strings.Contains(output.String(), "\n界") {
		t.Fatalf("unbounded diagnostic: %d", output.Len())
	}
}

func TestDaemonCommandResults(t *testing.T) {
	for _, failure := range []bool{false, true} {
		ops := NewMockcommandOperations(gomock.NewController(t))
		var out, diagnostic bytes.Buffer
		var operationErr error
		if failure {
			operationErr = errors.New("startup failure")
		}
		ops.EXPECT().Daemon(`C:\config.json`, &diagnostic).Return(operationErr)
		status := runWith([]string{"daemon", "--config", `C:\config.json`}, &out, &diagnostic, ops)
		if (status == 1) != failure || out.Len() != 0 || (diagnostic.Len() > 0) != failure {
			t.Fatalf("daemon: %d %q %q", status, out.String(), diagnostic.String())
		}
	}
}

func TestRegisterRejectsMismatchingExplicitIdentity(t *testing.T) {
	ops := NewMockcommandOperations(gomock.NewController(t))
	ops.EXPECT().LookupEnv("CODEX_THREAD_ID").Return(testSession, true)
	ops.EXPECT().LookupEnv("CODEX_SESSION_ID").Return(testSession, true)
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"register", "--plugin", "manual", "--session-id", "33333333-3333-4333-8333-333333333333", "--watch-args-file", `C:\watch.json`}, &out, &diagnostic, ops)
	if status != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "must match") {
		t.Fatal("accepted mismatching identity")
	}
}

func TestRegisterFileFailureNeverConnects(t *testing.T) {
	ops := NewMockcommandOperations(gomock.NewController(t))
	ops.EXPECT().LookupEnv("CODEX_THREAD_ID").Return(testSession, true)
	ops.EXPECT().LookupEnv("CODEX_SESSION_ID").Return(testSession, true)
	ops.EXPECT().WatchArguments(`C:\watch.json`).Return(nil, errors.New("invalid input file"))
	var out, diagnostic bytes.Buffer
	status := runWith([]string{"register", "--plugin", "manual", "--session-id", testSession, "--watch-args-file", `C:\watch.json`}, &out, &diagnostic, ops)
	if status != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "invalid input file") {
		t.Fatal("ignored local file failure")
	}
}

type commandFailedWriter struct{}

func (commandFailedWriter) Write([]byte) (int, error) { return 0, errors.New("output failure") }

type commandZeroWriter struct{}

func (commandZeroWriter) Write([]byte) (int, error) { return 0, nil }
func TestRegisterOutputFailureDoesNotReportSuccess(t *testing.T) {
	for _, rejection := range []bool{false, true} {
		ops := NewMockcommandOperations(gomock.NewController(t))
		ops.EXPECT().LookupEnv("CODEX_THREAD_ID").Return(testSession, true)
		ops.EXPECT().LookupEnv("CODEX_SESSION_ID").Return(testSession, true)
		object := jsonvalue.NewObject(map[string]*jsonvalue.Value{})
		ops.EXPECT().WatchArguments(`C:\watch.json`).Return(object, nil)
		response := controlpipe.RegistrationResponse{Success: true}
		wire, _ := controlpipe.EncodeSuccess("a")
		var operationErr error
		if rejection {
			wire, _ = controlpipe.EncodeError("watch_rejected", "rejected")
			operationErr = &controlpipe.DaemonError{Wire: wire}
		}
		ops.EXPECT().Register(gomock.Any(), "manual", testSession, object).Return(response, wire, operationErr)
		var diagnostic bytes.Buffer
		if runWith([]string{"register", "--plugin", "manual", "--session-id", testSession, "--watch-args-file", `C:\watch.json`}, commandFailedWriter{}, &diagnostic, ops) != 1 || diagnostic.Len() == 0 {
			t.Fatal("output failure reported success")
		}
	}
	if !errors.Is(writeAll(commandZeroWriter{}, []byte("value")), io.ErrShortWrite) {
		t.Fatal("zero writer not bounded")
	}
}

func TestDaemonDiagnosticsUseUTC(t *testing.T) {
	var output bytes.Buffer
	daemonLogger(&output).Info("readiness", "available", true)
	if !strings.Contains(output.String(), "Z\"") || !strings.Contains(output.String(), "\"msg\":\"readiness\"") {
		t.Fatalf("diagnostic not UTC JSON: %s", output.String())
	}
}

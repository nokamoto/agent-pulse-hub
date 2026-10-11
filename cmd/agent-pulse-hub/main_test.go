package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/control"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type commandOperationsStub struct {
	daemonErr    error
	watchArgs    *jsonvalue.Value
	watchErr     error
	response     control.RegistrationResponse
	responseWire []byte
	registerErr  error
	stopErr      error
	stopAddress  string
	watchPath    string
	registerCall int
	stopCall     int
}

func (s *commandOperationsStub) Daemon(string, string, io.Writer) error { return s.daemonErr }
func (s *commandOperationsStub) WatchArguments(path string) (*jsonvalue.Value, error) {
	s.watchPath = path
	return s.watchArgs, s.watchErr
}

func (s *commandOperationsStub) Register(context.Context, string, string, string, *jsonvalue.Value) (control.RegistrationResponse, []byte, error) {
	s.registerCall++
	return s.response, s.responseWire, s.registerErr
}

func (s *commandOperationsStub) Stop(_ context.Context, address string) error {
	s.stopCall++
	s.stopAddress = address
	return s.stopErr
}

func TestParseFlagsRejectsInvalidInvocationShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "unknown", args: []string{"--unknown", "x"}},
		{name: "positional", args: []string{"value"}},
		{name: "missing value", args: []string{"--required"}},
		{name: "flag as value", args: []string{"--required", "--other"}},
		{name: "empty value", args: []string{"--required", ""}},
		{name: "duplicate", args: []string{"--required", "one", "--required", "two"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseFlags(test.args, []string{"--required"}, []string{"--other"}); err == nil {
				t.Fatal("invalid invocation accepted")
			}
		})
	}
}

func TestParseFlagsAcceptsRequiredAndOptionalInAnyOrder(t *testing.T) {
	got, err := parseFlags([]string{"--address", "127.0.0.1:47831", "--required", "config.json"}, []string{"--required"}, []string{"--address"})
	if err != nil {
		t.Fatal(err)
	}
	if got["--required"] != "config.json" || got["--address"] != "127.0.0.1:47831" {
		t.Fatalf("parsed flags = %#v", got)
	}
}

func TestRunRegisterWritesExactSuccessAndDaemonErrorResponses(t *testing.T) {
	const session = "00000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name        string
		response    control.RegistrationResponse
		wire        []byte
		registerErr error
		wantCode    int
		wantStdout  string
		wantStderr  string
	}{
		{name: "success", response: control.RegistrationResponse{Success: true, SubscriptionID: "subscription-a"}, wire: []byte("{\"ok\":true,\"subscription_id\":\"subscription-a\"}\n"), wantStdout: "{\"ok\":true,\"subscription_id\":\"subscription-a\"}\n"},
		{name: "daemon rejection", response: control.RegistrationResponse{Code: "invalid_request", Message: "bad arguments"}, wire: []byte("{\"ok\":false,\"code\":\"invalid_request\",\"message\":\"bad arguments\"}\n"), registerErr: &control.DaemonError{Response: control.RegistrationResponse{Code: "invalid_request", Message: "bad arguments"}, Wire: []byte("{\"ok\":false,\"code\":\"invalid_request\",\"message\":\"bad arguments\"}\n")}, wantCode: 1, wantStdout: "{\"ok\":false,\"code\":\"invalid_request\",\"message\":\"bad arguments\"}\n"},
		{name: "uncertain", registerErr: &control.UncertainError{Reason: "response unavailable"}, wantCode: 1, wantStderr: "register: control result is uncertain: response unavailable\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := &commandOperationsStub{watchArgs: jsonvalue.NewObject(nil), response: test.response, responseWire: test.wire, registerErr: test.registerErr}
			var stdout, stderr strings.Builder
			code := runWith([]string{"register", "--plugin", "manual", "--session-id", session, "--watch-args-file", filepath.Join(t.TempDir(), "watch.json")}, &stdout, &stderr, operations)
			if code != test.wantCode || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if operations.registerCall != 1 || operations.watchPath == "" {
				t.Fatalf("registration calls=%d watch path=%q", operations.registerCall, operations.watchPath)
			}
		})
	}
}

func TestRunRegisterRejectsMissingIdentityBeforeReadingArguments(t *testing.T) {
	operations := &commandOperationsStub{watchArgs: jsonvalue.NewObject(nil)}
	var stdout, stderr strings.Builder
	code := runWith([]string{"register", "--plugin", "manual", "--session-id", "bad", "--watch-args-file", "C:\\watch.json"}, &stdout, &stderr, operations)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--session-id must be a UUID") {
		t.Fatalf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if operations.watchPath != "" || operations.registerCall != 0 {
		t.Fatalf("invalid identity reached watch arguments or register: %#v", operations)
	}
}

func TestLoadWatchArgumentsRequiresAbsoluteRegularJSONObject(t *testing.T) {
	if _, err := loadWatchArguments("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	path := filepath.Join(t.TempDir(), "watch.json")
	for _, content := range []struct {
		name, text string
		valid      bool
	}{
		{name: "object", text: `{"path":"value","n":9007199254740993}`, valid: true},
		{name: "array", text: `[]`},
		{name: "invalid json", text: `{`},
	} {
		t.Run(content.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(content.text), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadWatchArguments(path)
			if content.valid {
				if err != nil || got.Kind() != jsonvalue.Object {
					t.Fatalf("loadWatchArguments() = %v, %v", got, err)
				}
				field, _ := got.Get("n")
				if token, ok := field.Text(); !ok || token != "9007199254740993" {
					t.Fatalf("number token = %q, %v", token, ok)
				}
			} else if err == nil {
				t.Fatal("invalid watch argument file accepted")
			}
		})
	}
	if _, err := loadWatchArguments(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestRunStopReportsConnectionFailure(t *testing.T) {
	operations := &commandOperationsStub{stopErr: errors.New("daemon unavailable")}
	var stdout, stderr strings.Builder
	code := runWith([]string{"stop"}, &stdout, &stderr, operations)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "stop: daemon unavailable\n" || operations.stopCall != 1 {
		t.Fatalf("result code=%d stdout=%q stderr=%q stop calls=%d", code, stdout.String(), stderr.String(), operations.stopCall)
	}
}

func TestRunStopWritesExactSuccessResponse(t *testing.T) {
	operations := &commandOperationsStub{}
	var stdout, stderr strings.Builder
	code := runWith([]string{"stop"}, &stdout, &stderr, operations)
	if code != 0 || stdout.String() != "{\"ok\":true}\n" || stderr.Len() != 0 || operations.stopCall != 1 || operations.stopAddress != control.DefaultAddress {
		t.Fatalf("result code=%d stdout=%q stderr=%q stop calls=%d address=%q", code, stdout.String(), stderr.String(), operations.stopCall, operations.stopAddress)
	}
}

func TestDaemonInterruptAndPublicStopShareShutdownCoordinator(t *testing.T) {
	for _, trigger := range []string{"interrupt", "public stop"} {
		t.Run(trigger, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan context.Context, 1)
			served := make(chan context.Context, 1)
			publicStop := make(chan struct{})
			shutdown := make(chan struct{})
			done := make(chan error, 1)
			startCalls, shutdownCalls := 0, 0
			go func() {
				done <- runDaemonLifecycle(ctx, func(got context.Context) {
					startCalls++
					started <- got
				}, func(got context.Context) error {
					served <- got
					if trigger == "interrupt" {
						<-got.Done()
					} else {
						<-publicStop
					}
					return nil
				}, func() {
					shutdownCalls++
					close(shutdown)
				})
			}()

			if got := <-started; got != ctx {
				t.Fatal("plugin start did not receive the interrupt context")
			}
			if got := <-served; got != ctx {
				t.Fatal("server did not receive the interrupt context")
			}
			if trigger == "interrupt" {
				cancel()
			} else {
				close(publicStop)
			}
			if err := <-done; err != nil {
				t.Fatalf("daemon lifecycle = %v", err)
			}
			select {
			case <-shutdown:
			default:
				t.Fatal("daemon shutdown coordinator was not called")
			}
			if startCalls != 1 || shutdownCalls != 1 {
				t.Fatalf("start calls=%d shutdown calls=%d", startCalls, shutdownCalls)
			}
		})
	}
}

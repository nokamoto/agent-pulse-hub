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
	daemonArgs   []string
	argument     *jsonvalue.Value
	argumentErr  error
	argumentPath []string
	response     control.RegistrationResponse
	responseWire []byte
	registerErr  error
	stopErr      error
	stopAddress  string
	registerArgs []string
	registerCall int
	stopCall     int
}

func (s *commandOperationsStub) Daemon(configPath, stateDirectory, address string, _ io.Writer) error {
	s.daemonArgs = []string{configPath, stateDirectory, address}
	return s.daemonErr
}

func (s *commandOperationsStub) Arguments(path string) (*jsonvalue.Value, error) {
	s.argumentPath = append(s.argumentPath, path)
	return s.argument, s.argumentErr
}

func (s *commandOperationsStub) Register(_ context.Context, address, input string, _ *jsonvalue.Value, output string, _ *jsonvalue.Value) (control.RegistrationResponse, []byte, error) {
	s.registerCall++
	s.registerArgs = []string{address, input, output}
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
	if err != nil || got["--required"] != "config.json" || got["--address"] != "127.0.0.1:47831" {
		t.Fatalf("parsed flags = %#v, %v", got, err)
	}
}

func TestRunDaemonPassesOptionalStateDirectory(t *testing.T) {
	operations := &commandOperationsStub{}
	var stdout, stderr strings.Builder
	configPath := filepath.Join(t.TempDir(), "config.json")
	stateDirectory := filepath.Join(t.TempDir(), "state")
	code := runWith([]string{"daemon", "--state-dir", stateDirectory, "--config", configPath, "--address", "127.0.0.1:47832"}, &stdout, &stderr, operations)
	if code != 0 || len(operations.daemonArgs) != 3 || operations.daemonArgs[0] != configPath || operations.daemonArgs[1] != stateDirectory || operations.daemonArgs[2] != "127.0.0.1:47832" {
		t.Fatalf("daemon args=%v code=%d stderr=%q", operations.daemonArgs, code, stderr.String())
	}
}

func TestResolveStateDirectoryUsesCleanConfigurationDefaultAndAbsoluteOverride(t *testing.T) {
	root := t.TempDir()
	separator := string(os.PathSeparator)
	uncleanConfig := filepath.Join(root, "run") + separator + ".." + separator + "daemon.json"
	uncleanStateDirectory := filepath.Join(root, "run") + separator + ".." + separator + "state"
	for _, test := range []struct {
		name, config, selected, want string
		invalid                      bool
	}{
		{name: "default", config: uncleanConfig, want: filepath.Join(root, "daemon.json") + ".state"},
		{name: "override", config: filepath.Join(root, "daemon.json"), selected: uncleanStateDirectory, want: filepath.Join(root, "state")},
		{name: "relative config", config: "daemon.json", invalid: true},
		{name: "relative override", config: filepath.Join(root, "daemon.json"), selected: "state", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveStateDirectory(test.config, test.selected)
			if test.invalid {
				if err == nil {
					t.Fatalf("resolveStateDirectory(%q,%q) accepted", test.config, test.selected)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveStateDirectory() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestRunRegisterPassesBothPluginRolesAndWritesExactResponses(t *testing.T) {
	args := jsonvalue.NewObject(map[string]*jsonvalue.Value{"key": jsonvalue.NewString("value")})
	for _, test := range []struct {
		name        string
		registerErr error
		wire        []byte
		wantCode    int
		wantStdout  string
		wantStderr  string
	}{
		{name: "success", wire: []byte("{\"ok\":true,\"subscription_id\":\"s1\"}\n"), wantStdout: "{\"ok\":true,\"subscription_id\":\"s1\"}\n"},
		{name: "daemon error", registerErr: &control.DaemonError{Wire: []byte("{\"ok\":false,\"code\":\"destination_rejected\",\"message\":\"invalid\"}\n")}, wire: []byte("{\"ok\":false,\"code\":\"destination_rejected\",\"message\":\"invalid\"}\n"), wantCode: 1, wantStdout: "{\"ok\":false,\"code\":\"destination_rejected\",\"message\":\"invalid\"}\n"},
		{name: "uncertain", registerErr: &control.UncertainError{Reason: "response unavailable"}, wantCode: 1, wantStderr: "register: control result is uncertain: response unavailable\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := &commandOperationsStub{argument: args, responseWire: test.wire, registerErr: test.registerErr}
			var stdout, stderr strings.Builder
			flags := []string{"register", "--input-plugin", "manual", "--watch-args-file", "watch.json", "--output-plugin", "model", "--destination-args-file", "destination.json"}
			code := runWith(flags, &stdout, &stderr, operations)
			if code != test.wantCode || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if operations.registerCall != 1 || len(operations.argumentPath) != 2 || operations.argumentPath[0] != "watch.json" || operations.argumentPath[1] != "destination.json" {
				t.Fatalf("arguments=%v registration calls=%d", operations.argumentPath, operations.registerCall)
			}
			if len(operations.registerArgs) != 3 || operations.registerArgs[1] != "manual" || operations.registerArgs[2] != "model" {
				t.Fatalf("registration routing = %v", operations.registerArgs)
			}
		})
	}
}

func TestRunRegisterRejectsMissingRoleFlagsBeforeReadingFiles(t *testing.T) {
	operations := &commandOperationsStub{argument: jsonvalue.NewObject(nil)}
	var stdout, stderr strings.Builder
	code := runWith([]string{"register", "--input-plugin", "manual", "--watch-args-file", "watch.json", "--output-plugin", "model"}, &stdout, &stderr, operations)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--destination-args-file") {
		t.Fatalf("result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(operations.argumentPath) != 0 || operations.registerCall != 0 {
		t.Fatalf("invalid CLI reached operations: %#v", operations)
	}
}

func TestLoadArgumentsRequiresAbsoluteRegularJSONObject(t *testing.T) {
	if _, err := loadArguments("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	path := filepath.Join(t.TempDir(), "args.json")
	for _, test := range []struct {
		name  string
		data  []byte
		valid bool
	}{
		{name: "object exact number", data: []byte(`{"n":9007199254740993}`), valid: true},
		{name: "array", data: []byte(`[]`)},
		{name: "malformed", data: []byte(`{`)},
		{name: "BOM", data: []byte{0xef, 0xbb, 0xbf, '{', '}'}},
		{name: "invalid UTF8", data: []byte{'{', '"', 0xff, '"', ':', '1', '}'}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadArguments(path)
			if test.valid {
				if err != nil || got.Kind() != jsonvalue.Object {
					t.Fatalf("loadArguments() = %v, %v", got, err)
				}
				field, _ := got.Get("n")
				if token, ok := field.Text(); !ok || token != "9007199254740993" {
					t.Fatalf("number token = %q, %v", token, ok)
				}
			} else if err == nil {
				t.Fatal("invalid argument file accepted")
			}
		})
	}
	if _, err := loadArguments(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing argument file accepted")
	}
}

func TestRunStopReportsFailureAndWritesSuccess(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopErr    error
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "success", wantStdout: "{\"ok\":true}\n"},
		{name: "failure", stopErr: errors.New("daemon unavailable"), wantCode: 1, wantStderr: "stop: daemon unavailable\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := &commandOperationsStub{stopErr: test.stopErr}
			var stdout, stderr strings.Builder
			code := runWith([]string{"stop"}, &stdout, &stderr, operations)
			if code != test.wantCode || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr || operations.stopCall != 1 || operations.stopAddress != control.DefaultAddress {
				t.Fatalf("result code=%d stdout=%q stderr=%q operations=%+v", code, stdout.String(), stderr.String(), operations)
			}
		})
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
			go func() {
				done <- runDaemonLifecycle(ctx, func(got context.Context) error { started <- got; return nil }, func(got context.Context) error {
					served <- got
					if trigger == "interrupt" {
						<-got.Done()
					} else {
						<-publicStop
					}
					return nil
				}, func() { close(shutdown) })
			}()
			if got := <-started; got != ctx {
				t.Fatal("plugin startup received a different interrupt context")
			}
			if got := <-served; got != ctx {
				t.Fatal("server received a different interrupt context")
			}
			if trigger == "interrupt" {
				cancel()
			} else {
				close(publicStop)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			select {
			case <-shutdown:
			default:
				t.Fatal("shutdown coordinator was not called")
			}
		})
	}
}

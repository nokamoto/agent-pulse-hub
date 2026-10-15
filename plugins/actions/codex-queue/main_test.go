package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/application"
)

func TestParsePluginArgumentsRequiresOnlyAbsoluteCodexExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsePluginArguments([]string{"--codex-executable", executable})
	if err != nil || got != executable {
		t.Fatalf("parsePluginArguments()=%q, %v", got, err)
	}
	for _, arguments := range [][]string{
		{},
		{"--codex-executable", "codex"},
		{"--codex-executable", executable, "--extra"},
		{"--codex-executable", executable, "--codex-executable", executable},
	} {
		if _, err := parsePluginArguments(arguments); err == nil {
			t.Errorf("parsePluginArguments(%q) unexpectedly succeeded", arguments)
		}
	}
}

func TestParseRegisterArgumentsRequiresEachKnownFlagOnce(t *testing.T) {
	valid := []string{
		"--client-executable", "hub", "--address", "127.0.0.1:9000",
		"--input-plugin", "manual", "--watch-args-file", "watch.json", "--output-plugin", "codex",
	}
	got, err := parseRegisterArguments(valid)
	want := application.RegisterConfig{ClientExecutable: "hub", Address: "127.0.0.1:9000", InputPlugin: "manual", WatchArgsFile: "watch.json", OutputPlugin: "codex"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRegisterArguments()=%+v, %v", got, err)
	}
	for _, arguments := range [][]string{
		valid[:len(valid)-2],
		append(append([]string(nil), valid...), "--thread-id", "11111111-1111-4111-8111-111111111111"),
		{"--client-executable", "hub", "--client-executable", "other", "--address", "127.0.0.1:9000", "--input-plugin", "manual", "--watch-args-file", "watch.json", "--output-plugin", "codex"},
	} {
		if _, err := parseRegisterArguments(arguments); err == nil {
			t.Errorf("parseRegisterArguments(%q) unexpectedly succeeded", arguments)
		}
	}
}

func TestValidateRegisterConfigChecksOnlyLocalFilePrerequisites(t *testing.T) {
	client, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	watch := filepath.Join(t.TempDir(), "watch.json")
	if err := os.WriteFile(watch, []byte(`{"trigger_file":"unused"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := application.RegisterConfig{ClientExecutable: client, Address: "127.0.0.1:9000", InputPlugin: "manual", WatchArgsFile: watch, OutputPlugin: "codex"}
	if err := validateRegisterConfig(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.ClientExecutable = "hub"
	if err := validateRegisterConfig(invalid); err == nil {
		t.Fatal("relative client path was accepted")
	}
	invalid = valid
	invalid.WatchArgsFile = filepath.Join(t.TempDir(), "missing.json")
	if err := validateRegisterConfig(invalid); err == nil {
		t.Fatal("missing watch arguments file was accepted")
	}
}

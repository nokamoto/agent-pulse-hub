package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidatesWholeConfigurationBeforeStartup(t *testing.T) {
	directory := t.TempDir()
	codexPath := filepath.Join(directory, "codex.exe")
	pluginPath := filepath.Join(directory, "manual-plugin.exe")
	configPath := filepath.Join(directory, "hub.json")
	for _, path := range []string{codexPath, pluginPath} {
		if err := os.WriteFile(path, []byte("program"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	contents, err := json.Marshal(map[string]any{
		"codex_executable": codexPath,
		"plugins": []any{map[string]any{
			"name": "manual", "executable": pluginPath, "args": []string{"--test"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CodexExecutable != codexPath || len(loaded.Plugins) != 1 || loaded.Plugins[0].Executable != pluginPath {
		t.Fatalf("unexpected config: %#v", loaded)
	}
}

func TestLoadRejectsInvalidOrMissingExecutableBeforeReturning(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "hub.json")
	missingPath, err := json.Marshal(filepath.Join(directory, "missing.exe"))
	if err != nil {
		t.Fatal(err)
	}
	contents := `{"codex_executable":` + string(missingPath) + `,"plugins":[]}`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "codex_executable is unavailable") {
		t.Fatalf("Load error = %v, want missing Codex executable", err)
	}
}

func TestLoadRejectsDuplicateJSONKeys(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "hub.json")
	if err := os.WriteFile(configPath, []byte(`{"plugins":[],"plugins":[],"codex_executable":"C:\\codex.exe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("Load accepted duplicate JSON keys")
	}
}

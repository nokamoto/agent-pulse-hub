//go:build integration

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (e *mvpCaseEnv) runtimeStateInput(t mvpReporter, directory string) (mvpPluginConfig, string) {
	t.Helper()
	pluginDirectory := filepath.Join(directory, "runtime-input")
	if err := os.MkdirAll(pluginDirectory, 0o700); err != nil {
		t.Fatalf("create runtime input directory: %v", err)
	}
	fixture := filepath.Join(e.root, "cmd", "agent-pulse-hub", "testdata", "trigger-runtime-state-plugin.py")
	return mvpPluginConfig{
		Name: "cursor", Role: "input", Executable: e.python,
		Args: []string{"-B", fixture, pluginDirectory},
	}, pluginDirectory
}

func appendRuntimeUpdate(t mvpReporter, source, id, context string) {
	t.Helper()
	data, err := json.Marshal(map[string]string{"id": id, "context": context})
	if err != nil {
		t.Fatalf("encode runtime source update: %v", err)
	}
	file, err := os.OpenFile(source, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open runtime source %s: %v", source, err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("append runtime source %s: %v", source, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close runtime source %s: %v", source, err)
	}
}

func awaitRuntimeJournal(t mvpReporter, path, kind string, count int, timeout time.Duration) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if !waitMVP(t, timeout, func() bool {
		rows = mvpRowsOfKind(path, kind)
		return len(rows) >= count
	}) {
		t.Fatalf("runtime fixture journal %s did not reach %d %s rows; rows=%v", path, count, kind, readMVPJournal(path))
	}
	return rows
}

func runtimeRowsForSubscription(rows []map[string]any, subscriptionID string) []map[string]any {
	selected := make([]map[string]any, 0)
	for _, row := range rows {
		if row["subscription_id"] == subscriptionID {
			selected = append(selected, row)
		}
	}
	return selected
}

func copyStateDirectory(t mvpReporter, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatalf("create state copy %s: %v", destination, err)
	}
	for _, name := range []string{"registrations", "staging", "runtime"} {
		sourcePath := filepath.Join(source, name)
		destinationPath := filepath.Join(destination, name)
		if _, err := os.Lstat(sourcePath); errors.Is(err, os.ErrNotExist) && name == "runtime" {
			continue
		} else if err != nil {
			t.Fatalf("inspect source state directory %s: %v", sourcePath, err)
		}
		copyStateEntry(t, sourcePath, destinationPath)
	}
}

func copyStateEntry(t mvpReporter, source, destination string) {
	t.Helper()
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatalf("inspect state entry %s: %v", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cannot copy symlink in state fixture: %s", source)
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			t.Fatalf("create copied state directory %s: %v", destination, err)
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			t.Fatalf("read source state directory %s: %v", source, err)
		}
		for _, entry := range entries {
			copyStateEntry(t, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()))
		}
		return
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("state entry %s is not a regular file or directory", source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source state file %s: %v", source, err)
	}
	if err := os.WriteFile(destination, data, info.Mode().Perm()); err != nil {
		t.Fatalf("write copied state file %s: %v", destination, err)
	}
}

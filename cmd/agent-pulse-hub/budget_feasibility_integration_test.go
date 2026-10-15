//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unicode/utf8"
)

// Design probes remain outside TestIntegration and scoped acceptance reports.
// The setup measurement builds all three actual command binaries and compiles
// both tagged command suites within the approved reserve.
func TestDesignBudgetFeasibility(t *testing.T) {
	t.Logf("minimum setup only; OS=%s architecture=%s; populated caches; not product acceptance or a complete-suite measurement", runtime.GOOS, runtime.GOARCH)
	t.Run("CommonBuild", designBudgetCommonBuild)
	t.Run("FilePrimitives", designBudgetFilePrimitives)
}

func designBudgetCommonBuild(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	run := func(args ...string) time.Duration {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = root
		started := time.Now()
		output, err := command.CombinedOutput()
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("go %v: elapsed=%s: %v\n%s", args, elapsed, err, output)
		}
		t.Logf("go %v: elapsed=%s", args, elapsed)
		return elapsed
	}
	var productBuild, suiteCompilation time.Duration
	for _, item := range []struct{ name, packagePath string }{
		{name: "agent-pulse-hub", packagePath: "./cmd/agent-pulse-hub"},
		{name: "manual-plugin", packagePath: "./plugins/triggers/manual"},
		{name: "model-output-plugin", packagePath: "./plugins/actions/model-output"},
	} {
		elapsed := run("build", "-o", filepath.Join(directory, item.name+exeSuffix()), item.packagePath)
		productBuild += elapsed
	}
	for _, item := range []struct{ name, packagePath string }{
		{name: "agent-pulse-hub", packagePath: "./cmd/agent-pulse-hub"},
		{name: "manual-plugin", packagePath: "./plugins/triggers/manual"},
	} {
		// Compile only; nested execution would recursively repeat this probe.
		suiteCompilation += run("test", "-c", "-tags=integration", "-o", filepath.Join(directory, item.name+"-suite"+exeSuffix()), item.packagePath)
	}
	estimate := productBuild + suiteCompilation
	t.Logf("three actual command builds=%s; tagged suite compilation=%s; measured common setup=%s against 20s reserve; cold caches and dependency setup unmeasured", productBuild, suiteCompilation, estimate)
	if estimate >= 20*time.Second {
		t.Errorf("setup estimate exhausted 20s reserve: %s", estimate)
	}
}

func designBudgetFilePrimitives(t *testing.T) {
	t.Helper()
	started := time.Now()
	directory := t.TempDir()
	trigger := filepath.Join(directory, "trigger")
	if filepath.Clean(directory+string(os.PathSeparator)+"."+string(os.PathSeparator)+"trigger") != trigger {
		t.Fatal("cleaned path did not match")
	}
	for _, sample := range []struct {
		data      []byte
		validUTF8 bool
	}{
		{bytes.Repeat([]byte("a"), 8192), true},
		{bytes.Repeat([]byte("b"), 8193), true},
		{[]byte{0xff}, false},
		{[]byte("PULSE-MARKER 日本語\n"), true},
	} {
		temporary, claimed := filepath.Join(directory, "publish"), filepath.Join(directory, "claim")
		if err := os.WriteFile(temporary, sample.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, trigger); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(trigger, claimed); err != nil {
			t.Fatal(err)
		}
		read, err := os.ReadFile(claimed)
		if err != nil || !bytes.Equal(read, sample.data) || utf8.Valid(read) != sample.validUTF8 {
			t.Fatalf("byte preservation: %v", err)
		}
		if err := os.Remove(claimed); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{temporary, trigger, claimed} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("primitive left %s: %v", path, err)
			}
		}
	}
	// Qualify ordinary destination observation independently of plugin behavior.
	output := filepath.Join(directory, "output.jsonl")
	for _, record := range [][]byte{[]byte("{\"context\":\"PULSE-A\"}\n"), []byte("{\"context\":\"PULSE-B\"}\n")} {
		file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		written, writeErr := file.Write(record)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil || written != len(record) {
			t.Fatalf("append: bytes=%d write=%v close=%v", written, writeErr, closeErr)
		}
	}
	observed, err := os.ReadFile(output)
	if err != nil || string(observed) != "{\"context\":\"PULSE-A\"}\n{\"context\":\"PULSE-B\"}\n" {
		t.Fatalf("ordinary output read=%q error=%v", observed, err)
	}
	t.Logf("four input byte samples and sibling rename/read/remove; two JSONL append-close-read records; zero children; elapsed=%s; no product validation or delivery claim", time.Since(started))
}

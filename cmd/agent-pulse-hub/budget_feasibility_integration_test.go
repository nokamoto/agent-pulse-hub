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

// These independent probes measure the minimum setup behind the design's cost
// estimates. They implement neither product behavior nor MVP acceptance cases.
// Their TestDesignBudget prefix keeps them outside the existing integration
// runner's TestIntegration filter and its scoped acceptance reports.
func TestDesignBudgetFeasibility(t *testing.T) {
	t.Logf("minimum setup only; OS=%s architecture=%s; current checkout and existing Go cache; not product acceptance or a complete-suite measurement", runtime.GOOS, runtime.GOARCH)
	t.Run("CommonBuild", designBudgetCommonBuild)
	t.Run("FilePrimitives", designBudgetFilePrimitives)
	t.Run("DeliveryProcesses", designBudgetDeliveryProcesses)
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
	for _, command := range []string{"agent-pulse-hub", "manual-plugin"} {
		productBuild += run("build", "-o", filepath.Join(directory, command+".exe"), "./cmd/"+command)
	}
	for _, command := range []string{"agent-pulse-hub", "manual-plugin"} {
		// Compile only: running nested tests would repeat this probe recursively.
		suiteCompilation += run("test", "-c", "-tags=integration", "-o", filepath.Join(directory, command+"-suite.exe"), "./cmd/"+command)
	}
	commonBuild := productBuild + suiteCompilation
	t.Logf("current product builds=%s; tagged suite compilation=%s; combined=%s against 20s common build/setup reserve; dependency preparation and cold cache excluded; future product compilation/setup must be measured after implementation", productBuild, suiteCompilation, commonBuild)
	if commonBuild >= 20*time.Second {
		t.Errorf("current product builds and tagged compilation exhausted the 20s build/setup reserve: %s", commonBuild)
	}
}

func designBudgetFilePrimitives(t *testing.T) {
	t.Helper()
	started := time.Now()
	directory := t.TempDir()
	trigger := filepath.Join(directory, "trigger")
	if filepath.Clean(directory+string(os.PathSeparator)+"."+string(os.PathSeparator)+"trigger") != trigger {
		t.Fatal("standard cleaned spelling did not identify the same exact path")
	}
	for _, sample := range []struct {
		name      string
		data      []byte
		validUTF8 bool
	}{
		{"8192 bytes", bytes.Repeat([]byte("a"), 8192), true},
		{"8193 bytes", bytes.Repeat([]byte("b"), 8193), true},
		{"invalid UTF-8", []byte{0xff}, false},
		{"Unicode marker", []byte("PULSE-MARKER 日本語\n"), true},
	} {
		temporary := filepath.Join(directory, "publish")
		claimed := filepath.Join(directory, "claim")
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
			t.Fatalf("%s primitive byte preservation: error=%v", sample.name, err)
		}
		if err := os.Remove(claimed); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{temporary, trigger, claimed} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("primitive did not release %s: %v", path, err)
			}
		}
	}
	t.Logf("four bounded byte samples: sibling publish/claim rename, read/close, UTF-8 observation, remove and exact-path cleaning; zero children; elapsed=%s; no product size-limit, watch, ingestion or emission behavior claimed", time.Since(started))
}

func designBudgetDeliveryProcesses(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Nonzero", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestDesignBudgetHelperProcess$")
		command.Env = append(os.Environ(), "APH_DESIGN_BUDGET_HELPER=nonzero")
		started := time.Now()
		err := command.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 7 || command.ProcessState == nil {
			t.Fatalf("controlled nonzero child: state=%v error=%v", command.ProcessState, err)
		}
		t.Logf("one direct child PID=%d exited 7 and Wait completed; elapsed=%s", command.Process.Pid, time.Since(started))
	})
	t.Run("ThirtySecondCancellation", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "ready")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestDesignBudgetHelperProcess$")
		command.Env = append(os.Environ(), "APH_DESIGN_BUDGET_HELPER=hold", "APH_DESIGN_BUDGET_READY="+readyPath)
		started := time.Now()
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		waited := make(chan error, 1)
		go func() { waited <- command.Wait() }()
		collected := false
		defer func() {
			cancel()
			// CommandContext kills the direct child on failure as well as success.
			// Wait above always owns process collection; it is never called twice.
			if !collected {
				select {
				case <-waited:
					collected = true
				case <-time.After(5 * time.Second):
					t.Error("controlled child cleanup exceeded five seconds")
				}
			}
		}()
		readyDeadline := time.Now().Add(5 * time.Second)
		for {
			if value, err := os.ReadFile(readyPath); err == nil && string(value) == "ready" {
				break
			}
			if time.Now().After(readyDeadline) {
				t.Fatal("controlled held child did not become ready within five seconds")
			}
			time.Sleep(10 * time.Millisecond)
		}
		readyElapsed := time.Since(started)
		select {
		case err := <-waited:
			collected = true
			elapsed := time.Since(started)
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) || err == nil || command.ProcessState == nil || command.ProcessState.Success() {
				t.Fatalf("deadline cancellation did not stop and collect the child: ctx=%v state=%v error=%v", ctx.Err(), command.ProcessState, err)
			}
			if elapsed >= 35*time.Second {
				t.Errorf("minimum delivery deadline setup exceeded the 35s case cap: %s", elapsed)
			}
			t.Logf("one direct child PID=%d ready=%s; real 30s deadline, cancellation and Wait observed; elapsed=%s; time after deadline=%s; 35s case cap includes future adapter/plugin setup and cleanup not measured here", command.Process.Pid, readyElapsed, elapsed, elapsed-30*time.Second)
		case <-time.After(35*time.Second - time.Since(started)):
			t.Fatal("deadline child did not finish within the 35s case cap")
		}
	})
}

func TestDesignBudgetHelperProcess(t *testing.T) {
	switch os.Getenv("APH_DESIGN_BUDGET_HELPER") {
	case "nonzero":
		os.Exit(7)
	case "hold":
		if err := os.WriteFile(os.Getenv("APH_DESIGN_BUDGET_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		t.Fatal("held child outlived the controlling deadline")
	}
}

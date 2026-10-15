//go:build integration

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Model input plugin public Session acceptance", Serial, func() {
	It("emits valid file context unchanged and rejects invalid content without truncation or retry", Label("case:MVP-V04-MANUAL-INVALID-CONTENT"), func() {
		t := GinkgoT()
		root, err := os.MkdirTemp("", "manual-invalid-content-")
		if err != nil {
			t.Fatal(err)
		}
		DeferCleanup(func() { _ = os.RemoveAll(root) })
		host := startManualPluginHost(t, root)
		DeferCleanup(host.cleanup)
		trigger := filepath.Join(root, "trigger.txt")
		watch := host.watch(t, "watch-content", "subscription-content", trigger)
		if watch.Accepted == nil || !*watch.Accepted {
			t.Fatalf("watch rejected: %+v", watch)
		}

		valid := strings.Repeat("v", 8192)
		writeManualTrigger(t, trigger, []byte(valid))
		host.expectEvent(t, "subscription-content", valid)
		if _, err := os.Stat(trigger); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("valid trigger was not consumed: %v", err)
		}

		writeManualTrigger(t, trigger, []byte(strings.Repeat("x", 8193)))
		host.assertNoFrame(t, 250*time.Millisecond)
		waitManualLog(t, host.log, "manual_plugin_trigger_rejected", time.Second)
		writeManualTrigger(t, trigger, []byte{0xff, 0xfe})
		host.assertNoFrame(t, 250*time.Millisecond)
		waitManualLog(t, host.log, "manual_plugin_trigger_rejected", time.Second)
		writeManualTrigger(t, trigger, []byte("PULSE-MANUAL-MARKER 日本語"))
		host.expectEvent(t, "subscription-content", "PULSE-MANUAL-MARKER 日本語")
		host.assertNoFrame(t, 200*time.Millisecond)
		if !manualPluginLogContains(host.log, "claimed_path") {
			t.Fatalf("invalid content diagnostics omitted the claimed path: %s", readManualFile(host.log))
		}
	})

	It("rejects duplicate cleaned trigger paths and existing triggers while accepting a distinct absent path", Label("case:MVP-V02-MANUAL-PATHS"), func() {
		t := GinkgoT()
		root, err := os.MkdirTemp("", "manual-paths-")
		if err != nil {
			t.Fatal(err)
		}
		DeferCleanup(func() { _ = os.RemoveAll(root) })
		host := startManualPluginHost(t, root)
		DeferCleanup(host.cleanup)

		firstPath := filepath.Join(root, "first-trigger.txt")
		first := host.watch(t, "watch-first", "subscription-first", firstPath)
		if first.Accepted == nil || !*first.Accepted {
			t.Fatalf("absent trigger watch rejected: %+v", first)
		}
		cleanedEquivalent := filepath.Dir(firstPath) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(firstPath)
		duplicate := host.watch(t, "watch-duplicate", "subscription-duplicate", cleanedEquivalent)
		if duplicate.Accepted == nil || *duplicate.Accepted {
			t.Fatalf("cleaned-equivalent path was not rejected: %+v", duplicate)
		}
		existingPath := filepath.Join(root, "existing-trigger.txt")
		if err := os.WriteFile(existingPath, []byte("already here"), 0o600); err != nil {
			t.Fatal(err)
		}
		existing := host.watch(t, "watch-existing", "subscription-existing", existingPath)
		if existing.Accepted == nil || *existing.Accepted {
			t.Fatalf("existing trigger path was not rejected: %+v", existing)
		}
		secondPath := filepath.Join(root, "second-trigger.txt")
		second := host.watch(t, "watch-second", "subscription-second", secondPath)
		if second.Accepted == nil || !*second.Accepted {
			t.Fatalf("distinct absent trigger path rejected: %+v", second)
		}
		writeManualTrigger(t, secondPath, []byte("PULSE-SECOND-PATH"))
		host.expectEvent(t, "subscription-second", "PULSE-SECOND-PATH")
		host.assertNoFrame(t, 150*time.Millisecond)
	})
})

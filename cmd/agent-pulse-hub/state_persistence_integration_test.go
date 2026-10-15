//go:build integration

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// These cases are governed by docs/design/state-persistence.md. Pending defers
// product wiring only; the comments define the approved concrete observations.
var _ = Describe("Local registration persistence command acceptance", Serial, func() {
	It("restores saved routing values and deduplicates the restored registration", Label("case:SP-V01-RESTORE"), Pending, func() {
		// Requirements: state-persistence FR-001, FR-002, FR-004, NFR-001;
		// acceptance AC-001 and AC-002. Use the actual daemon, local register/stop
		// client, bundled manual trigger and model-output action; no live service.
		// Prepare one isolated config with names manual/model, absent absolute
		// trigger T, destination file A and an unregistered destination file B.
		// Start without --state-dir: expect daemon_ready restored_registrations=0
		// and storage at the cleaned absolute config pathname plus ".state".
		// Register T -> A from argument JSON files; retain returned subscription S.
		// Inspect exactly one registrations/S.json: version=1, S, both names and
		// complete decoded watch/destination argument objects equal the inputs.
		// Delete both client argument files; normal stop and await daemon/plugin
		// exits and lock release. Restart with unchanged config/default storage.
		// Await daemon_ready restored_registrations=1, without registration; publish
		// unique Unicode context E1 atomically to T and await its output record.
		// Recreate equivalent argument files, register again and require ID S;
		// publish distinct E2. A contains E1/E2 once each with source manual, ID S,
		// exact context, external_data=true and the existing permission notice.
		// B remains absent; record count remains one, no second watch is requested
		// and duplicate registration alone causes no delivery. Waits are bounded.
		// Budget: 10s case, shared warmed setup <=20s, cleanup <=5s suite-wide.
	})

	It("rejects registration when the local record cannot be committed", Label("case:SP-V02-SAVE-FAILURE"), Pending, func() {
		// Requirements: state-persistence FR-002, FR-003, NFR-001; AC-003.
		// Use actual daemon/client/manual/model-output with explicit isolated
		// --state-dir. Await daemon_ready count zero. Separately reject a bad
		// destination and an existing trigger-file watch: each register exits 1,
		// leaves no active route and no committed record, and delivers nothing.
		// Restore valid arguments with absent trigger T. While retaining the
		// daemon's .lock, replace its empty registrations directory with a regular
		// file. This is deliberate fixture fault injection, not operator guidance.
		// Register the otherwise valid T -> A route. Require exit 1, ok:false,
		// code storage_error and bounded diagnostic identifying storage path/reason.
		// Publish unique E-UNSAVED to T: no delivery or active subscription occurs,
		// no successful record exists, and the healthy shared input stays alive.
		// Normal stop; await configured exits/lock release. Restore the empty
		// directory while stopped, archive any leftover trigger deliberately,
		// restart same config/storage: ready count zero and no event replay.
		// Unit tests inject write/close/rename branches and assert existing saved
		// records survive; this case proves the real filesystem/activation boundary.
		// Budget: 10s case, real local processes only, awaited cleanup.
	})

	It("fails startup without partial admission when saved state cannot restore", Label("case:SP-V03-STARTUP-FAILURE"), Pending, func() {
		// Requirements: state-persistence FR-005, NFR-001; AC-004.
		// With actual daemon/client/manual/model-output, save two distinct routes
		// T1 -> A1 and T2 -> A2, normally stop, and preserve the complete tree.
		// In independent copies, (a) truncate/malform the later record, (b) change
		// its input_plugin to an unconfigured name, and (c) keep a file at saved
		// trigger T2 so current manual watch validation rejects restoration.
		// Ensure a valid earlier filename-ordered route remains in each copy.
		// For every startup: exit 1, no daemon_ready, no successful partial startup
		// and no delivery (including any event received on an earlier staged route).
		// Diagnostic names affected file or subscription and reason. Compare saved
		// bytes before/after: no deletion, rewriting or substitute destination.
		// Observe all started children exit and .lock release after failed startup.
		// Separately keep the valid daemon live, then launch another at a distinct
		// control address with the same state-dir: second exits 1 for .lock, touches
		// no first-owner state, and the first still routes a fresh unique event.
		// Fresh empty storage initialization is covered by V01; mocked read-error
		// unit checks cover unreadable files without OS-specific permission tricks.
		// Budget: 20s case; sequential copies and bounded process observations.
	})
})

// This probe qualifies local file mechanisms only, outside TestIntegration and
// all product acceptance reports. It does not implement or verify persistence.
func TestStatePersistenceFileFeasibility(t *testing.T) {
	started := time.Now()
	root := t.TempDir()
	lock := filepath.Join(root, ".lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second owner: %v", err)
	}
	staging, records := filepath.Join(root, "staging"), filepath.Join(root, "registrations")
	for _, directory := range []string{staging, records} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte(`{"version":1,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71","watch_args":{"number":9007199254740993,"text":"日本語"}}`)
	stage := filepath.Join(staging, "probe.tmp")
	file, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	n, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || n != len(payload) {
		t.Fatalf("write/close: n=%d write=%v close=%v", n, writeErr, closeErr)
	}
	final := filepath.Join(records, "8fc5db2a-77a0-4f60-bf92-bbe214a83a71.json")
	if err := os.Rename(stage, final); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(final)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("committed bytes: %q %v", got, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
	if err := os.Remove(final); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(records); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(records, []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, "another.json"), payload, 0o600); err == nil {
		t.Fatal("regular-file parent allowed a record write")
	}
	for _, path := range []string{records, staging, lock} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: entries=%v error=%v", entries, err)
	}
	t.Logf("OS=%s arch=%s; lock exclusion, exclusive staging write/close, sibling rename, exact bytes, regular-file obstruction and cleanup; zero children; elapsed=%s; feasibility only", runtime.GOOS, runtime.GOARCH, time.Since(started))
}

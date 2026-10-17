package registrationstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestRuntimeGenerationCommitRestoreAndCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testSubscription(t, testID)
	if err := store.Commit(registration); err != nil {
		t.Fatal(err)
	}
	if states, err := store.LoadRuntime([]domain.Subscription{registration}); err != nil || len(states) != 0 {
		t.Fatalf("initial runtime state = %v, %v", states, err)
	}

	first := runtimeTestState(t, `{"schema":"cursor-v1","payload":{"confirmed":["U1"],"n":9007199254740993}}`)
	if generation, err := store.CommitRuntime(registration, first); generation != 1 || err != nil {
		t.Fatalf("first CommitRuntime = %d, %v", generation, err)
	}
	second := runtimeTestState(t, `{"schema":"cursor-v1","payload":{"confirmed":["U1","U2"]}}`)
	if generation, err := store.CommitRuntime(registration, second); generation != 2 || err != nil {
		t.Fatalf("second CommitRuntime = %d, %v", generation, err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "runtime", testID, "*.json"))
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != generationName(2) {
		t.Fatalf("committed generations = %v, %v", paths, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	states, err := store.LoadRuntime([]domain.Subscription{registration})
	if err != nil || len(states) != 1 || !states[registration.ID].Equivalent(second) {
		t.Fatalf("restored runtime state = %v, %v", states, err)
	}
	watchNumber, _ := states[registration.ID].Get("payload")
	confirmed, _ := watchNumber.Get("confirmed")
	number, _ := confirmed.Items()[0].Text()
	if number != "U1" {
		t.Fatalf("restored progress = %s", number)
	}
	if got, err := os.ReadFile(filepath.Join(root, "registrations", testID+".json")); err != nil || strings.Contains(string(got), "runtime") {
		t.Fatalf("registration record changed to include runtime state: %s (%v)", got, err)
	}
}

func TestRuntimeSaveFailurePreservesPriorGenerationAndRemovesFirstEmptyDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	files := &faultFileSystem{fileSystem: osFileSystem{}}
	store, err := open(root, files)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registration := testSubscription(t, testID)
	if err := store.Commit(registration); err != nil {
		t.Fatal(err)
	}
	state := runtimeTestState(t, `{"schema":"cursor-v1","payload":{"confirmed":["U1"]}}`)
	if generation, err := store.CommitRuntime(registration, state); generation != 1 || err != nil {
		t.Fatalf("initial runtime save = %d, %v", generation, err)
	}

	files.failAt, files.failure = "rename", errors.New("injected rename failure")
	next := runtimeTestState(t, `{"schema":"cursor-v1","payload":{"confirmed":["U1","U2"]}}`)
	if generation, err := store.CommitRuntime(registration, next); generation != 0 || err == nil {
		t.Fatalf("failed runtime save = %d, %v", generation, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime", testID, generationName(2))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed save left a committed generation: %v", err)
	}
	files.failAt = ""
	states, err := store.LoadRuntime([]domain.Subscription{registration})
	if err != nil || len(states) != 1 || !states[registration.ID].Equivalent(state) {
		t.Fatalf("old generation after failed save = %v, %v", states, err)
	}

	other := testSubscription(t, "9fc5db2a-77a0-4f60-bf92-bbe214a83a71")
	if err := store.Commit(other); err != nil {
		t.Fatal(err)
	}
	files.failAt = "rename"
	if generation, err := store.CommitRuntime(other, state); generation != 0 || err == nil {
		t.Fatalf("failed first runtime save = %d, %v", generation, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime", string(other.ID))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed first save retained empty owner directory: %v", err)
	}
}

func TestRuntimeLoadFailsOnCorruptNewestAndDoesNotReadOrphans(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testSubscription(t, testID)
	if err := store.Commit(registration); err != nil {
		t.Fatal(err)
	}
	if generation, err := store.CommitRuntime(registration, runtimeTestState(t, `{"schema":"v1","payload":{"n":1}}`)); generation != 1 || err != nil {
		t.Fatalf("save generation 1 = %d, %v", generation, err)
	}
	ownerDirectory := filepath.Join(root, "runtime", testID)
	if err := os.WriteFile(filepath.Join(ownerDirectory, generationName(2)), []byte("corrupt newest"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanID := "7fc5db2a-77a0-4f60-bf92-bbe214a83a71"
	orphanDirectory := filepath.Join(root, "runtime", orphanID)
	if err := os.Mkdir(orphanDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDirectory, generationName(1)), []byte("not interpreted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if states, err := store.LoadRuntime(nil); err != nil || len(states) != 0 {
		t.Fatalf("orphan runtime files should not be interpreted: %v, %v", states, err)
	}
	if _, err := store.LoadRuntime([]domain.Subscription{registration}); err == nil || !strings.Contains(err.Error(), generationName(2)) {
		t.Fatalf("corrupt newest generation error = %v", err)
	}
	_ = store.Close()
}

func TestRuntimeDiagnosticsDoNotExposeOpaquePayloadKeys(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registration := testSubscription(t, testID)
	if err := store.Commit(registration); err != nil {
		t.Fatal(err)
	}
	ownerDirectory := filepath.Join(root, "runtime", testID)
	if err := os.Mkdir(ownerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"subscription_id":"` + testID + `","input_plugin":"` + registration.InputPlugin + `","generation":"1","state":{"schema":"v1","payload":{"private-token":1,"private-token":2}}}`
	if err := os.WriteFile(filepath.Join(ownerDirectory, generationName(1)), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.LoadRuntime([]domain.Subscription{registration})
	if err == nil || !strings.Contains(err.Error(), string(registration.ID)) || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("opaque payload diagnostic = %v", err)
	}
}

func TestLegacyRegistrationTreeCreatesRuntimeDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"registrations", "staging"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Lstat(filepath.Join(root, "runtime"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("legacy runtime directory = %v, %v", info, err)
	}
}

func TestNewRegistrationCannotClaimOrphanRuntimeDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	orphan := filepath.Join(root, "runtime", testID)
	if err := os.Mkdir(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, generationName(1)), []byte("opaque orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(testSubscription(t, testID)); err == nil || !strings.Contains(err.Error(), "already exists for a new registration") {
		t.Fatalf("orphan runtime identity was reused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "registrations", testID+".json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan collision committed a registration: %v", err)
	}
}

func runtimeTestState(t *testing.T, raw string) *jsonvalue.Value {
	t.Helper()
	state, err := jsonvalue.Parse([]byte(raw), 0)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

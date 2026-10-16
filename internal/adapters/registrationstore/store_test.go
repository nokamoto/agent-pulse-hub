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

const testID = "8fc5db2a-77a0-4f60-bf92-bbe214a83a71"

func testSubscription(t *testing.T, id string) domain.Subscription {
	t.Helper()
	watch, err := jsonvalue.Parse([]byte(`{"trigger":"/tmp/日本語","n":9007199254740993}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := jsonvalue.Parse([]byte(`{"output_file":"/tmp/out.jsonl","enabled":true}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Subscription{
		ID: domain.SubscriptionID(id), InputPlugin: "manual", OutputPlugin: "model",
		WatchArgs: watch, DestinationArgs: destination, Active: true,
	}
}

func TestOpenCommitLoadAndReleaseStateDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); err != nil {
		t.Fatalf("lock not held: %v", err)
	}
	if err := store.Commit(testSubscription(t, testID)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staging", "stale.tmp"), []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lock was not released: %v", err)
	}
	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(root, "staging", "stale.tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale stage was not removed: %v", err)
	}
	registrations, err := store.Load()
	if err != nil || len(registrations) != 1 {
		t.Fatalf("Load() = %v, %v", registrations, err)
	}
	want := testSubscription(t, testID)
	got := registrations[0]
	if got.ID != want.ID || got.InputPlugin != want.InputPlugin || got.OutputPlugin != want.OutputPlugin || !got.WatchArgs.Equivalent(want.WatchArgs) || !got.DestinationArgs.Equivalent(want.DestinationArgs) || got.Active {
		t.Fatalf("loaded registration = %+v", got)
	}
	watchNumber, _ := got.WatchArgs.Get("n")
	if token, _ := watchNumber.Text(); token != "9007199254740993" {
		t.Fatalf("exact number changed: %q", token)
	}
}

func TestOpenRequiresAbsoluteInitializedDirectoryAndExclusiveLock(t *testing.T) {
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative state path accepted")
	}
	parent := t.TempDir()
	if _, err := Open(filepath.Join(parent, "missing", "state")); err == nil {
		t.Fatal("missing parent accepted")
	}

	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open pre-existing empty directory: %v", err)
	}
	for _, directory := range []string{"registrations", "staging"} {
		info, err := os.Stat(filepath.Join(root, directory))
		if err != nil || !info.IsDir() {
			t.Fatalf("fresh directory %s = %v, %v", directory, info, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("closed store retained lock: %v", err)
	}

	incomplete := filepath.Join(parent, "incomplete")
	if err := os.Mkdir(incomplete, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(incomplete, "registrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(incomplete); err == nil {
		t.Fatal("partially initialized directory accepted")
	}
	if _, err := os.Stat(filepath.Join(incomplete, ".lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed open retained lock: %v", err)
	}

	store, err = Open(filepath.Join(parent, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(parent, "owned")); err == nil {
		t.Fatal("second owner acquired the same state directory")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	unknown := filepath.Join(parent, "unknown")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknown, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unknown); err == nil || !strings.Contains(err.Error(), "unknown entry") {
		t.Fatalf("unknown entry error = %v", err)
	}
}

func TestLoadRejectsInvalidRecordsAndDirectoryEntries(t *testing.T) {
	for _, test := range []struct {
		name string
		file string
		data []byte
	}{
		{name: "malformed", file: testID + ".json", data: []byte("{")},
		{name: "BOM", file: testID + ".json", data: []byte{0xef, 0xbb, 0xbf, '{', '}'}},
		{name: "duplicate fields", file: testID + ".json", data: []byte(`{"version":1,"version":1}`)},
		{name: "unknown field", file: testID + ".json", data: []byte(`{"version":1,"subscription_id":"` + testID + `","input_plugin":"manual","watch_args":{},"output_plugin":"model","destination_args":{},"extra":true}`)},
		{name: "fractional version", file: testID + ".json", data: []byte(`{"version":1.0,"subscription_id":"` + testID + `","input_plugin":"manual","watch_args":{},"output_plugin":"model","destination_args":{}}`)},
		{name: "filename mismatch", file: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.json", data: encodeRecord(testSubscription(t, testID))},
		{name: "uppercase filename", file: "8FC5DB2A-77A0-4F60-BF92-BBE214A83A71.json", data: []byte(`{}`)},
		{name: "oversized", file: testID + ".json", data: make([]byte, MaxRecordBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if err := os.MkdirAll(filepath.Join(root, "registrations"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "staging"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "registrations", test.file), test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err := store.Load(); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}

	t.Run("directory in registrations", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		if err := os.MkdirAll(filepath.Join(root, "registrations", testID+".json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "staging"), 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.Load(); err == nil {
			t.Fatal("directory record accepted")
		}
	})
}

func TestCommitWriteCloseAndRenameFailuresCleanStaging(t *testing.T) {
	failure := errors.New("injected filesystem failure")
	for _, stage := range []string{"write", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			files := &faultFileSystem{fileSystem: osFileSystem{}, failure: failure}
			root := filepath.Join(t.TempDir(), "state")
			store, err := open(root, files)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.Commit(testSubscription(t, testID)); err != nil {
				t.Fatalf("initial commit: %v", err)
			}
			files.failAt = stage
			second := testSubscription(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
			second.WatchArgs = testArgsStore(t, `{"trigger":"second"}`)
			if err := store.Commit(second); !errors.Is(err, failure) {
				t.Fatalf("Commit() error = %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "staging"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("staging after failed commit = %v, %v", entries, err)
			}
			entries, err = os.ReadDir(filepath.Join(root, "registrations"))
			if err != nil || len(entries) != 1 || entries[0].Name() != testID+".json" {
				t.Fatalf("records after failed commit = %v, %v", entries, err)
			}
			files.failAt = ""
			registrations, err := store.Load()
			if err != nil || len(registrations) != 1 || registrations[0].ID != testID {
				t.Fatalf("previous record did not survive failed %s: %v, %v", stage, registrations, err)
			}
		})
	}
}

func TestLoadReportsReadFailureWithoutChangingCommittedRecords(t *testing.T) {
	failure := errors.New("injected read failure")
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(testSubscription(t, testID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	files := &faultFileSystem{fileSystem: osFileSystem{}, failAt: "read", failure: failure}
	store, err = open(root, files)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Load(); !errors.Is(err, failure) {
		t.Fatalf("Load() read error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "registrations", testID+".json"))
	if err != nil {
		t.Fatalf("committed record was changed or deleted: %v", err)
	}
	if _, err := decodeRecord(data); err != nil {
		t.Fatalf("committed record was corrupted: %v", err)
	}
}

func TestLoadRejectsDuplicateCanonicalRoutesAndCleansStagingOnlyUnderLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Join(root, "registrations"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "staging"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := testSubscription(t, testID)
	second := testSubscription(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	second.WatchArgs, _ = jsonvalue.Parse([]byte(`{"n":9007199254740993.0,"trigger":"/tmp/日本語"}`), 0)
	if err := os.WriteFile(filepath.Join(root, "registrations", string(first.ID)+".json"), encodeRecord(first), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registrations", string(second.ID)+".json"), encodeRecord(second), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staging", "orphan.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(root, "staging", "orphan.tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale staging file remains: %v", err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "duplicates a route") {
		t.Fatalf("duplicate route error = %v", err)
	}
}

type faultFileSystem struct {
	fileSystem
	failAt  string
	failure error
}

func (f *faultFileSystem) OpenFile(path string, flags int, mode os.FileMode) (writeFile, error) {
	file, err := f.fileSystem.OpenFile(path, flags, mode)
	if err != nil {
		return nil, err
	}
	return faultWriteFile{writeFile: file, failAt: f.failAt, failure: f.failure}, nil
}

func (f *faultFileSystem) Open(path string) (readFile, error) {
	if f.failAt == "read" {
		return nil, f.failure
	}
	return f.fileSystem.Open(path)
}

func (f *faultFileSystem) Rename(oldPath, newPath string) error {
	if f.failAt == "rename" {
		return f.failure
	}
	return f.fileSystem.Rename(oldPath, newPath)
}

type faultWriteFile struct {
	writeFile
	failAt  string
	failure error
}

func testArgsStore(t *testing.T, source string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(source), 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f faultWriteFile) Write(data []byte) (int, error) {
	if f.failAt == "write" {
		return 0, f.failure
	}
	return f.writeFile.Write(data)
}

func (f faultWriteFile) Close() error {
	err := f.writeFile.Close()
	if f.failAt == "close" {
		return errors.Join(err, f.failure)
	}
	return err
}

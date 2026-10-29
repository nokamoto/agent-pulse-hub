package registrationstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func cancellationStore(t *testing.T) (*Store, domain.Subscription) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	owner := testSubscription(t, testID)
	if err := s.Commit(owner); err != nil {
		t.Fatal(err)
	}
	return s, owner
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s remains: %v", path, err)
	}
}

func TestCancellationCommitReuseRemoveAndRestart(t *testing.T) {
	s, owner := cancellationStore(t)
	other := testSubscription(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	other.InputPlugin = "other"
	if err := s.Commit(other); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.runtime, testID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Cancelled payloads need not parse, and do not inherit the runtime size cap.
	if err := os.WriteFile(filepath.Join(dir, generationName(1)), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCancellation(owner); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.cancellationPath(owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCancellation(owner); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.cancellationPath(owner.ID))
	if err != nil || string(before) != string(after) {
		t.Fatalf("intent replaced: %s %v", after, err)
	}
	cleanup, err := s.RemoveCancelled(owner)
	if cleanup != nil || err != nil {
		t.Fatalf("remove: %v %v", cleanup, err)
	}
	for _, path := range []string{dir, filepath.Join(s.records, testID+".json"), s.cancellationPath(owner.ID)} {
		requireAbsent(t, path)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if cleanup, err := reopened.RecoverCancellations([]string{"manual", "other"}); cleanup != nil || err != nil {
		t.Fatalf("recovery: %v %v", cleanup, err)
	}
	regs, err := reopened.Load()
	if err != nil || len(regs) != 1 || regs[0].ID != other.ID {
		t.Fatalf("retained: %v %v", regs, err)
	}
}

func TestCancellationRecoveryValidatesAllBeforeDeleting(t *testing.T) {
	for _, invalid := range []string{"json", "version", "extra", "duplicate", "owner", "input", "filename", "oversize", "directory", "runtime"} {
		t.Run(invalid, func(t *testing.T) {
			s, owner := cancellationStore(t)
			if err := s.BeginCancellation(owner); err != nil {
				t.Fatal(err)
			}
			badID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
			bad := domain.Subscription{ID: domain.SubscriptionID(badID), InputPlugin: "manual"}
			path := s.cancellationPath(bad.ID)
			data := encodeCancellation(bad)
			switch invalid {
			case "json":
				data = []byte("{")
			case "version":
				data = []byte(strings.Replace(string(data), `"version":1`, `"version":1.0`, 1))
			case "extra":
				data = []byte(strings.TrimSuffix(string(data), "}") + `,"extra":true}`)
			case "duplicate":
				data = []byte(strings.TrimSuffix(string(data), "}") + `,"version":1}`)
			case "owner":
				registration := testSubscription(t, badID)
				registration.InputPlugin = "different"
				if err := s.Commit(registration); err != nil {
					t.Fatal(err)
				}
			case "input":
				bad.InputPlugin = "unknown"
				data = encodeCancellation(bad)
			case "filename":
				path = filepath.Join(s.cancellations, "invalid.json")
			case "oversize":
				data = make([]byte, MaxRecordBytes+1)
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				if err := os.WriteFile(filepath.Join(s.runtime, badID), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if invalid != "directory" {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.RecoverCancellations([]string{"manual"}); err == nil {
				t.Fatal("invalid intent accepted")
			}
			if _, err := os.Stat(filepath.Join(s.records, testID+".json")); err != nil {
				t.Fatalf("earlier valid intent deleted before validation: %v", err)
			}
			if _, err := os.Stat(s.cancellationPath(owner.ID)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationCommitFailuresPreserveRegistration(t *testing.T) {
	for _, at := range []string{"write", "close", "rename"} {
		t.Run(at, func(t *testing.T) {
			s, owner := cancellationStore(t)
			failure := errors.New("injected " + at)
			s.files = &faultFileSystem{fileSystem: s.files, failAt: at, failure: failure}
			if err := s.BeginCancellation(owner); !errors.Is(err, failure) {
				t.Fatalf("failure=%v", err)
			}
			requireAbsent(t, s.cancellationPath(owner.ID))
			if _, err := os.Stat(filepath.Join(s.records, testID+".json")); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(s.staging)
			if err != nil || len(entries) != 0 {
				t.Fatalf("staging=%v %v", entries, err)
			}
		})
	}
}

type cancellationRemoveFS struct {
	fileSystem
	failPath string
	calls    []string
}

func (f *cancellationRemoveFS) Remove(path string) error {
	f.calls = append(f.calls, path)
	if path == f.failPath {
		return errors.New("injected removal failure")
	}
	return f.fileSystem.Remove(path)
}

func TestCancellationPartialRemovalAndHousekeepingRecovery(t *testing.T) {
	for _, stage := range []string{"runtime", "registration", "intent"} {
		t.Run(stage, func(t *testing.T) {
			s, owner := cancellationStore(t)
			dir := filepath.Join(s.runtime, testID)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			generation := filepath.Join(dir, generationName(1))
			if err := os.WriteFile(generation, []byte("opaque"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.BeginCancellation(owner); err != nil {
				t.Fatal(err)
			}
			registration := filepath.Join(s.records, testID+".json")
			intent := s.cancellationPath(owner.ID)
			fault := &cancellationRemoveFS{fileSystem: s.files}
			fault.failPath = map[string]string{"runtime": generation, "registration": registration, "intent": intent}[stage]
			s.files = fault
			cleanup, err := s.RemoveCancelled(owner)
			if stage == "intent" {
				if err != nil || cleanup == nil {
					t.Fatalf("housekeeping: %v %v", cleanup, err)
				}
				requireAbsent(t, registration)
				requireAbsent(t, dir)
			} else if err == nil || cleanup != nil {
				t.Fatalf("required removal: %v %v", cleanup, err)
			}
			if _, err := os.Stat(intent); err != nil {
				t.Fatal("intent lost", err)
			}
			fault.failPath = ""
			cleanup, err = s.RecoverCancellations([]string{"manual"})
			if cleanup != nil || err != nil {
				t.Fatalf("recovery: %v %v", cleanup, err)
			}
			for _, path := range []string{intent, registration, dir} {
				requireAbsent(t, path)
			}
			if cleanup, err := s.RecoverCancellations([]string{"manual"}); cleanup != nil || err != nil {
				t.Fatalf("idempotent recovery: %v %v", cleanup, err)
			}
		})
	}
}

func TestCancellationRuntimeObstructionDoesNotDeleteRegistration(t *testing.T) {
	s, owner := cancellationStore(t)
	if err := s.BeginCancellation(owner); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.runtime, testID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, generationName(1)), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveCancelled(owner); err == nil {
		t.Fatal("nonregular runtime entry accepted")
	}
	if _, err := os.Stat(filepath.Join(s.records, testID+".json")); err != nil {
		t.Fatal(err)
	}
}

type cancellationInspectFS struct {
	fileSystem
	op, path string
	symlink  bool
}

type cancellationInfo struct{ os.FileInfo }

func (i cancellationInfo) Mode() os.FileMode { return i.FileInfo.Mode() | os.ModeSymlink }

func (f cancellationInspectFS) Lstat(path string) (os.FileInfo, error) {
	if f.op == "stat" && path == f.path {
		return nil, errors.New("injected stat failure")
	}
	info, err := f.fileSystem.Lstat(path)
	if f.symlink && path == f.path && err == nil {
		return cancellationInfo{info}, nil
	}
	return info, err
}

func (f cancellationInspectFS) Open(path string) (readFile, error) {
	if f.op == "open" && path == f.path {
		return nil, errors.New("injected open failure")
	}
	file, err := f.fileSystem.Open(path)
	if err == nil && path == f.path {
		return cancellationReadFile{file, f.op}, nil
	}
	return file, err
}

func (f cancellationInspectFS) ReadDir(path string) ([]os.DirEntry, error) {
	if f.op == "readdir" && path == f.path {
		return nil, errors.New("injected readdir failure")
	}
	return f.fileSystem.ReadDir(path)
}

func (f cancellationInspectFS) OpenFile(path string, flags int, mode os.FileMode) (writeFile, error) {
	if f.op == "create" {
		return nil, errors.New("injected create failure")
	}
	return f.fileSystem.OpenFile(path, flags, mode)
}

type cancellationReadFile struct {
	readFile
	op string
}

func (f cancellationReadFile) Read(data []byte) (int, error) {
	if f.op == "read" {
		return 0, errors.New("injected read failure")
	}
	return f.readFile.Read(data)
}

func (f cancellationReadFile) Close() error {
	err := f.readFile.Close()
	if f.op == "close" {
		return errors.Join(err, errors.New("injected read-close failure"))
	}
	return err
}

func TestCancellationInspectionFailuresAndSymlinksPreserveState(t *testing.T) {
	for _, target := range []string{"intent", "registration", "runtime", "generation", "directory"} {
		for _, op := range []string{"stat", "open", "read", "close", "readdir", "symlink"} {
			if (target == "directory" && op != "readdir") || (target == "runtime" && op != "stat" && op != "symlink" && op != "readdir") || (target == "generation" && op != "stat" && op != "symlink") || ((target == "intent" || target == "registration") && op == "readdir") {
				continue
			}
			t.Run(target+"/"+op, func(t *testing.T) {
				s, owner := cancellationStore(t)
				if err := s.BeginCancellation(owner); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(s.runtime, testID)
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				generation := filepath.Join(dir, generationName(1))
				if err := os.WriteFile(generation, []byte("opaque"), 0o600); err != nil {
					t.Fatal(err)
				}
				registration := filepath.Join(s.records, testID+".json")
				path := map[string]string{"intent": s.cancellationPath(owner.ID), "registration": registration, "runtime": dir, "generation": generation, "directory": s.cancellations}[target]
				s.files = cancellationInspectFS{fileSystem: s.files, op: op, path: path, symlink: op == "symlink"}
				if _, err := s.RecoverCancellations([]string{"manual"}); err == nil {
					t.Fatal("invalid/unreadable state accepted")
				}
				if _, err := os.Stat(registration); err != nil {
					t.Fatal("registration changed", err)
				}
			})
		}
	}
}

func TestCancellationRejectsUncommittedWrongOwnerAndClosedStore(t *testing.T) {
	s, owner := cancellationStore(t)
	if _, err := s.RemoveCancelled(owner); err == nil {
		t.Fatal("removal without intent accepted")
	}
	invalid := owner
	invalid.ID = "../outside"
	if err := s.BeginCancellation(invalid); err == nil {
		t.Fatal("invalid owner accepted")
	}
	wrong := owner
	wrong.InputPlugin = "wrong"
	if err := s.BeginCancellation(wrong); err == nil {
		t.Fatal("wrong registration owner accepted")
	}
	s.files = cancellationInspectFS{fileSystem: s.files, op: "create"}
	if err := s.BeginCancellation(owner); err == nil {
		t.Fatal("create failure accepted")
	}
	s.files = osFileSystem{}
	if err := s.BeginCancellation(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveCancelled(wrong); err == nil {
		t.Fatal("wrong intent owner accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginCancellation(owner); err == nil {
		t.Fatal("closed begin accepted")
	}
	if _, err := s.RemoveCancelled(owner); err == nil {
		t.Fatal("closed remove accepted")
	}
	if _, err := s.RecoverCancellations([]string{"manual"}); err == nil {
		t.Fatal("closed recovery accepted")
	}
}

func TestCancellationDirectoryMigrationAndReservedType(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "older tree", true: "reserved regular file"}[invalid], func(t *testing.T) {
			s, _ := cancellationStore(t)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(s.cancellations); err != nil {
				t.Fatal(err)
			}
			if invalid {
				if err := os.WriteFile(s.cancellations, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := Open(s.root)
			if invalid {
				if err == nil {
					_ = reopened.Close()
					t.Fatal("non-directory reserved cancellation path accepted")
				}
				requireAbsent(t, s.lockPath)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			info, err := os.Lstat(s.cancellations)
			if err != nil || !isDirectory(info) {
				t.Fatalf("older tree migration: %v %v", info, err)
			}
		})
	}
}

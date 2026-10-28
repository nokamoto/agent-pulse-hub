package registrationstore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

type notificationMemoryEntry struct {
	name string
	mode os.FileMode
	data []byte
}

func (e notificationMemoryEntry) Name() string               { return e.name }
func (e notificationMemoryEntry) Size() int64                { return int64(len(e.data)) }
func (e notificationMemoryEntry) Mode() os.FileMode          { return e.mode }
func (e notificationMemoryEntry) ModTime() time.Time         { return time.Time{} }
func (e notificationMemoryEntry) IsDir() bool                { return e.mode.IsDir() }
func (e notificationMemoryEntry) Sys() any                   { return nil }
func (e notificationMemoryEntry) Type() os.FileMode          { return e.mode.Type() }
func (e notificationMemoryEntry) Info() (os.FileInfo, error) { return e, nil }

type notificationMemoryFS struct {
	entries map[string]notificationMemoryEntry
	fault   func(string, string) bool
}

func (f *notificationMemoryFS) fail(op, path string) bool { return f.fault != nil && f.fault(op, path) }

func (f *notificationMemoryFS) Lstat(p string) (os.FileInfo, error) {
	if f.fail("stat", p) {
		return nil, errors.New("controlled stat failure")
	}
	e, ok := f.entries[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return e, nil
}

func (f *notificationMemoryFS) ReadDir(p string) ([]os.DirEntry, error) {
	if f.fail("readdir", p) {
		return nil, errors.New("controlled read directory failure")
	}
	if _, ok := f.entries[p]; !ok {
		return nil, os.ErrNotExist
	}
	var result []os.DirEntry
	for path, e := range f.entries {
		if path != p && filepath.Dir(path) == p {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

func (f *notificationMemoryFS) Mkdir(p string, m os.FileMode) error {
	if _, ok := f.entries[p]; ok {
		return os.ErrExist
	}
	if f.fail("mkdir", p) {
		return errors.New("controlled mkdir failure")
	}
	f.entries[p] = notificationMemoryEntry{name: filepath.Base(p), mode: m | os.ModeDir}
	return nil
}

func (f *notificationMemoryFS) Remove(p string) error {
	if f.fail("remove", p) {
		return errors.New("controlled removal failure")
	}
	if _, ok := f.entries[p]; !ok {
		return os.ErrNotExist
	}
	for path := range f.entries {
		if path != p && filepath.Dir(path) == p {
			return errors.New("directory not empty")
		}
	}
	delete(f.entries, p)
	return nil
}

func (f *notificationMemoryFS) Rename(a, b string) error {
	if f.fail("rename", b) {
		return errors.New("controlled rename failure")
	}
	e, ok := f.entries[a]
	if !ok {
		return os.ErrNotExist
	}
	if _, ok := f.entries[b]; ok {
		return os.ErrExist
	}
	e.name = filepath.Base(b)
	f.entries[b] = e
	delete(f.entries, a)
	return nil
}

func (f *notificationMemoryFS) Open(p string) (readFile, error) {
	if f.fail("open", p) {
		return nil, errors.New("controlled read failure")
	}
	e, ok := f.entries[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(e.data)), nil
}

func (f *notificationMemoryFS) OpenFile(p string, flags int, m os.FileMode) (writeFile, error) {
	if f.fail("create", p) {
		return nil, errors.New("controlled create failure")
	}
	if _, ok := f.entries[p]; ok && flags&os.O_EXCL != 0 {
		return nil, os.ErrExist
	}
	f.entries[p] = notificationMemoryEntry{name: filepath.Base(p), mode: m}
	return &notificationMemoryWriter{fs: f, path: p}, nil
}

type notificationMemoryWriter struct {
	fs   *notificationMemoryFS
	path string
}

func notificationMockFS(t *testing.T, f *notificationMemoryFS) fileSystem {
	t.Helper()
	ctrl := gomock.NewController(t)
	mock := NewMockfileSystem(ctrl)
	mock.EXPECT().Lstat(gomock.Any()).DoAndReturn(f.Lstat).AnyTimes()
	mock.EXPECT().ReadDir(gomock.Any()).DoAndReturn(f.ReadDir).AnyTimes()
	mock.EXPECT().Mkdir(gomock.Any(), gomock.Any()).DoAndReturn(f.Mkdir).AnyTimes()
	mock.EXPECT().Remove(gomock.Any()).DoAndReturn(f.Remove).AnyTimes()
	mock.EXPECT().Rename(gomock.Any(), gomock.Any()).DoAndReturn(f.Rename).AnyTimes()
	mock.EXPECT().Open(gomock.Any()).DoAndReturn(f.Open).AnyTimes()
	mock.EXPECT().OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(path string, flags int, mode os.FileMode) (writeFile, error) {
		writer, err := f.OpenFile(path, flags, mode)
		if err != nil {
			return nil, err
		}
		file := NewMockwriteFile(ctrl)
		file.EXPECT().Write(gomock.Any()).DoAndReturn(writer.Write).AnyTimes()
		file.EXPECT().Close().DoAndReturn(writer.Close).AnyTimes()
		return file, nil
	}).AnyTimes()
	return mock
}

func (w *notificationMemoryWriter) Write(data []byte) (int, error) {
	if w.fs.fail("write", w.path) {
		return 0, errors.New("controlled write failure")
	}
	e := w.fs.entries[w.path]
	e.data = append(e.data, data...)
	w.fs.entries[w.path] = e
	return len(data), nil
}

func (w *notificationMemoryWriter) Close() error {
	if w.fs.fail("close", w.path) {
		return errors.New("controlled close failure")
	}
	return nil
}

func notificationMemoryStore(t *testing.T) (*Store, *notificationMemoryFS, string) {
	t.Helper()
	parent, err := filepath.Abs("notification-memory-parent")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "state")
	f := &notificationMemoryFS{entries: map[string]notificationMemoryEntry{parent: {name: filepath.Base(parent), mode: os.ModeDir | 0o700}}}
	s, err := open(root, notificationMockFS(t, f))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, _, _, err := s.LoadNotifications(); err != nil {
		t.Fatal(err)
	}
	return s, f, root
}

func notificationStoreRecord() domain.Notification {
	return domain.NewNotification(domain.Event{ID: "11111111-1111-4111-8111-111111111111", SubscriptionID: "22222222-2222-4222-8222-222222222222", InputPlugin: "source", OutputPlugin: "action", DestinationArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{}), Context: "private context"}, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
}

func TestNotificationCommitFailuresCloseGateAndNeverAuthorizeOrphanGuards(t *testing.T) {
	for _, op := range []string{"create", "write", "close", "guard_rename", "ledger_rename", "old_remove"} {
		t.Run(op, func(t *testing.T) {
			s, f, root := notificationMemoryStore(t)
			n := notificationStoreRecord()
			f.fault = func(operation, path string) bool {
				switch op {
				case "guard_rename":
					return operation == "rename" && strings.Contains(path, string(filepath.Separator)+"guards"+string(filepath.Separator))
				case "ledger_rename":
					return operation == "rename" && strings.Contains(path, string(filepath.Separator)+"ledger"+string(filepath.Separator))
				case "old_remove":
					return operation == "remove" && strings.HasSuffix(path, generationName(1))
				default:
					return operation == op
				}
			}
			if s.CommitNotifications([]domain.Notification{n}, n.CreatedAt) == nil {
				t.Fatal("fault accepted")
			}
			f.fault = nil
			if s.CommitNotifications([]domain.Notification{n}, n.CreatedAt) == nil {
				t.Fatal("same-process storage gate reopened")
			}
			_ = s.Close()
			restored, err := open(root, notificationMockFS(t, f))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			records, _, _, err := restored.LoadNotifications()
			if op == "ledger_rename" && err == nil {
				t.Fatal("orphan admission guard authorized stale ledger")
			}
			if err == nil && len(records) > 0 && len(records[0].Attempts) != 0 {
				t.Fatal("failure manufactured an attempt")
			}
		})
	}
}

func TestNotificationGuardsPreventStaleUnsentSnapshotRecovery(t *testing.T) {
	for _, fault := range []string{"missing_guard", "advanced_guard", "bad_highest", "bad_marker", "unknown_entry", "symlink", "missing_field"} {
		t.Run(fault, func(t *testing.T) {
			s, f, root := notificationMemoryStore(t)
			n := notificationStoreRecord()
			if err := s.CommitNotifications([]domain.Notification{n}, n.CreatedAt); err != nil {
				t.Fatal(err)
			}
			old := f.entries[s.notificationPath("ledger", generationName(2))]
			_ = n.Reserve("33333333-3333-4333-8333-333333333333", n.CreatedAt)
			if err := s.CommitNotifications([]domain.Notification{n}, n.CreatedAt); err != nil {
				t.Fatal(err)
			}
			path := s.notificationPath("ledger", generationName(3))
			switch fault {
			case "missing_guard":
				delete(f.entries, s.notificationPath("guards", notificationGuardName(string(n.Event.ID), 1)))
			case "advanced_guard":
				delete(f.entries, path)
				f.entries[s.notificationPath("ledger", generationName(2))] = old
			case "bad_highest":
				e := f.entries[path]
				e.data = []byte("{")
				f.entries[path] = e
			case "bad_marker":
				p := filepath.Join(root, "notification-store.json")
				e := f.entries[p]
				e.data = []byte(`{"version":2,"store_id":"11111111-1111-4111-8111-111111111111"}`)
				f.entries[p] = e
			case "unknown_entry":
				p := s.notificationPath("unknown")
				f.entries[p] = notificationMemoryEntry{name: "unknown", mode: 0o600}
			case "symlink":
				e := f.entries[path]
				e.mode = os.ModeSymlink
				f.entries[path] = e
			case "missing_field":
				e := f.entries[path]
				e.data = bytes.Replace(e.data, []byte(`"issued_at":null,`), nil, 1)
				f.entries[path] = e
			}
			_ = s.Close()
			restored, err := open(root, notificationMockFS(t, f))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if _, _, _, err := restored.LoadNotifications(); err == nil {
				t.Fatal("corrupt notification store recovered")
			}
		})
	}
}

func TestNotificationCompactionRecoveryNeverDeletesReferencedGuards(t *testing.T) {
	for _, fault := range []string{"receipt_rename", "snapshot_rename", "guard_remove", "receipt_remove"} {
		t.Run(fault, func(t *testing.T) {
			s, f, root := notificationMemoryStore(t)
			n := notificationStoreRecord()
			if err := s.CommitNotifications([]domain.Notification{n}, n.CreatedAt); err != nil {
				t.Fatal(err)
			}
			f.fault = func(op, path string) bool {
				switch fault {
				case "receipt_rename":
					return op == "rename" && strings.HasSuffix(path, "compaction.json")
				case "snapshot_rename":
					return op == "rename" && strings.HasSuffix(path, generationName(3))
				case "guard_remove":
					return op == "remove" && strings.Contains(path, string(filepath.Separator)+"guards"+string(filepath.Separator))
				case "receipt_remove":
					return op == "remove" && strings.HasSuffix(path, "compaction.json")
				}
				return false
			}
			if s.CommitNotifications([]domain.Notification{}, n.ExpiresAt) == nil {
				t.Fatal("compaction fault accepted")
			}
			f.fault = nil
			_ = s.Close()
			restored, err := open(root, notificationMockFS(t, f))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			records, _, _, err := restored.LoadNotifications()
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if fault == "receipt_rename" || fault == "snapshot_rename" {
				want = 1
			}
			if len(records) != want {
				t.Fatalf("compaction restored %d records; want %d", len(records), want)
			}
			if _, ok := f.entries[restored.notificationPath("compaction.json")]; ok {
				t.Fatal("compaction cleanup left receipt")
			}
		})
	}
}

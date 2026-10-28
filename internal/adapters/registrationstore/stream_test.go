package registrationstore

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

// The stream fault matrix mocks storage; command integration covers real files.
type streamMemoryNode struct {
	name string
	mode os.FileMode
	data []byte
}

func (n *streamMemoryNode) Name() string       { return n.name }
func (n *streamMemoryNode) Size() int64        { return int64(len(n.data)) }
func (n *streamMemoryNode) Mode() os.FileMode  { return n.mode }
func (n *streamMemoryNode) ModTime() time.Time { return time.Time{} }
func (n *streamMemoryNode) IsDir() bool        { return n.mode.IsDir() }
func (n *streamMemoryNode) Sys() any           { return nil }

type streamMemoryEntry struct{ node *streamMemoryNode }

func (e streamMemoryEntry) Name() string               { return e.node.name }
func (e streamMemoryEntry) IsDir() bool                { return e.node.IsDir() }
func (e streamMemoryEntry) Type() fs.FileMode          { return e.node.mode.Type() }
func (e streamMemoryEntry) Info() (fs.FileInfo, error) { return e.node, nil }

type streamMemoryFS struct {
	nodes map[string]*streamMemoryNode
	fail  string
}

func (m *streamMemoryFS) Lstat(path string) (os.FileInfo, error) {
	node, ok := m.nodes[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return node, nil
}

func (m *streamMemoryFS) ReadDir(path string) ([]os.DirEntry, error) {
	info, err := m.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("not directory")
	}
	var result []os.DirEntry
	for child, node := range m.nodes {
		if child != path && filepath.Dir(child) == path {
			result = append(result, streamMemoryEntry{node})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

func (m *streamMemoryFS) Mkdir(path string, mode os.FileMode) error {
	if _, exists := m.nodes[path]; exists {
		return fs.ErrExist
	}
	m.nodes[path] = &streamMemoryNode{name: filepath.Base(path), mode: mode | os.ModeDir}
	return nil
}

func (m *streamMemoryFS) Remove(path string) error {
	if m.fail == "cleanup" {
		return errors.New("injected cleanup")
	}
	if _, exists := m.nodes[path]; !exists {
		return fs.ErrNotExist
	}
	for child := range m.nodes {
		if child != path && filepath.Dir(child) == path {
			return errors.New("directory not empty")
		}
	}
	delete(m.nodes, path)
	return nil
}

func (m *streamMemoryFS) Rename(oldPath, newPath string) error {
	if m.fail == "rename" {
		return errors.New("injected rename")
	}
	if _, exists := m.nodes[newPath]; exists {
		return fs.ErrExist
	}
	node, exists := m.nodes[oldPath]
	if !exists {
		return fs.ErrNotExist
	}
	m.nodes[newPath] = node
	node.name = filepath.Base(newPath)
	delete(m.nodes, oldPath)
	for path, child := range m.nodes {
		if strings.HasPrefix(path, oldPath+string(filepath.Separator)) {
			m.nodes[newPath+strings.TrimPrefix(path, oldPath)] = child
			delete(m.nodes, path)
		}
	}
	return nil
}

func (m *streamMemoryFS) Open(path string) (readFile, error) {
	node, ok := m.nodes[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(string(node.data))), nil
}

type streamMemoryWrite struct {
	fs   *streamMemoryFS
	node *streamMemoryNode
}

func (w *streamMemoryWrite) Write(raw []byte) (int, error) {
	if w.fs.fail == "write" {
		return 0, errors.New("injected write")
	}
	w.node.data = append(w.node.data, raw...)
	return len(raw), nil
}

func (w *streamMemoryWrite) Close() error {
	if w.fs.fail == "close" {
		return errors.New("injected close")
	}
	return nil
}

func (m *streamMemoryFS) OpenFile(path string, flags int, mode os.FileMode) (writeFile, error) {
	if flags&os.O_EXCL != 0 {
		if _, exists := m.nodes[path]; exists {
			return nil, fs.ErrExist
		}
	}
	node := &streamMemoryNode{name: filepath.Base(path), mode: mode}
	m.nodes[path] = node
	return &streamMemoryWrite{fs: m, node: node}, nil
}

func memoryStreamStore(t *testing.T) (*Store, *streamMemoryFS, domain.Subscription) {
	t.Helper()
	root := filepath.Join(string(filepath.Separator), "stream-mock")
	files := &streamMemoryFS{nodes: make(map[string]*streamMemoryNode)}
	for _, name := range []string{"", "runtime", "staging", "registrations"} {
		path := filepath.Join(root, name)
		files.nodes[path] = &streamMemoryNode{name: filepath.Base(path), mode: os.ModeDir | 0o700}
	}
	store := &Store{root: root, staging: filepath.Join(root, "staging"), runtime: filepath.Join(root, "runtime"), records: filepath.Join(root, "registrations"), files: files}
	return store, files, testSubscription(t, testID)
}
func publishStream(action func() error) error { return action() }

func TestLegacySourceFailureRetainsOwnerDiagnosticWithoutPayload(t *testing.T) {
	store, files, owner := memoryStreamStore(t)
	legacy := runtimeTestState(t, `{"schema":"legacy","payload":{}}`)
	if _, err := store.CommitRuntime(owner, legacy); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.runtime, string(owner.ID), generationName(1))
	for _, invalid := range []string{`{`, `{"PRIVATE-PAYLOAD":1,"PRIVATE-PAYLOAD":2}`} {
		files.nodes[path].data = []byte(invalid)
		_, err := store.LoadRuntimeSources([]domain.Subscription{owner}, 65536)
		if err == nil {
			t.Fatal("corrupt legacy source accepted")
		}
		for _, expected := range []string{owner.InputPlugin, string(owner.ID), path} {
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("missing owner context %q: %v", expected, err)
			}
		}
		if strings.Contains(err.Error(), "PRIVATE-PAYLOAD") {
			t.Fatalf("opaque state leaked: %v", err)
		}
	}
}

func saveMockStream(t *testing.T, store *Store, owner domain.Subscription, body string) uint64 {
	t.Helper()
	tx, err := store.BeginRuntimeStream(owner, "schema", 65536)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Append(0, []byte(body)); err != nil {
		t.Fatal(err)
	}
	g, err := tx.Commit(1, publishStream)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestStreamStorageRoundTripMigrationAndHighestSelection(t *testing.T) {
	store, files, owner := memoryStreamStore(t)
	legacy := runtimeTestState(t, `{"schema":"legacy","payload":{"n":9007199254740993}}`)
	if _, err := store.CommitRuntime(owner, legacy); err != nil {
		t.Fatal(err)
	}
	sources, err := store.LoadRuntimeSources([]domain.Subscription{owner}, 65536)
	if err != nil || sources[owner.ID].Streamed() || sources[owner.ID].Schema() != "legacy" {
		t.Fatalf("legacy source: %v %v", sources, err)
	}
	oldPath := filepath.Join(store.runtime, testID, generationName(1))
	oldRaw := append([]byte(nil), files.nodes[oldPath].data...)
	tx, err := store.BeginRuntimeStream(owner, "schema", 65536)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 2; i++ {
		body := `{"text":"` + strings.Repeat("x", 20000) + `"}`
		if err := tx.Append(i, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	g, err := tx.Commit(2, publishStream)
	if err != nil || g != 2 {
		t.Fatalf("save %d %v", g, err)
	}
	if _, exists := files.nodes[oldPath]; exists {
		t.Fatal("legacy generation not cleaned after commit")
	}
	files.nodes[oldPath] = &streamMemoryNode{name: filepath.Base(oldPath), mode: 0o600, data: oldRaw}
	sources, err = store.LoadRuntimeSources([]domain.Subscription{owner}, 65536)
	if err != nil {
		t.Fatal(err)
	}
	source := sources[owner.ID]
	if !source.Streamed() || source.Count() != 2 {
		t.Fatal("wrong streamed source")
	}
	if _, err := source.Chunk(1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRuntimeSources([]domain.Subscription{owner}, 100); err == nil {
		t.Fatal("budget reduction fell back")
	}
	newest := filepath.Join(store.runtime, testID, "00000000000000000002.stream")
	delete(files.nodes, filepath.Join(newest, chunkName(1)))
	if _, err := store.LoadRuntimeSources([]domain.Subscription{owner}, 65536); err == nil {
		t.Fatal("missing newest chunk fell back to legacy")
	}
}

func TestStreamStorageFailureAndFenceNeverReplaceCommit(t *testing.T) {
	for _, fault := range []string{"write", "close", "rename", "fence", "count", "budget"} {
		t.Run(fault, func(t *testing.T) {
			store, files, owner := memoryStreamStore(t)
			saveMockStream(t, store, owner, `{"n":1}`)
			tx, err := store.BeginRuntimeStream(owner, "schema", 10)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "write" || fault == "close" {
				files.fail = fault
			}
			body := []byte(`{"n":2}`)
			if fault == "budget" {
				body = []byte(`{"body":"exceeds budget"}`)
			}
			err = tx.Append(0, body)
			if err == nil {
				publish := publishStream
				if fault == "fence" {
					publish = func(func() error) error { return errors.New("fenced") }
				}
				count := uint64(1)
				if fault == "count" {
					count = 2
				}
				if fault == "rename" {
					files.fail = "rename"
				}
				g, saveErr := tx.Commit(count, publish)
				if g != 0 {
					t.Fatal("failed save committed")
				}
				err = saveErr
			}
			if err == nil {
				t.Fatal("fault did not fail")
			}
			files.fail = ""
			if err := tx.Abort(); err != nil {
				t.Fatal(err)
			}
			sources, err := store.LoadRuntimeSources([]domain.Subscription{owner}, 65536)
			if err != nil {
				t.Fatal(err)
			}
			data, err := sources[owner.ID].Chunk(0)
			if err != nil || string(data.Marshal()) != `{"n":1}` {
				t.Fatalf("prior progress changed: %v %v", data, err)
			}
			entries, _ := files.ReadDir(store.staging)
			if len(entries) != 0 {
				t.Fatal("staging leaked")
			}
		})
	}
}

func TestStreamCleanupRecognizesOnlyOwnedSafeStructure(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		store, files, _ := memoryStreamStore(t)
		path := filepath.Join(store.staging, "runtime-stream-"+testID)
		_ = files.Mkdir(path, 0o700)
		name := chunkName(4)
		mode := os.FileMode(0o600)
		if unsafe {
			mode = os.ModeSymlink
		}
		files.nodes[filepath.Join(path, name)] = &streamMemoryNode{name: name, mode: mode, data: []byte("incomplete")}
		err := store.cleanStaging()
		if (err != nil) != unsafe {
			t.Fatalf("unsafe %v err=%v", unsafe, err)
		}
		if unsafe {
			if _, exists := files.nodes[path]; !exists {
				t.Fatal("unsafe directory removed")
			}
		}
	}
}

func TestStreamStorageRejectsDuplicateFormatsAndMetadataCorruption(t *testing.T) {
	for _, kind := range []string{"equal_generation", "invalid_json", "unknown_manifest", "wrong_bytes", "foreign_owner", "unexpected_file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, files, owner := memoryStreamStore(t)
			saveMockStream(t, store, owner, `{"n":1}`)
			path := filepath.Join(store.runtime, testID, "00000000000000000001.stream")
			manifest := files.nodes[filepath.Join(path, "manifest.json")]
			switch kind {
			case "equal_generation":
				legacyPath := filepath.Join(store.runtime, testID, generationName(1))
				files.nodes[legacyPath] = &streamMemoryNode{name: generationName(1), mode: 0o600, data: []byte("{}")}
			case "invalid_json":
				files.nodes[filepath.Join(path, chunkName(0))].data = []byte("{")
			case "unknown_manifest":
				manifest.data = []byte(strings.TrimSuffix(string(manifest.data), "}") + `,"extra":true}`)
			case "wrong_bytes":
				manifest.data = []byte(strings.Replace(string(manifest.data), `"data_bytes":"7"`, `"data_bytes":"6"`, 1))
			case "foreign_owner":
				manifest.data = []byte(strings.Replace(string(manifest.data), testID, "9fc5db2a-77a0-4f60-bf92-bbe214a83a71", 1))
			case "unexpected_file":
				files.nodes[filepath.Join(path, "unknown")] = &streamMemoryNode{name: "unknown", mode: 0o600}
			case "symlink":
				files.nodes[filepath.Join(path, chunkName(0))].mode = os.ModeSymlink
			}
			if _, err := store.LoadRuntimeSources([]domain.Subscription{owner}, hub.DefaultRuntimeStateMaxBytes); err == nil {
				t.Fatal("corrupt state accepted")
			}
		})
	}
}

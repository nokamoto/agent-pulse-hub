package manualplugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestWatchRejectsDuplicateCleanedPathAndAcceptsDistinctPath(t *testing.T) {
	files := newFakeTriggerFiles()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newWithFiles(slog.New(slog.NewTextHandler(io.Discard, nil)), files)
	p.active = true
	t.Cleanup(p.stopWatches)
	stream := &recordingSession{ctx: ctx}
	path := filepath.Join(files.directory, "trigger")
	unclean := files.directory + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + "trigger"
	for _, test := range []struct {
		id, path string
		accepted bool
	}{
		{"original", path, true},
		{"duplicate", unclean, false},
		{"distinct", filepath.Join(files.directory, "distinct"), true},
		// The conflict key is an exact string, without case folding.
		{"different-case", filepath.Join(files.directory, "Trigger"), true},
	} {
		if err := p.acceptWatch(ctx, stream, watchFrame(test.id, test.path)); err != nil {
			t.Fatal(err)
		}
		frames := stream.snapshot()
		result := frames[len(frames)-1]
		if result.Type != "watch_result" || result.RequestID != test.id || result.Accepted == nil || *result.Accepted != test.accepted {
			t.Fatalf("watch %s: got %+v, accepted=%v", test.id, result, test.accepted)
		}
		if !test.accepted && result.Error == "" {
			t.Fatal("duplicate watch must include a rejection reason")
		}
	}
	if len(p.watches) != 3 || p.paths[path] != "original" {
		t.Fatalf("duplicate watch altered active watches: %+v", p.paths)
	}
}

func TestWatchTargetRejectsUnavailableDirectoryOrExistingTarget(t *testing.T) {
	for _, test := range []struct {
		name     string
		missing  bool
		existing bool
	}{
		{name: "missing directory", missing: true},
		{name: "existing target", existing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := newFakeTriggerFiles()
			files.missingDirectory = test.missing
			path := filepath.Join(files.directory, "trigger")
			if test.existing {
				files.contents[path] = []byte("existing")
			}
			if _, err := watchTargetFromArguments(watchFrame("watch", path).WatchArgs, files); err == nil {
				t.Fatal("unavailable watch target was accepted")
			}
			if files.probes != 0 {
				t.Fatal("invalid watch target reached the write probe")
			}
		})
	}
}

func TestTriggerFailuresEmitNothingAndDoNotRetryClaims(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		fault   string
	}{
		{name: "empty", content: []byte{}},
		{name: "8193 bytes", content: bytes.Repeat([]byte("x"), 8193)},
		{name: "invalid UTF-8", content: []byte{'x', 0xff}},
		{name: "claim", content: []byte("marker"), fault: "claim"},
		{name: "open", content: []byte("marker"), fault: "open"},
		{name: "read", content: []byte("marker"), fault: "read"},
		{name: "close", content: []byte("marker"), fault: "close"},
		{name: "remove", content: []byte("marker"), fault: "remove"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := newFakeTriggerFiles()
			files.fault = test.fault
			path := filepath.Join(files.directory, "trigger")
			files.contents[path] = test.content
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			diagnostics := &diagnosticWriter{observed: make(chan struct{}, 1)}
			p := newWithFiles(slog.New(slog.NewTextHandler(diagnostics, nil)), files)
			stream := &recordingSession{ctx: ctx}
			watch := &watcher{subscriptionID: "watch", target: watchTarget{path: path, directory: files.directory}, done: make(chan struct{})}
			go p.runWatch(ctx, stream, watch)
			defer func() { cancel(); <-watch.done }()
			select {
			case <-diagnostics.observed:
			case <-time.After(2 * time.Second):
				t.Fatal("trigger failure did not produce a diagnostic")
			}
			// Poll past the failed ingestion to detect accidental claimed-file retries.
			time.Sleep(3 * pollInterval)
			cancel()
			<-watch.done
			if frames := stream.snapshot(); len(frames) != 0 {
				t.Fatalf("failed trigger emitted frames: %+v", frames)
			}
			files.mu.Lock()
			defer files.mu.Unlock()
			if test.fault == "claim" {
				// An unsuccessful rename never owns a claim. Retrying the original
				// publication is permitted; opening or removing it is not.
				if files.opens != 0 || files.removes != 0 || len(files.contents) != 1 {
					t.Fatalf("claim failure reached consumption: opens=%d removes=%d", files.opens, files.removes)
				}
			} else {
				if files.opens != 1 || files.successfulClaims != 1 {
					t.Fatalf("claimed file was retried: opens=%d claims=%d", files.opens, files.successfulClaims)
				}
				if _, exists := files.contents[path]; exists || len(files.contents) != 1 {
					t.Fatal("failed claim must remain as a sibling for operator inspection")
				}
				if test.fault != "open" && files.closes != 1 {
					t.Fatalf("claimed reader close count=%d, want 1", files.closes)
				}
				wantRemoves := 0
				if test.fault == "remove" {
					wantRemoves = 1
				}
				if files.removes != wantRemoves {
					t.Fatalf("removal count=%d, want %d", files.removes, wantRemoves)
				}
			}
			if !strings.Contains(diagnostics.String(), "manual_plugin_trigger_rejected") || !strings.Contains(diagnostics.String(), "claimed_path=") {
				t.Fatalf("failure diagnostic lacks operation/path: %s", diagnostics.String())
			}
			for retainedPath := range files.contents {
				if !strings.Contains(diagnostics.String(), filepath.Base(retainedPath)) {
					t.Fatalf("failure diagnostic omits retained path %q: %s", retainedPath, diagnostics.String())
				}
			}
		})
	}
}

func TestTriggerPreserves8192BytesAndRemovesClaim(t *testing.T) {
	files := newFakeTriggerFiles()
	path := filepath.Join(files.directory, "trigger")
	content := strings.Repeat("é", 4096)
	files.contents[path] = []byte(content)
	actual, claim, err := consumeTriggerWithFiles(watchTarget{path: path, directory: files.directory}, files)
	if err != nil || actual != content || filepath.Dir(claim) != files.directory || !strings.HasPrefix(filepath.Base(claim), claimFileNamePrefix) {
		t.Fatalf("valid trigger: bytes=%d claim=%q err=%v", len(actual), claim, err)
	}
	if len(files.contents) != 0 || files.opens != 1 || files.closes != 1 || files.removes != 1 {
		t.Fatalf("valid claim was not consumed exactly once: %+v", files)
	}
	if actual, _, err := consumeTriggerWithFiles(watchTarget{path: path, directory: files.directory}, files); actual != "" || err != nil || files.opens != 1 {
		t.Fatalf("consumed trigger was retried: context=%q err=%v", actual, err)
	}
}

func watchFrame(id, path string) pluginprotocol.Frame {
	return pluginprotocol.Frame{Type: "watch", RequestID: id, SubscriptionID: id, WatchArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"trigger_file": jsonvalue.NewString(path)})}
}

type recordingSession struct {
	ctx    context.Context
	mu     sync.Mutex
	frames []pluginprotocol.Frame
}

func (s *recordingSession) Context() context.Context { return s.ctx }
func (s *recordingSession) Recv() ([]byte, error)    { return nil, io.EOF }
func (s *recordingSession) Send(payload []byte) error {
	frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, frame)
	return nil
}

func (s *recordingSession) snapshot() []pluginprotocol.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pluginprotocol.Frame(nil), s.frames...)
}

type diagnosticWriter struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	observed chan struct{}
}

func (w *diagnosticWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(data)
	select {
	case w.observed <- struct{}{}:
	default:
	}
	return n, err
}

func (w *diagnosticWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

type fakeTriggerFiles struct {
	mu               sync.Mutex
	directory        string
	contents         map[string][]byte
	fault            string
	missingDirectory bool
	probes           int
	successfulClaims int
	opens            int
	closes           int
	removes          int
}

func newFakeTriggerFiles() *fakeTriggerFiles {
	return &fakeTriggerFiles{directory: filepath.Join(os.TempDir(), "manual-unit"), contents: make(map[string][]byte)}
}

func (f *fakeTriggerFiles) Stat(path string) (os.FileInfo, error) {
	if path != f.directory || f.missingDirectory {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{directory: true}, nil
}

func (f *fakeTriggerFiles) Lstat(path string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.contents[path]; !exists {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{}, nil
}

func (f *fakeTriggerFiles) CreateTemp(directory, _ string) (temporaryFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return fakeTemporaryFile{path: filepath.Join(directory, "probe")}, nil
}

func (f *fakeTriggerFiles) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, exists := f.contents[oldPath]
	if !exists {
		return os.ErrNotExist
	}
	if f.fault == "claim" {
		return errors.New("injected claim failure")
	}
	f.contents[newPath] = data
	delete(f.contents, oldPath)
	f.successfulClaims++
	return nil
}

func (f *fakeTriggerFiles) Open(path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.fault == "open" {
		return nil, errors.New("injected open failure")
	}
	return &fakeTriggerReader{Reader: bytes.NewReader(f.contents[path]), files: f}, nil
}

func (f *fakeTriggerFiles) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if filepath.Base(path) == "probe" {
		return nil
	}
	f.removes++
	if f.fault == "remove" {
		return errors.New("injected remove failure")
	}
	delete(f.contents, path)
	return nil
}

type fakeTriggerReader struct {
	*bytes.Reader
	files *fakeTriggerFiles
}

func (r *fakeTriggerReader) Read(data []byte) (int, error) {
	if r.files.fault == "read" {
		return 0, errors.New("injected read failure")
	}
	return r.Reader.Read(data)
}

func (r *fakeTriggerReader) Close() error {
	r.files.mu.Lock()
	defer r.files.mu.Unlock()
	r.files.closes++
	if r.files.fault == "close" {
		return errors.New("injected close failure")
	}
	return nil
}

type fakeTemporaryFile struct{ path string }

func (f fakeTemporaryFile) Name() string { return f.path }
func (fakeTemporaryFile) Close() error   { return nil }

type fakeFileInfo struct{ directory bool }

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool      { return f.directory }
func (fakeFileInfo) Sys() any           { return nil }
func (f fakeFileInfo) Mode() os.FileMode {
	if f.directory {
		return os.ModeDir | 0o700
	}
	return 0o600
}

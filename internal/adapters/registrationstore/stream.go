package registrationstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type runtimeSource struct {
	store        *Store
	path, schema string
	count        uint64
	stream       bool
	payload      *jsonvalue.Value
}

func (r *runtimeSource) Schema() string { return r.schema }
func (r *runtimeSource) Count() uint64  { return r.count }
func (r *runtimeSource) Streamed() bool { return r.stream }
func (r *runtimeSource) Chunk(index uint64) (*jsonvalue.Value, error) {
	if index >= r.count {
		return nil, errors.New("runtime chunk index out of range")
	}
	if !r.stream {
		return r.payload, nil
	}
	raw, err := r.store.readBounded(filepath.Join(r.path, chunkName(index)), 32768)
	if err != nil {
		return nil, err
	}
	value, err := jsonvalue.Parse(raw, 32768)
	if err != nil || value.Kind() != jsonvalue.Object {
		return nil, errors.New("runtime chunk is not a strict JSON object")
	}
	return value, nil
}
func chunkName(index uint64) string { return fmt.Sprintf("%020d.json", index) }
func (s *Store) readBounded(path string, limit int) ([]byte, error) {
	info, err := s.files.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !isRegularFile(info) || info.Size() > int64(limit) {
		return nil, errors.New("invalid or oversized runtime file")
	}
	file, err := s.files.Open(path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(raw) > limit {
		return nil, errors.New("runtime file exceeds byte limit")
	}
	return raw, nil
}

func (s *Store) LoadRuntimeSources(registrations []domain.Subscription, budget uint64) (map[domain.SubscriptionID]hub.RuntimeSource, error) {
	if budget == 0 {
		return nil, errors.New("runtime byte budget must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("state directory is closed")
	}
	owners := make(map[string]domain.Subscription, len(registrations))
	for _, r := range registrations {
		owners[string(r.ID)] = r
	}
	entries, err := s.files.ReadDir(s.runtime)
	if err != nil {
		return nil, err
	}
	result := make(map[domain.SubscriptionID]hub.RuntimeSource)
	for _, entry := range entries {
		id := entry.Name()
		if !validID(id) {
			return nil, errors.New("invalid runtime owner directory")
		}
		path := filepath.Join(s.runtime, id)
		info, err := s.files.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !isDirectory(info) {
			return nil, errors.New("runtime owner must be a real directory")
		}
		generations, err := s.runtimeGenerations(path)
		if err != nil {
			return nil, fmt.Errorf("runtime state registration %s: %w", id, err)
		}
		if len(generations) == 0 {
			return nil, fmt.Errorf("runtime state registration %s has no committed generation", id)
		}
		registration, owned := owners[id]
		if !owned {
			continue
		}
		latest := generations[len(generations)-1]
		selected := filepath.Join(path, latest.name)
		if latest.streamed {
			source, err := s.loadStreamSource(selected, registration, latest.number, budget)
			if err != nil {
				return nil, fmt.Errorf("runtime state registration %s: %w", id, err)
			}
			result[registration.ID] = source
		} else {
			raw, err := s.readRuntimeRecord(selected)
			if err != nil {
				return nil, fmt.Errorf("load runtime state for input plugin %q registration %s: %w", registration.InputPlugin, registration.ID, err)
			}
			envelope, err := decodeRuntimeRecord(raw, registration, latest.number)
			if err != nil {
				return nil, fmt.Errorf("parse runtime state for input plugin %q registration %s from %s: %w", registration.InputPlugin, registration.ID, selected, err)
			}
			schema, _ := protocol.StringField(envelope, "schema")
			payload, _ := envelope.Get("payload")
			result[registration.ID] = &runtimeSource{store: s, schema: schema, count: 1, payload: payload}
		}
	}
	return result, nil
}

func (s *Store) loadStreamSource(path string, owner domain.Subscription, generation, budget uint64) (*runtimeSource, error) {
	raw, err := s.readBounded(filepath.Join(path, "manifest.json"), 16384)
	if err != nil {
		return nil, err
	}
	manifest, err := jsonvalue.Parse(raw, 16384)
	if err != nil {
		return nil, err
	}
	fields := []string{"version", "subscription_id", "input_plugin", "generation", "schema", "chunk_count", "data_bytes"}
	if err := protocol.ValidateFields(manifest, fields, fields...); err != nil {
		return nil, err
	}
	version, ok := protocol.NumberField(manifest, "version")
	if !ok || version != "2" {
		return nil, errors.New("unsupported stream manifest version")
	}
	id, idOK := protocol.StringField(manifest, "subscription_id")
	input, inputOK := protocol.StringField(manifest, "input_plugin")
	g, gOK := protocol.StringField(manifest, "generation")
	if !idOK || id != string(owner.ID) || !inputOK || input != owner.InputPlugin || !gOK || g != strconv.FormatUint(generation, 10) {
		return nil, errors.New("runtime manifest owner or generation mismatch")
	}
	schema, schemaOK := protocol.StringField(manifest, "schema")
	if !schemaOK || schema == "" || !utf8.ValidString(schema) || len(schema) > 128 {
		return nil, errors.New("invalid runtime schema")
	}
	countText, countOK := protocol.StringField(manifest, "chunk_count")
	bytesText, bytesOK := protocol.StringField(manifest, "data_bytes")
	count, err := pluginprotocol.StreamCounter(countText, true)
	if !countOK || err != nil {
		return nil, errors.New("invalid runtime chunk count")
	}
	size, err := pluginprotocol.StreamCounter(bytesText, false)
	if !bytesOK || err != nil || size > budget || count > budget/2 {
		return nil, fmt.Errorf("runtime state exceeds byte budget %d", budget)
	}
	entries, err := s.files.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if uint64(len(entries)) != count+1 {
		return nil, errors.New("runtime chunk count mismatch")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "manifest.json" {
			continue
		}
		if len(name) != 25 || !strings.HasSuffix(name, ".json") {
			return nil, errors.New("unexpected stream directory entry")
		}
		index, err := strconv.ParseUint(name[:20], 10, 64)
		if err != nil || index >= count || chunkName(index) != name {
			return nil, errors.New("invalid runtime chunk index")
		}
	}
	source := &runtimeSource{store: s, path: path, schema: schema, count: count, stream: true}
	var total uint64
	for i := uint64(0); i < count; i++ {
		data, err := s.readBounded(filepath.Join(path, chunkName(i)), 32768)
		if err != nil {
			return nil, err
		}
		value, err := jsonvalue.Parse(data, 32768)
		if err != nil || value.Kind() != jsonvalue.Object {
			return nil, errors.New("invalid runtime chunk JSON")
		}
		if uint64(len(data)) > budget-total {
			return nil, fmt.Errorf("runtime state exceeds byte budget %d", budget)
		}
		total += uint64(len(data))
	}
	if total != size {
		return nil, errors.New("runtime state data_bytes mismatch")
	}
	return source, nil
}

func (s *Store) ownerLock(id domain.SubscriptionID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeLocks == nil {
		s.runtimeLocks = make(map[string]*sync.Mutex)
		s.runtimeLockRefs = make(map[string]int)
	}
	key := string(id)
	if s.runtimeLocks[key] == nil {
		s.runtimeLocks[key] = &sync.Mutex{}
	}
	s.runtimeLockRefs[key]++
	return s.runtimeLocks[key]
}

func (s *Store) releaseOwnerLock(id domain.SubscriptionID, lock *sync.Mutex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(id)
	if s.runtimeLocks[key] != lock {
		return
	}
	s.runtimeLockRefs[key]--
	if s.runtimeLockRefs[key] == 0 {
		delete(s.runtimeLocks, key)
		delete(s.runtimeLockRefs, key)
	}
}

type streamTransaction struct {
	store                            *Store
	owner                            domain.Subscription
	schema                           string
	budget, total, count, generation uint64
	path, final, ownerPath           string
	older                            []runtimeGeneration
	created, finished                bool
	lock                             *sync.Mutex
}

func (s *Store) BeginRuntimeStream(owner domain.Subscription, schema string, budget uint64) (hub.RuntimeStreamTransaction, error) {
	if !validID(string(owner.ID)) || owner.InputPlugin == "" || schema == "" || len(schema) > 128 || !utf8.ValidString(schema) || budget == 0 {
		return nil, errors.New("invalid runtime stream owner, schema or budget")
	}
	lock := s.ownerLock(owner.ID)
	lock.Lock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		lock.Unlock()
		s.releaseOwnerLock(owner.ID, lock)
		return nil, errors.New("state directory is closed")
	}
	t := &streamTransaction{store: s, owner: owner, schema: schema, budget: budget, ownerPath: filepath.Join(s.runtime, string(owner.ID)), lock: lock}
	fail := func(err error) (hub.RuntimeStreamTransaction, error) { return nil, errors.Join(err, t.Abort()) }
	info, err := s.files.Lstat(t.ownerPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.files.Mkdir(t.ownerPath, 0o700); err != nil {
			return fail(err)
		}
		t.created = true
	} else if err != nil {
		return fail(err)
	} else if !isDirectory(info) {
		return fail(errors.New("runtime owner must be a real directory"))
	}
	t.older, err = s.runtimeGenerations(t.ownerPath)
	if err != nil {
		return fail(err)
	}
	if !t.created && len(t.older) == 0 {
		return fail(errors.New("existing runtime state has no generation"))
	}
	t.generation = 1
	if len(t.older) > 0 {
		last := t.older[len(t.older)-1].number
		if last == ^uint64(0) {
			return fail(errors.New("runtime generation exhausted"))
		}
		t.generation = last + 1
	}
	nonce, err := newStagingName()
	if err != nil {
		return fail(err)
	}
	t.path = filepath.Join(s.staging, "runtime-stream-"+strings.TrimSuffix(nonce, ".tmp"))
	if err := s.files.Mkdir(t.path, 0o700); err != nil {
		t.path = ""
		return fail(err)
	}
	t.final = filepath.Join(t.ownerPath, fmt.Sprintf("%020d.stream", t.generation))
	return t, nil
}

func (t *streamTransaction) Append(index uint64, data []byte) error {
	if t.finished || index != t.count || t.count == ^uint64(0) || len(data) > 32768 || uint64(len(data)) > t.budget-t.total {
		return errors.New("invalid, exhausted or over-budget runtime stream chunk")
	}
	value, err := jsonvalue.Parse(data, 32768)
	if err != nil || value.Kind() != jsonvalue.Object {
		return errors.New("runtime chunk must be a strict object")
	}
	if string(value.Marshal()) != string(data) {
		return errors.New("runtime chunk must use compact encoding")
	}
	if err := t.write(filepath.Join(t.path, chunkName(index)), data); err != nil {
		return err
	}
	t.total += uint64(len(data))
	t.count++
	return nil
}

func (t *streamTransaction) write(path string, data []byte) error {
	file, err := t.store.files.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeAll(file, data)
	return errors.Join(writeErr, file.Close())
}

func (t *streamTransaction) Commit(count uint64, publish func(func() error) error) (uint64, error) {
	if t.finished || count == 0 || count != t.count {
		return 0, errors.New("runtime stream count mismatch")
	}
	manifest := jsonvalue.NewObject(map[string]*jsonvalue.Value{"version": jsonvalue.NewNumber("2"), "subscription_id": jsonvalue.NewString(string(t.owner.ID)), "input_plugin": jsonvalue.NewString(t.owner.InputPlugin), "generation": jsonvalue.NewString(strconv.FormatUint(t.generation, 10)), "schema": jsonvalue.NewString(t.schema), "chunk_count": jsonvalue.NewString(strconv.FormatUint(t.count, 10)), "data_bytes": jsonvalue.NewString(strconv.FormatUint(t.total, 10))})
	if err := t.write(filepath.Join(t.path, "manifest.json"), manifest.Marshal()); err != nil {
		return 0, err
	}
	if publish == nil {
		return 0, errors.New("runtime stream owner expired before publication")
	}
	if _, err := t.store.files.Lstat(t.final); !errors.Is(err, fs.ErrNotExist) {
		return 0, errors.New("runtime generation already exists or is inaccessible")
	}
	if err := publish(func() error { return t.store.files.Rename(t.path, t.final) }); err != nil {
		return 0, err
	}
	t.finished = true
	t.path = ""
	defer func() { t.lock.Unlock(); t.store.releaseOwnerLock(t.owner.ID, t.lock) }()
	var cleanup error
	for _, old := range t.older {
		path := filepath.Join(t.ownerPath, old.name)
		if old.streamed {
			cleanup = errors.Join(cleanup, t.store.removeStreamDirectory(path, false))
		} else if err := t.store.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			cleanup = errors.Join(cleanup, err)
		}
	}
	return t.generation, cleanup
}

func (t *streamTransaction) Abort() error {
	if t.finished {
		return nil
	}
	t.finished = true
	defer func() { t.lock.Unlock(); t.store.releaseOwnerLock(t.owner.ID, t.lock) }()
	var err error
	if t.path != "" {
		err = t.store.removeStreamDirectory(t.path, true)
	}
	if t.created {
		if removeErr := t.store.files.Remove(t.ownerPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

func (s *Store) removeStreamDirectory(path string, staging bool) error {
	info, err := s.files.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isDirectory(info) {
		return errors.New("stream cleanup target must be a real directory")
	}
	parent := filepath.Dir(path)
	if staging && parent != s.staging {
		return errors.New("stream cleanup escaped staging")
	}
	if !staging && filepath.Dir(parent) != s.runtime {
		return errors.New("stream cleanup escaped runtime")
	}
	paths, err := s.streamDirectoryFiles(path)
	if err != nil {
		return err
	}
	for _, child := range paths {
		if err := s.files.Remove(child); err != nil {
			return err
		}
	}
	return s.files.Remove(path)
}

// streamDirectoryFiles validates names and file types without parsing payloads.
func (s *Store) streamDirectoryFiles(path string) ([]string, error) {
	entries, err := s.files.ReadDir(path)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name != "manifest.json" {
			if len(name) != 25 || !strings.HasSuffix(name, ".json") {
				return nil, errors.New("unsafe stream cleanup entry")
			}
			index, err := strconv.ParseUint(name[:20], 10, 64)
			if err != nil || chunkName(index) != name {
				return nil, errors.New("unsafe stream cleanup index")
			}
		}
		info, err := s.files.Lstat(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		if !isRegularFile(info) {
			return nil, errors.New("unsafe stream cleanup file type")
		}
		paths = append(paths, filepath.Join(path, name))
	}
	return paths, nil
}

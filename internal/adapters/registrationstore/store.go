package registrationstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	MaxRecordBytes         = 128 * 1024
	maxStoredRegistrations = 1_024
)

type readFile interface {
	Read([]byte) (int, error)
	Close() error
}

type writeFile interface {
	Write([]byte) (int, error)
	Close() error
}

type fileSystem interface {
	Lstat(string) (os.FileInfo, error)
	ReadDir(string) ([]os.DirEntry, error)
	Mkdir(string, os.FileMode) error
	Remove(string) error
	Rename(string, string) error
	Open(string) (readFile, error)
	OpenFile(string, int, os.FileMode) (writeFile, error)
}

type osFileSystem struct{}

func (osFileSystem) Lstat(path string) (os.FileInfo, error)     { return os.Lstat(path) }
func (osFileSystem) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (osFileSystem) Mkdir(path string, mode os.FileMode) error  { return os.Mkdir(path, mode) }
func (osFileSystem) Remove(path string) error                   { return os.Remove(path) }
func (osFileSystem) Rename(oldPath, newPath string) error       { return os.Rename(oldPath, newPath) }
func (osFileSystem) Open(path string) (readFile, error)         { return os.Open(path) }
func (osFileSystem) OpenFile(path string, flags int, mode os.FileMode) (writeFile, error) {
	return os.OpenFile(path, flags, mode)
}

// Store owns the exclusive state-directory marker until Close. The caller
// keeps it open until configured plugins have stopped.
type Store struct {
	mu           sync.Mutex
	files        fileSystem
	root         string
	lockPath     string
	records      string
	staging      string
	runtime      string
	lockOwned    bool
	closed       bool
	runtimeLocks map[string]*sync.Mutex
}

func Open(root string) (*Store, error) { return open(root, osFileSystem{}) }

func open(root string, files fileSystem) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("state directory path must be absolute")
	}
	root = filepath.Clean(root)
	parent := filepath.Dir(root)
	parentInfo, err := files.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect state directory parent %s: %w", parent, err)
	}
	if !isDirectory(parentInfo) {
		return nil, fmt.Errorf("state directory parent %s must be an existing directory", parent)
	}

	rootInfo, err := files.Lstat(root)
	createdRoot := false
	if errors.Is(err, fs.ErrNotExist) {
		if err := files.Mkdir(root, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory %s: %w", root, err)
		}
		createdRoot = true
	} else if err != nil {
		return nil, fmt.Errorf("inspect state directory %s: %w", root, err)
	} else if !isDirectory(rootInfo) {
		return nil, fmt.Errorf("state directory %s must be a real directory", root)
	}

	store := &Store{
		files:    files,
		root:     root,
		lockPath: filepath.Join(root, ".lock"),
		records:  filepath.Join(root, "registrations"),
		staging:  filepath.Join(root, "staging"),
		runtime:  filepath.Join(root, "runtime"),
	}
	if err := files.Mkdir(store.lockPath, 0o700); err != nil {
		cause := fmt.Errorf("acquire state directory lock %s: %w", store.lockPath, err)
		if createdRoot {
			if removeErr := files.Remove(root); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				cause = errors.Join(cause, fmt.Errorf("remove incomplete state directory %s: %w", root, removeErr))
			}
		}
		return nil, cause
	}
	store.lockOwned = true

	var createdDirectories []string
	fail := func(cause error) (*Store, error) {
		for _, path := range createdDirectories {
			if removeErr := files.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				cause = errors.Join(cause, fmt.Errorf("remove incomplete state directory %s: %w", path, removeErr))
			}
		}
		if closeErr := store.Close(); closeErr != nil {
			cause = errors.Join(cause, closeErr)
		}
		if createdRoot {
			if removeErr := files.Remove(root); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				cause = errors.Join(cause, fmt.Errorf("remove incomplete state directory %s: %w", root, removeErr))
			}
		}
		return nil, cause
	}

	entries, err := files.ReadDir(root)
	if err != nil {
		return fail(fmt.Errorf("read state directory %s: %w", root, err))
	}
	freshRoot := len(entries) == 1 && entries[0].Name() == ".lock"
	if createdRoot && !freshRoot {
		return fail(fmt.Errorf("new state directory %s changed during initialization", root))
	}
	if freshRoot {
		for _, path := range []string{store.records, store.staging, store.runtime} {
			if err := files.Mkdir(path, 0o700); err != nil {
				return fail(fmt.Errorf("create state directory %s: %w", path, err))
			}
			createdDirectories = append(createdDirectories, path)
		}
	} else {
		seen := make(map[string]bool, 4)
		for _, entry := range entries {
			name := entry.Name()
			if name != ".lock" && name != "registrations" && name != "staging" && name != "runtime" {
				return fail(fmt.Errorf("state directory %s contains unknown entry %q", root, name))
			}
			seen[name] = true
		}
		if !seen["registrations"] || !seen["staging"] || !seen[".lock"] {
			return fail(fmt.Errorf("initialized state directory %s is missing a reserved directory", root))
		}
		for _, path := range []string{store.records, store.staging} {
			info, err := files.Lstat(path)
			if err != nil {
				return fail(fmt.Errorf("inspect state directory %s: %w", path, err))
			}
			if !isDirectory(info) {
				return fail(fmt.Errorf("state path %s must be a real directory", path))
			}
		}
		if seen["runtime"] {
			info, err := files.Lstat(store.runtime)
			if err != nil {
				return fail(fmt.Errorf("inspect state directory %s: %w", store.runtime, err))
			}
			if !isDirectory(info) {
				return fail(fmt.Errorf("state path %s must be a real directory", store.runtime))
			}
		} else {
			if err := files.Mkdir(store.runtime, 0o700); err != nil {
				return fail(fmt.Errorf("create state directory %s: %w", store.runtime, err))
			}
			createdDirectories = append(createdDirectories, store.runtime)
		}
	}
	if err := store.cleanStaging(); err != nil {
		return fail(err)
	}
	return store, nil
}

// Load reads committed records in filename order. A successful result is a
// complete state snapshot; malformed or unreadable entries reject the entire
// startup.
func (s *Store) Load() ([]domain.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("state directory is closed")
	}
	entries, err := s.files.ReadDir(s.records)
	if err != nil {
		return nil, fmt.Errorf("read registration directory %s: %w", s.records, err)
	}
	if len(entries) > maxStoredRegistrations {
		return nil, fmt.Errorf("registration directory %s exceeds capacity %d", s.records, maxStoredRegistrations)
	}
	registrations := make([]domain.Subscription, 0, len(entries))
	ids := make(map[domain.SubscriptionID]struct{}, len(entries))
	keys := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		path := filepath.Join(s.records, entry.Name())
		info, err := s.files.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect registration file %s: %w", path, err)
		}
		if !isRegularFile(info) {
			return nil, fmt.Errorf("registration path %s must be a regular file", path)
		}
		idFromName := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Name() != idFromName+".json" || !validID(idFromName) {
			return nil, fmt.Errorf("registration filename %s is invalid", path)
		}
		if info.Size() > MaxRecordBytes {
			return nil, fmt.Errorf("registration file %s exceeds %d bytes", path, MaxRecordBytes)
		}
		file, err := s.files.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open registration file %s: %w", path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("read registration file %s: %w", path, errors.Join(readErr, closeErr))
		}
		if len(data) > MaxRecordBytes {
			return nil, fmt.Errorf("registration file %s exceeds %d bytes", path, MaxRecordBytes)
		}
		registration, err := decodeRecord(data)
		if err != nil {
			return nil, fmt.Errorf("parse registration file %s: %w", path, err)
		}
		if string(registration.ID) != idFromName {
			return nil, fmt.Errorf("registration file %s filename does not match subscription ID", path)
		}
		if _, exists := ids[registration.ID]; exists {
			return nil, fmt.Errorf("registration ID %s is duplicated", registration.ID)
		}
		ids[registration.ID] = struct{}{}
		key := routeKey(registration)
		if _, exists := keys[key]; exists {
			return nil, fmt.Errorf("registration file %s duplicates a route", path)
		}
		keys[key] = struct{}{}
		registrations = append(registrations, registration)
	}
	return registrations, nil
}

// Commit writes a complete record through a unique staging file. The final
// rename is the commit point.
func (s *Store) Commit(registration domain.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("state directory is closed")
	}
	if !validID(string(registration.ID)) || registration.InputPlugin == "" || registration.OutputPlugin == "" || registration.WatchArgs == nil || registration.WatchArgs.Kind() != jsonvalue.Object || registration.DestinationArgs == nil || registration.DestinationArgs.Kind() != jsonvalue.Object {
		return errors.New("registration record is invalid")
	}
	runtimePath := filepath.Join(s.runtime, string(registration.ID))
	if _, err := s.files.Lstat(runtimePath); err == nil {
		return fmt.Errorf("runtime state directory %s already exists for a new registration", runtimePath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect runtime state directory %s: %w", runtimePath, err)
	}
	data := encodeRecord(registration)
	if len(data) > MaxRecordBytes {
		return fmt.Errorf("registration record exceeds %d bytes", MaxRecordBytes)
	}
	stagePath, file, err := s.createStagingFile()
	if err != nil {
		return err
	}
	writeErr := writeAll(file, data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cause := fmt.Errorf("write registration staging file %s: %w", stagePath, errors.Join(writeErr, closeErr))
		return errors.Join(cause, s.removeStaging(stagePath))
	}
	finalPath := filepath.Join(s.records, string(registration.ID)+".json")
	if _, err := s.files.Lstat(finalPath); err == nil {
		return errors.Join(fmt.Errorf("registration file %s already exists", finalPath), s.removeStaging(stagePath))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(fmt.Errorf("inspect registration file %s: %w", finalPath, err), s.removeStaging(stagePath))
	}
	if err := s.files.Rename(stagePath, finalPath); err != nil {
		return errors.Join(fmt.Errorf("commit registration file %s: %w", finalPath, err), s.removeStaging(stagePath))
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.lockOwned {
		return nil
	}
	if err := s.files.Remove(s.lockPath); err != nil {
		return fmt.Errorf("release state directory lock %s: %w", s.lockPath, err)
	}
	s.lockOwned = false
	s.closed = true
	return nil
}

func (s *Store) cleanStaging() error {
	entries, err := s.files.ReadDir(s.staging)
	if err != nil {
		return fmt.Errorf("read staging directory %s: %w", s.staging, err)
	}
	for _, entry := range entries {
		path := filepath.Join(s.staging, entry.Name())
		if strings.HasPrefix(entry.Name(), "runtime-stream-") {
			nonce := strings.TrimPrefix(entry.Name(), "runtime-stream-")
			if !validID(nonce) {
				return fmt.Errorf("unsafe runtime staging directory %s", path)
			}
			if err := s.removeStreamDirectory(path, true); err != nil {
				return err
			}
			continue
		}
		info, err := s.files.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect staging entry %s: %w", path, err)
		}
		if !isRegularFile(info) {
			return fmt.Errorf("staging entry %s must be a regular file", path)
		}
		if err := s.files.Remove(path); err != nil {
			return fmt.Errorf("remove stale staging file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) createStagingFile() (string, writeFile, error) {
	for attempt := 0; attempt < 8; attempt++ {
		name, err := newStagingName()
		if err != nil {
			return "", nil, fmt.Errorf("create registration staging name: %w", err)
		}
		path := filepath.Join(s.staging, name)
		file, err := s.files.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create registration staging file %s: %w", path, err)
		}
		return path, file, nil
	}
	return "", nil, fmt.Errorf("create registration staging file in %s: unique name unavailable", s.staging)
}

func (s *Store) removeStaging(path string) error {
	if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove registration staging file %s: %w", path, err)
	}
	return nil
}

func decodeRecord(data []byte) (domain.Subscription, error) {
	value, err := jsonvalue.Parse(data, MaxRecordBytes)
	if err != nil {
		return domain.Subscription{}, err
	}
	fields := []string{"version", "subscription_id", "input_plugin", "watch_args", "output_plugin", "destination_args"}
	if err := protocol.ValidateFields(value, fields, fields...); err != nil {
		return domain.Subscription{}, errors.New("record fields are invalid")
	}
	version, ok := value.Get("version")
	if !ok || version.Kind() != jsonvalue.Number {
		return domain.Subscription{}, errors.New("record version is invalid")
	}
	versionToken, _ := version.Text()
	if versionToken != "1" {
		return domain.Subscription{}, errors.New("record version is unsupported")
	}
	id, idOK := protocol.StringField(value, "subscription_id")
	input, inputOK := protocol.StringField(value, "input_plugin")
	output, outputOK := protocol.StringField(value, "output_plugin")
	watch, watchOK := protocol.ObjectField(value, "watch_args")
	destination, destinationOK := protocol.ObjectField(value, "destination_args")
	if !idOK || !validID(id) || !inputOK || input == "" || !outputOK || output == "" || !watchOK || !destinationOK {
		return domain.Subscription{}, errors.New("record values are invalid")
	}
	return domain.Subscription{
		ID:              domain.SubscriptionID(id),
		InputPlugin:     input,
		OutputPlugin:    output,
		WatchArgs:       watch,
		DestinationArgs: destination,
	}, nil
}

func encodeRecord(registration domain.Subscription) []byte {
	return jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":          jsonvalue.NewNumber("1"),
		"subscription_id":  jsonvalue.NewString(string(registration.ID)),
		"input_plugin":     jsonvalue.NewString(registration.InputPlugin),
		"watch_args":       registration.WatchArgs,
		"output_plugin":    jsonvalue.NewString(registration.OutputPlugin),
		"destination_args": registration.DestinationArgs,
	}).Marshal()
}

func routeKey(registration domain.Subscription) string {
	return string(jsonvalue.NewArray(
		jsonvalue.NewString(registration.InputPlugin), registration.WatchArgs,
		jsonvalue.NewString(registration.OutputPlugin), registration.DestinationArgs,
	).Canonical())
}

func validID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, character := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func isDirectory(info os.FileInfo) bool {
	return info != nil && info.Mode().IsDir() && info.Mode()&os.ModeSymlink == 0
}

func isRegularFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func writeAll(file writeFile, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func newStagingName() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded) + ".tmp", nil
}

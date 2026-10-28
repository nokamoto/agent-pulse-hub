package registrationstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxRuntimeRecordBytes = 49_152

type runtimeGeneration struct {
	number   uint64
	name     string
	streamed bool
}

// LoadRuntime validates every runtime directory and restores only generations
// owned by a currently committed registration. Orphan payloads are never read.
func (s *Store) LoadRuntime(registrations []domain.Subscription) (map[domain.SubscriptionID]*jsonvalue.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("state directory is closed")
	}
	info, err := s.files.Lstat(s.runtime)
	if err != nil {
		return nil, fmt.Errorf("inspect runtime state path %s: %w", s.runtime, err)
	}
	if !isDirectory(info) {
		return nil, fmt.Errorf("runtime state path %s must be a real directory", s.runtime)
	}

	owners := make(map[domain.SubscriptionID]domain.Subscription, len(registrations))
	for _, registration := range registrations {
		owners[registration.ID] = registration
	}
	entries, err := s.files.ReadDir(s.runtime)
	if err != nil {
		return nil, fmt.Errorf("read runtime state directory %s: %w", s.runtime, err)
	}
	states := make(map[domain.SubscriptionID]*jsonvalue.Value)
	for _, entry := range entries {
		id := entry.Name()
		if !validID(id) {
			return nil, fmt.Errorf("runtime state directory name %q is invalid", id)
		}
		registration, owned := owners[domain.SubscriptionID(id)]
		directory := filepath.Join(s.runtime, id)
		directoryInfo, err := s.files.Lstat(directory)
		if err != nil {
			return nil, fmt.Errorf("inspect runtime state path %s: %w", directory, err)
		}
		if !isDirectory(directoryInfo) {
			return nil, fmt.Errorf("runtime state path %s must be a real directory", directory)
		}
		generations, err := s.runtimeGenerations(directory)
		if err != nil {
			if owned {
				return nil, fmt.Errorf("validate runtime state for input plugin %q registration %s: %w", registration.InputPlugin, registration.ID, err)
			}
			return nil, err
		}
		if len(generations) == 0 {
			if owned {
				return nil, fmt.Errorf("runtime state directory %s for input plugin %q registration %s has no committed generation", directory, registration.InputPlugin, registration.ID)
			}
			return nil, fmt.Errorf("runtime state directory %s has no committed generation", directory)
		}
		if !owned {
			continue
		}
		latest := generations[len(generations)-1]
		path := filepath.Join(directory, latest.name)
		data, err := s.readRuntimeRecord(path)
		if err != nil {
			return nil, fmt.Errorf("load runtime state for input plugin %q registration %s: %w", registration.InputPlugin, registration.ID, err)
		}
		state, err := decodeRuntimeRecord(data, registration, latest.number)
		if err != nil {
			return nil, fmt.Errorf("parse runtime state for input plugin %q registration %s from %s: %w", registration.InputPlugin, registration.ID, path, err)
		}
		states[registration.ID] = state
	}
	return states, nil
}

// CommitRuntime atomically commits the next immutable generation. A nonzero
// generation means the rename committed even if older-file cleanup then failed.
func (s *Store) CommitRuntime(registration domain.Subscription, state *jsonvalue.Value) (uint64, error) {
	if !validID(string(registration.ID)) || registration.InputPlugin == "" {
		return 0, errors.New("runtime state owner is invalid")
	}
	if err := pluginprotocol.ValidateStateEnvelope(state); err != nil {
		return 0, err
	}
	ownerLock := s.ownerLock(registration.ID)
	ownerLock.Lock()
	defer ownerLock.Unlock()
	data := encodeRuntimeRecord(registration, state, 1)
	if len(data) > MaxRuntimeRecordBytes {
		return 0, fmt.Errorf("runtime state record exceeds %d bytes", MaxRuntimeRecordBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("state directory is closed")
	}
	ownerDirectory := filepath.Join(s.runtime, string(registration.ID))
	createdDirectory := false
	info, err := s.files.Lstat(ownerDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.files.Mkdir(ownerDirectory, 0o700); err != nil {
			return 0, fmt.Errorf("create runtime state directory %s: %w", ownerDirectory, err)
		}
		createdDirectory = true
	} else if err != nil {
		return 0, fmt.Errorf("inspect runtime state directory %s: %w", ownerDirectory, err)
	} else if !isDirectory(info) {
		return 0, fmt.Errorf("runtime state path %s must be a real directory", ownerDirectory)
	}

	generations, err := s.runtimeGenerations(ownerDirectory)
	if err != nil {
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, err)
	}
	if !createdDirectory && len(generations) == 0 {
		return 0, errors.New("existing runtime state directory has no committed generation")
	}
	var latest uint64
	if len(generations) != 0 {
		latest = generations[len(generations)-1].number
	}
	if latest == ^uint64(0) {
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, errors.New("runtime state generation is exhausted"))
	}
	generation := latest + 1
	data = encodeRuntimeRecord(registration, state, generation)
	if len(data) > MaxRuntimeRecordBytes {
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, fmt.Errorf("runtime state record exceeds %d bytes", MaxRuntimeRecordBytes))
	}
	stagePath, file, err := s.createStagingFile()
	if err != nil {
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, err)
	}
	writeErr := writeAll(file, data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cause := fmt.Errorf("write runtime state staging file %s: %w", stagePath, errors.Join(writeErr, closeErr))
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, errors.Join(cause, s.removeStaging(stagePath)))
	}

	name := generationName(generation)
	finalPath := filepath.Join(ownerDirectory, name)
	if _, err := s.files.Lstat(finalPath); err == nil {
		cause := fmt.Errorf("runtime state generation %s already exists", finalPath)
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, errors.Join(cause, s.removeStaging(stagePath)))
	} else if !errors.Is(err, fs.ErrNotExist) {
		cause := fmt.Errorf("inspect runtime state generation %s: %w", finalPath, err)
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, errors.Join(cause, s.removeStaging(stagePath)))
	}
	if err := s.files.Rename(stagePath, finalPath); err != nil {
		cause := fmt.Errorf("commit runtime state generation %s: %w", finalPath, err)
		return 0, s.removeNewRuntimeDirectory(ownerDirectory, createdDirectory, errors.Join(cause, s.removeStaging(stagePath)))
	}

	var cleanupErr error
	for _, old := range generations {
		oldPath := filepath.Join(ownerDirectory, old.name)
		if err := s.files.Remove(oldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove older runtime state generation %s: %w", oldPath, err))
		}
	}
	return generation, cleanupErr
}

func (s *Store) runtimeGenerations(directory string) ([]runtimeGeneration, error) {
	entries, err := s.files.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read runtime state directory %s: %w", directory, err)
	}
	generations := make([]runtimeGeneration, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		streamed := strings.HasSuffix(name, ".stream")
		parseName := name
		if streamed {
			parseName = strings.TrimSuffix(name, ".stream") + ".json"
		}
		number, valid := parseGenerationName(parseName)
		if !valid {
			return nil, fmt.Errorf("runtime state generation filename %s is invalid", filepath.Join(directory, name))
		}
		path := filepath.Join(directory, name)
		info, err := s.files.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect runtime state generation %s: %w", path, err)
		}
		if (!streamed && !isRegularFile(info)) || (streamed && !isDirectory(info)) {
			return nil, fmt.Errorf("runtime state generation %s must be a regular file", path)
		}
		if !streamed && info.Size() > MaxRuntimeRecordBytes {
			return nil, fmt.Errorf("runtime state file %s exceeds %d bytes", path, MaxRuntimeRecordBytes)
		}
		generations = append(generations, runtimeGeneration{number: number, name: name, streamed: streamed})
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i].number < generations[j].number })
	for i := 1; i < len(generations); i++ {
		if generations[i].number == generations[i-1].number {
			return nil, errors.New("runtime generation occurs in both formats")
		}
	}
	return generations, nil
}

func (s *Store) readRuntimeRecord(path string) ([]byte, error) {
	file, err := s.files.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runtime state file %s: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxRuntimeRecordBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read runtime state file %s: %w", path, errors.Join(readErr, closeErr))
	}
	if len(data) > MaxRuntimeRecordBytes {
		return nil, fmt.Errorf("runtime state file %s exceeds %d bytes", path, MaxRuntimeRecordBytes)
	}
	return data, nil
}

func (s *Store) removeNewRuntimeDirectory(path string, created bool, cause error) error {
	if created {
		if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			cause = errors.Join(cause, fmt.Errorf("remove empty runtime state directory %s: %w", path, err))
		}
	}
	return cause
}

func decodeRuntimeRecord(data []byte, owner domain.Subscription, filenameGeneration uint64) (*jsonvalue.Value, error) {
	value, err := jsonvalue.Parse(data, MaxRuntimeRecordBytes)
	if err != nil {
		// Parser diagnostics can quote trigger-owned object keys (for example,
		// duplicate keys inside the opaque state payload). Keep those values out
		// of startup diagnostics while retaining the owning registration/path.
		return nil, errors.New("record JSON is invalid")
	}
	fields := []string{"version", "subscription_id", "input_plugin", "generation", "state"}
	if err := protocol.ValidateFields(value, fields, fields...); err != nil {
		return nil, errors.New("record fields are invalid")
	}
	version, versionOK := protocol.NumberField(value, "version")
	id, idOK := protocol.StringField(value, "subscription_id")
	input, inputOK := protocol.StringField(value, "input_plugin")
	generation, generationOK := protocol.StringField(value, "generation")
	state, stateOK := value.Get("state")
	if !versionOK || version != "1" || !idOK || id != string(owner.ID) || !inputOK || input != owner.InputPlugin || !generationOK || generation != strconv.FormatUint(filenameGeneration, 10) || !stateOK {
		return nil, errors.New("record identity or generation is invalid")
	}
	if err := pluginprotocol.ValidateStateEnvelope(state); err != nil {
		return nil, fmt.Errorf("state envelope is invalid: %w", err)
	}
	return state, nil
}

func encodeRuntimeRecord(registration domain.Subscription, state *jsonvalue.Value, generation uint64) []byte {
	return jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":         jsonvalue.NewNumber("1"),
		"subscription_id": jsonvalue.NewString(string(registration.ID)),
		"input_plugin":    jsonvalue.NewString(registration.InputPlugin),
		"generation":      jsonvalue.NewString(strconv.FormatUint(generation, 10)),
		"state":           state,
	}).Marshal()
}

func generationName(generation uint64) string {
	return fmt.Sprintf("%020d.json", generation)
}

func parseGenerationName(name string) (uint64, bool) {
	if len(name) != 25 || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	if err != nil || value == 0 || generationName(value) != name {
		return 0, false
	}
	return value, true
}

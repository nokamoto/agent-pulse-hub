package registrationstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

// BeginCancellation durably records an explicit removal request. The caller
// fences and drains the target's saves before calling it.
func (s *Store) BeginCancellation(owner domain.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("state directory is closed")
	}
	if !validID(string(owner.ID)) || owner.InputPlugin == "" {
		return errors.New("cancellation owner is invalid")
	}
	if err := s.validateCancellationOwner(owner); err != nil {
		return err
	}
	path := s.cancellationPath(owner.ID)
	if _, err := s.files.Lstat(path); err == nil {
		stored, err := s.readCancellation(path, string(owner.ID))
		if err != nil {
			return err
		}
		if stored.InputPlugin != owner.InputPlugin {
			return fmt.Errorf("cancellation intent %s has a different input owner", path)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect cancellation intent %s: %w", path, err)
	}
	stage, file, err := s.createStagingFile()
	if err != nil {
		return err
	}
	writeErr := writeAll(file, encodeCancellation(owner))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(fmt.Errorf("write cancellation intent staging file %s: %w", stage, errors.Join(writeErr, closeErr)), s.removeStaging(stage))
	}
	// The store lock and daemon exclusion serialize all legitimate writers.
	if _, err := s.files.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = errors.New("intent already exists")
		}
		return errors.Join(fmt.Errorf("inspect cancellation intent %s before commit: %w", path, err), s.removeStaging(stage))
	}
	if err := s.files.Rename(stage, path); err != nil {
		return errors.Join(fmt.Errorf("commit cancellation intent %s: %w", path, err), s.removeStaging(stage))
	}
	return nil
}

// RemoveCancelled requires confirmed watch termination and a committed intent.
// A cleanup error concerns only the remaining intent; err means removal failed.
func (s *Store) RemoveCancelled(owner domain.Subscription) (cleanupErr error, err error) {
	ownerLock := s.ownerLock(owner.ID)
	ownerLock.Lock()
	defer func() { ownerLock.Unlock(); s.releaseOwnerLock(owner.ID, ownerLock) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("state directory is closed")
	}
	if !validID(string(owner.ID)) || owner.InputPlugin == "" {
		return nil, errors.New("cancellation owner is invalid")
	}
	intent, err := s.readCancellation(s.cancellationPath(owner.ID), string(owner.ID))
	if err != nil {
		return nil, err
	}
	if intent.InputPlugin != owner.InputPlugin {
		return nil, errors.New("cancellation intent input owner does not match")
	}
	if err := s.validateCancellationOwner(intent); err != nil {
		return nil, err
	}
	if _, err := s.cancellationRuntimeFiles(intent.ID); err != nil {
		return nil, err
	}
	return s.removeCancelled(intent)
}

// RecoverCancellations validates the complete intent set before any deletion.
// Call after Open and before Load or plugin launch with configured input names.
func (s *Store) RecoverCancellations(configuredInputs []string) (cleanupErr error, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("state directory is closed")
	}
	inputs := make(map[string]bool, len(configuredInputs))
	for _, input := range configuredInputs {
		inputs[input] = true
	}
	entries, err := s.files.ReadDir(s.cancellations)
	if err != nil {
		return nil, fmt.Errorf("read cancellation directory %s: %w", s.cancellations, err)
	}
	intents := make([]domain.Subscription, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) || entry.Name() != id+".json" {
			return nil, fmt.Errorf("cancellation filename %s is invalid", filepath.Join(s.cancellations, entry.Name()))
		}
		intent, err := s.readCancellation(filepath.Join(s.cancellations, entry.Name()), id)
		if err != nil {
			return nil, err
		}
		if !inputs[intent.InputPlugin] {
			return nil, fmt.Errorf("cancellation intent %s input plugin %q is not a configured input", id, intent.InputPlugin)
		}
		if err := s.validateCancellationOwner(intent); err != nil {
			return nil, err
		}
		if _, err := s.cancellationRuntimeFiles(intent.ID); err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	for _, intent := range intents {
		cleanup, err := s.removeCancelled(intent)
		cleanupErr = errors.Join(cleanupErr, cleanup)
		if err != nil {
			return cleanupErr, err
		}
	}
	return cleanupErr, nil
}

func (s *Store) cancellationPath(id domain.SubscriptionID) string {
	return filepath.Join(s.cancellations, string(id)+".json")
}

func (s *Store) readCancellation(path, expectedID string) (domain.Subscription, error) {
	data, err := s.readCancellationFile(path)
	if err != nil {
		return domain.Subscription{}, err
	}
	value, err := jsonvalue.Parse(data, MaxRecordBytes)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("cancellation intent %s JSON is invalid", path)
	}
	fields := []string{"version", "subscription_id", "input_plugin"}
	if protocol.ValidateFields(value, fields, fields...) != nil {
		return domain.Subscription{}, fmt.Errorf("cancellation intent %s fields are invalid", path)
	}
	version, versionOK := protocol.NumberField(value, "version")
	id, idOK := protocol.StringField(value, "subscription_id")
	input, inputOK := protocol.StringField(value, "input_plugin")
	if !versionOK || version != "1" || !idOK || !validID(id) || id != expectedID || !inputOK || input == "" {
		return domain.Subscription{}, fmt.Errorf("cancellation intent %s identity or version is invalid", path)
	}
	return domain.Subscription{ID: domain.SubscriptionID(id), InputPlugin: input}, nil
}

func (s *Store) readCancellationFile(path string) ([]byte, error) {
	info, err := s.files.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect cancellation record %s: %w", path, err)
	}
	if !isRegularFile(info) || info.Size() > MaxRecordBytes {
		return nil, fmt.Errorf("cancellation record %s must be a regular file within %d bytes", path, MaxRecordBytes)
	}
	file, err := s.files.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open cancellation record %s: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxRecordBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read cancellation record %s: %w", path, errors.Join(readErr, closeErr))
	}
	if len(data) > MaxRecordBytes {
		return nil, fmt.Errorf("cancellation record %s exceeds %d bytes", path, MaxRecordBytes)
	}
	return data, nil
}

func (s *Store) validateCancellationOwner(owner domain.Subscription) error {
	path := filepath.Join(s.records, string(owner.ID)+".json")
	if _, err := s.files.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect cancelled registration %s: %w", path, err)
	}
	data, err := s.readCancellationFile(path)
	if err != nil {
		return err
	}
	registration, err := decodeRecord(data)
	if err != nil {
		return fmt.Errorf("cancelled registration %s is invalid", path)
	}
	if registration.ID != owner.ID || registration.InputPlugin != owner.InputPlugin {
		return fmt.Errorf("cancelled registration %s does not match intent owner", path)
	}
	return nil
}

// cancellationRuntimeFiles inspects types without interpreting opaque payloads.
func (s *Store) cancellationRuntimeFiles(id domain.SubscriptionID) ([]string, error) {
	directory := filepath.Join(s.runtime, string(id))
	info, err := s.files.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect cancelled runtime directory %s: %w", directory, err)
	}
	if !isDirectory(info) {
		return nil, fmt.Errorf("cancelled runtime path %s must be a real directory", directory)
	}
	entries, err := s.files.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read cancelled runtime directory %s: %w", directory, err)
	}
	paths := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := s.files.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect cancelled runtime entry %s: %w", path, err)
		}
		if strings.HasSuffix(entry.Name(), ".stream") {
			name := strings.TrimSuffix(entry.Name(), ".stream") + ".json"
			if _, valid := parseGenerationName(name); !valid || !isDirectory(info) {
				return nil, fmt.Errorf("cancelled runtime entry %s must be a real stream generation directory", path)
			}
			files, err := s.streamDirectoryFiles(path)
			if err != nil {
				return nil, fmt.Errorf("inspect cancelled stream generation %s: %w", path, err)
			}
			paths = append(paths, files...)
		} else if _, valid := parseGenerationName(entry.Name()); !valid || !isRegularFile(info) {
			return nil, fmt.Errorf("cancelled runtime entry %s must be a regular generation file", path)
		}
		paths = append(paths, path)
	}
	return append(paths, directory), nil
}

func (s *Store) removeCancelled(owner domain.Subscription) (cleanupErr error, err error) {
	paths, err := s.cancellationRuntimeFiles(owner.ID)
	if err != nil {
		return nil, err
	}
	paths = append(paths, filepath.Join(s.records, string(owner.ID)+".json"))
	for _, path := range paths {
		if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove cancelled state %s: %w", path, err)
		}
	}
	path := s.cancellationPath(owner.ID)
	if err := s.files.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove cancellation intent housekeeping %s: %w", path, err), nil
	}
	return nil, nil
}

func encodeCancellation(owner domain.Subscription) []byte {
	return jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":         jsonvalue.NewNumber("1"),
		"subscription_id": jsonvalue.NewString(string(owner.ID)),
		"input_plugin":    jsonvalue.NewString(owner.InputPlugin),
	}).Marshal()
}

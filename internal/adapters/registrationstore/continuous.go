package registrationstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

const continuousMarker = "continuous-active.json"

func (s *Store) BeginContinuousRun(id domain.SubscriptionID, sessionID string) error {
	if !validID(string(id)) || !validID(sessionID) {
		return errors.New("invalid continuous run identity")
	}
	lock := s.ownerLock(id)
	lock.Lock()
	defer func() { lock.Unlock(); s.releaseOwnerLock(id, lock) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginContinuousRunLocked(id, sessionID)
}

func (s *Store) beginContinuousRunLocked(id domain.SubscriptionID, sessionID string) error {
	if s.closed {
		return errors.New("state directory is closed")
	}
	directory := filepath.Join(s.runtime, string(id))
	info, err := s.files.Lstat(directory)
	created := errors.Is(err, fs.ErrNotExist)
	if created {
		if err := s.files.Mkdir(directory, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !isDirectory(info) {
		return fmt.Errorf("runtime owner %s must be a real directory", directory)
	}
	path := filepath.Join(directory, continuousMarker)
	file, err := s.files.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return s.removeNewRuntimeDirectory(directory, created, err)
	}
	raw := fmt.Appendf(nil, "{\"version\":1,\"subscription_id\":%q,\"session_id\":%q}", id, sessionID)
	writeErr := writeAll(file, raw)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return s.removeNewRuntimeDirectory(directory, created, errors.Join(err, s.files.Remove(path)))
	}
	if s.continuousRuns == nil {
		s.continuousRuns = make(map[domain.SubscriptionID]string)
	}
	s.continuousRuns[id] = sessionID
	return nil
}

func (s *Store) EndContinuousRun(id domain.SubscriptionID, sessionID string) error {
	if !validID(string(id)) || !validID(sessionID) {
		return errors.New("invalid continuous run identity")
	}
	lock := s.ownerLock(id)
	lock.Lock()
	defer func() { lock.Unlock(); s.releaseOwnerLock(id, lock) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endContinuousRunLocked(id, sessionID)
}

func (s *Store) endContinuousRunLocked(id domain.SubscriptionID, sessionID string) error {
	if s.closed {
		return errors.New("state directory is closed")
	}
	directory := filepath.Join(s.runtime, string(id))
	info, err := s.files.Lstat(directory)
	if err != nil {
		return err
	}
	if !isDirectory(info) {
		return fmt.Errorf("runtime owner %s must be a real directory", directory)
	}
	path := filepath.Join(directory, continuousMarker)
	session, err := s.readContinuousRun(path, string(id))
	if err != nil {
		return err
	}
	if session != sessionID {
		return fmt.Errorf("continuous run marker %s belongs to another session", path)
	}
	entries, err := s.files.ReadDir(directory)
	if err != nil {
		return err
	}
	if err := s.files.Remove(path); err != nil {
		return err
	}
	delete(s.continuousRuns, id)
	if len(entries) == 1 && entries[0].Name() == continuousMarker {
		return s.files.Remove(directory)
	}
	return nil
}

func (s *Store) ownsContinuousRun(id domain.SubscriptionID, directory string) bool {
	expected := s.continuousRuns[id]
	if expected == "" {
		return false
	}
	session, err := s.readContinuousRun(filepath.Join(directory, continuousMarker), string(id))
	return err == nil && session == expected
}

func (s *Store) readContinuousRun(path, id string) (string, error) {
	raw, err := s.readBounded(path, 1024)
	if err != nil {
		return "", err
	}
	value, err := protocol.ParseFrame(raw)
	if err != nil {
		return "", errors.New("invalid continuous run marker JSON")
	}
	fields := []string{"version", "subscription_id", "session_id"}
	version, _ := protocol.NumberField(value, "version")
	owner, _ := protocol.StringField(value, "subscription_id")
	session, _ := protocol.StringField(value, "session_id")
	if protocol.ValidateFields(value, fields, fields...) != nil || version != "1" || owner != id || !validID(session) {
		return "", errors.New("invalid continuous run marker identity")
	}
	return session, nil
}

func (s *Store) rejectContinuousRun(directory, id string) error {
	path := filepath.Join(directory, continuousMarker)
	if _, err := s.files.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	reason := "previous run did not complete final persistence"
	if _, err := s.readContinuousRun(path, id); err != nil {
		reason = "marker could not be validated"
	}
	return fmt.Errorf("continuous_progress_unverified: registration %s marker %s: %s", id, path, reason)
}

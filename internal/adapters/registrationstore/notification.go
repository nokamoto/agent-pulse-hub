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
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/notificationcodec"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

type notificationMarker struct {
	Version int    `json:"version"`
	StoreID string `json:"store_id"`
}
type notificationLedger struct {
	Version       int                        `json:"version"`
	StoreID       string                     `json:"store_id"`
	Generation    string                     `json:"generation"`
	SavedAt       time.Time                  `json:"saved_at"`
	Notifications []notificationcodec.Record `json:"notifications"`
}
type notificationGuard struct {
	Version            int       `json:"version"`
	StoreID            string    `json:"store_id"`
	NotificationID     string    `json:"notification_id"`
	Ordinal            int       `json:"ordinal"`
	AttemptID          *string   `json:"attempt_id"`
	BindingHash        string    `json:"binding_hash"`
	ReservedGeneration string    `json:"reserved_generation"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}
type notificationCompaction struct {
	Version    int      `json:"version"`
	StoreID    string   `json:"store_id"`
	Generation string   `json:"generation"`
	Guards     []string `json:"guards"`
}
type notificationStoreState struct {
	id         string
	generation uint64
	savedAt    time.Time
	records    []domain.Notification
	guards     map[string]notificationGuard
	blocked    error
}

func (s *Store) notificationPath(names ...string) string {
	return filepath.Join(append([]string{s.root, "notifications"}, names...)...)
}

func (s *Store) readNotificationFile(path string, max int) ([]byte, error) {
	info, err := s.files.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !isRegularFile(info) || info.Size() > int64(max) {
		return nil, fmt.Errorf("notification entry %s must be a bounded regular file", path)
	}
	f, err := s.files.Open(path)
	if err != nil {
		return nil, err
	}
	data, re := io.ReadAll(io.LimitReader(f, int64(max)+1))
	ce := f.Close()
	if re != nil || ce != nil {
		return nil, errors.Join(re, ce)
	}
	if len(data) > max {
		return nil, errors.New("notification entry exceeds size limit")
	}
	return data, nil
}

func (s *Store) publishNotification(path string, value any) error {
	data, err := notificationcodec.Marshal(value)
	if err != nil {
		return err
	}
	stage, file, err := s.createStagingFile()
	if err != nil {
		return err
	}
	we := writeAll(file, data)
	ce := file.Close()
	if we != nil || ce != nil {
		return errors.Join(we, ce, s.removeStaging(stage))
	}
	if _, err := s.files.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(errors.New("notification commit target is not absent"), err, s.removeStaging(stage))
	}
	if err := s.files.Rename(stage, path); err != nil {
		return errors.Join(err, s.removeStaging(stage))
	}
	return nil
}

// LoadNotifications initializes only an absent pair after negotiation. Existing
// stores are validated before cancellation housekeeping and plugin launch.
func (s *Store) LoadNotifications() ([]domain.Notification, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, time.Time{}, false, errors.New("state directory is closed")
	}
	initialized := false
	if s.notifications == nil {
		if err := s.loadNotificationsLocked(); err != nil {
			return nil, time.Time{}, false, err
		}
		if s.notifications == nil {
			id, err := newStagingName()
			if err != nil {
				return nil, time.Time{}, false, err
			}
			id = strings.TrimSuffix(id, ".tmp")
			for _, p := range []string{s.notificationPath(), s.notificationPath("ledger"), s.notificationPath("guards")} {
				if err := s.files.Mkdir(p, 0o700); err != nil {
					return nil, time.Time{}, false, err
				}
			}
			if err := s.publishNotification(filepath.Join(s.root, "notification-store.json"), notificationMarker{1, id}); err != nil {
				return nil, time.Time{}, false, err
			}
			s.notifications = &notificationStoreState{id: id, guards: map[string]notificationGuard{}}
			if err := s.commitNotificationsLocked([]domain.Notification{}, time.Now().UTC()); err != nil {
				return nil, time.Time{}, false, err
			}
			initialized = true
		}
	}
	if s.notifications.blocked != nil {
		return nil, time.Time{}, false, s.notifications.blocked
	}
	result := make([]domain.Notification, len(s.notifications.records))
	for i, n := range s.notifications.records {
		result[i] = n.Clone()
	}
	return result, s.notifications.savedAt, initialized, nil
}

func (s *Store) loadNotificationsLocked() error {
	markerPath := filepath.Join(s.root, "notification-store.json")
	_, me := s.files.Lstat(markerPath)
	_, de := s.files.Lstat(s.notificationPath())
	if errors.Is(me, fs.ErrNotExist) && errors.Is(de, fs.ErrNotExist) {
		return nil
	}
	if me != nil || de != nil {
		return errors.New("notification store marker and directory must both exist")
	}
	for _, p := range []string{s.notificationPath(), s.notificationPath("ledger"), s.notificationPath("guards")} {
		info, err := s.files.Lstat(p)
		if err != nil {
			return err
		}
		if !isDirectory(info) {
			return errors.New("notification store paths must be real directories")
		}
	}
	entries, err := s.files.ReadDir(s.notificationPath())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "ledger" && e.Name() != "guards" && e.Name() != "compaction.json" {
			return errors.New("unknown notification store entry")
		}
	}
	data, err := s.readNotificationFile(markerPath, 1024)
	if err != nil {
		return err
	}
	var marker notificationMarker
	if err := notificationcodec.StrictDecode(data, &marker, "version", "store_id"); err != nil {
		return err
	}
	if marker.Version != 1 || !domain.ValidNotificationID(marker.StoreID) {
		return errors.New("invalid notification store marker")
	}
	gens, err := s.notificationGenerations()
	if err != nil {
		return err
	}
	if len(gens) == 0 {
		return errors.New("empty notification ledger directory")
	}
	latest := gens[len(gens)-1]
	data, err = s.readNotificationFile(s.notificationPath("ledger", latest.name), 134217728)
	if err != nil {
		return err
	}
	var ledger notificationLedger
	if err := notificationcodec.StrictDecode(data, &ledger, "version", "store_id", "generation", "saved_at", "notifications"); err != nil {
		return err
	}
	if ledger.Version != 1 || ledger.StoreID != marker.StoreID || ledger.Generation != strconv.FormatUint(latest.number, 10) || ledger.SavedAt.IsZero() || ledger.SavedAt.Location() != time.UTC || ledger.Notifications == nil || len(ledger.Notifications) > 1024 {
		return errors.New("invalid notification ledger header")
	}
	state := &notificationStoreState{id: marker.StoreID, generation: latest.number, savedAt: ledger.SavedAt, guards: map[string]notificationGuard{}}
	previous := ""
	nonterminal := 0
	for _, r := range ledger.Notifications {
		n, err := r.Domain()
		if err != nil {
			return err
		}
		if r.ID <= previous {
			return errors.New("notification ledger is not strictly sorted")
		}
		previous = r.ID
		if !n.Terminal() {
			nonterminal++
		}
		state.records = append(state.records, n)
	}
	if nonterminal > 128 {
		return errors.New("notification ledger exceeds admitted work capacity")
	}
	s.notifications = state
	for _, g := range gens[:len(gens)-1] {
		if err := s.files.Remove(s.notificationPath("ledger", g.name)); err != nil {
			return err
		}
	}
	if err := s.finishNotificationCompactionLocked(); err != nil {
		return err
	}
	guards, err := s.files.ReadDir(s.notificationPath("guards"))
	if err != nil {
		return err
	}
	if len(guards) > 4096 {
		return errors.New("notification guard capacity exceeded")
	}
	for _, e := range guards {
		data, err := s.readNotificationFile(s.notificationPath("guards", e.Name()), 1024)
		if err != nil {
			return err
		}
		var g notificationGuard
		if err := notificationcodec.StrictDecode(data, &g, "version", "store_id", "notification_id", "ordinal", "attempt_id", "binding_hash", "reserved_generation", "created_at", "expires_at"); err != nil {
			return err
		}
		generation, err := strconv.ParseUint(g.ReservedGeneration, 10, 64)
		if err != nil || generation == 0 || strconv.FormatUint(generation, 10) != g.ReservedGeneration || generation > state.generation || g.Version != 1 || g.StoreID != state.id || !domain.ValidNotificationID(g.NotificationID) || g.Ordinal < 0 || g.Ordinal > 3 || notificationGuardName(g.NotificationID, g.Ordinal) != e.Name() {
			return errors.New("invalid notification guard")
		}
		state.guards[e.Name()] = g
	}
	expected := 0
	for _, n := range state.records {
		var previousGeneration uint64
		for ordinal := 0; ordinal <= len(n.Attempts); ordinal++ {
			expected++
			g, ok := state.guards[notificationGuardName(string(n.Event.ID), ordinal)]
			if !ok || g.BindingHash != n.BindingHash || !g.CreatedAt.Equal(n.CreatedAt) || !g.ExpiresAt.Equal(n.ExpiresAt) || (ordinal == 0 && g.AttemptID != nil) || (ordinal > 0 && (g.AttemptID == nil || *g.AttemptID != n.Attempts[ordinal-1].ID)) {
				return errors.New("notification admission or reservation guard mismatch")
			}
			generation, _ := strconv.ParseUint(g.ReservedGeneration, 10, 64)
			if generation <= previousGeneration {
				return errors.New("notification reservation generations do not increase")
			}
			previousGeneration = generation
		}
	}
	if expected != len(state.guards) {
		return errors.New("orphan or advanced notification guard")
	}
	return nil
}

func notificationGuardName(id string, ordinal int) string {
	return fmt.Sprintf("%s.%d.json", id, ordinal)
}

func (s *Store) notificationGenerations() ([]runtimeGeneration, error) {
	entries, err := s.files.ReadDir(s.notificationPath("ledger"))
	if err != nil {
		return nil, err
	}
	result := make([]runtimeGeneration, 0, len(entries))
	for _, entry := range entries {
		number, valid := parseGenerationName(entry.Name())
		if !valid {
			return nil, errors.New("invalid notification ledger filename")
		}
		info, err := s.files.Lstat(s.notificationPath("ledger", entry.Name()))
		if err != nil {
			return nil, err
		}
		if !isRegularFile(info) || info.Size() > 134217728 {
			return nil, errors.New("notification ledger must be a bounded regular file")
		}
		result = append(result, runtimeGeneration{number: number, name: entry.Name()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].number < result[j].number })
	return result, nil
}

func (s *Store) CommitNotifications(records []domain.Notification, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.notifications == nil {
		return errors.New("notification store is unavailable")
	}
	if s.notifications.blocked != nil {
		return s.notifications.blocked
	}
	if err := s.commitNotificationsLocked(records, now); err != nil {
		s.notifications.blocked = err
		return err
	}
	return nil
}

func (s *Store) commitNotificationsLocked(records []domain.Notification, now time.Time) error {
	state := s.notifications
	if state.generation == ^uint64(0) || len(records) > 1024 {
		return errors.New("notification generation or retention capacity exhausted")
	}
	generation := state.generation + 1
	ledger := notificationLedger{Version: 1, StoreID: state.id, Generation: strconv.FormatUint(generation, 10), SavedAt: now, Notifications: []notificationcodec.Record{}}
	sorted := append([]domain.Notification{}, records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Event.ID < sorted[j].Event.ID })
	old := map[domain.DeliveryID]domain.Notification{}
	for _, n := range state.records {
		old[n.Event.ID] = n
	}
	wanted := map[string]bool{}
	for i, n := range sorted {
		if err := notificationcodec.Validate(n); err != nil {
			return err
		}
		if i > 0 && sorted[i-1].Event.ID == n.Event.ID {
			return errors.New("duplicate notification identity")
		}
		if before, ok := old[n.Event.ID]; ok {
			if before.BindingHash != n.BindingHash || !before.CreatedAt.Equal(n.CreatedAt) || !before.ExpiresAt.Equal(n.ExpiresAt) || len(n.Attempts) < len(before.Attempts) {
				return errors.New("notification binding changed or attempt ordinal rolled back")
			}
			if len(n.Attempts) > len(before.Attempts)+1 {
				return errors.New("notification reservation skipped an ordinal")
			}
			for j, a := range before.Attempts {
				if n.Attempts[j].ID != a.ID || !n.Attempts[j].ReservedAt.Equal(a.ReservedAt) || (a.Status != "in_progress" && a.Status != n.Attempts[j].Status && !(a.Status == "unknown" && n.Attempts[j].Status == "accepted")) {
					return errors.New("notification attempt identity or acceptance changed")
				}
			}
		} else if len(n.Attempts) != 0 {
			return errors.New("notification admission must commit before reservation")
		}
		ledger.Notifications = append(ledger.Notifications, notificationcodec.FromDomain(n))
		for ordinal := 0; ordinal <= len(n.Attempts); ordinal++ {
			name := notificationGuardName(string(n.Event.ID), ordinal)
			wanted[name] = true
			if _, exists := state.guards[name]; exists {
				continue
			}
			var attempt *string
			if ordinal > 0 {
				id := n.Attempts[ordinal-1].ID
				attempt = &id
			}
			g := notificationGuard{1, state.id, string(n.Event.ID), ordinal, attempt, n.BindingHash, ledger.Generation, n.CreatedAt, n.ExpiresAt}
			if err := s.publishNotification(s.notificationPath("guards", name), g); err != nil {
				return err
			}
			state.guards[name] = g
		}
	}
	var removed []string
	for name := range state.guards {
		if !wanted[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	for _, n := range state.records {
		if _, exists := wanted[notificationGuardName(string(n.Event.ID), 0)]; !exists && (now.Before(n.ExpiresAt) || len(n.Attempts) > 0 && n.Attempts[len(n.Attempts)-1].Status == "in_progress") {
			return errors.New("unexpired notification cannot be removed")
		}
	}
	if len(removed) > 0 {
		if err := s.publishNotification(s.notificationPath("compaction.json"), notificationCompaction{1, state.id, ledger.Generation, removed}); err != nil {
			return err
		}
	}
	data, err := notificationcodec.Marshal(ledger)
	if err != nil || len(data) > 134217728 {
		return errors.New("notification snapshot exceeds bound")
	}
	if err := s.publishNotification(s.notificationPath("ledger", generationName(generation)), ledger); err != nil {
		return err
	}
	state.records = make([]domain.Notification, len(sorted))
	for i, n := range sorted {
		state.records[i] = n.Clone()
	}
	state.savedAt = now
	state.generation = generation
	gens, err := s.notificationGenerations()
	if err != nil {
		return err
	}
	for _, g := range gens {
		if g.number < generation {
			if err := s.files.Remove(s.notificationPath("ledger", g.name)); err != nil {
				return err
			}
		}
	}
	if len(removed) > 0 {
		if err := s.finishNotificationCompactionLocked(); err != nil {
			return err
		}
		for _, name := range removed {
			delete(state.guards, name)
		}
	}
	return nil
}

func (s *Store) finishNotificationCompactionLocked() error {
	path := s.notificationPath("compaction.json")
	_, err := s.files.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := s.readNotificationFile(path, 1048576)
	if err != nil {
		return err
	}
	var receipt notificationCompaction
	if err := notificationcodec.StrictDecode(data, &receipt, "version", "store_id", "generation", "guards"); err != nil {
		return err
	}
	generation, err := strconv.ParseUint(receipt.Generation, 10, 64)
	if err != nil || generation == 0 || strconv.FormatUint(generation, 10) != receipt.Generation || receipt.Version != 1 || receipt.StoreID != s.notifications.id || len(receipt.Guards) > 4096 {
		return errors.New("invalid compaction receipt")
	}
	seen := map[string]bool{}
	for _, name := range receipt.Guards {
		parts := strings.Split(name, ".")
		if len(parts) != 3 || !domain.ValidNotificationID(parts[0]) || len(parts[1]) != 1 || parts[1] < "0" || parts[1] > "3" || parts[2] != "json" || seen[name] {
			return errors.New("invalid compaction guard name")
		}
		seen[name] = true
	}
	if generation <= s.notifications.generation {
		for _, n := range s.notifications.records {
			for i := 0; i <= len(n.Attempts); i++ {
				if seen[notificationGuardName(string(n.Event.ID), i)] {
					return errors.New("compaction would delete a live guard")
				}
			}
		}
		for _, name := range receipt.Guards {
			p := s.notificationPath("guards", name)
			info, err := s.files.Lstat(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !isRegularFile(info) {
				return errors.New("compaction guard is not a regular file")
			}
			if err := s.files.Remove(p); err != nil {
				return err
			}
		}
	}
	return s.files.Remove(path)
}

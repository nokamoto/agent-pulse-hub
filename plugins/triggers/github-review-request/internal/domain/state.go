package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const maxStateBytes = 32_768

type Entry struct {
	IssueID    uint64
	Status     uint8
	Generation uint64
}

type Attempt struct {
	IssueID    uint64
	Generation uint64
	URL        string
}

type Tracker struct {
	host    string
	userID  uint64
	entries map[uint64]Entry
	urls    map[uint64]string
	nextGen uint64
}

func NewTracker(host string, userID uint64) *Tracker {
	return &Tracker{
		host: host, userID: userID,
		entries: make(map[uint64]Entry), urls: make(map[uint64]string),
	}
}

// RestoreTracker validates the complete plugin-owned checkpoint and its
// identity before returning any restored progress.
func RestoreTracker(state *jsonvalue.Value, host string, userID uint64) (*Tracker, error) {
	if state == nil || state.Kind() != jsonvalue.Object || !exactFields(state, "schema", "payload") {
		return nil, errors.New("checkpoint envelope fields are invalid")
	}
	schemaValue, _ := state.Get("schema")
	schema, ok := jsonvalue.ParseString(schemaValue)
	if !ok || schema != StateSchema {
		return nil, errors.New("checkpoint schema is incompatible")
	}
	payload, exists := state.Get("payload")
	if !exists || payload.Kind() != jsonvalue.Object || !exactFields(payload, "host", "user_id", "entries") {
		return nil, errors.New("checkpoint payload fields are invalid")
	}
	hostValue, _ := payload.Get("host")
	savedHost, hostOK := jsonvalue.ParseString(hostValue)
	userValue, _ := payload.Get("user_id")
	savedUser, userOK := jsonvalue.ParseString(userValue)
	if !hostOK || savedHost != host || !userOK || savedUser != strconv.FormatUint(userID, 10) {
		return nil, errors.New("checkpoint identity does not match the selected host and authenticated user")
	}
	rowsValue, _ := payload.Get("entries")
	if rowsValue.Kind() != jsonvalue.Array {
		return nil, errors.New("checkpoint entries must be an array")
	}
	rows := rowsValue.Items()
	if len(rows) > MaxMatches {
		return nil, errors.New("checkpoint exceeds the supported entry capacity")
	}
	tracker := NewTracker(host, userID)
	var previous uint64
	for index, row := range rows {
		if row.Kind() != jsonvalue.Array {
			return nil, errors.New("checkpoint entry must be a two-item array")
		}
		items := row.Items()
		if len(items) != 2 {
			return nil, errors.New("checkpoint entry must be a two-item array")
		}
		idText, idOK := jsonvalue.ParseString(items[0])
		issueID, validID := ParsePositiveID(idText)
		statusText, statusOK := items[1].Text()
		if items[1].Kind() != jsonvalue.Number || !statusOK || (statusText != "0" && statusText != "1" && statusText != "2") {
			return nil, errors.New("checkpoint status must be integer 0, 1, or 2")
		}
		if !idOK || !validID || (index > 0 && issueID <= previous) {
			return nil, errors.New("checkpoint IDs must be canonical, unique, and sorted numerically")
		}
		previous = issueID
		tracker.nextGen++
		tracker.entries[issueID] = Entry{IssueID: issueID, Status: uint8(statusText[0] - '0'), Generation: tracker.nextGen}
	}
	if len(state.Marshal()) > maxStateBytes {
		return nil, errors.New("checkpoint exceeds the supported encoded-size limit")
	}
	return tracker, nil
}

func exactFields(value *jsonvalue.Value, fields ...string) bool {
	if value == nil || value.Kind() != jsonvalue.Object {
		return false
	}
	got := value.Fields()
	if len(got) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, ok := got[field]; !ok {
			return false
		}
	}
	return true
}

// Reconcile commits a complete observation. The caller must never call this
// for an incomplete retrieval.
func (t *Tracker) Reconcile(matches map[uint64]string) error {
	if len(matches) > MaxMatches {
		return errors.New("observation exceeds the supported entry capacity")
	}
	for id, rawURL := range matches {
		if id == 0 || !ValidatePullURL(rawURL, t.host) {
			return fmt.Errorf("observation contains an invalid pull request record")
		}
	}
	for id := range t.entries {
		if _, present := matches[id]; !present {
			delete(t.entries, id)
		}
	}
	for id := range matches {
		if _, exists := t.entries[id]; exists {
			continue
		}
		if t.nextGen == math.MaxUint64 {
			return errors.New("occurrence generation exhausted")
		}
		t.nextGen++
		t.entries[id] = Entry{IssueID: id, Status: 0, Generation: t.nextGen}
	}
	t.urls = make(map[uint64]string, len(matches))
	for id, rawURL := range matches {
		t.urls[id] = rawURL
	}
	return nil
}

// NextAttempt marks the lowest numbered unsent occurrence attempted before
// returning it to the caller for emission.
func (t *Tracker) NextAttempt() (Attempt, bool) {
	ids := t.sortedIDs()
	for _, id := range ids {
		entry := t.entries[id]
		if entry.Status != 0 {
			continue
		}
		rawURL, present := t.urls[id]
		if !present {
			continue
		}
		entry.Status = 1
		t.entries[id] = entry
		return Attempt{IssueID: id, Generation: entry.Generation, URL: rawURL}, true
	}
	return Attempt{}, false
}

// ApplyNotice records only accepted delivery, and only for the still-current
// occurrence. Every terminal outcome releases the caller's outstanding slot.
func (t *Tracker) ApplyNotice(issueID, generation uint64, status string) {
	entry, exists := t.entries[issueID]
	if !exists || entry.Generation != generation || status != "accepted" {
		return
	}
	entry.Status = 2
	t.entries[issueID] = entry
}

func (t *Tracker) Checkpoint() (*jsonvalue.Value, error) {
	if len(t.entries) > MaxMatches {
		return nil, errors.New("checkpoint exceeds the supported entry capacity")
	}
	rows := make([]*jsonvalue.Value, 0, len(t.entries))
	for _, id := range t.sortedIDs() {
		entry := t.entries[id]
		rows = append(rows, jsonvalue.NewArray(
			jsonvalue.NewString(strconv.FormatUint(id, 10)),
			jsonvalue.NewNumber(strconv.Itoa(int(entry.Status))),
		))
	}
	payload := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"host":    jsonvalue.NewString(t.host),
		"user_id": jsonvalue.NewString(strconv.FormatUint(t.userID, 10)),
		"entries": jsonvalue.NewArray(rows...),
	})
	state := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"schema":  jsonvalue.NewString(StateSchema),
		"payload": payload,
	})
	if len(state.Marshal()) > maxStateBytes {
		return nil, errors.New("checkpoint exceeds the supported encoded-size limit")
	}
	return state, nil
}

func (t *Tracker) sortedIDs() []uint64 {
	ids := make([]uint64, 0, len(t.entries))
	for id := range t.entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

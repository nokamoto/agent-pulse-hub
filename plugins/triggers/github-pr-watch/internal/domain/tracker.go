package domain

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type Change struct {
	Signal     *SignalChange `json:"signal,omitempty"`
	Sequence   uint64        `json:"sequence"`
	Kind       string        `json:"kind"`
	At         string        `json:"at"`
	Item       Item          `json:"item"`
	Digest     string        `json:"digest"`
	BodyStatus string        `json:"body_status"`
	Omission   string        `json:"omission,omitempty"`
}
type Position struct {
	Value    string `json:"value"`
	Sequence uint64 `json:"sequence"`
}
type Saved struct {
	Scope        WatchConfig   `json:"scope"`
	Nonce        string        `json:"nonce"`
	Sequence     uint64        `json:"sequence"`
	Baseline     bool          `json:"baseline"`
	Fingerprints []Fingerprint `json:"fingerprints"`
	Journal      []Change      `json:"journal"`
	Positions    []Position    `json:"positions"`
	Aggregate    Aggregate     `json:"aggregate"`
	Metadata     []Item        `json:"metadata"`
	StartedAt    string        `json:"started_at"`
	EndedAt      string        `json:"ended_at"`
	Confirmed    uint64        `json:"confirmed"`
	Attempted    uint64        `json:"attempted"`
	Comparison   *uint64       `json:"comparison,omitempty"`
}
type Tracker struct {
	SignalProfile     bool
	Saved             Saved
	current           *Observation
	cursor            uint64
	prepared          *uint64
	ObservationBudget uint64
	HistoryBudget     uint64
	RefreshFailure    string
	Observations      uint64
	previous          map[uint64]Item
	legacyItems       map[string]bool
	legacyChanges     map[uint64]bool
	historyBytes      uint64
}

func NewTracker(scope WatchConfig, nonce []byte) (*Tracker, error) {
	if len(nonce) != 16 {
		return nil, errors.New("invalid nonce")
	}
	return &Tracker{Saved: Saved{Scope: scope, Nonce: hex.EncodeToString(nonce), Fingerprints: []Fingerprint{}, Journal: []Change{}, Positions: []Position{}, Metadata: []Item{}}, ObservationBudget: DefaultByteBudget, HistoryBudget: DefaultByteBudget}, nil
}

func Restore(scope WatchConfig, envelope *jsonvalue.Value) (*Tracker, error) {
	if envelope == nil || envelope.Kind() != jsonvalue.Object || len(envelope.Fields()) != 2 {
		return nil, errors.New("invalid restored state")
	}
	schema, ok := envelope.Get("schema")
	if !ok {
		return nil, errors.New("invalid restored state")
	}
	name, ok := jsonvalue.ParseString(schema)
	if !ok || name != StateSchema || len(envelope.Marshal()) > 32768 {
		return nil, errors.New("invalid restored state")
	}
	payload, ok := envelope.Get("payload")
	if !ok || payload.Kind() != jsonvalue.Object {
		return nil, errors.New("invalid restored state")
	}
	for _, field := range []string{"scope", "nonce", "sequence", "baseline", "fingerprints", "journal", "positions", "aggregate", "metadata", "started_at", "ended_at", "confirmed", "attempted"} {
		if _, ok := payload.Get(field); !ok {
			return nil, errors.New("invalid restored state")
		}
	}
	for _, field := range []string{"fingerprints", "journal", "positions", "metadata"} {
		value, _ := payload.Get(field)
		if value.Kind() != jsonvalue.Array {
			return nil, errors.New("invalid restored state")
		}
	}
	var saved Saved
	decoder := json.NewDecoder(strings.NewReader(string(payload.Marshal())))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&saved) != nil || saved.Scope != scope {
		return nil, errors.New("invalid restored state")
	}
	nonce, err := hex.DecodeString(saved.Nonce)
	if err != nil || len(nonce) != 16 || saved.Confirmed > saved.Sequence || saved.Attempted > saved.Sequence || (saved.Confirmed > 1 && saved.Confirmed > saved.Attempted) {
		return nil, errors.New("invalid restored state")
	}
	if saved.Baseline {
		start, startErr := time.Parse(time.RFC3339Nano, saved.StartedAt)
		end, endErr := time.Parse(time.RFC3339Nano, saved.EndedAt)
		if saved.Sequence == 0 || saved.Confirmed == 0 || saved.Aggregate.ID == "" || saved.Aggregate.URL == "" || saved.Aggregate.Head == "" || (saved.Aggregate.State != "OPEN" && saved.Aggregate.State != "CLOSED" && saved.Aggregate.State != "MERGED") || startErr != nil || endErr != nil || end.Before(start) {
			return nil, errors.New("invalid restored state")
		}
	}
	t := &Tracker{Saved: saved, cursor: saved.Confirmed}
	if err := t.validateSignalRecords(); err != nil {
		return nil, errors.New("invalid restored state")
	}
	if saved.Comparison != nil {
		if *saved.Comparison > saved.Sequence {
			return nil, errors.New("invalid restored state")
		}
	}
	previous := uint64(0)
	for _, change := range saved.Journal {
		if change.Sequence <= previous || (previous > 0 && change.Sequence != previous+1) || change.Sequence > saved.Sequence || change.Item.ID == "" || !validDigest(change.Digest) {
			return nil, errors.New("invalid restored state")
		}
		previous = change.Sequence
	}
	if saved.Sequence > 1 && (len(saved.Journal) == 0 || previous != saved.Sequence) {
		return nil, errors.New("invalid restored state")
	}
	needed := saved.Confirmed
	for _, position := range saved.Positions {
		if position.Sequence < needed {
			needed = position.Sequence
		}
	}
	if needed < saved.Sequence && len(saved.Journal) > 0 && saved.Journal[0].Sequence > needed+1 {
		return nil, errors.New("invalid restored state")
	}
	seen := map[string]bool{}
	for _, f := range saved.Fingerprints {
		key := f.Kind + ":" + f.ID
		if f.ID == "" || !validDigest(f.Digest) || seen[key] {
			return nil, errors.New("invalid restored state")
		}
		seen[key] = true
	}
	if len(saved.Metadata) != len(saved.Fingerprints) {
		return nil, errors.New("invalid restored state")
	}
	metadataSeen := map[string]bool{}
	for _, item := range saved.Metadata {
		key := item.Kind + ":" + item.ID
		if !seen[key] || metadataSeen[key] || item.Body != "" || !validDigest(item.BodyDigest) {
			return nil, errors.New("invalid restored state")
		}
		metadataSeen[key] = true
	}
	for _, position := range saved.Positions {
		if position.Sequence < 2 || position.Value != t.position(position.Sequence) || position.Sequence > saved.Attempted {
			return nil, errors.New("invalid restored state")
		}
	}
	if !saved.Baseline && (saved.Sequence != 0 || len(saved.Fingerprints) != 0 || len(saved.Journal) != 0 || len(saved.Positions) != 0) {
		return nil, errors.New("invalid restored state")
	}
	if err := t.legacyCapacity(); err != nil {
		return nil, errors.New("invalid restored state")
	}
	t.ObservationBudget = DefaultByteBudget
	t.HistoryBudget = DefaultByteBudget
	return t, nil
}

func (t *Tracker) position(sequence uint64) string {
	nonce, _ := hex.DecodeString(t.Saved.Nonce)
	raw := make([]byte, 25)
	raw[0] = 1
	copy(raw[1:17], nonce)
	binary.BigEndian.PutUint64(raw[17:], sequence)
	return "gpw1." + base64.RawURLEncoding.EncodeToString(raw)
}

func (t *Tracker) ValidatePosition(value string) (uint64, error) {
	for _, p := range t.Saved.Positions {
		if p.Value == value {
			return p.Sequence, nil
		}
	}
	if t.Saved.Comparison != nil && value == t.position(*t.Saved.Comparison) {
		return *t.Saved.Comparison, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "gpw1."))
	if err != nil || !strings.HasPrefix(value, "gpw1.") || len(raw) != 25 || raw[0] != 1 {
		return 0, errors.New("position_invalid")
	}
	if hex.EncodeToString(raw[1:17]) != t.Saved.Nonce {
		return 0, errors.New("position_foreign")
	}
	return 0, errors.New("continuity_expired: inspect the PR and explicitly start a new baseline if lost continuity is acceptable")
}

func (t *Tracker) Resume(sequence uint64) {
	t.cursor = sequence
	t.Saved.Comparison = &sequence
	t.prepared = nil
}
func (t *Tracker) Prepare(sequence uint64) { t.prepared = &sequence }
func (t *Tracker) CancelPreparation()      { t.prepared = nil }

func (t *Tracker) Reconcile(o Observation) error {
	var signalState SignalState
	var signalChanges []SignalChange
	if t.SignalProfile {
		if o.Signals == nil || o.Signals.Pair.PRID != o.PR.ID || o.Signals.Pair.Head != o.PR.Head {
			return errors.New("incomplete_retrieval")
		}
		var previous *SignalState
		if t.Saved.Baseline {
			state, err := signalStateFromItems(t.Saved.Metadata)
			if err != nil {
				return err
			}
			previous = &state
		}
		var err error
		signalState, signalChanges, err = CompareSignals(previous, *o.Signals)
		if err != nil {
			return err
		}
		o.Items = append(append([]Item{}, o.Items...), signalItems(signalState, o.PR.URL)...)
	}
	if err := observationCapacity(o, t.observationBudget()); err != nil {
		return err
	}
	fs, err := fingerprints(o)
	if err != nil {
		return err
	}
	candidate := *t
	if candidate.Observations == math.MaxUint64 {
		return errors.New("sequence_exhausted")
	}
	candidate.Observations++
	candidate.Saved = t.Saved
	candidate.Saved.Journal = append([]Change{}, t.Saved.Journal...)
	candidate.previous = make(map[uint64]Item, len(t.previous))
	for sequence, item := range t.previous {
		candidate.previous[sequence] = item
	}
	candidate.legacyChanges = make(map[uint64]bool, len(t.legacyChanges))
	for sequence, legacy := range t.legacyChanges {
		candidate.legacyChanges[sequence] = legacy
	}
	candidate.legacyItems = nil
	candidate.pruneAccepted()
	journalStart := len(candidate.Saved.Journal)
	candidate.historyBytes = 2
	for i, c := range candidate.Saved.Journal {
		raw, _ := compact(candidate.retainedChange(c))
		n := uint64(len(raw))
		if i > 0 {
			n++
		}
		if candidate.historyBytes > candidate.historyBudget() || n > candidate.historyBudget()-candidate.historyBytes {
			return &ResourceError{"history_resource_exhausted", "--history-byte-budget", candidate.historyBudget(), saturatedAdd(candidate.historyBytes, n)}
		}
		candidate.historyBytes += n
	}
	if !candidate.Saved.Baseline {
		candidate.Saved.Sequence = 1
		candidate.Saved.Confirmed = 1
		candidate.cursor = 1
		candidate.Saved.Baseline = true
	} else {
		old := map[string]Fingerprint{}
		current := map[string]Fingerprint{}
		items := map[string]Item{}
		for _, f := range t.Saved.Fingerprints {
			old[f.Kind+":"+f.ID] = f
		}
		for _, f := range fs {
			current[f.Kind+":"+f.ID] = f
		}
		for _, item := range o.Items {
			items[item.Kind+":"+item.ID] = item
		}
		keys := make([]string, 0, len(old)+len(current))
		seen := map[string]bool{}
		for key := range old {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range current {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sortStrings(keys)
		for _, key := range keys {
			before, was := old[key]
			after, is := current[key]
			if isSignalKind(before.Kind) || isSignalKind(after.Kind) {
				continue
			}
			if was && is && before.Digest == after.Digest {
				continue
			}
			kind := "edited"
			item := items[key]
			d := bodyDigest(item.Body)
			if !was {
				kind = "created"
			}
			if !is {
				kind = "disappeared"
				for _, previous := range t.Saved.Metadata {
					if previous.Kind == before.Kind && previous.ID == before.ID {
						item = previous
						d = previous.BodyDigest
						item.BodyDigest = ""
						break
					}
				}
			} else if was {
				for _, previous := range t.Saved.Metadata {
					if previous.Kind == item.Kind && previous.ID == item.ID && (previous.State != item.State || digest(previous.Resolved) != digest(item.Resolved) || digest(previous.Outdated) != digest(item.Outdated) || digest(previous.ResolvedBy) != digest(item.ResolvedBy)) {
						kind = "state_changed"
						break
					}
				}
			}
			var previousItem *Item
			if was {
				for _, previous := range t.Saved.Metadata {
					if previous.Kind == before.Kind && previous.ID == before.ID {
						previousItem = &previous
						break
					}
				}
			}
			if !is && item.Kind == "" && t.legacyItems[key] {
				candidate.legacyChanges[candidate.Saved.Sequence+1] = true
			}
			if err := candidate.appendChange(kind, item, d, o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), previousItem); err != nil {
				return err
			}
		}
		if t.Saved.Aggregate.Head != o.PR.Head {
			previousItem := Item{ID: t.Saved.Aggregate.ID, Kind: "pull_request", URL: t.Saved.Aggregate.URL, State: t.Saved.Aggregate.Head}
			if err := candidate.appendChange("head_changed", Item{ID: o.PR.ID, Kind: "pull_request", URL: o.PR.URL, State: o.PR.Head}, digest(o.PR.Head), o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), &previousItem); err != nil {
				return err
			}
		}
		if t.Saved.Aggregate.State != o.PR.State || digest(t.Saved.Aggregate.MergedAt) != digest(o.PR.MergedAt) {
			kind := "lifecycle_changed"
			if t.Saved.Aggregate.State == "CLOSED" && o.PR.State == "OPEN" {
				kind = "reopened"
			}
			if o.PR.State == "MERGED" {
				kind = "merged"
			}
			previousItem := Item{ID: t.Saved.Aggregate.ID, Kind: "pull_request", URL: t.Saved.Aggregate.URL, State: t.Saved.Aggregate.State}
			if err := candidate.appendChange(kind, Item{ID: o.PR.ID, Kind: "pull_request", URL: o.PR.URL, State: o.PR.State}, digest(o.PR), o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), &previousItem); err != nil {
				return err
			}
		}
	}
	for i := range signalChanges {
		change := signalChanges[i]
		if change.CI != nil {
			r := retainedCI(*change.CI)
			change.CI = &r
		}
		if change.PreviousCI != nil {
			r := retainedCI(*change.PreviousCI)
			change.PreviousCI = &r
		}
		if err := candidate.appendChange("signal_episode", Item{ID: change.ID, Kind: change.Category, URL: o.PR.URL}, digest(change), o.EndedAt.Format(time.RFC3339Nano), nil); err != nil {
			return err
		}
		candidate.Saved.Journal[len(candidate.Saved.Journal)-1].Signal = &change
	}
	if t.Saved.Baseline {
		candidate.orderNewChanges(journalStart, t.Saved.Sequence)
	}
	if t.SignalProfile {
		for i := journalStart; i < len(candidate.Saved.Journal); i++ {
			change := &candidate.Saved.Journal[i]
			if change.Signal != nil {
				signalState.AssignEpisode(change.Signal, change.Sequence)
				change.Digest = digest(change.Signal)
			}
		}
		discussion := []Item{}
		for _, item := range o.Items {
			if !isSignalKind(item.Kind) {
				discussion = append(discussion, item)
			}
		}
		o.Items = append(discussion, signalItems(signalState, o.PR.URL)...)
		fs, err = fingerprints(o)
		if err != nil {
			return err
		}
	}
	candidate.Saved.Fingerprints = fs
	candidate.Saved.Aggregate = o.PR
	candidate.Saved.Metadata = make([]Item, 0, len(o.Items))
	for _, item := range o.Items {
		item.BodyDigest = bodyDigest(item.Body)
		item.Body = ""
		candidate.Saved.Metadata = append(candidate.Saved.Metadata, item)
	}
	candidate.sortMetadata()
	candidate.Saved.StartedAt = o.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	candidate.Saved.EndedAt = o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	candidate.current = &o
	candidate.RefreshFailure = ""
	candidate.pruneAccepted()
	for i := range candidate.Saved.Journal {
		c := &candidate.Saved.Journal[i]
		raw, _ := compact(candidate.retainedChange(*c))
		if len(raw)+32 > 32768 && c.Item.Body != "" {
			c.Item.Body = ""
			c.BodyStatus = "shortened"
			c.Omission = "body omitted to retain identity and relationships"
		}
	}
	if err := candidate.capacity(); err != nil {
		return err
	}
	*t = candidate
	return nil
}

func sortStrings(s []string) {
	sort.Strings(s)
}

func (t *Tracker) orderNewChanges(start int, base uint64) {
	type entry struct {
		change   Change
		previous *Item
		legacy   bool
	}
	entries := make([]entry, 0, len(t.Saved.Journal)-start)
	for _, c := range t.Saved.Journal[start:] {
		e := entry{change: c, legacy: t.legacyChanges[c.Sequence]}
		if old, ok := t.previous[c.Sequence]; ok {
			e.previous = &old
		}
		entries = append(entries, e)
		delete(t.previous, c.Sequence)
		delete(t.legacyChanges, c.Sequence)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].change, entries[j].change
		if a.Item.Kind != b.Item.Kind {
			return a.Item.Kind < b.Item.Kind
		}
		if a.Item.ID != b.Item.ID {
			return a.Item.ID < b.Item.ID
		}
		return a.Kind < b.Kind
	})
	for i, e := range entries {
		e.change.Sequence = base + uint64(i) + 1
		t.Saved.Journal[start+i] = e.change
		if e.legacy {
			t.legacyChanges[e.change.Sequence] = true
		}
		if e.previous != nil {
			t.previous[e.change.Sequence] = *e.previous
		}
	}
}

func excerpt(body string, limit int) string {
	if len(body) <= limit {
		return body
	}
	for limit > 0 && !utf8.RuneStart(body[limit]) {
		limit--
	}
	return body[:limit]
}

func (t *Tracker) appendChange(kind string, item Item, d, at string, previous *Item) error {
	if t.Saved.Sequence == math.MaxUint64 {
		return errors.New("sequence_exhausted")
	}
	full := item.Body
	item.Body = excerpt(full, 512)
	c := Change{Sequence: t.Saved.Sequence + 1, Kind: kind, At: at, Item: item, Digest: d, BodyStatus: "complete"}
	if full == "" {
		c.BodyStatus = "empty"
	}
	if item.Body != full {
		c.BodyStatus = "shortened"
		c.Omission = "historical body excerpt; full acquired content digest retained"
	}
	if kind == "disappeared" {
		c.BodyStatus = "unavailable"
		c.Omission = "disappeared content is no longer retrievable; inspect related PR link"
	}
	if previous != nil {
		t.previous[c.Sequence] = *previous
	}
	raw, _ := compact(t.retainedChange(c))
	if len(raw)+32 > 32768 {
		c.Item.Body = ""
		c.BodyStatus = "shortened"
		c.Omission = "body omitted to retain identity and relationships"
		raw, _ = compact(t.retainedChange(c))
	}
	if len(raw)+32 > 32768 {
		return errors.New("item_metadata_exhausted")
	}
	n := uint64(len(raw))
	if len(t.Saved.Journal) > 0 {
		n++
	}
	if t.historyBytes > t.historyBudget() || n > t.historyBudget()-t.historyBytes {
		return &ResourceError{"history_resource_exhausted", "--history-byte-budget", t.historyBudget(), saturatedAdd(t.historyBytes, n)}
	}
	t.historyBytes += n
	t.Saved.Sequence = c.Sequence
	t.Saved.Journal = append(t.Saved.Journal, c)
	return nil
}

func (t *Tracker) Notice(sequence uint64, status string) {
	if status == "accepted" && sequence > t.Saved.Confirmed {
		t.Saved.Confirmed = sequence
		if t.Saved.Comparison == nil {
			t.cursor = sequence
		}
	}
}

func (t *Tracker) pruneAccepted() {
	through := t.Saved.Confirmed
	if t.Saved.Comparison != nil && *t.Saved.Comparison < through {
		through = *t.Saved.Comparison
	}
	for _, position := range t.Saved.Positions {
		if position.Sequence < through {
			through = position.Sequence
		}
	}
	if t.prepared != nil && *t.prepared < through {
		through = *t.prepared
	}
	journal := []Change{}
	removed := uint64(0)
	for _, change := range t.Saved.Journal {
		if change.Sequence <= through {
			removed = change.Sequence
			delete(t.previous, change.Sequence)
			delete(t.legacyChanges, change.Sequence)
		} else {
			journal = append(journal, change)
		}
	}
	if removed == 0 {
		return
	}
	t.Saved.Journal = journal
}

func changeView(change Change) map[string]any {
	raw, _ := json.Marshal(change)
	value := map[string]any{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	item := value["item"].(map[string]any)
	if change.Item.Kind == "review" || change.Item.Kind == "conversation" || change.Item.Kind == "review_comment" {
		unavailable := map[string]string{}
		for _, key := range []string{"author", "created_at", "updated_at", "submitted_at", "commit", "original_commit", "review", "reply_to", "thread"} {
			if field, ok := item[key]; !ok || field == nil || field == "" {
				item[key] = nil
				unavailable[key] = "not available or not applicable"
			}
		}
		item["unavailable"] = unavailable
	}
	if change.Item.Kind == "thread" {
		item["url_kind"] = "first_visible_comment_related_link"
		if change.Item.URL == "" {
			item["url"] = nil
			item["unavailable"] = map[string]string{"url": "no visible thread comment"}
		}
	}
	return value
}

func (t *Tracker) legacyCapacity() error {
	if len(t.Saved.Fingerprints) > 32 || len(t.Saved.Journal) > 32 || len(t.Saved.Positions) > 4 {
		return errors.New("coverage_capacity_exceeded")
	}
	for _, region := range []struct {
		value any
		limit int
	}{{t.Saved.Fingerprints, 6144}, {t.Saved.Journal, 20480}, {t.Saved.Positions, 1024}} {
		raw, _ := json.Marshal(region.value)
		if len(raw) > region.limit {
			return errors.New("coverage_capacity_exceeded")
		}
	}
	for _, record := range t.Saved.Journal {
		raw, _ := json.Marshal(record)
		if len(raw) > 640 {
			return errors.New("coverage_capacity_exceeded")
		}
	}
	for _, record := range t.Saved.Fingerprints {
		raw, _ := json.Marshal(record)
		if len(raw) > 192 {
			return errors.New("coverage_capacity_exceeded")
		}
	}
	metadata := t.Saved
	metadata.Fingerprints = nil
	metadata.Journal = nil
	metadata.Positions = nil
	raw, _ := json.Marshal(metadata)
	if len(raw) > 4096 {
		return errors.New("coverage_capacity_exceeded")
	}
	envelope := map[string]any{"schema": StateSchema, "payload": t.Saved}
	raw, _ = json.Marshal(envelope)
	if len(raw) > 32768 {
		return errors.New("coverage_capacity_exceeded")
	}
	return nil
}

func validDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}

func (t *Tracker) Checkpoint() (*jsonvalue.Value, error) {
	if err := t.legacyCapacity(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"schema": StateSchema, "payload": t.Saved})
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return jsonvalue.Parse(raw, 32768)
}

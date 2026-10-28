package domain

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const StreamStateSchema = "github-pr-watch-v2"

type streamHeader struct {
	Kind                string      `json:"kind"`
	Version             int         `json:"version"`
	Scope               WatchConfig `json:"scope"`
	Nonce               string      `json:"nonce"`
	Sequence            uint64      `json:"sequence"`
	ObservationSequence uint64      `json:"observation_sequence"`
	Baseline            bool        `json:"baseline"`
	Aggregate           Aggregate   `json:"aggregate"`
	StartedAt           string      `json:"started_at"`
	EndedAt             string      `json:"ended_at"`
	Confirmed           uint64      `json:"confirmed"`
	Attempted           uint64      `json:"attempted"`
	Comparison          *uint64     `json:"comparison"`
	Positions           []Position  `json:"positions"`
	ItemCount           uint64      `json:"item_count"`
	ChangeCount         uint64      `json:"change_count"`
	FirstChange         uint64      `json:"first_change_sequence"`
	LastChange          uint64      `json:"last_change_sequence"`
}

type streamItem struct {
	Fingerprint Fingerprint `json:"fingerprint"`
	Item        Item        `json:"item"`
	Legacy      bool        `json:"legacy_origin,omitempty"`
}

// Extra v2 fields deliberately do not alter the frozen v1 Change decoder.
type streamChange struct {
	Change
	Previous *Item `json:"previous,omitempty"`
	Legacy   bool  `json:"legacy_origin,omitempty"`
}

func (t *Tracker) retainedChange(c Change) streamChange {
	result := streamChange{Change: c}
	result.Legacy = t.legacyChanges[c.Sequence]
	if item, ok := t.previous[c.Sequence]; ok {
		result.Previous = &item
	}
	return result
}

func compact(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func (t *Tracker) observationBudget() uint64 {
	if t.ObservationBudget == 0 {
		return DefaultByteBudget
	}
	return t.ObservationBudget
}

func (t *Tracker) historyBudget() uint64 {
	if t.HistoryBudget == 0 {
		return DefaultByteBudget
	}
	return t.HistoryBudget
}

func observationCapacity(o Observation, budget uint64) error {
	// Encode records independently so the budget is checked before constructing
	// an aggregate encoding or copying any item into retained progress.
	base := o
	base.Items = []Item{}
	raw, err := compact(base)
	if err != nil {
		return err
	}
	used := uint64(len(raw))
	for i, item := range o.Items {
		raw, err = compact(item)
		if err != nil {
			return err
		}
		n := uint64(len(raw))
		if i > 0 {
			n++
		}
		if used > budget || n > budget-used {
			return &ResourceError{"observation_resource_exhausted", "--observation-byte-budget", budget, saturatedAdd(used, n)}
		}
		used += n
	}
	if used > budget {
		return &ResourceError{"observation_resource_exhausted", "--observation-byte-budget", budget, used}
	}
	return nil
}

func saturatedAdd(a, b uint64) uint64 {
	if b > math.MaxUint64-a {
		return math.MaxUint64
	}
	return a + b
}

func (t *Tracker) capacity() error {
	if err := t.savedObservationCapacity(); err != nil {
		return err
	}
	used := uint64(2)
	for i, change := range t.Saved.Journal {
		raw, err := compact(t.retainedChange(change))
		if err != nil {
			return err
		}
		n := uint64(len(raw))
		if i > 0 {
			n++
		}
		if used > t.historyBudget() || n > t.historyBudget()-used {
			return &ResourceError{"history_resource_exhausted", "--history-byte-budget", t.historyBudget(), saturatedAdd(used, n)}
		}
		used += n
		if len(raw)+32 > 32768 {
			return errors.New("item_metadata_exhausted")
		}
	}
	if used > t.historyBudget() {
		return &ResourceError{"history_resource_exhausted", "--history-byte-budget", t.historyBudget(), used}
	}
	// Validate mandatory metadata, including the bounded header, before committing
	// an observation. A checkpoint cannot discover a hidden item-count ceiling.
	_, err := t.streamObjects(false)
	return err
}

func (t *Tracker) savedObservationCapacity() error {
	budget := t.observationBudget()
	used := uint64(2)
	for i, item := range t.Saved.Metadata {
		raw, err := compact(item)
		if err != nil {
			return err
		}
		n := uint64(len(raw))
		if i > 0 {
			n++
		}
		if used > budget || n > budget-used {
			return &ResourceError{"observation_resource_exhausted", "--observation-byte-budget", budget, saturatedAdd(used, n)}
		}
		used += n
	}
	if used > budget {
		return &ResourceError{"observation_resource_exhausted", "--observation-byte-budget", budget, used}
	}
	return nil
}

// StreamCheckpoint returns one immutable logical cut. Every returned Value owns
// its bytes and cannot change when later observations or notices are applied.
func (t *Tracker) StreamCheckpoint() ([]*jsonvalue.Value, error) {
	if err := t.capacity(); err != nil {
		return nil, err
	}
	return t.streamObjects(true)
}

func (t *Tracker) streamObjects(materialize bool) ([]*jsonvalue.Value, error) {
	s := t.Saved
	h := streamHeader{Kind: "header", Version: 2, Scope: s.Scope, Nonce: s.Nonce, Sequence: s.Sequence, ObservationSequence: t.Observations, Baseline: s.Baseline, Aggregate: s.Aggregate, StartedAt: s.StartedAt, EndedAt: s.EndedAt, Confirmed: s.Confirmed, Attempted: s.Attempted, Comparison: s.Comparison, Positions: s.Positions, ItemCount: uint64(len(s.Metadata)), ChangeCount: uint64(len(s.Journal))}
	if len(s.Journal) > 0 {
		h.FirstChange = s.Journal[0].Sequence
		h.LastChange = s.Journal[len(s.Journal)-1].Sequence
	}
	header, err := compact(h)
	if err != nil || len(header) > 32768 {
		return nil, errors.New("item_metadata_exhausted")
	}
	objects := []*jsonvalue.Value{}
	appendObject := func(raw []byte) error {
		if !materialize {
			return nil
		}
		value, err := jsonvalue.Parse(raw, 32768)
		if err == nil {
			objects = append(objects, value)
		}
		return err
	}
	if err := appendObject(header); err != nil {
		return nil, err
	}
	// Metadata and fingerprints have the same canonical order in committed v2.
	items := make(map[string]Item, len(s.Metadata))
	for _, item := range s.Metadata {
		items[item.Kind+":"+item.ID] = item
	}
	pack := func(kind, field string, count int, record func(int) any) error {
		prefix := []byte(`{"kind":"` + kind + `","` + field + `":[`)
		out := append([]byte{}, prefix...)
		empty := true
		for i := 0; i < count; i++ {
			raw, err := compact(record(i))
			if err != nil {
				return err
			}
			if len(prefix)+len(raw)+2 > 32768 {
				return errors.New("item_metadata_exhausted")
			}
			extra := len(raw)
			if !empty {
				extra++
			}
			if len(out)+extra+2 > 32768 {
				if err := appendObject(append(out, ']', '}')); err != nil {
					return err
				}
				out = append([]byte{}, prefix...)
				empty = true
			}
			if !empty {
				out = append(out, ',')
			}
			out = append(out, raw...)
			empty = false
		}
		if !empty {
			return appendObject(append(out, ']', '}'))
		}
		return nil
	}
	if err := pack("items", "items", len(s.Fingerprints), func(i int) any {
		f := s.Fingerprints[i]
		return streamItem{Fingerprint: f, Item: items[f.Kind+":"+f.ID], Legacy: t.legacyItems[f.Kind+":"+f.ID]}
	}); err != nil {
		return nil, err
	}
	if err := pack("changes", "changes", len(s.Journal), func(i int) any { return t.retainedChange(s.Journal[i]) }); err != nil {
		return nil, err
	}
	return objects, nil
}

func strictDecode(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func RestoreStream(scope WatchConfig, schema string, objects []*jsonvalue.Value, observationBudget, historyBudget uint64) (*Tracker, error) {
	invalid := func() (*Tracker, error) { return nil, errors.New("invalid restored state") }
	if schema == StateSchema {
		if len(objects) != 1 {
			return invalid()
		}
		raw, err := compact(map[string]any{"schema": schema, "payload": json.RawMessage(objects[0].Marshal())})
		if err != nil {
			return invalid()
		}
		envelope, err := jsonvalue.Parse(raw, 32768)
		if err != nil {
			return invalid()
		}
		t, err := Restore(scope, envelope)
		if err != nil {
			return nil, err
		}
		t.ObservationBudget = observationBudget
		t.HistoryBudget = historyBudget
		if t.Saved.Baseline {
			t.Observations = 1
		}
		t.sortMetadata()
		t.legacyItems = map[string]bool{}
		for _, item := range t.Saved.Metadata {
			t.legacyItems[item.Kind+":"+item.ID] = true
		}
		t.legacyChanges = map[uint64]bool{}
		for _, c := range t.Saved.Journal {
			t.legacyChanges[c.Sequence] = true
		}
		if err := t.capacity(); err != nil {
			return nil, err
		}
		return t, nil
	}
	if schema != StreamStateSchema || len(objects) == 0 {
		return invalid()
	}
	var h streamHeader
	if !validHeaderShape(objects[0]) {
		return invalid()
	}
	if objects[0] == nil || objects[0].Kind() != jsonvalue.Object || len(objects[0].Marshal()) > 32768 || strictDecode(objects[0].Marshal(), &h) != nil || h.Kind != "header" || h.Version != 2 || h.Scope != scope || h.Positions == nil {
		return invalid()
	}
	s := Saved{Scope: h.Scope, Nonce: h.Nonce, Sequence: h.Sequence, Baseline: h.Baseline, Aggregate: h.Aggregate, StartedAt: h.StartedAt, EndedAt: h.EndedAt, Confirmed: h.Confirmed, Attempted: h.Attempted, Comparison: h.Comparison, Positions: h.Positions, Fingerprints: []Fingerprint{}, Metadata: []Item{}, Journal: []Change{}}
	phase := 0
	previous := map[uint64]Item{}
	legacyItems := map[string]bool{}
	legacyChanges := map[uint64]bool{}
	for _, object := range objects[1:] {
		if object == nil || object.Kind() != jsonvalue.Object || len(object.Marshal()) > 32768 {
			return invalid()
		}
		kind, _ := object.Get("kind")
		name, ok := jsonvalue.ParseString(kind)
		if !ok {
			return invalid()
		}
		switch name {
		case "items":
			if phase > 1 {
				return invalid()
			}
			phase = 1
			var section struct {
				Kind  string       `json:"kind"`
				Items []streamItem `json:"items"`
			}
			if strictDecode(object.Marshal(), &section) != nil || len(section.Items) == 0 {
				return invalid()
			}
			for _, item := range section.Items {
				s.Fingerprints = append(s.Fingerprints, item.Fingerprint)
				s.Metadata = append(s.Metadata, item.Item)
				if item.Legacy {
					legacyItems[item.Item.Kind+":"+item.Item.ID] = true
				}
			}
		case "changes":
			phase = 2
			var section struct {
				Kind    string         `json:"kind"`
				Changes []streamChange `json:"changes"`
			}
			if strictDecode(object.Marshal(), &section) != nil || len(section.Changes) == 0 {
				return invalid()
			}
			for _, change := range section.Changes {
				s.Journal = append(s.Journal, change.Change)
				if change.Legacy {
					legacyChanges[change.Sequence] = true
				}
				if change.Previous != nil {
					previous[change.Sequence] = *change.Previous
				}
			}
		default:
			return invalid()
		}
	}
	if uint64(len(s.Metadata)) != h.ItemCount || uint64(len(s.Journal)) != h.ChangeCount {
		return invalid()
	}
	if len(s.Journal) == 0 {
		if h.FirstChange != 0 || h.LastChange != 0 {
			return invalid()
		}
	} else if h.FirstChange != s.Journal[0].Sequence || h.LastChange != s.Journal[len(s.Journal)-1].Sequence {
		return invalid()
	}
	t := &Tracker{Saved: s, cursor: s.Confirmed, ObservationBudget: observationBudget, HistoryBudget: historyBudget, Observations: h.ObservationSequence, previous: previous, legacyItems: legacyItems, legacyChanges: legacyChanges}
	if err := t.validateV2(); err != nil {
		return invalid()
	}
	if err := t.capacity(); err != nil {
		return nil, err
	}
	return t, nil
}

func validHeaderShape(value *jsonvalue.Value) bool {
	if value == nil || value.Kind() != jsonvalue.Object {
		return false
	}
	for _, field := range []string{"kind", "version", "scope", "nonce", "sequence", "observation_sequence", "baseline", "aggregate", "started_at", "ended_at", "confirmed", "attempted", "comparison", "positions", "item_count", "change_count", "first_change_sequence", "last_change_sequence"} {
		v, ok := value.Get(field)
		if !ok {
			return false
		}
		kind := jsonvalue.Number
		switch field {
		case "kind", "nonce", "started_at", "ended_at":
			kind = jsonvalue.String
		case "scope", "aggregate":
			kind = jsonvalue.Object
		case "positions":
			kind = jsonvalue.Array
		case "baseline":
			kind = jsonvalue.Boolean
		case "comparison":
			if v.Kind() == jsonvalue.Null {
				continue
			}
		}
		if v.Kind() != kind {
			return false
		}
	}
	return true
}

func (t *Tracker) validateV2() error {
	s := t.Saved
	invalid := errors.New("invalid restored state")
	nonce, err := hex.DecodeString(s.Nonce)
	if err != nil || len(nonce) != 16 || s.Confirmed > s.Sequence || s.Attempted > s.Sequence || (s.Confirmed > 1 && s.Confirmed > s.Attempted) || len(s.Positions) > 4 {
		return invalid
	}
	if s.Comparison != nil && *s.Comparison > s.Sequence {
		return invalid
	}
	if !s.Baseline {
		if s.Sequence != 0 || s.Confirmed != 0 || s.Attempted != 0 || t.Observations != 0 || len(s.Fingerprints) != 0 || len(s.Journal) != 0 || len(s.Positions) != 0 {
			return invalid
		}
		return nil
	}
	start, a := time.Parse(time.RFC3339Nano, s.StartedAt)
	end, b := time.Parse(time.RFC3339Nano, s.EndedAt)
	if s.Sequence == 0 || s.Confirmed == 0 || t.Observations == 0 || s.Aggregate.ID == "" || s.Aggregate.URL == "" || s.Aggregate.Head == "" || (s.Aggregate.State != "OPEN" && s.Aggregate.State != "CLOSED" && s.Aggregate.State != "MERGED") || a != nil || b != nil || end.Before(start) {
		return invalid
	}
	previous := ""
	for i, f := range s.Fingerprints {
		key := f.Kind + ":" + f.ID
		item := s.Metadata[i]
		if f.ID == "" || (f.Kind == "" && !t.legacyItems[key]) || key <= previous || !validDigest(f.Digest) || item.Kind != f.Kind || item.ID != f.ID || item.Body != "" || !validDigest(item.BodyDigest) {
			return invalid
		}
		previous = key
	}
	needed := s.Confirmed
	if s.Comparison != nil && *s.Comparison < needed {
		needed = *s.Comparison
	}
	for _, p := range s.Positions {
		if p.Sequence < 2 || p.Sequence > s.Attempted || p.Value != t.position(p.Sequence) {
			return invalid
		}
		if p.Sequence < needed {
			needed = p.Sequence
		}
	}
	last := uint64(0)
	for _, c := range s.Journal {
		if c.Sequence == 0 || (c.Sequence < 2 && !t.legacyChanges[c.Sequence]) || (last != 0 && c.Sequence != last+1) || c.Sequence > s.Sequence || c.Item.ID == "" || (c.Item.Kind == "" && !t.legacyChanges[c.Sequence]) || !validDigest(c.Digest) || len(c.Item.Body) > 512 {
			return invalid
		}
		if _, err := time.Parse(time.RFC3339Nano, c.At); err != nil && !t.legacyChanges[c.Sequence] {
			return invalid
		}
		if !t.legacyChanges[c.Sequence] {
			switch c.BodyStatus {
			case "empty":
				if c.Item.Body != "" {
					return invalid
				}
			case "complete", "shortened", "unavailable":
			default:
				return invalid
			}
		}
		last = c.Sequence
		if previous, ok := t.previous[c.Sequence]; ok {
			if previous.ID != c.Item.ID || previous.Kind != c.Item.Kind || previous.Body != "" {
				return invalid
			}
		}
	}
	if needed < s.Sequence && (len(s.Journal) == 0 || s.Journal[0].Sequence > needed+1 || last != s.Sequence) {
		return invalid
	}
	if len(s.Journal) > 0 && last != s.Sequence {
		return invalid
	}
	return nil
}

func (t *Tracker) sortMetadata() {
	sort.Slice(t.Saved.Fingerprints, func(i, j int) bool {
		return t.Saved.Fingerprints[i].Kind+":"+t.Saved.Fingerprints[i].ID < t.Saved.Fingerprints[j].Kind+":"+t.Saved.Fingerprints[j].ID
	})
	sort.Slice(t.Saved.Metadata, func(i, j int) bool {
		return t.Saved.Metadata[i].Kind+":"+t.Saved.Metadata[i].ID < t.Saved.Metadata[j].Kind+":"+t.Saved.Metadata[j].ID
	})
}

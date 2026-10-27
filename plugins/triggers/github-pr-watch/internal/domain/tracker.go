package domain

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type Change struct {
	Sequence   uint64 `json:"sequence"`
	Kind       string `json:"kind"`
	At         string `json:"at"`
	Item       Item   `json:"item"`
	Digest     string `json:"digest"`
	BodyStatus string `json:"body_status"`
	Omission   string `json:"omission,omitempty"`
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
	Saved    Saved
	current  *Observation
	cursor   uint64
	prepared *uint64
}

func NewTracker(scope WatchConfig, nonce []byte) (*Tracker, error) {
	if len(nonce) != 16 {
		return nil, errors.New("invalid nonce")
	}
	return &Tracker{Saved: Saved{Scope: scope, Nonce: hex.EncodeToString(nonce), Fingerprints: []Fingerprint{}, Journal: []Change{}, Positions: []Position{}, Metadata: []Item{}}}, nil
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
	if err := t.capacity(); err != nil {
		return nil, errors.New("invalid restored state")
	}
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
	fs, err := fingerprints(o)
	if err != nil {
		return err
	}
	candidate := *t
	candidate.Saved = t.Saved
	candidate.Saved.Journal = append([]Change{}, t.Saved.Journal...)
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
			if err := candidate.appendChange(kind, item, d, o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
				return err
			}
		}
		if t.Saved.Aggregate.Head != o.PR.Head {
			if err := candidate.appendChange("head_changed", Item{ID: o.PR.ID, Kind: "pull_request", URL: o.PR.URL, State: o.PR.Head}, digest(o.PR.Head), o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
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
			if err := candidate.appendChange(kind, Item{ID: o.PR.ID, Kind: "pull_request", URL: o.PR.URL, State: o.PR.State}, digest(o.PR), o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
				return err
			}
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
	candidate.Saved.StartedAt = o.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	candidate.Saved.EndedAt = o.EndedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	candidate.current = &o
	if err := candidate.capacity(); err != nil {
		candidate.pruneAccepted()
		if err := candidate.capacity(); err != nil {
			return err
		}
	}
	*t = candidate
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
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

func (t *Tracker) appendChange(kind string, item Item, d, at string) error {
	if t.Saved.Sequence == math.MaxUint64 {
		return errors.New("sequence_exhausted")
	}
	t.Saved.Sequence++
	full := item.Body
	item.Body = excerpt(full, 64)
	c := Change{Sequence: t.Saved.Sequence, Kind: kind, At: at, Item: item, Digest: d, BodyStatus: "complete"}
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
	raw, _ := json.Marshal(c)
	if len(raw) > 640 {
		c.Item.Body = ""
		c.BodyStatus = "shortened"
		c.Omission = "body omitted to retain identity and relationships"
		raw, _ = json.Marshal(c)
	}
	if len(raw) > 640 {
		return errors.New("coverage_capacity_exceeded")
	}
	t.Saved.Journal = append(t.Saved.Journal, c)
	return nil
}

func (t *Tracker) Context() (string, uint64, error) {
	eligible := []Change{}
	for _, change := range t.Saved.Journal {
		if change.Sequence > t.cursor {
			if t.current != nil {
				for _, item := range t.current.Items {
					if item.Kind == change.Item.Kind && item.ID == change.Item.ID && bodyDigest(item.Body) == change.Digest {
						change.Item.Body = item.Body
						change.Omission = ""
						change.BodyStatus = "complete"
						if item.Body == "" {
							change.BodyStatus = "empty"
						}
						break
					}
				}
			}
			eligible = append(eligible, change)
		}
	}
	if len(eligible) == 0 {
		return "", 0, nil
	}
	if len(eligible) > 8 {
		return "", 0, errors.New("notification_capacity_exceeded")
	}
	sequence := eligible[len(eligible)-1].Sequence
	position := t.position(sequence)
	current := map[string]any{"pr": t.Saved.Aggregate, "reviews": []any{}, "requests": []any{}, "threads": []any{}}
	for _, f := range t.Saved.Fingerprints {
		if f.Kind == "review" || f.Kind == "request" || f.Kind == "thread" {
			entry := map[string]any{"id": f.ID, "digest": f.Digest}
			for _, item := range t.Saved.Metadata {
				if item.ID == f.ID && item.Kind == f.Kind {
					entry["state"] = item.State
					entry["resolved"] = item.Resolved
					entry["outdated"] = item.Outdated
					entry["reviewer"] = item.Reviewer
					entry["reviewer_id"] = item.ReviewerID
					entry["reviewer_type"] = item.ReviewerType
				}
			}
			key := map[string]string{"review": "reviews", "request": "requests", "thread": "threads"}[f.Kind]
			current[key] = append(current[key].([]any), entry)
		}
	}
	if encoded, _ := json.Marshal(eligible); len(encoded) > 5120 {
		for i := range eligible {
			if len(eligible[i].Item.Body) > 64 {
				eligible[i].Item.Body = excerpt(eligible[i].Item.Body, 64)
				eligible[i].BodyStatus = "shortened"
				eligible[i].Omission = "body shortened for notification capacity; inspect item link"
			}
		}
	}
	omissions := []any{}
	for _, c := range eligible {
		if c.Omission != "" {
			omissions = append(omissions, map[string]any{"id": c.Item.ID, "reason": c.Omission, "url": c.Item.URL, "digest": c.Digest})
		}
	}
	views := make([]map[string]any, 0, len(eligible))
	for _, change := range eligible {
		views = append(views, changeView(change))
	}
	contextValue := map[string]any{"external_data": true, "pr": t.Saved.Aggregate, "waiting_reason": t.Saved.Scope.WaitingReason, "represented_actor": t.Saved.Scope.RepresentedActor, "observation": map[string]string{"started_at": t.Saved.StartedAt, "ended_at": t.Saved.EndedAt}, "changes": views, "current": current, "omissions": omissions, "confirmation_position": position}
	changed, _ := json.Marshal(views)
	summary, _ := json.Marshal(current)
	raw, _ := json.Marshal(contextValue)
	metadata, _ := json.Marshal(map[string]any{"pr": t.Saved.Aggregate, "waiting_reason": t.Saved.Scope.WaitingReason, "represented_actor": t.Saved.Scope.RepresentedActor, "position": position, "omissions": omissions})
	if len(changed) > 5120 || len(summary) > 1536 || len(metadata) > 1024 || len(raw) > 8192 {
		return "", 0, errors.New("notification_capacity_exceeded")
	}
	candidate := *t
	candidate.Saved = t.Saved
	candidate.Saved.Positions = append([]Position{}, t.Saved.Positions...)
	found := false
	for _, p := range candidate.Saved.Positions {
		if p.Value == position {
			found = true
		}
	}
	if !found {
		candidate.Saved.Positions = append(candidate.Saved.Positions, Position{position, sequence})
		if len(candidate.Saved.Positions) > 4 {
			candidate.Saved.Positions = candidate.Saved.Positions[len(candidate.Saved.Positions)-4:]
		}
	}
	candidate.Saved.Attempted = sequence
	if err := candidate.capacity(); err != nil {
		return "", 0, err
	}
	*t = candidate
	return string(raw), sequence, nil
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
	if t.cursor < through {
		through = t.cursor
	}
	if t.prepared != nil && *t.prepared < through {
		through = *t.prepared
	}
	journal := []Change{}
	removed := uint64(0)
	for _, change := range t.Saved.Journal {
		if change.Sequence <= through {
			removed = change.Sequence
		} else {
			journal = append(journal, change)
		}
	}
	if removed == 0 {
		return
	}
	t.Saved.Journal = journal
	positions := []Position{}
	for _, position := range t.Saved.Positions {
		if position.Sequence >= removed {
			positions = append(positions, position)
		}
	}
	t.Saved.Positions = positions
}

func changeView(change Change) map[string]any {
	raw, _ := json.Marshal(change)
	value := map[string]any{}
	_ = json.Unmarshal(raw, &value)
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

func (t *Tracker) capacity() error {
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
	if err := t.capacity(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"schema": StateSchema, "payload": t.Saved})
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return jsonvalue.Parse(raw, 32768)
}

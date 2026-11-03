package domain

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// These helpers construct legacy progress fixtures for the frozen decoders.
func (t *Tracker) Context() (string, uint64, error) { return t.context() }

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
}

func (t *Tracker) Notice(sequence uint64, status string) {
	if status == "accepted" && sequence > t.Saved.Confirmed {
		t.Saved.Confirmed = sequence
		if t.Saved.Comparison == nil {
			t.cursor = sequence
		}
	}
}

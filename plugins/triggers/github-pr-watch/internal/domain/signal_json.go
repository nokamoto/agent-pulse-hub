package domain

import (
	"bytes"
	"encoding/json"
	"errors"
)

func decodeSignalFields(raw []byte, target any, fields []string, nullable ...string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil || len(object) != len(fields) {
		return errors.New("invalid signal record")
	}
	allowsNull := map[string]bool{}
	for _, field := range nullable {
		allowsNull[field] = true
	}
	for _, field := range fields {
		v, ok := object[field]
		if !ok || (!allowsNull[field] && bytes.Equal(bytes.TrimSpace(v), []byte("null"))) {
			return errors.New("invalid signal record")
		}
	}
	return strictDecode(raw, target)
}

func (r *CIResult) UnmarshalJSON(raw []byte) error {
	type plain CIResult
	return decodeSignalFields(raw, (*plain)(r), []string{"source", "id", "repository_id", "head", "suite_id", "app_id", "name", "status", "conclusion", "started_at", "completed_at", "created_at", "updated_at", "details_url", "url", "description", "description_digest", "description_omission", "selected", "last_episode"}, "conclusion", "started_at", "completed_at", "created_at", "updated_at", "details_url", "url", "description")
}

func (r *ConflictPair) UnmarshalJSON(raw []byte) error {
	type plain ConflictPair
	return decodeSignalFields(raw, (*plain)(r), []string{"pr_id", "head", "base_repository_id", "base_ref", "base_sha"})
}

func (r *ConflictComparison) UnmarshalJSON(raw []byte) error {
	type plain ConflictComparison
	return decodeSignalFields(raw, (*plain)(r), []string{"state", "pair", "period", "confirmed", "last_episode"}, "pair")
}

func (r *ReadinessComparison) UnmarshalJSON(raw []byte) error {
	type plain ReadinessComparison
	return decodeSignalFields(raw, (*plain)(r), []string{"draft", "last_episode"})
}

func (r *SignalConflictRecord) UnmarshalJSON(raw []byte) error {
	type plain SignalConflictRecord
	return decodeSignalFields(raw, (*plain)(r), []string{"comparison", "repository_id", "observed_pair", "base_exists", "mergeable"})
}

func (r *SignalChange) UnmarshalJSON(raw []byte) error {
	type plain SignalChange
	return decodeSignalFields(raw, (*plain)(r), []string{"category", "id", "episode", "ci", "previous_ci", "pair", "pair_period", "previous_conflict", "conflict", "previous_draft", "draft"}, "ci", "previous_ci", "pair", "previous_draft", "draft")
}

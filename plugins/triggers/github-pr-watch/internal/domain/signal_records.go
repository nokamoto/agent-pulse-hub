package domain

import "errors"

const SignalStateSchema = "github-pr-watch-v3"

type SignalConflictRecord struct {
	Comparison   ConflictComparison `json:"comparison"`
	RepositoryID string             `json:"repository_id"`
	ObservedPair ConflictPair       `json:"observed_pair"`
	BaseExists   bool               `json:"base_exists"`
	Mergeable    string             `json:"mergeable"`
}

type SignalRecord struct {
	CI        *CIResult             `json:"ci,omitempty"`
	Conflict  *SignalConflictRecord `json:"conflict,omitempty"`
	Readiness *ReadinessComparison  `json:"readiness,omitempty"`
}

func isSignalKind(kind string) bool {
	return kind == "ci_check" || kind == "ci_status" || kind == "signal_conflict" || kind == "signal_readiness"
}

func (t *Tracker) StateSchema() string {
	if t.SignalProfile {
		return SignalStateSchema
	}
	return StreamStateSchema
}

func signalItems(state SignalState, link string) []Item {
	items := make([]Item, 0, len(state.Observation.CI)+2)
	for _, result := range state.Observation.CI {
		r := retainedCI(result)
		items = append(items, Item{ID: r.ID, Kind: r.Source, URL: link, Signal: &SignalRecord{CI: &r}})
	}
	conflict := SignalConflictRecord{Comparison: state.Conflict, RepositoryID: state.Observation.RepositoryID, ObservedPair: state.Observation.Pair, BaseExists: state.Observation.BaseExists, Mergeable: state.Observation.Mergeable}
	ready := state.Readiness
	items = append(items, Item{ID: state.Observation.Pair.PRID, Kind: "signal_conflict", URL: link, Signal: &SignalRecord{Conflict: &conflict}}, Item{ID: state.Observation.Pair.PRID, Kind: "signal_readiness", URL: link, Signal: &SignalRecord{Readiness: &ready}})
	return items
}

func retainedCI(result CIResult) CIResult {
	if result.Description != nil {
		if result.DescriptionDigest == "" {
			result.DescriptionDigest = bodyDigest(*result.Description)
		}
		if len(*result.Description) > 512 {
			text := excerpt(*result.Description, 512)
			result.Description = &text
			result.DescriptionOmission = "description shortened; full acquired digest retained"
		}
	}
	return result
}

func signalStateFromItems(items []Item) (SignalState, error) {
	state := SignalState{Observation: SignalObservation{CI: []CIResult{}}}
	conflict, ready := false, false
	invalid := errors.New("invalid restored signal state")
	for _, item := range items {
		if !isSignalKind(item.Kind) {
			if item.Signal != nil {
				return state, invalid
			}
			continue
		}
		r := item.Signal
		if r == nil {
			return state, invalid
		}
		switch item.Kind {
		case "ci_check", "ci_status":
			if r.CI == nil || r.Conflict != nil || r.Readiness != nil || r.CI.Source != item.Kind || r.CI.ID != item.ID {
				return state, invalid
			}
			state.Observation.CI = append(state.Observation.CI, *r.CI)
		case "signal_conflict":
			if conflict || r.Conflict == nil || r.CI != nil || r.Readiness != nil || item.ID != r.Conflict.ObservedPair.PRID {
				return state, invalid
			}
			conflict = true
			state.Conflict = r.Conflict.Comparison
			state.Observation.RepositoryID = r.Conflict.RepositoryID
			state.Observation.Pair = r.Conflict.ObservedPair
			state.Observation.BaseExists = r.Conflict.BaseExists
			state.Observation.Mergeable = r.Conflict.Mergeable
		case "signal_readiness":
			if ready || r.Readiness == nil || r.CI != nil || r.Conflict != nil {
				return state, invalid
			}
			ready = true
			state.Readiness = *r.Readiness
			state.Observation.Draft = r.Readiness.Draft
		}
	}
	if !conflict || !ready {
		return state, invalid
	}
	for _, item := range items {
		if item.Kind == "signal_readiness" && item.ID != state.Observation.Pair.PRID {
			return state, invalid
		}
	}
	return state, nil
}

func (t *Tracker) validateSignalRecords() error {
	invalid := errors.New("invalid restored signal state")
	if !t.SignalProfile {
		for _, item := range t.Saved.Metadata {
			if isSignalKind(item.Kind) || item.Signal != nil {
				return invalid
			}
		}
		for _, change := range t.Saved.Journal {
			if isSignalKind(change.Item.Kind) || change.Signal != nil {
				return invalid
			}
		}
		return nil
	}
	state, err := signalStateFromItems(t.Saved.Metadata)
	if err != nil || state.Validate(t.Saved.Sequence) != nil || state.Observation.Pair.PRID != t.Saved.Aggregate.ID || state.Observation.Pair.Head != t.Saved.Aggregate.Head || state.Conflict.Period > t.Observations {
		return invalid
	}
	for i, item := range t.Saved.Metadata {
		if item.Signal == nil {
			continue
		}
		if item.Signal.CI != nil && !validRetainedCI(*item.Signal.CI) {
			return invalid
		}
		fs, err := fingerprints(Observation{PR: t.Saved.Aggregate, Items: []Item{item}})
		if err != nil || fs[0].Digest != t.Saved.Fingerprints[i].Digest {
			return invalid
		}
	}
	for _, c := range t.Saved.Journal {
		if !isSignalKind(c.Item.Kind) {
			if c.Signal != nil {
				return invalid
			}
			continue
		}
		s := c.Signal
		if s == nil || s.Category != c.Item.Kind || s.ID != c.Item.ID || s.Episode != c.Sequence || c.Kind != "signal_episode" || digest(s) != c.Digest {
			return invalid
		}
		switch s.Category {
		case "ci_check", "ci_status":
			if s.CI == nil || !validRetainedCI(*s.CI) || s.CI.Source != s.Category || s.CI.ID != s.ID || s.CI.LastEpisode != c.Sequence || s.CI.Head == "" || s.CI.RepositoryID == "" {
				return invalid
			}
			if class, err := s.CI.FailureClass(); err != nil || class != "failure" {
				return invalid
			}
		case "signal_conflict":
			if s.Pair == nil || s.Pair.PRID != s.ID || s.Pair.Head == "" || s.Pair.BaseSHA == "" || s.Pair.BaseRef == "" || s.Pair.BaseRepositoryID == "" || s.PairPeriod == 0 || s.PairPeriod > t.Observations || s.Conflict != "CONFLICTING" {
				return invalid
			}
		case "signal_readiness":
			if s.PreviousDraft == nil || !*s.PreviousDraft || s.Draft == nil || *s.Draft {
				return invalid
			}
		}
	}
	return nil
}

func validRetainedCI(result CIResult) bool {
	if result.Description == nil {
		return result.DescriptionDigest == "" && result.DescriptionOmission == ""
	}
	if !validDigest(result.DescriptionDigest) || len(*result.Description) > 512 {
		return false
	}
	if result.DescriptionOmission == "" {
		return bodyDigest(*result.Description) == result.DescriptionDigest
	}
	return result.DescriptionOmission == "description shortened; full acquired digest retained"
}

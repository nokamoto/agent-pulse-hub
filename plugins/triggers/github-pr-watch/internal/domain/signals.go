package domain

import (
	"errors"
	"math"
	"sort"
	"strconv"
)

// CIResult retains individual provider results, including non-selected attempts.
// A nil optional value is distinct from an empty string.
type CIResult struct {
	Source              string  `json:"source"`
	ID                  string  `json:"id"`
	RepositoryID        string  `json:"repository_id"`
	Head                string  `json:"head"`
	SuiteID             string  `json:"suite_id"`
	AppID               string  `json:"app_id"`
	Name                string  `json:"name"`
	Status              string  `json:"status"`
	Conclusion          *string `json:"conclusion"`
	StartedAt           *string `json:"started_at"`
	CompletedAt         *string `json:"completed_at"`
	CreatedAt           *string `json:"created_at"`
	UpdatedAt           *string `json:"updated_at"`
	DetailsURL          *string `json:"details_url"`
	URL                 *string `json:"url"`
	Description         *string `json:"description"`
	DescriptionDigest   string  `json:"description_digest"`
	DescriptionOmission string  `json:"description_omission"`
	Selected            bool    `json:"selected"`
	LastEpisode         uint64  `json:"last_episode"`
}

type ConflictPair struct {
	PRID             string `json:"pr_id"`
	Head             string `json:"head"`
	BaseRepositoryID string `json:"base_repository_id"`
	BaseRef          string `json:"base_ref"`
	BaseSHA          string `json:"base_sha"`
}

type SignalObservation struct {
	RepositoryID string       `json:"repository_id"`
	Pair         ConflictPair `json:"pair"`
	BaseExists   bool         `json:"base_exists"`
	Mergeable    string       `json:"mergeable"`
	Draft        bool         `json:"draft"`
	CI           []CIResult   `json:"ci"`
}

type ConflictComparison struct {
	State       string        `json:"state"`
	Pair        *ConflictPair `json:"pair"`
	Period      uint64        `json:"period"`
	Confirmed   bool          `json:"confirmed"`
	LastEpisode uint64        `json:"last_episode"`
}

type ReadinessComparison struct {
	Draft       bool   `json:"draft"`
	LastEpisode uint64 `json:"last_episode"`
}

// SignalState is the comparison portion of a complete observation. Recovery
// updates it even when no notification journal entry is warranted.
type SignalState struct {
	Observation SignalObservation   `json:"observation"`
	Conflict    ConflictComparison  `json:"conflict"`
	Readiness   ReadinessComparison `json:"readiness"`
}

type SignalChange struct {
	Category         string        `json:"category"`
	ID               string        `json:"id"`
	Episode          uint64        `json:"episode"`
	CI               *CIResult     `json:"ci"`
	PreviousCI       *CIResult     `json:"previous_ci"`
	Pair             *ConflictPair `json:"pair"`
	PairPeriod       uint64        `json:"pair_period"`
	PreviousConflict string        `json:"previous_conflict"`
	Conflict         string        `json:"conflict"`
	PreviousDraft    *bool         `json:"previous_draft"`
	Draft            *bool         `json:"draft"`
}

// FailureClass validates exact provider enums before classifying eligibility.
func (r CIResult) FailureClass() (string, error) {
	switch r.Source {
	case "ci_status":
		switch r.Status {
		case "error", "failure":
			return "failure", nil
		case "pending", "success":
			return "non_failure", nil
		}
	case "ci_check":
		failure := false
		if r.Conclusion != nil {
			switch *r.Conclusion {
			case "FAILURE", "STARTUP_FAILURE", "TIMED_OUT", "ACTION_REQUIRED":
				failure = true
			case "CANCELLED", "STALE", "NEUTRAL", "SKIPPED", "SUCCESS":
			default:
				return "", errors.New("unsupported_api")
			}
		}
		switch r.Status {
		case "PENDING", "QUEUED", "REQUESTED", "WAITING", "IN_PROGRESS":
			return "non_final", nil
		case "COMPLETED":
			if r.Conclusion == nil {
				return "", errors.New("incomplete_retrieval")
			}
			if failure {
				return "failure", nil
			}
			return "non_failure", nil
		}
	}
	return "", errors.New("unsupported_api")
}

func (r CIResult) resultVersion() string {
	if r.Source == "ci_status" {
		return digest(r.Status)
	}
	return digest(struct {
		Status      string
		Conclusion  *string
		StartedAt   *string
		CompletedAt *string
	}{r.Status, r.Conclusion, r.StartedAt, r.CompletedAt})
}

func (o SignalObservation) Validate() error {
	if o.RepositoryID == "" || o.Pair.PRID == "" || o.Pair.Head == "" || o.Pair.BaseRepositoryID == "" || o.Pair.BaseRef == "" || o.Pair.BaseSHA == "" {
		return errors.New("incomplete_retrieval")
	}
	if o.Mergeable != "UNKNOWN" && o.Mergeable != "MERGEABLE" && o.Mergeable != "CONFLICTING" {
		return errors.New("unsupported_api")
	}
	seen := map[string]bool{}
	channels := map[string]bool{}
	statusChannels := map[string]bool{}
	for _, r := range o.CI {
		key := r.Source + ":" + r.ID
		if r.ID == "" || r.Name == "" || r.RepositoryID != o.RepositoryID || r.Head != o.Pair.Head || seen[key] {
			return errors.New("incomplete_retrieval")
		}
		if _, err := r.FailureClass(); err != nil {
			return err
		}
		if r.Source == "ci_check" && (r.SuiteID == "" || r.AppID == "") {
			return errors.New("incomplete_retrieval")
		}
		if r.Source == "ci_status" {
			number, err := strconv.ParseUint(r.ID, 10, 64)
			if err != nil || number == 0 || strconv.FormatUint(number, 10) != r.ID {
				return errors.New("incomplete_retrieval")
			}
			statusChannels[r.Name] = true
			if r.Selected {
				if channels[r.Name] {
					return errors.New("incomplete_retrieval")
				}
				channels[r.Name] = true
			}
		}
		seen[key] = true
	}
	if len(channels) != len(statusChannels) {
		return errors.New("incomplete_retrieval")
	}
	return nil
}

// CompareSignals stages comparison and eligible changes without mutating the
// previous cut. The tracker assigns global sequences after merging categories.
func CompareSignals(previous *SignalState, observed SignalObservation) (SignalState, []SignalChange, error) {
	if err := observed.Validate(); err != nil {
		return SignalState{}, nil, err
	}
	observed.CI = append([]CIResult{}, observed.CI...)
	sort.Slice(observed.CI, func(i, j int) bool {
		return observed.CI[i].Source+":"+observed.CI[i].ID < observed.CI[j].Source+":"+observed.CI[j].ID
	})
	next := SignalState{Observation: observed, Readiness: ReadinessComparison{Draft: observed.Draft}}
	changes := []SignalChange{}
	old := map[string]CIResult{}
	if previous != nil {
		next.Conflict = previous.Conflict
		next.Readiness.LastEpisode = previous.Readiness.LastEpisode
		for _, r := range previous.Observation.CI {
			old[r.Source+":"+r.ID] = r
		}
	}
	for i := range next.Observation.CI {
		r := &next.Observation.CI[i]
		before, known := old[r.Source+":"+r.ID]
		known = known && before.RepositoryID == r.RepositoryID && before.Head == r.Head
		r.LastEpisode = 0
		if known {
			r.LastEpisode = before.LastEpisode
		}
		class, _ := r.FailureClass()
		if previous == nil || class != "failure" || (known && before.resultVersion() == r.resultVersion()) {
			continue
		}
		copyResult := *r
		change := SignalChange{Category: r.Source, ID: r.ID, CI: &copyResult}
		if known {
			copyBefore := before
			change.PreviousCI = &copyBefore
		}
		changes = append(changes, change)
	}
	if observed.BaseExists {
		pair := observed.Pair
		if next.Conflict.Pair == nil || *next.Conflict.Pair != pair {
			if next.Conflict.Period == math.MaxUint64 {
				return SignalState{}, nil, errors.New("sequence_exhausted")
			}
			next.Conflict.Pair = &pair
			next.Conflict.Period++
			next.Conflict.Confirmed = false
		}
		if observed.Mergeable == "CONFLICTING" {
			if previous != nil && !next.Conflict.Confirmed {
				changes = append(changes, SignalChange{Category: "signal_conflict", ID: pair.PRID, Pair: &pair, PairPeriod: next.Conflict.Period, PreviousConflict: previous.Conflict.State, Conflict: observed.Mergeable})
			}
			next.Conflict.Confirmed = true
		}
		if observed.Mergeable == "MERGEABLE" {
			next.Conflict.Confirmed = false
		}
		next.Conflict.State = observed.Mergeable
	} else {
		next.Conflict.State = "unavailable"
	}
	if previous != nil && previous.Readiness.Draft && !observed.Draft {
		before, after := true, false
		changes = append(changes, SignalChange{Category: "signal_readiness", ID: observed.Pair.PRID, PreviousDraft: &before, Draft: &after})
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Category+":"+changes[i].ID < changes[j].Category+":"+changes[j].ID
	})
	return next, changes, nil
}

// AssignEpisode binds a change to the common journal, with no separate cursor.
func (s *SignalState) AssignEpisode(change *SignalChange, sequence uint64) {
	change.Episode = sequence
	switch change.Category {
	case "signal_conflict":
		s.Conflict.LastEpisode = sequence
	case "signal_readiness":
		s.Readiness.LastEpisode = sequence
	default:
		for i := range s.Observation.CI {
			r := &s.Observation.CI[i]
			if r.Source == change.Category && r.ID == change.ID {
				r.LastEpisode = sequence
				change.CI.LastEpisode = sequence
			}
		}
	}
}

// Validate checks the scalar and CI comparison records before a restored cut
// can participate in acquisition or confirm a retained journal position.
func (s SignalState) Validate(sequence uint64) error {
	invalid := errors.New("invalid restored signal state")
	if sequence == 0 || s.Observation.Validate() != nil || s.Readiness.Draft != s.Observation.Draft {
		return invalid
	}
	validEpisode := func(episode uint64) bool { return episode == 0 || (episode >= 2 && episode <= sequence) }
	if !validEpisode(s.Readiness.LastEpisode) || !validEpisode(s.Conflict.LastEpisode) {
		return invalid
	}
	for _, result := range s.Observation.CI {
		if !validEpisode(result.LastEpisode) {
			return invalid
		}
	}
	if s.Conflict.Pair == nil {
		if s.Conflict.Period != 0 || s.Conflict.Confirmed || s.Conflict.LastEpisode != 0 || s.Observation.BaseExists {
			return invalid
		}
	} else {
		pair := s.Conflict.Pair
		if s.Conflict.Period == 0 || pair.PRID != s.Observation.Pair.PRID || pair.Head == "" || pair.BaseRepositoryID == "" || pair.BaseRef == "" || pair.BaseSHA == "" {
			return invalid
		}
	}
	if !s.Observation.BaseExists {
		if s.Conflict.State != "unavailable" {
			return invalid
		}
	} else {
		if *s.Conflict.Pair != s.Observation.Pair || s.Conflict.State != s.Observation.Mergeable {
			return invalid
		}
		if s.Conflict.State == "CONFLICTING" && !s.Conflict.Confirmed {
			return invalid
		}
		if s.Conflict.State == "MERGEABLE" && s.Conflict.Confirmed {
			return invalid
		}
	}
	return nil
}

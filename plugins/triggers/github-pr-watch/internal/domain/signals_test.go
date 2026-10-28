package domain

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func signalText(s string) *string { return &s }

func signalFixture() SignalObservation {
	return SignalObservation{
		RepositoryID: "repo", Pair: ConflictPair{PRID: "pr", Head: "H1", BaseRepositoryID: "repo", BaseRef: "main", BaseSHA: "B1"},
		BaseExists: true, Mergeable: "MERGEABLE", Draft: true,
		CI: []CIResult{{Source: "ci_check", ID: "K1", RepositoryID: "repo", Head: "H1", SuiteID: "suite", AppID: "app", Name: "build", Status: "COMPLETED", Conclusion: signalText("SUCCESS"), Selected: true}},
	}
}

func TestSignalResultClassification(t *testing.T) {
	for _, conclusion := range []string{"FAILURE", "STARTUP_FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "CANCELLED", "STALE", "NEUTRAL", "SKIPPED", "SUCCESS", "unsupported"} {
		for _, status := range []string{"COMPLETED", "PENDING", "QUEUED", "REQUESTED", "WAITING", "IN_PROGRESS", "unsupported"} {
			t.Run(conclusion+"/"+status, func(t *testing.T) {
				r := CIResult{Source: "ci_check", Status: status, Conclusion: signalText(conclusion)}
				class, err := r.FailureClass()
				if conclusion == "unsupported" || status == "unsupported" {
					if err == nil {
						t.Fatal("unsupported provider value accepted")
					}
					return
				}
				want := "non_final"
				if status == "COMPLETED" {
					want = "non_failure"
					if conclusion == "FAILURE" || conclusion == "STARTUP_FAILURE" || conclusion == "TIMED_OUT" || conclusion == "ACTION_REQUIRED" {
						want = "failure"
					}
				}
				if err != nil || class != want {
					t.Fatalf("class=%s err=%v want=%s", class, err, want)
				}
			})
		}
	}
	for _, status := range []string{"error", "failure", "pending", "success", "EXPECTED", ""} {
		t.Run("status/"+status, func(t *testing.T) {
			class, err := (CIResult{Source: "ci_status", Status: status}).FailureClass()
			switch status {
			case "error", "failure":
				if err != nil || class != "failure" {
					t.Fatal(class, err)
				}
			case "pending", "success":
				if err != nil || class != "non_failure" {
					t.Fatal(class, err)
				}
			default:
				if err == nil {
					t.Fatal("unsupported status accepted")
				}
			}
		})
	}
	if _, err := (CIResult{Source: "ci_check", Status: "COMPLETED"}).FailureClass(); err == nil {
		t.Fatal("completed null accepted")
	}
	if class, err := (CIResult{Source: "ci_check", Status: "IN_PROGRESS"}).FailureClass(); err != nil || class != "non_final" {
		t.Fatal(class, err)
	}
}

func TestSignalFailureVersions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SignalObservation)
		count  int
	}{
		{"unchanged", func(*SignalObservation) {}, 0},
		{"metadata only", func(o *SignalObservation) {
			o.CI[0].Name = "renamed"
			o.CI[0].DetailsURL = signalText("https://external.test/")
		}, 0},
		{"failed run", func(o *SignalObservation) { o.CI[0].Conclusion = signalText("FAILURE") }, 1},
		{"new failure same name", func(o *SignalObservation) {
			r := o.CI[0]
			r.ID = "K2"
			r.Conclusion = signalText("FAILURE")
			r.Selected = false
			o.CI = append(o.CI, r)
		}, 1},
		{"new status", func(o *SignalObservation) {
			o.CI = append(o.CI, CIResult{Source: "ci_status", ID: "10", RepositoryID: "repo", Head: "H1", Name: "build", Status: "failure", Selected: true})
		}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline, changes, err := CompareSignals(nil, signalFixture())
			if err != nil || len(changes) != 0 {
				t.Fatal(changes, err)
			}
			o := signalFixture()
			test.mutate(&o)
			next, changes, err := CompareSignals(&baseline, o)
			if err != nil || len(changes) != test.count {
				t.Fatalf("changes=%v err=%v", changes, err)
			}
			for i := range changes {
				next.AssignEpisode(&changes[i], uint64(i+2))
			}
			_, repeated, err := CompareSignals(&next, o)
			if err != nil || len(repeated) != 0 {
				t.Fatal(repeated, err)
			}
		})
	}
	o := signalFixture()
	o.CI[0].Conclusion = signalText("FAILURE")
	state, changes, err := CompareSignals(nil, o)
	if err != nil || len(changes) != 0 {
		t.Fatal("existing failure baseline", changes, err)
	}
	o.CI[0].CompletedAt = signalText("2026-10-28T01:00:00Z")
	state, changes, err = CompareSignals(&state, o)
	if err != nil || len(changes) != 1 || changes[0].PreviousCI == nil {
		t.Fatal("provenance change", changes, err)
	}
	state.AssignEpisode(&changes[0], 2)
	o.CI[0].Status = "IN_PROGRESS"
	state, changes, err = CompareSignals(&state, o)
	if err != nil || len(changes) != 0 {
		t.Fatal("recovery", changes, err)
	}
	o.CI[0].Status = "COMPLETED"
	state, changes, err = CompareSignals(&state, o)
	if err != nil || len(changes) != 1 {
		t.Fatal("recurrence", changes, err)
	}
	o.CI = nil
	state, _, _ = CompareSignals(&state, o)
	o = signalFixture()
	o.CI[0].Conclusion = signalText("FAILURE")
	_, changes, err = CompareSignals(&state, o)
	if err != nil || len(changes) != 1 {
		t.Fatal("reappearing failure", changes, err)
	}
}

func TestSignalConflictPeriods(t *testing.T) {
	var state *SignalState
	for _, step := range []struct {
		base, mergeable string
		exists          bool
		count           int
		period          uint64
		confirmed       bool
	}{
		{"B1", "CONFLICTING", true, 0, 1, true},
		{"B1", "UNKNOWN", true, 0, 1, true},
		{"B1", "CONFLICTING", true, 0, 1, true},
		{"B2", "UNKNOWN", true, 0, 2, false},
		{"B2", "CONFLICTING", true, 1, 2, true},
		{"B1", "CONFLICTING", true, 1, 3, true},
		{"B1", "UNKNOWN", false, 0, 3, true},
		{"B1", "CONFLICTING", true, 0, 3, true},
		{"B1", "MERGEABLE", true, 0, 3, false},
		{"B1", "CONFLICTING", true, 1, 3, true},
	} {
		o := signalFixture()
		o.Pair.BaseSHA = step.base
		o.Mergeable = step.mergeable
		o.BaseExists = step.exists
		next, changes, err := CompareSignals(state, o)
		if err != nil || len(changes) != step.count || next.Conflict.Period != step.period || next.Conflict.Confirmed != step.confirmed {
			t.Fatalf("step=%+v next=%+v changes=%v err=%v", step, next.Conflict, changes, err)
		}
		state = &next
	}
	state.Conflict.Period = math.MaxUint64
	o := signalFixture()
	o.Pair.BaseRef = "release"
	if _, _, err := CompareSignals(state, o); err == nil {
		t.Fatal("period overflow accepted")
	}
	o = signalFixture()
	o.Mergeable = "UNKNOWN"
	baseline, _, _ := CompareSignals(nil, o)
	o.Mergeable = "CONFLICTING"
	if _, changes, err := CompareSignals(&baseline, o); err != nil || len(changes) != 1 {
		t.Fatal(changes, err)
	}
}

func TestSignalReadinessAndAtomicValidation(t *testing.T) {
	o := signalFixture()
	o.Draft = false
	state, changes, err := CompareSignals(nil, o)
	if err != nil || len(changes) != 0 {
		t.Fatal(changes, err)
	}
	for _, draft := range []bool{false, true, true, false, false, true, false} {
		o.Draft = draft
		want := 0
		if state.Readiness.Draft && !draft {
			want = 1
		}
		next, changes, err := CompareSignals(&state, o)
		if err != nil || len(changes) != want {
			t.Fatal(changes, err)
		}
		if want == 1 && (changes[0].Category != "signal_readiness" || !*changes[0].PreviousDraft || *changes[0].Draft) {
			t.Fatal(changes)
		}
		state = next
	}
	before, _ := json.Marshal(state)
	o.CI[0].Head = "old-head"
	if _, _, err := CompareSignals(&state, o); err == nil {
		t.Fatal("wrong head accepted")
	}
	after, _ := json.Marshal(state)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed candidate mutated previous state")
	}
}

func TestSignalRestoredComparisonValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SignalState)
		bad    bool
	}{
		{"baseline", func(*SignalState) {}, false},
		{"future episode", func(s *SignalState) { s.Readiness.LastEpisode = 3 }, true},
		{"baseline episode", func(s *SignalState) { s.Conflict.LastEpisode = 1 }, true},
		{"missing pair", func(s *SignalState) { s.Conflict.Pair = nil }, true},
		{"zero period", func(s *SignalState) { s.Conflict.Period = 0 }, true},
		{"state mismatch", func(s *SignalState) { s.Conflict.State = "UNKNOWN" }, true},
		{"draft mismatch", func(s *SignalState) { s.Readiness.Draft = false }, true},
		{"wrong CI head", func(s *SignalState) { s.Observation.CI[0].Head = "older" }, true},
		{"duplicate CI", func(s *SignalState) { s.Observation.CI = append(s.Observation.CI, s.Observation.CI[0]) }, true},
		{"future CI episode", func(s *SignalState) { s.Observation.CI[0].LastEpisode = 3 }, true},
		{"invented confirmation", func(s *SignalState) { s.Conflict.Confirmed = true }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _, err := CompareSignals(nil, signalFixture())
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&state)
			if err := state.Validate(2); (err != nil) != test.bad {
				t.Fatal(err)
			}
		})
	}
}

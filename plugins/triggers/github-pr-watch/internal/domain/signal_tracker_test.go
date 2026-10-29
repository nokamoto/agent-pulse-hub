package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSignalConflictSurvivesLifecycleChanges(t *testing.T) {
	tr := tracker(t)
	tr.SignalProfile = true
	o := testObservation()
	s := signalFixture()
	s.Pair.PRID, s.Pair.Head = o.PR.ID, o.PR.Head
	s.CI[0].Head = o.PR.Head
	s.Mergeable = "CONFLICTING"
	o.Signals = &s
	if err := tr.Reconcile(o); err != nil {
		t.Fatal(err)
	}
	initial, err := signalStateFromItems(tr.Saved.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, lifecycle := range []string{"CLOSED", "OPEN", "MERGED", "OPEN"} {
		o.PR.State = lifecycle
		o.PR.MergedAt = nil
		if lifecycle == "MERGED" {
			o.PR.MergedAt = signalText("2026-10-29T12:50:38Z")
		}
		// Neither an UNKNOWN report nor a missing base resolves prior confirmation.
		s.Mergeable = "UNKNOWN"
		s.BaseExists = lifecycle != "MERGED"
		if err := tr.Reconcile(o); err != nil {
			t.Fatal(err)
		}
		state, err := signalStateFromItems(tr.Saved.Metadata)
		if err != nil || !state.Conflict.Confirmed || state.Conflict.Period != initial.Conflict.Period || !reflect.DeepEqual(state.Conflict.Pair, initial.Conflict.Pair) {
			t.Fatal("lifecycle reset conflict comparison", lifecycle, state, err)
		}
		for _, change := range tr.Saved.Journal {
			if change.Signal != nil {
				t.Fatal("lifecycle invented signal change", lifecycle, change)
			}
		}
	}
	// Reappearance with the same confirmed pair must stay suppressed.
	s.BaseExists, s.Mergeable = true, "CONFLICTING"
	if err := tr.Reconcile(o); err != nil {
		t.Fatal(err)
	}
	for _, change := range tr.Saved.Journal {
		if change.Signal != nil {
			t.Fatal("prior conflict notified again", change)
		}
	}
}

func TestSignalTrackerPrefixAndRestoredSuffix(t *testing.T) {
	tr := tracker(t)
	tr.SignalProfile = true
	o := testObservation()
	s := signalFixture()
	s.Pair.PRID = o.PR.ID
	s.Pair.Head = o.PR.Head
	for i := 1; i < 128; i++ {
		r := s.CI[0]
		r.ID = fmt.Sprintf("K%03d", i)
		r.Name = strings.Repeat("n", 256)
		r.Description = signalText(strings.Repeat("d", 256))
		s.CI = append(s.CI, r)
	}
	for i := range s.CI {
		s.CI[i].Head = o.PR.Head
	}
	o.Signals = &s
	if err := tr.Reconcile(o); err != nil {
		t.Fatal(err)
	}
	if text, _, err := tr.Context(); err != nil || text != "" {
		t.Fatal(text, err)
	}
	for cycle := 0; cycle < 3; cycle++ {
		s.CI[0].Conclusion = signalText("FAILURE")
		s.CI[0].CompletedAt = signalText(fmt.Sprintf("attempt-%d", cycle))
		s.Mergeable = "CONFLICTING"
		s.Pair.BaseSHA = fmt.Sprintf("B%d", cycle+2)
		s.Draft = false
		if err := tr.Reconcile(o); err != nil {
			t.Fatal(err)
		}
		s.CI[0].Conclusion = signalText("SUCCESS")
		s.Mergeable = "MERGEABLE"
		s.Draft = true
		if err := tr.Reconcile(o); err != nil {
			t.Fatal(err)
		}
	}
	text, through, err := tr.Context()
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Changes      []json.RawMessage
		Continuation struct {
			HasMore   bool `json:"has_more"`
			Remaining int  `json:"remaining_changes"`
		}
	}
	if json.Unmarshal([]byte(text), &view) != nil || len(view.Changes) != 8 || !view.Continuation.HasMore || view.Continuation.Remaining != 1 || through != 9 {
		t.Fatalf("through=%d context=%s", through, text)
	}
	tr.Notice(through, "accepted")
	objects, err := tr.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	bytes := 0
	for _, v := range objects {
		bytes += len(v.Marshal())
	}
	if bytes <= 32768 {
		t.Fatal("snapshot did not exceed an object", bytes)
	}
	restored, err := RestoreStream(tr.Saved.Scope, SignalStateSchema, objects, DefaultByteBudget, DefaultByteBudget, true)
	if err != nil {
		t.Fatal(err)
	}
	text, through, err = restored.Context()
	if err != nil || through != 10 {
		t.Fatal(text, through, err)
	}
	if json.Unmarshal([]byte(text), &view) != nil || len(view.Changes) != 1 || view.Continuation.HasMore {
		t.Fatal(text)
	}
	restored.Notice(through, "accepted")
	if err := restored.Reconcile(o); err != nil {
		t.Fatal(err)
	}
	if text, _, err := restored.Context(); err != nil || text != "" {
		t.Fatal(text, err)
	}
	if _, err := RestoreStream(tr.Saved.Scope, StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget, true); err == nil || err.Error() != "signal_state_incompatible" {
		t.Fatal(err)
	}
	if _, err := RestoreStream(tr.Saved.Scope, SignalStateSchema, objects, DefaultByteBudget, DefaultByteBudget); err == nil {
		t.Fatal("v2 accepted v3")
	}
}

func TestSignalJSONRejectsMissingNullableAndBooleanFields(t *testing.T) {
	result := signalFixture().CI[0]
	raw, _ := json.Marshal(result)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for name := range fields {
		t.Run(name, func(t *testing.T) {
			copy := map[string]json.RawMessage{}
			for key, value := range fields {
				copy[key] = value
			}
			delete(copy, name)
			bad, _ := json.Marshal(copy)
			var decoded CIResult
			if json.Unmarshal(bad, &decoded) == nil {
				t.Fatal("missing field accepted", name)
			}
		})
	}
	for _, raw := range []string{`{"draft":null,"last_episode":0}`, `{"last_episode":0}`, `{"draft":false,"last_episode":null}`} {
		var ready ReadinessComparison
		if json.Unmarshal([]byte(raw), &ready) == nil {
			t.Fatal("ambiguous readiness accepted", raw)
		}
	}
}

func TestSignalDescriptionRetainsFullDigestBeforeShortening(t *testing.T) {
	r := CIResult{Description: signalText(strings.Repeat("日本語", 1000))}
	full := *r.Description
	retained := retainedCI(r)
	if retained.DescriptionDigest != bodyDigest(full) || len(*retained.Description) > 512 || retained.DescriptionOmission == "" || *r.Description != full || !validRetainedCI(retained) {
		t.Fatal(retained)
	}
}

package domain

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestParseWatchConfig(t *testing.T) {
	t.Run("defaults and canonical host", func(t *testing.T) {
		args := object(t, `{"host":"GHE.Example.Test"}`)
		got, err := ParseWatchConfig(args)
		if err != nil {
			t.Fatal(err)
		}
		if got.Host != "ghe.example.test" || got.APIBase != "https://ghe.example.test/api/v3" || got.PollInterval != time.Minute {
			t.Fatalf("unexpected config: %#v", got)
		}
	})
	t.Run("explicit github interval", func(t *testing.T) {
		got, err := ParseWatchConfig(object(t, `{"poll_interval":"250ms"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got.Host != DefaultHost || got.APIBase != "https://api.github.com" || got.PollInterval != 250*time.Millisecond {
			t.Fatalf("unexpected config: %#v", got)
		}
	})
	for _, input := range []string{
		`[]`,
		`{"unknown":true}`,
		`{"host":1}`,
		`{"host":""}`,
		`{"host":"https://github.com"}`,
		`{"host":"user@github.com"}`,
		`{"host":"github.com:443"}`,
		`{"host":"github.com/path"}`,
		`{"host":"github.com?x=1"}`,
		`{"host":"github.com."}`,
		`{"host":"127.0.0.1"}`,
		`{"host":"999.2.3.4"}`,
		`{"host":"[2001:db8::1]"}`,
		`{"host":"münich.example"}`,
		`{"poll_interval":60}`,
		`{"poll_interval":"0s"}`,
		`{"poll_interval":"-1s"}`,
		`{"poll_interval":"bad"}`,
		`{"poll_interval":"999999999999999999999h"}`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseWatchConfig(object(t, input)); err == nil {
				t.Fatal("expected invalid watch configuration")
			}
		})
	}
}

func TestValidatePullURL(t *testing.T) {
	valid := "https://github.com/owner/repo/pull/123"
	if !ValidatePullURL(valid, "github.com") {
		t.Fatal("valid pull request URL was rejected")
	}
	if id, ok := ParsePullURL(valid, "github.com"); !ok || id != 123 {
		t.Fatalf("pull request number = %d, valid = %v", id, ok)
	}
	for _, raw := range []string{
		"http://github.com/owner/repo/pull/123",
		"https://github.com.evil.test/owner/repo/pull/123",
		"https://github.com:443/owner/repo/pull/123",
		"https://user@github.com/owner/repo/pull/123",
		"https://github.com/owner/repo/pull/123?token=x",
		"https://github.com/owner/repo/pull/123#fragment",
		"https://github.com/owner/repo/issues/123",
		"https://github.com/owner/repo/pull/0",
		"https://github.com/owner/repo/pull/1/extra",
		"https://github.com/owner/re%2Fbad/pull/123",
		"https://github.com./owner/repo/pull/123",
		"https://ghe.example.test/owner/repo/pull/123",
	} {
		if ValidatePullURL(raw, "github.com") {
			t.Errorf("accepted invalid URL %q", raw)
		}
	}
}

func TestTrackerOccurrenceAndCheckpoint(t *testing.T) {
	tracker := NewTracker("github.com", 42)
	if created, err := tracker.Reconcile(nil); err != nil || !created {
		t.Fatalf("empty baseline: created=%v err=%v", created, err)
	}
	first := map[uint64]string{
		10: "https://github.com/owner/repo/pull/10",
		2:  "https://github.com/owner/repo/pull/2",
	}
	if _, err := tracker.Reconcile(first); err != nil {
		t.Fatal(err)
	}
	attempt, ok := tracker.NextAttempt()
	if !ok || attempt.IssueID != 2 || attempt.URL != first[2] {
		t.Fatalf("first attempt = %#v, valid = %v", attempt, ok)
	}
	tracker.ApplyNotice(attempt.IssueID, attempt.Generation, "accepted")
	state, err := tracker.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := state.Get("payload")
	rows, _ = rows.Get("entries")
	encoded := string(state.Marshal())
	if encoded != `{"payload":{"baseline_established":true,"entries":[["2",2],["10",0]],"host":"github.com","user_id":"42"},"schema":"github-review-request-v2"}` {
		t.Fatalf("checkpoint encoding = %s", encoded)
	}
	if len(rows.Items()) != 2 {
		t.Fatalf("checkpoint rows = %d", len(rows.Items()))
	}
	restored, err := RestoreTracker(state, "github.com", 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Reconcile(first); err != nil {
		t.Fatal(err)
	}
	if a, ok := restored.NextAttempt(); !ok || a.IssueID != 10 {
		t.Fatalf("restored eligible entry = %+v, present = %v", a, ok)
	}
	if _, err := restored.Reconcile(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Reconcile(map[uint64]string{2: first[2]}); err != nil {
		t.Fatal(err)
	}
	newOccurrence, ok := restored.NextAttempt()
	if !ok || newOccurrence.IssueID != 2 || newOccurrence.Generation == attempt.Generation {
		t.Fatalf("reappeared occurrence = %#v, prior generation=%d", newOccurrence, attempt.Generation)
	}
	restored.ApplyNotice(attempt.IssueID, attempt.Generation, "accepted")
	state, err = restored.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSON(string(state.Marshal()), `"entries":[["2",1]]`) {
		t.Fatalf("late notice changed the new attempted occurrence: %s", state.Marshal())
	}
}

func TestRestoreTrackerRejectsInvalidState(t *testing.T) {
	valid := `{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["2",1]]}}`
	for _, input := range []string{
		`{"schema":"other","payload":{"host":"github.com","user_id":"42","entries":[]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[],"extra":true}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"43","entries":[]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"GITHUB.com","user_id":"42","entries":[]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["02",1]]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["2",1.0]]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["3",1],["2",2]]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["2",3]]}}`,
		`{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["2",1],["2",2]]}}`,
		`{"schema":"github-review-request-v2","payload":{"host":"github.com","user_id":"42","entries":[]}}`,
		`{"schema":"github-review-request-v2","payload":{"host":"github.com","user_id":"42","baseline_established":"true","entries":[]}}`,
		`{"schema":"github-review-request-v2","payload":{"host":"github.com","user_id":"42","baseline_established":false,"entries":[["2",3]]}}`,
		`{"schema":"github-review-request-v2","payload":{"host":"github.com","user_id":"42","baseline_established":true,"entries":[["2",4]]}}`,
	} {
		state, err := jsonvalue.Parse([]byte(input), 32_768)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreTracker(state, "github.com", 42); err == nil {
			t.Errorf("accepted invalid state %s", input)
		}
	}
	state, err := jsonvalue.Parse([]byte(valid), 32_768)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreTracker(state, "ghe.example.test", 42); err == nil {
		t.Fatal("accepted checkpoint for another host")
	}
}

func TestTrackerRestoresBaselineAndUpgradesLegacyProgress(t *testing.T) {
	const matchA = "https://github.com/one/repo/pull/1"
	const matchB = "https://github.com/two/repo/pull/1"
	matches := map[uint64]string{
		10: matchA, 20: matchB,
		30: "https://github.com/one/repo/pull/3",
		40: "https://github.com/one/repo/pull/4",
	}
	for _, tc := range []struct {
		name            string
		saved           func(t *testing.T) *jsonvalue.Value
		baselineCreated bool
		wantAttempts    []uint64
	}{
		{
			name: "v2 before first complete result",
			saved: func(t *testing.T) *jsonvalue.Value {
				return trackerCheckpoint(t, NewTracker("github.com", 42))
			},
			baselineCreated: true,
		},
		{
			name: "v2 after empty baseline",
			saved: func(t *testing.T) *jsonvalue.Value {
				tracker := NewTracker("github.com", 42)
				if _, err := tracker.Reconcile(nil); err != nil {
					t.Fatal(err)
				}
				return trackerCheckpoint(t, tracker)
			},
			wantAttempts: []uint64{10, 20, 30, 40},
		},
		{
			name: "v1 empty entries",
			saved: func(t *testing.T) *jsonvalue.Value {
				return object(t, `{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[]}}`)
			},
			wantAttempts: []uint64{10, 20, 30, 40},
		},
		{
			name: "v1 preserves all statuses",
			saved: func(t *testing.T) *jsonvalue.Value {
				return object(t, `{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[["10",0],["20",1],["30",2]]}}`)
			},
			wantAttempts: []uint64{10, 40},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker, err := RestoreTracker(tc.saved(t), "github.com", 42)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := tracker.NextAttempt(); ok {
				t.Fatal("restored an attempt without a current URL")
			}
			created, err := tracker.Reconcile(matches)
			if err != nil || created != tc.baselineCreated {
				t.Fatalf("baseline created=%v, error=%v", created, err)
			}
			for _, id := range tc.wantAttempts {
				a, ok := tracker.NextAttempt()
				if !ok || a.IssueID != id || a.URL != matches[id] {
					t.Fatalf("attempt = %+v, want issue %d at %s", a, id, matches[id])
				}
				tracker.ApplyNotice(a.IssueID, a.Generation, "failed")
			}
			if _, err := tracker.Reconcile(matches); err != nil {
				t.Fatal(err)
			}
			if a, ok := tracker.NextAttempt(); ok {
				t.Fatalf("unchanged observation emitted %+v", a)
			}
			state := string(trackerCheckpoint(t, tracker).Marshal())
			if !strings.Contains(state, `"schema":"github-review-request-v2"`) ||
				!strings.Contains(state, `"baseline_established":true`) ||
				strings.Contains(state, "https://") {
				t.Fatalf("invalid v2 progress: %s", state)
			}
			if tc.baselineCreated {
				if _, err := tracker.Reconcile(nil); err != nil {
					t.Fatal(err)
				}
				if _, err := tracker.Reconcile(map[uint64]string{10: matchA}); err != nil {
					t.Fatal(err)
				}
				if a, ok := tracker.NextAttempt(); !ok || a.URL != matchA {
					t.Fatalf("baseline match did not become eligible after reappearance: %+v", a)
				}
			}
		})
	}
}

func TestTrackerCheckpointFitsMaximumWidthProgress(t *testing.T) {
	host := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)
	tracker := NewTracker(host, math.MaxUint64)
	matches := make(map[uint64]string, MaxMatches)
	for offset := uint64(0); offset < MaxMatches; offset++ {
		id := uint64(math.MaxUint64) - offset
		matches[id] = "https://" + host + "/owner/repo/pull/" + strconv.FormatUint(id, 10)
	}
	if _, err := tracker.Reconcile(matches); err != nil {
		t.Fatal(err)
	}
	state := trackerCheckpoint(t, tracker)
	if size := len(state.Marshal()); size >= 28_000 {
		t.Fatalf("maximum checkpoint size = %d, want below 28000", size)
	}
	if restored, err := RestoreTracker(state, host, math.MaxUint64); err != nil || len(restored.entries) != MaxMatches {
		t.Fatalf("maximum checkpoint did not round trip: %v", err)
	}
}

func trackerCheckpoint(t *testing.T, tracker *Tracker) *jsonvalue.Value {
	t.Helper()
	state, err := tracker.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func object(t *testing.T, input string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(input), 32_768)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func containsJSON(value, text string) bool {
	for i := 0; i+len(text) <= len(value); i++ {
		if value[i:i+len(text)] == text {
			return true
		}
	}
	return false
}

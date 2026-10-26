package domain

import (
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
	first := map[uint64]string{
		10: "https://github.com/owner/repo/pull/10",
		2:  "https://github.com/owner/repo/pull/2",
	}
	if err := tracker.Reconcile(first); err != nil {
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
	if encoded != `{"payload":{"entries":[["2",2],["10",0]],"host":"github.com","user_id":"42"},"schema":"github-review-request-v1"}` {
		t.Fatalf("checkpoint encoding = %s", encoded)
	}
	if len(rows.Items()) != 2 {
		t.Fatalf("checkpoint rows = %d", len(rows.Items()))
	}
	restored, err := RestoreTracker(state, "github.com", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Reconcile(map[uint64]string{10: first[10]}); err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.NextAttempt(); !ok {
		t.Fatal("unsent entry was not eligible after restore")
	}
	if err := restored.Reconcile(map[uint64]string{}); err != nil {
		t.Fatal(err)
	}
	if err := restored.Reconcile(map[uint64]string{2: first[2]}); err != nil {
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

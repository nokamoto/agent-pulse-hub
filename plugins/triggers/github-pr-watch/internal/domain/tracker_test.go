package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func testConfig() WatchConfig {
	return WatchConfig{Host: "github.com", Owner: "owner", Repository: "repo", PullNumber: 42, WaitingReason: "review changes", RepresentedActor: "author"}
}

func testObservation(items ...Item) Observation {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	return Observation{StartedAt: now, EndedAt: now.Add(time.Second), PR: Aggregate{ID: "PR1", URL: "https://github.com/owner/repo/pull/42", Head: "H1", State: "OPEN"}, Items: items}
}

func comment(id, body string) Item {
	return Item{ID: id, Kind: "conversation", Body: body, URL: "https://github.com/owner/repo/pull/42#issuecomment-" + id}
}

func tracker(t *testing.T) *Tracker {
	t.Helper()
	result, err := NewTracker(testConfig(), make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func observe(t *testing.T, tracker *Tracker, o Observation) {
	t.Helper()
	if err := tracker.Reconcile(o); err != nil {
		t.Fatal(err)
	}
}

func notification(t *testing.T, tracker *Tracker) (string, uint64) {
	t.Helper()
	text, sequence, err := tracker.Context()
	if err != nil || text == "" {
		t.Fatalf("context=%q sequence=%d err=%v", text, sequence, err)
	}
	return text, sequence
}

func position(t *testing.T, text string) string {
	t.Helper()
	var value struct {
		ConfirmationPosition string `json:"confirmation_position"`
	}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value.ConfirmationPosition
}

func TestParseWatchRejectsInvalidSelection(t *testing.T) {
	valid := `{"host":"github.com","owner":"owner","repository":"repo","pull_number":42,"waiting_reason":"review","represented_actor":"author"}`
	for _, test := range []struct {
		name, input string
		ok          bool
	}{{"valid", valid, true}, {"unknown", strings.Replace(valid, `"pull_number":42`, `"extra":true,"pull_number":42`, 1), false}, {"fraction", strings.Replace(valid, `42`, `42.0`, 1), false}, {"host", strings.Replace(valid, "github.com", "GitHub.com", 1), false}, {"separator", strings.Replace(valid, `"owner"`, `"own%2Fer"`, 2), false}, {"dot", strings.Replace(valid, `"repo"`, `".."`, 1), false}, {"oversized", strings.Replace(valid, `"review"`, `"`+strings.Repeat("a", 513)+`"`, 1), false}} {
		t.Run(test.name, func(t *testing.T) {
			value, err := jsonvalue.Parse([]byte(test.input), 8192)
			if err != nil {
				if test.ok {
					t.Fatal(err)
				}
				return
			}
			_, err = ParseWatch(value, "github.com")
			if (err == nil) != test.ok {
				t.Fatalf("error=%v want success=%v", err, test.ok)
			}
		})
	}
}

func TestSilentBaselineAndContentComparison(t *testing.T) {
	tr := tracker(t)
	a := comment("C1", "same")
	b := comment("C2", "other")
	observe(t, tr, testObservation(a, b))
	if text, _, err := tr.Context(); text != "" || err != nil {
		t.Fatalf("baseline context=%q err=%v", text, err)
	}
	changedTime := "2026-10-28T00:00:00Z"
	a.UpdatedAt = &changedTime
	observe(t, tr, testObservation(b, a))
	if tr.Saved.Sequence != 1 {
		t.Fatal("ordering or timestamps invented a change")
	}
	a.Body = "edited without timestamp change"
	observe(t, tr, testObservation(a, b))
	text, sequence := notification(t, tr)
	if sequence != 2 || !strings.Contains(text, a.Body) || !strings.Contains(text, `"unavailable"`) {
		t.Fatalf("context=%s sequence=%d", text, sequence)
	}
	observe(t, tr, testObservation(a, b))
	if len(tr.Saved.Journal) != 1 {
		t.Fatal("unchanged content duplicated journal")
	}
}

func TestRetainsRevertedTransitionsAndOlderPositions(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "initial")))
	text, p1 := notification(t, tr)
	pos := position(t, text)
	tr.Notice(p1, "accepted")
	observe(t, tr, testObservation(comment("C1", "edited")))
	observe(t, tr, testObservation(comment("C1", "initial")))
	sequence, err := tr.ValidatePosition(pos)
	if err != nil {
		t.Fatal(err)
	}
	tr.Resume(sequence)
	text, _ = notification(t, tr)
	if strings.Count(text, `"kind":"edited"`) != 2 {
		t.Fatalf("reverted transitions lost: %s", text)
	}
}

func TestRestoreSuppressesAcceptedNotificationRetainsAssessment(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "first")))
	text, p1 := notification(t, tr)
	tr.Notice(p1, "accepted")
	tr.Resume(p1)
	observe(t, tr, testObservation(comment("C1", "second")))
	_, p2 := notification(t, tr)
	tr.Notice(p2, "accepted")
	state, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(testConfig(), state)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Saved.Comparison == nil || *restored.Saved.Comparison != p1 {
		t.Fatal("assessment overwritten")
	}
	if text, _, err := restored.Context(); text != "" || err != nil {
		t.Fatalf("accepted repeated on restart: %s %v", text, err)
	}
	older, err := restored.ValidatePosition(position(t, text))
	if err != nil {
		t.Fatal(err)
	}
	restored.Resume(older)
	notification(t, restored)
	observe(t, restored, testObservation(comment("C1", "second"), comment("C2", "fresh")))
	if restored.Saved.Sequence != p2+1 {
		t.Fatal("fresh change lost")
	}
}

func TestResourceBudgetRejectsCandidateWithoutProgress(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	before, _ := tr.Checkpoint()
	items := []Item{}
	for i := 0; i < 33; i++ {
		items = append(items, comment(fmt.Sprint(i), "body"))
	}
	tr.ObservationBudget = 1024
	if err := tr.Reconcile(testObservation(items...)); err == nil || !strings.HasPrefix(err.Error(), "observation_resource_exhausted") {
		t.Fatalf("error=%v", err)
	}
	after, _ := tr.Checkpoint()
	if string(before.Marshal()) != string(after.Marshal()) {
		t.Fatal("rejected observation mutated saved state")
	}
	tr.ObservationBudget = DefaultByteBudget
	for i := 0; i < 9; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
	}
	text, sequence, err := tr.Context()
	if err != nil || text == "" || sequence < 2 || sequence > 9 || !strings.Contains(text, `"has_more":true`) {
		t.Fatalf("context=%s sequence=%d err=%v", text, sequence, err)
	}
}

func TestPositionsBoundToRegistrationAndRetainedHistory(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	positions := []string{}
	for i := 0; i < 6; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
		text, seq := notification(t, tr)
		positions = append(positions, position(t, text))
		tr.Notice(seq, "accepted")
		tr.Resume(seq)
	}
	if _, err := tr.ValidatePosition(positions[0]); err == nil || !strings.HasPrefix(err.Error(), "continuity_expired") {
		t.Fatalf("old position err=%v", err)
	}
	other, _ := NewTracker(testConfig(), []byte("different nonce!"))
	if _, err := other.ValidatePosition(positions[5]); err == nil || err.Error() != "position_foreign" {
		t.Fatalf("foreign err=%v", err)
	}
	for i := 6; i < 45; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
		_, seq := notification(t, tr)
		tr.Notice(seq, "accepted")
		tr.Resume(seq)
	}
	if len(tr.Saved.Journal) > 32 {
		t.Fatal("journal did not prune accepted history")
	}
}

func TestDisappearancePreservesProvenanceAfterRestore(t *testing.T) {
	tr := tracker(t)
	item := comment("C1", "original body")
	author := "reviewer"
	item.Author = &author
	observe(t, tr, testObservation(item))
	state, _ := tr.Checkpoint()
	tr, err := Restore(testConfig(), state)
	if err != nil {
		t.Fatal(err)
	}
	observe(t, tr, testObservation())
	text, _ := notification(t, tr)
	if !strings.Contains(text, item.URL) || !strings.Contains(text, author) || !strings.Contains(text, bodyDigest(item.Body)) || !strings.Contains(text, `"body_status":"unavailable"`) {
		t.Fatalf("missing disappeared provenance: %s", text)
	}
}

func TestLongBodyShorteningAndFullDigest(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	body := strings.Repeat("日本語", 2000)
	observe(t, tr, testObservation(comment("C1", body)))
	text, _ := notification(t, tr)
	if len(text) > 8192 || !strings.Contains(text, `"body_status":"shortened"`) || !strings.Contains(text, bodyDigest(body)) {
		t.Fatalf("invalid shortening: bytes=%d context=%s", len(text), text)
	}
}

func TestRestoreRejectsMissingUnreportedHistory(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	for i := 0; i < 3; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
	}
	notification(t, tr)
	for _, test := range []struct {
		name  string
		alter func(*Saved)
	}{{"empty journal", func(s *Saved) { s.Journal = []Change{} }}, {"missing suffix", func(s *Saved) { s.Journal = s.Journal[:len(s.Journal)-1] }}, {"missing required prefix", func(s *Saved) { s.Journal = s.Journal[1:] }}} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *tr
			candidate.Saved = tr.Saved
			candidate.Saved.Journal = append([]Change{}, tr.Saved.Journal...)
			test.alter(&candidate.Saved)
			state, err := candidate.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(testConfig(), state); err == nil {
				t.Fatal("missing history accepted")
			}
		})
	}
}

func TestPreparedOlderPositionPinsHistoryUntilGrant(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	for i := 0; i < 31; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
		_, seq := notification(t, tr)
		tr.Notice(seq, "accepted")
	}
	prepared := tr.Saved.Positions[0]
	tr.Prepare(prepared.Sequence)
	for i := 31; i < 33; i++ {
		observe(t, tr, testObservation(comment("C1", fmt.Sprint(i))))
	}
	if tr.Saved.Journal[0].Sequence != prepared.Sequence+1 {
		t.Fatalf("prepared history pruned: first=%d position=%d", tr.Saved.Journal[0].Sequence, prepared.Sequence)
	}
	if _, err := tr.ValidatePosition(prepared.Value); err != nil {
		t.Fatal(err)
	}
	state, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state.Marshal()), "prepared") {
		t.Fatal("staged position persisted")
	}
	if tr.Saved.Comparison != nil {
		t.Fatal("preparation changed committed comparison")
	}
	tr.Resume(prepared.Sequence)
	text, _ := notification(t, tr)
	if strings.Count(text, `"kind":"edited"`) != 5 {
		t.Fatalf("retained changes missing: %s", text)
	}
}

func TestRestoreRejectsConfirmationWithoutAttempt(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "first")))
	notification(t, tr)
	observe(t, tr, testObservation(comment("C1", "second")))
	tr.Saved.Confirmed = tr.Saved.Sequence
	state, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(testConfig(), state); err == nil {
		t.Fatal("unattempted change became confirmed")
	}
}

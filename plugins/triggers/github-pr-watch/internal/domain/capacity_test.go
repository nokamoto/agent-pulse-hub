package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestCapacityCompleteSnapshotAndOldChanges(t *testing.T) {
	for _, count := range []int{31, 32, 33, 100, 1000} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("items=%d/mixed=%t", count, mixed), func(t *testing.T) {
				tr := tracker(t)
				items := make([]Item, 0, count)
				for i := 0; i < count; i++ {
					item := comment(fmt.Sprintf("C%04d", i), "original")
					if mixed {
						switch i % 4 {
						case 1:
							item.Kind = "review"
							item.State = "APPROVED"
						case 2:
							item.Kind = "request"
							item.Reviewer = "reviewer"
						case 3:
							item.Kind = "thread"
							resolved := false
							item.Resolved = &resolved
						}
					}
					items = append(items, item)
				}
				observe(t, tr, testObservation(items...))
				observe(t, tr, testObservation(items...))
				if tr.Saved.Sequence != 1 {
					t.Fatal("unchanged snapshot created a transition")
				}
				items[0].Body = "old edit"
				items = append(items[:1], items[2:]...)
				items = append(items, comment("C1001", "new"))
				changed := testObservation(items...)
				changed.PR.Head = "H2"
				observe(t, tr, changed)
				if tr.Saved.Sequence != 5 {
					t.Fatalf("want four exact changes; got %d", tr.Saved.Sequence)
				}
				objects, err := tr.StreamCheckpoint()
				if err != nil {
					t.Fatal(err)
				}
				bytes := 0
				for _, object := range objects {
					n := len(object.Marshal())
					bytes += n
					if n > 32768 {
						t.Fatalf("object bytes=%d", n)
					}
					previousKey := ""
					for _, change := range tr.Saved.Journal {
						key := change.Item.Kind + ":" + change.Item.ID
						if key < previousKey {
							t.Fatal("simultaneous differences were not sorted before sequencing")
						}
						previousKey = key
					}
				}
				if count == 1000 && bytes <= 32768 {
					t.Fatal("large snapshot did not exceed the old envelope")
				}
				restored, err := RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
				if err != nil {
					t.Fatal(err)
				}
				before := restored.Saved.Sequence
				observe(t, restored, changed)
				if restored.Saved.Sequence != before {
					t.Fatal("restoration invented a change")
				}
				changed.Items[0].Body = "second old edit"
				observe(t, restored, changed)
				if restored.Saved.Sequence != before+1 {
					t.Fatal("old item stopped being compared")
				}
			})
		}
	}
}

func TestSimultaneousStateChangesPreservePreviousAfterSorting(t *testing.T) {
	tr := tracker(t)
	review := comment("R1", "review")
	review.Kind = "review"
	review.State = "APPROVED"
	observe(t, tr, testObservation(review))
	review.State = "DISMISSED"
	round := testObservation(review)
	round.PR.Head = "H2"
	round.PR.State = "CLOSED"
	observe(t, tr, round)
	if len(tr.Saved.Journal) != 3 {
		t.Fatal("missing simultaneous changes")
	}
	for i, kind := range []string{"head_changed", "lifecycle_changed", "state_changed"} {
		change := tr.Saved.Journal[i]
		if change.Sequence != uint64(i+2) || change.Kind != kind {
			t.Fatalf("change[%d]=%+v", i, change)
		}
		old := tr.previous[change.Sequence]
		want := map[string]string{"head_changed": "H1", "lifecycle_changed": "OPEN", "state_changed": "APPROVED"}[kind]
		if old.State != want {
			t.Fatalf("previous remapping for %s=%+v", kind, old)
		}
	}
}

func TestLegacyDecoderAcceptedNestedZeroFormsSurviveSecondRestore(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "first")))
	tr.Saved.Journal[0].At = ""
	tr.Saved.Journal[0].BodyStatus = ""
	tr.Saved.Journal[0].Item.Kind = ""
	legacy, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := legacy.Get("payload")
	migrated, err := RestoreStream(testConfig(), StateSchema, []*jsonvalue.Value{payload}, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := migrated.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	again, err := RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migrated.Saved, again.Saved) {
		t.Fatal("legacy accepted nested values changed")
	}
}

func TestLegacyEmptyKindDisappearanceSurvivesObservationAndRestart(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation(comment("legacy", "original")))
	tr.Saved.Fingerprints[0].Kind = ""
	tr.Saved.Metadata[0].Kind = ""
	legacy, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := legacy.Get("payload")
	migrated, err := RestoreStream(testConfig(), StateSchema, []*jsonvalue.Value{payload}, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := migrated.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	migrated, err = RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	// Include a normal new item in the same round to exercise legacy flag
	// association when simultaneous transitions receive sorted sequences.
	observe(t, migrated, testObservation(comment("new", "normal")))
	objects, err = migrated.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Saved.Journal) != 2 || restored.Saved.Journal[0].Kind != "disappeared" || restored.Saved.Journal[0].Item.ID != "legacy" || !restored.legacyChanges[2] || restored.legacyChanges[3] {
		t.Fatalf("legacy transition association lost: %+v", restored.Saved.Journal)
	}
}

func TestContinuationAndImmutableStreamAcrossRestart(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	items := []Item{}
	for i := 0; i < 40; i++ {
		body := ""
		if i%2 == 0 {
			body = strings.Repeat("日本語", 3000)
		}
		items = append(items, comment(fmt.Sprintf("C%03d", i), body))
	}
	observe(t, tr, testObservation(items...))
	next := uint64(2)
	batches := 0
	for {
		text, through, err := tr.Context()
		if err != nil {
			t.Fatal(err)
		}
		if text == "" {
			break
		}
		batches++
		if len(text) > 8192 {
			t.Fatalf("context bytes=%d", len(text))
		}
		var context struct {
			Changes      []Change `json:"changes"`
			Continuation struct {
				HasMore   bool   `json:"has_more"`
				Remaining int    `json:"remaining_changes"`
				Through   uint64 `json:"through_sequence"`
				Tail      uint64 `json:"observed_through_sequence"`
				Operation string `json:"operation"`
			} `json:"continuation"`
		}
		if err := json.Unmarshal([]byte(text), &context); err != nil {
			t.Fatal(err)
		}
		if len(context.Changes) == 0 || len(context.Changes) > 8 {
			t.Fatalf("prefix size=%d", len(context.Changes))
		}
		for _, c := range context.Changes {
			if c.Sequence != next {
				t.Fatalf("sequence=%d want %d", c.Sequence, next)
			}
			next++
		}
		if through != next-1 || context.Continuation.Through != through || context.Continuation.Tail != 41 || context.Continuation.Remaining != int(41-through) || context.Continuation.HasMore != (through < 41) {
			t.Fatalf("wrong continuation: %s", text)
		}
		if context.Continuation.HasMore && context.Continuation.Operation != "rewait" {
			t.Fatal("missing continuation operation")
		}
		tr.Notice(through, "accepted")
		objects, err := tr.StreamCheckpoint()
		if err != nil {
			t.Fatal(err)
		}
		frozen := make([]string, len(objects))
		for i, o := range objects {
			frozen[i] = string(o.Marshal())
		}
		tr.Resume(through)
		for i, o := range objects {
			if string(o.Marshal()) != frozen[i] {
				t.Fatal("snapshot mutated after cut")
			}
		}
		tr, err = RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
		if err != nil {
			t.Fatal(err)
		}
		// Restart re-arms through accepted delivery, without another observation.
		if through == 41 {
			if text, _, err := tr.Context(); text != "" || err != nil {
				t.Fatalf("confirmed prefix repeated: %s %v", text, err)
			}
			break
		}
	}
	if next != 42 || batches < 5 {
		t.Fatalf("next=%d batches=%d", next, batches)
	}
}

func TestHistoryBudgetPreservesCandidateAndContinuation(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "first")))
	text, seq := notification(t, tr)
	pos := position(t, text)
	tr.HistoryBudget = uint64(2)
	before := tr.Saved.Sequence
	if err := tr.Reconcile(testObservation(comment("C1", "second"))); err == nil || !strings.HasPrefix(err.Error(), "history_resource_exhausted") {
		t.Fatalf("error=%v", err)
	}
	if tr.Saved.Sequence != before {
		t.Fatal("failed candidate advanced progress")
	}
	if _, err := tr.ValidatePosition(pos); err != nil {
		t.Fatal(err)
	}
	tr.HistoryBudget = DefaultByteBudget
	text, again := notification(t, tr)
	if again != seq || !strings.Contains(text, "first") {
		t.Fatal("existing continuation lost")
	}
}

func TestLegacyMigrationPreservesAllProgress(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation())
	observe(t, tr, testObservation(comment("C1", "first")))
	text, seq := notification(t, tr)
	tr.Notice(seq, "accepted")
	tr.Resume(seq)
	observe(t, tr, testObservation(comment("C1", "second")))
	legacy, err := tr.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := legacy.Get("payload")
	restored, err := RestoreStream(testConfig(), StateSchema, []*jsonvalue.Value{payload}, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tr.Saved, restored.Saved) {
		t.Fatal("migration changed logical progress")
	}
	if _, err := restored.ValidatePosition(position(t, text)); err != nil {
		t.Fatal(err)
	}
	objects, err := restored.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	restored, err = RestoreStream(testConfig(), StreamStateSchema, objects, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	text, through := notification(t, restored)
	if through != 3 || !strings.Contains(text, "second") {
		t.Fatalf("pending migration suffix=%s", text)
	}
}

func TestStreamRejectsInconsistentObjects(t *testing.T) {
	tr := tracker(t)
	observe(t, tr, testObservation(comment("C1", "original")))
	observe(t, tr, testObservation(comment("C1", "edited")))
	notification(t, tr)
	objects, err := tr.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		alter func([]*jsonvalue.Value) []*jsonvalue.Value
	}{
		{"missing items", func(o []*jsonvalue.Value) []*jsonvalue.Value { return append(o[:1], o[2:]...) }},
		{"duplicate items", func(o []*jsonvalue.Value) []*jsonvalue.Value { return append(o, o[1]) }},
		{"reordered sections", func(o []*jsonvalue.Value) []*jsonvalue.Value { o[1], o[2] = o[2], o[1]; return o }},
		{"wrong count", func(o []*jsonvalue.Value) []*jsonvalue.Value {
			fields := o[0].Fields()
			fields["item_count"] = jsonvalue.NewNumber("99")
			o[0] = jsonvalue.NewObject(fields)
			return o
		}},
		{"missing header field", func(o []*jsonvalue.Value) []*jsonvalue.Value {
			fields := o[0].Fields()
			delete(fields, "change_count")
			o[0] = jsonvalue.NewObject(fields)
			return o
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := test.alter(append([]*jsonvalue.Value{}, objects...))
			if _, err := RestoreStream(testConfig(), StreamStateSchema, input, DefaultByteBudget, DefaultByteBudget); err == nil {
				t.Fatal("invalid stream accepted")
			}
		})
	}
}

func TestBudgetDecimalOptions(t *testing.T) {
	for _, s := range []string{"1", "0001", "67108864", "18446744073709551615"} {
		if _, err := ParseByteBudget(s); err != nil {
			t.Fatalf("valid %s: %v", s, err)
		}
	}
	for _, s := range []string{"", "0", "00", "+1", "-1", "1.0", "1e3", "18446744073709551616", " 1"} {
		if _, err := ParseByteBudget(s); err == nil {
			t.Fatalf("invalid %s", s)
		}
	}
}

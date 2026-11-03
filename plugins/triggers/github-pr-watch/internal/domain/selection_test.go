package domain

import (
	"fmt"
	"strings"
	"testing"
)

func TestContinuousSelectionRestoresOnlyUnselectedSuffix(t *testing.T) {
	tr := tracker(t)
	tr.StartContinuous()
	observe(t, tr, testObservation())
	var items []Item
	for i := range 9 {
		items = append(items, comment(fmt.Sprintf("C%02d", i), "new"))
	}
	observe(t, tr, testObservation(items...))
	text, prefix, err := tr.Select()
	if err != nil || prefix != 9 || !strings.Contains(text, `"remaining_changes":1`) || strings.Contains(text, `"operation"`) {
		t.Fatalf("prefix=%d err=%v context=%s", prefix, err, text)
	}
	tr.FinishSelection(prefix, "unknown")
	if tr.Saved.Confirmed != 1 {
		t.Fatal("unknown advanced accepted progress")
	}
	objects, err := tr.StreamCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStream(testConfig(), tr.StateSchema(), objects, DefaultByteBudget, DefaultByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	text, suffix, err := restored.Select()
	if err != nil || suffix != 10 || strings.Contains(text, "C00") || !strings.Contains(text, "C08") {
		t.Fatalf("suffix=%d err=%v context=%s", suffix, err, text)
	}
	items = append(items, comment("C09", "later observation"))
	observe(t, restored, testObservation(items...))
	if _, through, err := restored.Select(); err != nil || through != 0 {
		t.Fatalf("second selection was allowed before the terminal notice: %d, %v", through, err)
	}
	restored.FinishSelection(suffix, "discarded")
	if len(restored.Saved.Positions) != 1 || restored.Saved.Positions[0].Sequence != prefix {
		t.Fatal("discarded range changed retained positions")
	}
	if text, through, err := restored.Select(); err != nil || through != suffix+1 || !strings.Contains(text, "C08") || !strings.Contains(text, "C09") {
		t.Fatalf("discarded suffix unavailable: through=%d err=%v context=%s", through, err, text)
	}
}

func TestContinuousMigrationDoesNotUseLegacyComparisonAsSelection(t *testing.T) {
	for _, comparison := range []uint64{1, 3} {
		t.Run(fmt.Sprint(comparison), func(t *testing.T) {
			tr := tracker(t)
			observe(t, tr, testObservation())
			first := comment("C01", "first")
			observe(t, tr, testObservation(first))
			_, attempted := notification(t, tr)
			observe(t, tr, testObservation(first, comment("C02", "second")))
			tr.Saved.Comparison = &comparison
			objects, err := tr.StreamCheckpoint()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreStream(testConfig(), tr.StateSchema(), objects, DefaultByteBudget, DefaultByteBudget)
			if err != nil {
				t.Fatal(err)
			}
			restored.StartContinuous()
			if restored.cursor != attempted || restored.Saved.Confirmed != 1 || restored.Saved.Nonce != tr.Saved.Nonce {
				t.Fatal("legacy migration changed selection, acceptance, or identity")
			}
			text, through, err := restored.Select()
			if err != nil || through != 3 || strings.Contains(text, "C01") || !strings.Contains(text, "C02") {
				t.Fatalf("migration suffix=%d err=%v context=%s", through, err, text)
			}
		})
	}
}

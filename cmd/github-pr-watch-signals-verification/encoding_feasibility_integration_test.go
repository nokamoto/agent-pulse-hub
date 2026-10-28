//go:build integration

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This fixed fixture measures encoding and file round trips, not a production
// serializer, state validator, prefix selector, core commit or acceptance case.
// The real Session/rewait/restart paths are required by GPWS-I01/I02.
func TestSignalEncodingFeasibility(t *testing.T) {
	started := time.Now()
	position := func(seq uint64) string {
		data := make([]byte, 25)
		data[0] = 1
		binary.BigEndian.PutUint64(data[17:], seq)
		return "gpw1." + base64.RawURLEncoding.EncodeToString(data)
	}
	encode := func(value any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	items := make([]any, 0, 130)
	for i := range 128 {
		lastEpisode := 0
		if i >= 125 {
			lastEpisode = 3 + (i-125)*3
		}
		items = append(items, map[string]any{
			"category": "ci_check", "id": fmt.Sprintf("K%03d", i),
			"repository_id": "R1", "head": strings.Repeat("a", 40),
			"suite_id": "S1", "app_id": "A1", "name": strings.Repeat("n", 256),
			"description": strings.Repeat("d", 256), "status": "COMPLETED",
			"conclusion": "SUCCESS", "started_at": "2026-10-28T00:00:00Z",
			"completed_at": "2026-10-28T00:00:01Z", "latest": true,
			"last_episode": lastEpisode, "url": fmt.Sprintf("https://github.com/acme/widget/runs/%d", i),
		})
	}
	items = append(items,
		map[string]any{"category": "signal_conflict", "id": "PR17", "state": "MERGEABLE", "pair": "H1/B4", "period": 4, "confirmed": false, "last_episode": 10},
		map[string]any{"category": "signal_readiness", "id": "PR17", "isDraft": true, "last_episode": 11},
	)
	changes := make([]any, 0, 9)
	for i := range 9 {
		category := []string{"ci_check", "signal_conflict", "signal_readiness"}[i%3]
		record := map[string]any{
			"sequence": i + 3, "episode": i + 3, "category": category,
			"id":   "PR17",
			"head": strings.Repeat("a", 40), "base": strings.Repeat("b", 40),
			"observed_at": "2026-10-28T00:01:00Z", "url": "https://github.com/acme/widget/pull/17",
		}
		switch category {
		case "ci_check":
			record["id"] = fmt.Sprintf("K%03d", 125+i/3)
			record["source"] = "CheckRun"
			record["name"] = strings.Repeat("n", 256)
			record["previous"] = "SUCCESS"
			record["status"], record["conclusion"] = "COMPLETED", "FAILURE"
			record["started_at"], record["completed_at"] = "2026-10-28T00:00:00Z", "2026-10-28T00:00:01Z"
		case "signal_conflict":
			record["previous"], record["current"] = "MERGEABLE", "CONFLICTING"
			record["base_repository"], record["base_ref"], record["period"] = "R1", "main", 2+i/3
		case "signal_readiness":
			record["previous"], record["current"] = true, false
		}
		changes = append(changes, record)
	}
	header := map[string]any{
		"kind": "header", "version": 3, "signal_version": 1, "baseline": true,
		"scope": "acme/widget#17", "nonce": strings.Repeat("0", 32),
		"sequence": 11, "observation_sequence": 8, "item_count": len(items),
		"change_count": len(changes), "retained_first": 3, "retained_last": 11,
		"comparison": 2, "attempted": 10, "confirmed": 10,
		"positions": []string{position(2), position(10)},
	}
	objects := []any{header}
	// Fixed groups intentionally leave ample object headroom. They are not the
	// implementation's packing policy and are not a new supported item limit.
	for first := 0; first < len(items); first += 16 {
		objects = append(objects, map[string]any{"kind": "items", "items": items[first:min(first+16, len(items))]})
	}
	objects = append(objects, map[string]any{"kind": "changes", "changes": changes})
	directory := t.TempDir()
	total, largestObject, largestFrame := 0, 0, 0
	for index, object := range objects {
		data := encode(object)
		if len(data) > 32768 {
			t.Fatalf("fixture object %d exceeds stream bound: %d", index, len(data))
		}
		total += len(data)
		largestObject = max(largestObject, len(data))
		frame := encode(map[string]any{"version": 2, "type": "checkpoint_stream_chunk", "request_id": "request-1", "subscription_id": "subscription-1", "index": fmt.Sprint(index), "data": json.RawMessage(data)})
		largestFrame = max(largestFrame, len(frame))
		if len(frame) > 65536 {
			t.Fatal("fixture frame exceeds common message bound")
		}
		path := filepath.Join(directory, fmt.Sprintf("%020d.json", index))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		readback, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, readback) {
			t.Fatalf("object round trip: %v", err)
		}
	}
	if total <= 32768 {
		t.Fatal("fixture did not cross the old single-envelope boundary")
	}
	contextFor := func(records []any, through, remaining int) []byte {
		continuation := map[string]any{"through_sequence": through, "observed_through_sequence": 11, "has_more": remaining > 0, "remaining_changes": remaining}
		if remaining > 0 {
			continuation["operation"] = "rewait"
		}
		return encode(map[string]any{
			"external_data": true, "pr": "https://github.com/acme/widget/pull/17",
			"waiting_reason": strings.Repeat("w", 512), "represented_actor": strings.Repeat("a", 128),
			"changes": records, "confirmation_position": position(uint64(through)), "continuation": continuation,
			"current":   map[string]any{"head": strings.Repeat("a", 40), "lifecycle": "OPEN", "isDraft": true, "conflict": "MERGEABLE", "pair": "H1/B4", "ci": map[string]int{"COMPLETED/SUCCESS": 128}},
			"omissions": map[string]any{"ci_details": map[string]any{"total": 128, "represented_ids": []string{}, "omitted": 128, "url": "https://github.com/acme/widget/pull/17"}},
		})
	}
	prefix, suffix := contextFor(changes[:8], 10, 1), contextFor(changes[8:], 11, 0)
	if len(prefix) > 8192 || len(suffix) > 8192 {
		t.Fatalf("fixed eight/one contexts exceed bound: %d/%d", len(prefix), len(suffix))
	}
	// Retain the exact fixture suffix through JSON/file restoration, independent
	// of any future product filtering/selection logic.
	var restored map[string]json.RawMessage
	data, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("%020d.json", len(objects)-1)))
	if err != nil || json.Unmarshal(data, &restored) != nil {
		t.Fatalf("journal readback: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(restored["changes"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 || !bytes.Equal(entries[8], encode(changes[8])) {
		t.Fatal("fixture suffix was not retained exactly")
	}
	t.Logf("mechanism only: %d objects, %d total bytes, largest object/frame %d/%d, prefix/suffix %d/%d bytes, %s; no product acceptance", len(objects), total, largestObject, largestFrame, len(prefix), len(suffix), time.Since(started))
}

package domain

import (
	"errors"
	"sort"
)

// Context selects only a contiguous prefix. Nothing after the returned position
// is assessed or discarded; another explicit wait can select the retained tail.
func (t *Tracker) Context() (string, uint64, error) {
	count := 0
	selected := make([]Change, 0, 8)
	for _, c := range t.Saved.Journal {
		if c.Sequence > t.cursor {
			count++
			if len(selected) < 8 {
				selected = append(selected, c)
			}
		}
	}
	if count == 0 {
		return "", 0, nil
	}
	current := t.currentSummary()
	encode := func(changes []Change) ([]byte, error) {
		through := changes[len(changes)-1].Sequence
		continuation := map[string]any{"has_more": len(changes) < count, "remaining_changes": count - len(changes), "through_sequence": through, "observed_through_sequence": t.Saved.Sequence}
		if len(changes) < count {
			continuation["operation"] = "rewait"
		}
		views := make([]map[string]any, 0, len(changes))
		omissions := []any{}
		for _, c := range changes {
			view := changeView(c)
			if previous, ok := t.previous[c.Sequence]; ok {
				view["previous"] = previous
			} else if c.Kind != "created" {
				view["previous_unavailable"] = "not retained by the earlier state format"
			}
			views = append(views, view)
			if c.Omission != "" {
				omissions = append(omissions, map[string]any{"id": c.Item.ID, "reason": c.Omission, "url": c.Item.URL, "digest": c.Digest})
			}
		}
		observation := map[string]any{"started_at": t.Saved.StartedAt, "ended_at": t.Saved.EndedAt, "current_state": "latest_complete_observation", "current_may_be_newer_than_prefix": true}
		if t.RefreshFailure != "" {
			observation["refresh_failure"] = t.RefreshFailure
			observation["current_state"] = "last_complete_observation"
		}
		return compact(map[string]any{"external_data": true, "pr": t.Saved.Aggregate, "waiting_reason": t.Saved.Scope.WaitingReason, "represented_actor": t.Saved.Scope.RepresentedActor, "observation": observation, "changes": views, "current": current, "omissions": omissions, "confirmation_position": t.position(through), "continuation": continuation})
	}
	var raw []byte
	var err error
	// Prefer full current text if the matching acquired digest is still present.
	// A reverted or deleted transition keeps its own historical excerpt instead.
	for i := range selected {
		c := &selected[i]
		if t.current != nil {
			for _, item := range t.current.Items {
				if item.Kind == c.Item.Kind && item.ID == c.Item.ID && bodyDigest(item.Body) == c.Digest && c.Kind != "disappeared" {
					c.Item.Body = excerpt(item.Body, 4096)
					c.BodyStatus = "complete"
					c.Omission = ""
					if item.Body == "" {
						c.BodyStatus = "empty"
					}
					if c.Item.Body != item.Body {
						c.BodyStatus = "shortened"
						c.Omission = "body shortened for notification capacity; full acquired digest and source link retained"
					}
					break
				}
			}
		}
	}
	for {
		raw, err = encode(selected)
		if err != nil {
			return "", 0, err
		}
		if len(raw) <= 8192 {
			break
		}
		shortened := false
		for i := range selected {
			c := &selected[i]
			if len(c.Item.Body) > 0 {
				limit := 512
				if len(c.Item.Body) <= 512 {
					limit = 0
				}
				c.Item.Body = excerpt(c.Item.Body, limit)
				c.BodyStatus = "shortened"
				c.Omission = "body shortened for notification capacity; full acquired digest and source link retained"
				shortened = true
			}
		}
		if shortened {
			continue
		}
		if len(selected) == 1 {
			return "", 0, errors.New("notification_metadata_exhausted")
		}
		selected = selected[:len(selected)-1]
	}
	sequence := selected[len(selected)-1].Sequence
	position := t.position(sequence)
	positions := append([]Position{}, t.Saved.Positions...)
	found := false
	for _, p := range positions {
		if p.Value == position {
			found = true
		}
	}
	if !found {
		positions = append(positions, Position{position, sequence})
		sort.Slice(positions, func(i, j int) bool { return positions[i].Sequence < positions[j].Sequence })
		if len(positions) > 4 {
			positions = positions[len(positions)-4:]
		}
	}
	t.Saved.Positions = positions
	if sequence > t.Saved.Attempted {
		t.Saved.Attempted = sequence
	}
	return string(raw), sequence, nil
}

func (t *Tracker) currentSummary() map[string]any {
	current := map[string]any{"pr": t.Saved.Aggregate, "observed_at": t.Saved.EndedAt, "reviews": []any{}, "requests": []any{}, "threads": []any{}, "totals": map[string]int{}, "omitted_counts": map[string]int{}, "represented_ids": map[string][]string{}, "url": t.Saved.Aggregate.URL}
	for _, category := range []struct{ kind, key string }{{"review", "reviews"}, {"request", "requests"}, {"thread", "threads"}} {
		entries := []any{}
		ids := []string{}
		total := 0
		for _, item := range t.Saved.Metadata {
			if item.Kind != category.kind {
				continue
			}
			total++
			if len(entries) >= 2 {
				continue
			}
			entry := map[string]any{"id": item.ID, "state": item.State, "resolved": item.Resolved, "outdated": item.Outdated, "reviewer": item.Reviewer, "reviewer_id": item.ReviewerID, "reviewer_type": item.ReviewerType}
			encoded, _ := compact(entry)
			if len(encoded) > 384 {
				continue
			}
			entries = append(entries, entry)
			ids = append(ids, item.ID)
		}
		current[category.key] = entries
		current["totals"].(map[string]int)[category.key] = total
		current["omitted_counts"].(map[string]int)[category.key] = total - len(entries)
		current["represented_ids"].(map[string][]string)[category.key] = ids
	}
	return current
}

package domain

func ciResultView(result CIResult) map[string]any {
	class, _ := result.FailureClass()
	view := map[string]any{"source": result.Source, "id": result.ID, "repository_id": result.RepositoryID, "head": result.Head, "name": result.Name, "status": result.Status, "class": class, "details_url": result.DetailsURL}
	if result.DescriptionDigest != "" {
		view["description_digest"] = result.DescriptionDigest
		view["description_omission"] = "optional description omitted; inspect source link"
	}
	if result.DetailsURL == nil {
		view["details_unavailable"] = "provider did not supply a details link"
	}
	if result.Source == "ci_check" {
		view["suite_id"] = result.SuiteID
		view["app_id"] = result.AppID
		view["conclusion"] = result.Conclusion
		view["started_at"] = result.StartedAt
		view["completed_at"] = result.CompletedAt
		view["url"] = result.URL
		if result.URL == nil {
			view["url_unavailable"] = "provider did not supply a source link"
		}
		if result.StartedAt == nil || result.CompletedAt == nil {
			view["time_unavailable"] = "provider did not supply execution timestamps"
		}
	} else {
		view["created_at"] = result.CreatedAt
		view["updated_at"] = result.UpdatedAt
		if result.CreatedAt == nil || result.UpdatedAt == nil {
			view["time_unavailable"] = "provider did not supply status timestamps"
		}
	}
	return view
}

func (t *Tracker) signalChangeView(change Change) map[string]any {
	s := change.Signal
	view := map[string]any{"sequence": change.Sequence, "episode": s.Episode, "at": change.At, "category": s.Category, "id": s.ID, "url": change.Item.URL}
	switch s.Category {
	case "ci_check", "ci_status":
		view["ci"] = ciResultView(*s.CI)
		view["current_head"] = s.CI.Head == t.Saved.Aggregate.Head
		view["source_state"] = "absent"
		for _, item := range t.Saved.Metadata {
			if item.Kind != s.Category || item.ID != s.ID || item.Signal == nil || item.Signal.CI == nil {
				continue
			}
			r := item.Signal.CI
			if r.Head != s.CI.Head {
				continue
			}
			view["source_state"] = "superseded"
			if r.Selected && r.resultVersion() == s.CI.resultVersion() {
				view["source_state"] = "current"
			}
			view["current_result"] = map[string]any{"status": r.Status, "conclusion": r.Conclusion, "selected": r.Selected}
		}
		if s.PreviousCI != nil {
			view["previous_result"] = map[string]any{"status": s.PreviousCI.Status, "conclusion": s.PreviousCI.Conclusion, "started_at": s.PreviousCI.StartedAt, "completed_at": s.PreviousCI.CompletedAt}
		}
	case "signal_conflict":
		view["pair"] = s.Pair
		view["pair_period"] = s.PairPeriod
		view["previous"] = s.PreviousConflict
		view["current"] = s.Conflict
		view["interpretation"] = "GitHub observed mergeability report for acquired revisions"
	case "signal_readiness":
		view["previous_draft"] = s.PreviousDraft
		view["is_draft"] = s.Draft
	}
	return view
}

func (t *Tracker) signalSummary(current map[string]any) {
	state, err := signalStateFromItems(t.Saved.Metadata)
	if err != nil {
		return
	}
	current["is_draft"] = state.Readiness.Draft
	current["conflict"] = map[string]any{"state": state.Conflict.State, "pair": state.Observation.Pair, "base_exists": state.Observation.BaseExists, "last_comparable_pair": state.Conflict.Pair, "period": state.Conflict.Period, "confirmed_in_period": state.Conflict.Confirmed}
	groups := map[string]map[string]int{"ci_check": {}, "ci_status": {}}
	counts := map[string]int{"ci_check": 0, "ci_status": 0}
	for _, result := range state.Observation.CI {
		if !result.Selected {
			continue
		}
		key := result.Status
		if result.Conclusion != nil {
			key += "/" + *result.Conclusion
		}
		groups[result.Source][key]++
		counts[result.Source]++
	}
	current["ci"] = map[string]any{"head": t.Saved.Aggregate.Head, "result_groups": groups, "selected_totals": counts, "acquired_records": len(state.Observation.CI), "no_observable_results": len(state.Observation.CI) == 0, "details": []any{}, "omitted_count": counts["ci_check"] + counts["ci_status"], "represented_ids": []string{}, "url": t.Saved.Aggregate.URL}
	// Reserve the scalar summary before spending bytes on optional current lists.
	for _, key := range []string{"reviews", "requests", "threads"} {
		current[key] = []any{}
		current["omitted_counts"].(map[string]int)[key] = current["totals"].(map[string]int)[key]
		current["represented_ids"].(map[string][]string)[key] = []string{}
	}
}

func (t *Tracker) fillSignalDetails(current map[string]any, encode func() ([]byte, error)) {
	ci := current["ci"].(map[string]any)
	for _, item := range t.Saved.Metadata {
		if item.Signal == nil {
			key := map[string]string{"review": "reviews", "request": "requests", "thread": "threads"}[item.Kind]
			if key == "" {
				continue
			}
			entries := current[key].([]any)
			ids := current["represented_ids"].(map[string][]string)[key]
			current[key] = append(entries, map[string]any{"id": item.ID, "state": item.State, "resolved": item.Resolved, "outdated": item.Outdated, "reviewer": item.Reviewer, "reviewer_id": item.ReviewerID, "reviewer_type": item.ReviewerType})
			current["represented_ids"].(map[string][]string)[key] = append(ids, item.ID)
			current["omitted_counts"].(map[string]int)[key]--
			if raw, err := encode(); err != nil || len(raw) > 8192 {
				current[key] = entries
				current["represented_ids"].(map[string][]string)[key] = ids
				current["omitted_counts"].(map[string]int)[key]++
				break
			}
			continue
		}
		if item.Signal.CI == nil || !item.Signal.CI.Selected {
			continue
		}
		entries := ci["details"].([]any)
		ci["details"] = append(entries, ciResultView(*item.Signal.CI))
		ids := ci["represented_ids"].([]string)
		ci["represented_ids"] = append(ids, item.Kind+":"+item.ID)
		ci["omitted_count"] = ci["omitted_count"].(int) - 1
		raw, err := encode()
		if err != nil || len(raw) > 8192 {
			ci["details"] = entries
			ci["represented_ids"] = ids
			ci["omitted_count"] = ci["omitted_count"].(int) + 1
			break
		}
	}
}

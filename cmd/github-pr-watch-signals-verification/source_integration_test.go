//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type signalFixtureState struct {
	CI             int
	NameBytes      int
	Failed         bool
	StatusFailed   bool
	Conflict       string
	Base           int
	Draft          bool
	Comment        int
	Attempt        int
	Failure        string
	OldHead        bool
	AssociatedBase int
	Lifecycle      string
	FinalBase      int
}

func (f *gpwFixture) changeSignals(change func(*signalFixtureState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.signal)
}

func signalHead() string      { return strings.Repeat("a", 40) }
func signalBase(n int) string { return fmt.Sprintf("%040x", n+100) }

func (f *gpwFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	query := "REST"
	vars := map[string]any{}
	if r.Method == http.MethodPost {
		var call struct {
			Query     string
			Variables map[string]any
		}
		if json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&call) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		query, vars = call.Query, call.Variables
	}
	cursor, _ := vars["cursor"].(string)
	f.requests = append(f.requests, gpwRequest{r.Host, r.URL.Path, token, query, cursor})
	if !f.valid[token] {
		http.Error(w, "private-token-marker", 401)
		return
	}
	if r.Host == "ghes.example.test" {
		w.Header().Set("X-GitHub-Enterprise-Version", "3.19.4")
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(query, "PullRequestMetadata") {
		f.snapshot = f.signal
		f.signalReads = 0
	}
	s := f.snapshot
	if r.Method == http.MethodGet {
		if r.URL.Path != "/repos/acme/widget/commits/"+signalHead()+"/statuses" && r.URL.Path != "/api/v3/repos/acme/widget/commits/"+signalHead()+"/statuses" {
			http.Error(w, "foreign status path", 400)
			return
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || r.Header.Get("Accept") != "application/vnd.github+json" {
			http.Error(w, "missing headers", 400)
			return
		}
		page := r.URL.Query().Get("page")
		if page == "2" && s.Failure == "status-page" {
			http.Error(w, "private-status-marker", 500)
			return
		}
		state := "success"
		id := 20
		if s.StatusFailed && page == "1" {
			state = "failure"
		}
		if page == "2" {
			id = 10
		} else {
			prefix := "/repos/"
			if r.Host != "api.github.com" {
				prefix = "/api/v3/repos/"
			}
			w.Header().Set("Link", "<https://"+r.Host+prefix+"acme/widget/commits/"+signalHead()+"/statuses?per_page=100&page=2>; rel=\"next\"")
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"id": id, "context": "status", "state": state, "created_at": "2026-10-28T00:00:00Z", "updated_at": "2026-10-28T00:00:00Z", "target_url": nil, "description": nil}})
		return
	}
	associated := s.AssociatedBase
	if associated == 0 {
		associated = s.Base
	}
	lifecycle := s.Lifecycle
	if lifecycle == "" {
		lifecycle = "OPEN"
	}
	var mergedAt any
	if lifecycle == "MERGED" {
		mergedAt = "2026-10-29T12:50:38Z"
	}
	pr := map[string]any{"id": "PR17", "url": "https://github.com/acme/widget/pull/17", "headRefOid": signalHead(), "state": lifecycle, "mergedAt": mergedAt, "baseRepository": map[string]any{"id": "repo"}, "baseRefName": "main", "baseRefOid": signalBase(associated), "baseRef": map[string]any{"id": "main-ref", "target": map[string]any{"oid": signalBase(s.Base)}}, "mergeable": s.Conflict, "isDraft": s.Draft}
	data := map[string]any{}
	switch {
	case strings.Contains(query, "PullRequestSignals"):
		f.signalReads++
		if s.Failure == "pr-read" {
			http.Error(w, "private-conflict-marker", 500)
			return
		}
		if f.signalReads == 2 {
			if s.FinalBase != 0 {
				pr["baseRef"] = map[string]any{"id": "main-ref", "target": map[string]any{"oid": signalBase(s.FinalBase)}}
			}
			f.rounds++
		}
		data["repository"] = map[string]any{"id": "repo", "pullRequest": pr}
	case strings.Contains(query, "PullRequestCheckSuites"):
		head := signalHead()
		if s.OldHead {
			head = strings.Repeat("b", 40)
		}
		data["repository"] = map[string]any{"id": "repo", "object": map[string]any{"oid": signalHead(), "checkSuites": gpwConnection([]any{map[string]any{"id": "suite", "app": map[string]any{"id": "app"}, "commit": map[string]any{"oid": head}}}, false, "")}}
	case strings.Contains(query, "PullRequestCheckRuns"):
		start := 0
		if cursor != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(cursor, "ci:"))
		}
		if start > 0 && s.Failure == "check-page" {
			http.Error(w, "private-check-marker", 500)
			return
		}
		nodes := []any{}
		end := min(start+25, s.CI)
		for i := start; i < end; i++ {
			name := fmt.Sprintf("check-%03d", i)
			if i > 0 && s.NameBytes > 0 {
				name = strings.Repeat("n", s.NameBytes)
			}
			conclusion := "SUCCESS"
			if i == 0 && s.Failed {
				conclusion = "FAILURE"
			}
			at := fmt.Sprintf("2026-10-28T00:%02d:00Z", s.Attempt)
			nodes = append(nodes, map[string]any{"id": fmt.Sprintf("K%03d", i), "name": name, "status": "COMPLETED", "conclusion": conclusion, "startedAt": at, "completedAt": at, "detailsUrl": "https://ci.example.test/" + fmt.Sprint(i), "url": "https://github.com/checks/" + fmt.Sprint(i)})
		}
		data["node"] = map[string]any{"id": "suite", "app": map[string]any{"id": "app"}, "commit": map[string]any{"oid": signalHead()}, "checkRuns": gpwConnection(nodes, end < s.CI, fmt.Sprintf("ci:%d", end))}
	case strings.Contains(query, "PullRequestComments"):
		nodes := []any{}
		if s.Comment > 0 {
			nodes = append(nodes, gpwComment("C1", "conversation"))
		}
		pr["comments"] = gpwConnection(nodes, false, "")
		data["repository"] = map[string]any{"pullRequest": pr}
	case strings.Contains(query, "PullRequestReviews"):
		pr["reviews"] = gpwConnection([]any{}, false, "")
		data["repository"] = map[string]any{"pullRequest": pr}
	case strings.Contains(query, "PullRequestRequests"):
		pr["reviewRequests"] = gpwConnection([]any{}, false, "")
		data["repository"] = map[string]any{"pullRequest": pr}
	case strings.Contains(query, "PullRequestThreads"):
		pr["reviewThreads"] = gpwConnection([]any{}, false, "")
		data["repository"] = map[string]any{"pullRequest": pr}
	default:
		data["repository"] = map[string]any{"id": "repo", "pullRequest": pr}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func signalWaitState(t mvpReporter, h *gpwHost, id string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	var header map[string]any
	if !waitMVP(t, 3*time.Second, func() bool { header = gpwState(t, h.checkpoint(t, id, "state-barrier")); return ready(header) }) {
		t.Fatalf("signal state barrier: %v log=%s", header, readMVPFile(h.log))
	}
	return header
}

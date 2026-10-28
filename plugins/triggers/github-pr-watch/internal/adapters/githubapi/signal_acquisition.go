package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

func (c *Client) readSignalPR(ctx context.Context, config domain.WatchConfig, token string, total *int) (domain.Aggregate, domain.SignalObservation, error) {
	query := `query PullRequestSignals($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name,followRenames:false){id pullRequest(number:$number){` + signalPRFields + `}}}`
	data, err := c.request(ctx, config, token, query, variables(config), total)
	if err != nil {
		return domain.Aggregate{}, domain.SignalObservation{}, err
	}
	return parseSignalPR(data)
}

// acquireChecks pins the repository commit and every suite to the acquired head.
// ALL supplies the result history; LATEST supplies selection, never guessed age.
func (c *Client) acquireChecks(ctx context.Context, config domain.WatchConfig, token string, observed domain.SignalObservation, total *int) ([]domain.CIResult, error) {
	results := []domain.CIResult{}
	cursor := ""
	seenCursors := map[string]bool{}
	seenSuites := map[string]string{}
	seenRuns := map[string]domain.CIResult{}
	for {
		vars := variables(config)
		vars["head"] = observed.Pair.Head
		vars["cursor"] = nil
		if cursor != "" {
			vars["cursor"] = cursor
		}
		query := `query PullRequestCheckSuites($owner:String!,$name:String!,$head:String!,$cursor:String){repository(owner:$owner,name:$name,followRenames:false){id object(expression:$head){... on Commit {oid checkSuites(first:25,after:$cursor){nodes{id app{id} commit{oid}} ` + pageInfo + `}}}}}`
		data, err := c.request(ctx, config, token, query, vars, total)
		if err != nil {
			return nil, err
		}
		var repo struct {
			ID     string
			Object map[string]json.RawMessage
		}
		if json.Unmarshal(data["repository"], &repo) != nil || repo.ID != observed.RepositoryID || repo.Object == nil {
			return nil, failure("incomplete_retrieval")
		}
		var commit string
		if json.Unmarshal(repo.Object["oid"], &commit) != nil || commit != observed.Pair.Head {
			return nil, failure("observation_changed_during_acquisition")
		}
		nodes, more, end, err := connection(repo.Object["checkSuites"])
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			var suite struct {
				ID     string
				App    *struct{ ID string }
				Commit *struct{ OID string }
			}
			encoded, _ := json.Marshal(node)
			if json.Unmarshal(encoded, &suite) != nil || suite.ID == "" || suite.App == nil || suite.App.ID == "" || suite.Commit == nil {
				return nil, failure("incomplete_retrieval")
			}
			if suite.Commit.OID != observed.Pair.Head {
				return nil, failure("observation_changed_during_acquisition")
			}
			if previous, found := seenSuites[suite.ID]; found {
				if previous != string(encoded) {
					return nil, failure("incomplete_retrieval")
				}
				continue
			}
			seenSuites[suite.ID] = string(encoded)
			all, err := c.acquireRuns(ctx, config, token, observed, suite.ID, suite.App.ID, "ALL", total)
			if err != nil {
				return nil, err
			}
			latest, err := c.acquireRuns(ctx, config, token, observed, suite.ID, suite.App.ID, "LATEST", total)
			if err != nil {
				return nil, err
			}
			byID := map[string]int{}
			for i, run := range all {
				byID[run.ID] = i
			}
			for _, run := range latest {
				i, found := byID[run.ID]
				if !found || !reflect.DeepEqual(all[i], run) {
					return nil, failure("incomplete_retrieval")
				}
				all[i].Selected = true
			}
			for _, run := range all {
				if previous, found := seenRuns[run.ID]; found {
					if !reflect.DeepEqual(previous, run) {
						return nil, failure("incomplete_retrieval")
					}
					continue
				}
				seenRuns[run.ID] = run
				results = append(results, run)
			}
		}
		if !more {
			return results, nil
		}
		if seenCursors[end] {
			return nil, failure("pagination_failure")
		}
		seenCursors[end] = true
		cursor = end
	}
}

func (c *Client) acquireRuns(ctx context.Context, config domain.WatchConfig, token string, observed domain.SignalObservation, suite, app, filter string, total *int) ([]domain.CIResult, error) {
	results := []domain.CIResult{}
	seen := map[string]domain.CIResult{}
	cursor := ""
	cursors := map[string]bool{}
	for {
		vars := map[string]any{"suite": suite, "cursor": nil}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		query := `query PullRequestCheckRuns($suite:ID!,$cursor:String){node(id:$suite){... on CheckSuite {id app{id} commit{oid} checkRuns(first:25,after:$cursor,filterBy:{checkType:` + filter + `}){nodes{` + checkFields + `} ` + pageInfo + `}}}}`
		data, err := c.request(ctx, config, token, query, vars, total)
		if err != nil {
			return nil, err
		}
		var node struct {
			ID        string
			App       *struct{ ID string }
			Commit    *struct{ OID string }
			CheckRuns json.RawMessage
		}
		if json.Unmarshal(data["node"], &node) != nil || node.ID != suite || node.App == nil || node.App.ID != app || node.Commit == nil {
			return nil, failure("incomplete_retrieval")
		}
		if node.Commit.OID != observed.Pair.Head {
			return nil, failure("observation_changed_during_acquisition")
		}
		nodes, more, end, err := connection(node.CheckRuns)
		if err != nil {
			return nil, err
		}
		for _, raw := range nodes {
			run, err := parseCheck(raw, observed.RepositoryID, observed.Pair.Head, suite, app)
			if err != nil {
				return nil, err
			}
			if previous, found := seen[run.ID]; found {
				if !reflect.DeepEqual(previous, run) {
					return nil, failure("incomplete_retrieval")
				}
				continue
			}
			seen[run.ID] = run
			results = append(results, run)
		}
		if !more {
			return results, nil
		}
		if cursors[end] {
			return nil, failure("pagination_failure")
		}
		cursors[end] = true
		cursor = end
	}
}

// statusPage bounds each request and all decoded bytes before growing the
// candidate. The caller supplies the combined GraphQL/REST round accounting.
func (c *Client) statusPage(ctx context.Context, config domain.WatchConfig, token, head string, page uint64, budget uint64, used *uint64) ([]map[string]json.RawMessage, uint64, error) {
	if !validCommitOID(head) || page == 0 {
		return nil, 0, failure("incomplete_retrieval")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, statusEndpoint(config, head, page), nil)
	if err != nil {
		return nil, 0, failure("network")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "agent-pulse-hub-github-pr-watch")
	response, err := c.http.Do(req)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return nil, 0, failure("timeout")
		}
		return nil, 0, failure("network")
	}
	defer response.Body.Close()
	if *used > budget {
		return nil, 0, failure("observation_resource_exhausted")
	}
	limit := uint64(256 << 10)
	if remaining := budget - *used; remaining < limit {
		limit = remaining
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, 0, failure("network")
	}
	if len(body) > 256<<10 {
		return nil, 0, failure("coverage_capacity_exceeded")
	}
	if uint64(len(body)) > budget-*used {
		return nil, 0, failure("observation_resource_exhausted")
	}
	*used += uint64(len(body))
	retry := retryGuidance(response.Header, c.now())
	if response.StatusCode != http.StatusOK {
		code := "access_denied"
		switch {
		case response.StatusCode == 401:
			code = "authentication_rejected"
		case response.StatusCode == 404:
			code = "not_found_or_inaccessible"
		case response.StatusCode == 410 || response.StatusCode == 400 || response.StatusCode == 422:
			code = "unsupported_api"
		case response.StatusCode >= 500:
			code = "service"
		case response.StatusCode >= 300 && response.StatusCode < 400:
			code = "redirect_rejected"
		}
		lower := strings.ToLower(string(body))
		if response.StatusCode == 429 || (response.StatusCode == 403 && (response.Header.Get("X-RateLimit-Remaining") == "0" || !retry.IsZero() || strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limited") || strings.Contains(lower, "secondary rate"))) {
			code = "rate_limit"
		}
		return nil, 0, &application.Failure{Code: code, StatusCode: response.StatusCode, RetryAt: retry}
	}
	if config.Host != "github.com" && !strings.HasPrefix(response.Header.Get("X-GitHub-Enterprise-Version"), "3.19.") {
		return nil, 0, failure("unsupported_api")
	}
	if _, err := jsonvalue.Parse(body, 256<<10); err != nil {
		return nil, 0, failure("incomplete_retrieval")
	}
	var records []map[string]json.RawMessage
	if json.Unmarshal(body, &records) != nil || records == nil || len(records) > 100 {
		return nil, 0, failure("incomplete_retrieval")
	}
	for _, record := range records {
		if record == nil {
			return nil, 0, failure("incomplete_retrieval")
		}
	}
	next, err := nextStatusPage(strings.Join(response.Header.Values("Link"), ","), config, head, page)
	if err != nil {
		return nil, 0, err
	}
	return records, next, nil
}

func (c *Client) acquireStatuses(ctx context.Context, config domain.WatchConfig, token string, observed domain.SignalObservation, budget uint64, used *uint64) ([]domain.CIResult, error) {
	results := []domain.CIResult{}
	seen := map[string]domain.CIResult{}
	channels := map[string]bool{}
	page := uint64(1)
	for {
		records, next, err := c.statusPage(ctx, config, token, observed.Pair.Head, page, budget, used)
		if err != nil {
			return nil, err
		}
		for _, raw := range records {
			r, err := parseStatus(raw, observed.RepositoryID, observed.Pair.Head)
			if err != nil {
				return nil, err
			}
			if previous, found := seen[r.ID]; found {
				previous.Selected = false
				if !reflect.DeepEqual(previous, r) {
					return nil, failure("incomplete_retrieval")
				}
				continue
			}
			r.Selected = !channels[r.Name]
			channels[r.Name] = true
			seen[r.ID] = r
			results = append(results, r)
		}
		if next == 0 {
			return results, nil
		}
		page = next
	}
}

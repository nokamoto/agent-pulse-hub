package githubapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

const (
	pageInfo         = `pageInfo { hasNextPage endCursor }`
	discussionFields = `id body author { login } createdAt updatedAt url`
	reviewFields     = discussionFields + ` state submittedAt commit { oid }`
	commentFields    = discussionFields + ` state commit { oid } originalCommit { oid } pullRequestReview { id } replyTo { id }`
	requestFields    = `id requestedReviewer { __typename ... on User { id login } ... on Team { id name slug } ... on Mannequin { id login } }`
	threadFields     = `id isResolved isOutdated resolvedBy { login }`
	aggregateFields  = `id url headRefOid state mergedAt`
)

type Client struct {
	signals    bool
	http       *http.Client
	now        func() time.Time
	byteBudget uint64
}

func New() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	return NewWithHTTPClient(&http.Client{Transport: transport})
}

func NewWithHTTPClient(client *http.Client) *Client {
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copyClient, now: time.Now, byteBudget: domain.DefaultByteBudget}
}

func (c *Client) WithByteBudget(budget uint64) *Client { c.byteBudget = budget; return c }
func (c *Client) WithSignals(enabled bool) *Client     { c.signals = enabled; return c }
func (c *Client) budget() uint64 {
	if c.byteBudget == 0 {
		return domain.DefaultByteBudget
	}
	return c.byteBudget
}

func (c *Client) Validate(ctx context.Context, config domain.WatchConfig, token string) error {
	if c.signals {
		_, err := c.Observe(ctx, config, token)
		return err
	}
	query := `query ValidatePullRequest($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name,followRenames:false){pullRequest(number:$number){` + aggregateFields + ` reviews(first:1){nodes{` + reviewFields + `} ` + pageInfo + `} comments(first:1){nodes{` + discussionFields + `} ` + pageInfo + `} reviewRequests(first:1){nodes{` + requestFields + `} ` + pageInfo + `} reviewThreads(first:1){nodes{` + threadFields + ` comments(first:1){nodes{` + commentFields + `} ` + pageInfo + `}} ` + pageInfo + `}}}}`
	total := uint64(0)
	data, err := c.request(ctx, config, token, query, variables(config), &total)
	if err != nil {
		return err
	}
	pr, err := pull(data)
	if err != nil {
		return err
	}
	if _, err := parseAggregate(pr); err != nil {
		return err
	}
	for name, kind := range map[string]string{"reviews": "review", "comments": "conversation", "reviewRequests": "request", "reviewThreads": "thread"} {
		nodes, _, _, err := connection(pr[name])
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if _, _, err := parseItem(node, kind); err != nil {
				return err
			}
			if kind == "thread" {
				comments, _, _, err := connection(node["comments"])
				if err != nil {
					return err
				}
				for _, comment := range comments {
					if _, _, err := parseItem(comment, "review_comment"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (c *Client) Observe(ctx context.Context, config domain.WatchConfig, token string) (domain.Observation, error) {
	roundCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	result := domain.Observation{StartedAt: c.now(), Items: []domain.Item{}}
	total := uint64(0)
	candidateBytes := uint64(256)
	fields, repositoryFields := aggregateFields, ""
	if c.signals {
		fields, repositoryFields = signalPRFields, "id "
	}
	query := `query PullRequestMetadata($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name,followRenames:false){` + repositoryFields + `pullRequest(number:$number){` + fields + `}}}`
	data, err := c.request(roundCtx, config, token, query, variables(config), &total)
	if err != nil {
		return result, err
	}
	pr, err := pull(data)
	if err != nil {
		return result, err
	}
	result.PR, err = parseAggregate(pr)
	if err != nil {
		return result, err
	}
	var fence signalFence
	if c.signals {
		_, _, fence, err = parseSignalFence(data)
		if err != nil {
			return result, err
		}
		result.PR, result.Signals, err = c.beginSignals(roundCtx, config, token, &total, fence)
		if err != nil {
			return result, err
		}
	}
	for _, category := range []struct{ name, queryName, fields, kind string }{{"reviews", "PullRequestReviews", reviewFields, "review"}, {"comments", "PullRequestComments", discussionFields, "conversation"}, {"reviewRequests", "PullRequestRequests", requestFields, "request"}, {"reviewThreads", "PullRequestThreads", threadFields, "thread"}} {
		cursor := ""
		seenCursors := map[string]bool{}
		seenItems := map[string]string{}
		for {
			vars := variables(config)
			vars["cursor"] = nil
			if cursor != "" {
				vars["cursor"] = cursor
			}
			query := `query ` + category.queryName + `($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name,followRenames:false){pullRequest(number:$number){` + category.name + `(first:25,after:$cursor){nodes{` + category.fields + `} ` + pageInfo + `}}}}`
			data, err := c.request(roundCtx, config, token, query, vars, &total)
			if err != nil {
				return result, err
			}
			pr, err := pull(data)
			if err != nil {
				return result, err
			}
			nodes, next, end, err := connection(pr[category.name])
			if err != nil {
				return result, err
			}
			for _, node := range nodes {
				item, skip, err := parseItem(node, category.kind)
				if err != nil {
					return result, err
				}
				if skip {
					continue
				}
				if category.kind == "request" {
					item.URL = result.PR.URL
				}
				raw, _ := json.Marshal(node)
				if previous, seen := seenItems[item.ID]; seen {
					if previous != string(raw) {
						return result, failure("incomplete_retrieval")
					}
					continue
				}
				seenItems[item.ID] = string(raw)
				if category.kind == "thread" {
					comments, err := c.threadComments(roundCtx, config, token, item.ID, &total, &candidateBytes)
					if err != nil {
						return result, err
					}
					if len(comments) > 0 {
						item.URL = comments[0].URL
					}
					result.Items = append(result.Items, comments...)
				}
				if err := c.accountItem(item, &candidateBytes); err != nil {
					return result, err
				}
				result.Items = append(result.Items, item)
			}
			if !next {
				break
			}
			if end == "" || seenCursors[end] {
				return result, failure("pagination_failure")
			}
			seenCursors[end] = true
			cursor = end
		}
	}
	if c.signals {
		result.PR, err = c.finishSignals(roundCtx, config, token, result.Signals, &total, fence)
		if err != nil {
			return result, err
		}
	}
	result.EndedAt = c.now()
	return result, nil
}

func (c *Client) threadComments(ctx context.Context, config domain.WatchConfig, token, thread string, total, candidateBytes *uint64) ([]domain.Item, error) {
	result := []domain.Item{}
	cursor := ""
	seenCursors := map[string]bool{}
	seenItems := map[string]string{}
	for {
		query := `query PullRequestThreadComments($thread:ID!,$cursor:String){node(id:$thread){... on PullRequestReviewThread { comments(first:25,after:$cursor){nodes{` + commentFields + `} ` + pageInfo + `}}}}`
		vars := map[string]any{"thread": thread, "cursor": nil}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := c.request(ctx, config, token, query, vars, total)
		if err != nil {
			return nil, err
		}
		var node map[string]json.RawMessage
		if json.Unmarshal(data["node"], &node) != nil || node == nil {
			return nil, failure("incomplete_retrieval")
		}
		nodes, next, end, err := connection(node["comments"])
		if err != nil {
			return nil, err
		}
		for _, raw := range nodes {
			item, skip, err := parseItem(raw, "review_comment")
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			encoded, _ := json.Marshal(raw)
			if previous, seen := seenItems[item.ID]; seen {
				if previous != string(encoded) {
					return nil, failure("incomplete_retrieval")
				}
				continue
			}
			seenItems[item.ID] = string(encoded)
			item.Thread = thread
			if err := c.accountItem(item, candidateBytes); err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		if !next {
			break
		}
		if end == "" || seenCursors[end] {
			return nil, failure("pagination_failure")
		}
		seenCursors[end] = true
		cursor = end
	}
	return result, nil
}

func variables(c domain.WatchConfig) map[string]any {
	return map[string]any{"owner": c.Owner, "name": c.Repository, "number": c.PullNumber}
}
func failure(code string) error { return &application.Failure{Code: code} }

func (c *Client) accountItem(item domain.Item, used *uint64) error {
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(item); err != nil {
		return err
	}
	n := uint64(raw.Len())
	if *used > c.budget() || n > c.budget()-*used {
		return &domain.ResourceError{Code: "observation_resource_exhausted", Option: "--observation-byte-budget", Budget: c.budget(), Observed: *used + n}
	}
	*used += n
	return nil
}

func (c *Client) request(ctx context.Context, config domain.WatchConfig, token, query string, vars map[string]any, total *uint64) (map[string]json.RawMessage, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	endpoint := "https://" + config.Host + "/api/graphql"
	if config.Host == "github.com" {
		endpoint = "https://api.github.com/graphql"
	}
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, failure("network")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-pulse-hub-github-pr-watch")
	response, err := c.http.Do(req)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return nil, failure("timeout")
		}
		return nil, failure("network")
	}
	defer response.Body.Close()
	limit := uint64(256 << 10)
	if *total > c.budget() {
		return nil, &domain.ResourceError{Code: "observation_resource_exhausted", Option: "--observation-byte-budget", Budget: c.budget(), Observed: *total}
	}
	if remaining := c.budget() - *total; remaining < limit {
		limit = remaining
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, failure("network")
	}
	if len(body) > 256<<10 {
		return nil, failure("coverage_capacity_exceeded")
	}
	if uint64(len(body)) > c.budget()-*total {
		return nil, &domain.ResourceError{Code: "observation_resource_exhausted", Option: "--observation-byte-budget", Budget: c.budget(), Observed: *total + uint64(len(body))}
	}
	*total += uint64(len(body))
	retry := retryGuidance(response.Header, c.now())
	if response.StatusCode != http.StatusOK {
		code := "access_denied"
		switch {
		case response.StatusCode == 401:
			code = "authentication_rejected"
		case response.StatusCode == 404:
			code = "not_found_or_inaccessible"
		case response.StatusCode == 410:
			code = "unsupported_api"
		case response.StatusCode >= 500:
			code = "service"
		case response.StatusCode >= 300 && response.StatusCode < 400:
			code = "redirect_rejected"
		}
		lower := strings.ToLower(string(body))
		structuredRate := strings.Contains(lower, "rate_limited") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "secondary rate")
		if response.StatusCode == 429 || (response.StatusCode == 403 && (structuredRate || response.Header.Get("X-RateLimit-Remaining") == "0" || !retry.IsZero())) {
			code = "rate_limit"
		}
		return nil, &application.Failure{Code: code, StatusCode: response.StatusCode, RetryAt: retry}
	}
	if config.Host != "github.com" {
		version := response.Header.Get("X-GitHub-Enterprise-Version")
		if !strings.HasPrefix(version, "3.19.") {
			return nil, failure("unsupported_api")
		}
	}
	if _, err := jsonvalue.Parse(body, 256<<10); err != nil {
		return nil, failure("incomplete_retrieval")
	}
	var envelope struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []json.RawMessage          `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, failure("incomplete_retrieval")
	}
	if len(envelope.Errors) > 0 {
		code := "unsupported_api"
		for _, entry := range envelope.Errors {
			lower := strings.ToLower(string(entry))
			if strings.Contains(lower, "rate_limited") {
				code = "rate_limit"
			}
			if strings.Contains(lower, "forbidden") {
				code = "access_denied"
			}
		}
		return nil, &application.Failure{Code: code, StatusCode: response.StatusCode, RetryAt: retry}
	}
	if envelope.Data == nil {
		return nil, failure("incomplete_retrieval")
	}
	return envelope.Data, nil
}

func retryGuidance(header http.Header, now time.Time) time.Time {
	retry := time.Time{}
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
		const maxSeconds = int64((1<<63 - 1) / int64(time.Second))
		if seconds > maxSeconds {
			seconds = maxSeconds
		}
		retry = now.Add(time.Duration(seconds) * time.Second)
	} else if at, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		retry = at
	}
	if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && header.Get("X-RateLimit-Remaining") == "0" {
		if at := time.Unix(reset, 0); at.After(retry) {
			retry = at
		}
	}
	return retry
}

func pull(data map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	var repository map[string]json.RawMessage
	var pr map[string]json.RawMessage
	if json.Unmarshal(data["repository"], &repository) != nil || repository == nil || json.Unmarshal(repository["pullRequest"], &pr) != nil || pr == nil {
		return nil, failure("not_found_or_inaccessible")
	}
	return pr, nil
}

func parseAggregate(raw map[string]json.RawMessage) (domain.Aggregate, error) {
	var value struct {
		ID         string
		URL        string
		HeadRefOid string
		State      string
		MergedAt   *string
	}
	encoded, _ := json.Marshal(raw)
	if json.Unmarshal(encoded, &value) != nil || value.ID == "" || value.URL == "" || value.HeadRefOid == "" || (value.State != "OPEN" && value.State != "CLOSED" && value.State != "MERGED") {
		return domain.Aggregate{}, failure("incomplete_retrieval")
	}
	return domain.Aggregate{ID: value.ID, URL: value.URL, Head: value.HeadRefOid, State: value.State, MergedAt: value.MergedAt}, nil
}

func connection(raw json.RawMessage) ([]map[string]json.RawMessage, bool, string, error) {
	var value struct {
		Nodes    *[]map[string]json.RawMessage `json:"nodes"`
		PageInfo *struct {
			HasNextPage *bool   `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Nodes == nil || value.PageInfo == nil || value.PageInfo.HasNextPage == nil || len(*value.Nodes) > 25 {
		return nil, false, "", failure("pagination_failure")
	}
	for _, node := range *value.Nodes {
		if node == nil {
			return nil, false, "", failure("incomplete_retrieval")
		}
	}
	end := ""
	if value.PageInfo.EndCursor != nil {
		end = *value.PageInfo.EndCursor
	}
	if *value.PageInfo.HasNextPage && (end == "" || len(*value.Nodes) == 0) {
		return nil, false, "", failure("pagination_failure")
	}
	return *value.Nodes, *value.PageInfo.HasNextPage, end, nil
}

func parseItem(raw map[string]json.RawMessage, kind string) (domain.Item, bool, error) {
	var value struct {
		ID                string
		Body              *string
		Author            *struct{ Login string }
		CreatedAt         *string
		UpdatedAt         *string
		SubmittedAt       *string
		URL               string
		State             string
		Commit            *struct{ Oid string }
		OriginalCommit    *struct{ Oid string }
		PullRequestReview *struct{ ID string }
		ReplyTo           *struct{ ID string }
		IsResolved        *bool
		IsOutdated        *bool
		ResolvedBy        *struct{ Login string }
		RequestedReviewer *struct {
			Typename string `json:"__typename"`
			ID       string
			Login    string
			Name     string
			Slug     string
		}
	}
	encoded, _ := json.Marshal(raw)
	if json.Unmarshal(encoded, &value) != nil || value.ID == "" {
		return domain.Item{}, false, failure("incomplete_retrieval")
	}
	if value.State == "PENDING" {
		return domain.Item{}, true, nil
	}
	required := []string{"id"}
	switch kind {
	case "review":
		required = append(required, "body", "author", "createdAt", "updatedAt", "submittedAt", "commit", "url", "state")
	case "conversation":
		required = append(required, "body", "author", "createdAt", "updatedAt", "url")
	case "review_comment":
		required = append(required, "body", "author", "createdAt", "updatedAt", "url", "state", "commit", "originalCommit", "pullRequestReview", "replyTo")
	case "thread":
		required = append(required, "isResolved", "isOutdated", "resolvedBy")
	case "request":
		required = append(required, "requestedReviewer")
	}
	for _, key := range required {
		if _, ok := raw[key]; !ok {
			return domain.Item{}, false, failure("incomplete_retrieval")
		}
	}
	item := domain.Item{ID: value.ID, Kind: kind, Body: "", CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, SubmittedAt: value.SubmittedAt, URL: value.URL, State: value.State, Resolved: value.IsResolved, Outdated: value.IsOutdated}
	if value.Author != nil {
		item.Author = &value.Author.Login
	}
	if value.Body != nil {
		item.Body = *value.Body
	}
	if value.Commit != nil {
		item.Commit = &value.Commit.Oid
	}
	if value.OriginalCommit != nil {
		item.OriginalCommit = &value.OriginalCommit.Oid
	}
	if value.PullRequestReview != nil {
		item.Review = &value.PullRequestReview.ID
	}
	if value.ReplyTo != nil {
		item.ReplyTo = &value.ReplyTo.ID
	}
	if value.ResolvedBy != nil {
		item.ResolvedBy = &value.ResolvedBy.Login
	}
	switch kind {
	case "review", "conversation", "review_comment":
		if value.Body == nil || value.URL == "" {
			return item, false, failure("incomplete_retrieval")
		}
	case "thread":
		if value.IsResolved == nil || value.IsOutdated == nil {
			return item, false, failure("incomplete_retrieval")
		}
	case "request":
		reviewer := value.RequestedReviewer
		if reviewer == nil || reviewer.ID == "" {
			return item, false, failure("incomplete_retrieval")
		}
		item.ReviewerType = reviewer.Typename
		item.ReviewerID = reviewer.ID
		switch reviewer.Typename {
		case "User", "Mannequin":
			item.Reviewer = reviewer.Login
		case "Team":
			item.Reviewer = reviewer.Slug
			if item.Reviewer == "" {
				item.Reviewer = reviewer.Name
			}
		default:
			return item, false, failure("unsupported_api")
		}
		if item.Reviewer == "" {
			return item, false, failure("incomplete_retrieval")
		}
	}
	return item, false, nil
}

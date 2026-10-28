package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"go.uber.org/mock/gomock"
)

func acquisitionFixture() (domain.WatchConfig, domain.SignalObservation) {
	config := domain.WatchConfig{Host: "github.com", Owner: "acme", Repository: "widget", PullNumber: 17}
	observed := domain.SignalObservation{RepositoryID: "repo", Pair: domain.ConflictPair{PRID: "pr", Head: strings.Repeat("a", 40), BaseRepositoryID: "repo", BaseRef: "main", BaseSHA: strings.Repeat("b", 40)}, BaseExists: true, Mergeable: "UNKNOWN", Draft: true}
	return config, observed
}

func checkRaw(id, conclusion string) map[string]any {
	return map[string]any{"id": id, "name": "same name", "status": "COMPLETED", "conclusion": conclusion, "startedAt": nil, "completedAt": nil, "detailsUrl": nil, "url": nil}
}

func connectionRaw(nodes []map[string]any, more bool, cursor any) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor}}
}

func TestAcquireChecksCompleteAndSelected(t *testing.T) {
	for _, variant := range []string{"complete", "latest absent from all", "latest field mismatch", "wrong suite commit", "duplicate conflict", "cursor loop"} {
		t.Run(variant, func(t *testing.T) {
			config, observed := acquisitionFixture()
			ctrl := gomock.NewController(t)
			transport := NewMockRoundTripper(ctrl)
			calls := 0
			transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 10 {
					t.Fatal("unbounded page loop")
				}
				if req.URL.String() != "https://api.github.com/graphql" || req.Header.Get("Authorization") != "Bearer secret" {
					t.Fatal("origin/credential changed")
				}
				var request struct {
					Query     string
					Variables map[string]any
				}
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				data := map[string]any{}
				if strings.Contains(request.Query, "PullRequestCheckSuites") {
					if request.Variables["head"] != observed.Pair.Head {
						t.Fatal("head not pinned")
					}
					head := observed.Pair.Head
					if variant == "wrong suite commit" {
						head = strings.Repeat("c", 40)
					}
					data["repository"] = map[string]any{"id": "repo", "object": map[string]any{"oid": observed.Pair.Head, "checkSuites": connectionRaw([]map[string]any{{"id": "suite", "app": map[string]any{"id": "app"}, "commit": map[string]any{"oid": head}}}, false, nil)}}
				} else {
					if request.Variables["suite"] != "suite" || !strings.Contains(request.Query, "checkType:") {
						t.Fatal("suite query missing")
					}
					latest := strings.Contains(request.Query, "checkType:LATEST")
					nodes := []map[string]any{checkRaw("K1", "FAILURE")}
					more := false
					var cursor any
					if latest {
						nodes = []map[string]any{checkRaw("K2", "SUCCESS")}
						if variant == "latest absent from all" {
							nodes[0]["id"] = "missing"
						}
						if variant == "latest field mismatch" {
							nodes[0]["conclusion"] = "FAILURE"
						}
					} else if request.Variables["cursor"] == nil {
						more = true
						cursor = "c1"
					} else {
						nodes = []map[string]any{checkRaw("K2", "SUCCESS")}
						if variant == "duplicate conflict" {
							nodes[0]["id"] = "K1"
						}
						if variant == "cursor loop" {
							more = true
							cursor = "c1"
						}
					}
					data["node"] = map[string]any{"id": "suite", "app": map[string]any{"id": "app"}, "commit": map[string]any{"oid": observed.Pair.Head}, "checkRuns": connectionRaw(nodes, more, cursor)}
				}
				body, _ := json.Marshal(map[string]any{"data": data})
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
			}).AnyTimes()
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			total := 0
			results, err := client.acquireChecks(context.Background(), config, "secret", observed, &total)
			if variant != "complete" {
				if err == nil || results != nil {
					t.Fatal("partial acquisition accepted", results, err)
				}
				return
			}
			if err != nil || len(results) != 2 || results[0].Selected || !results[1].Selected || results[0].ID != "K1" || results[1].ID != "K2" || total == 0 {
				t.Fatal(results, err, total)
			}
		})
	}
}

func statusRaw(id int, state string) map[string]any {
	return map[string]any{"id": id, "context": "build", "state": state, "created_at": nil, "updated_at": nil, "target_url": nil, "description": nil}
}

func TestAcquireStatusesPaginationAndIdentity(t *testing.T) {
	for _, variant := range []string{"complete", "foreign next", "duplicate conflict", "page error", "malformed", "null page", "budget"} {
		t.Run(variant, func(t *testing.T) {
			config, observed := acquisitionFixture()
			ctrl := gomock.NewController(t)
			transport := NewMockRoundTripper(ctrl)
			calls := 0
			transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 2 {
					t.Fatal("unexpected request")
				}
				if req.Method != "GET" || req.Header.Get("Authorization") != "Bearer secret" || req.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || req.Header.Get("Accept") != "application/vnd.github+json" || req.URL.String() != statusEndpoint(config, observed.Pair.Head, uint64(calls)) {
					t.Fatal("status origin/headers changed")
				}
				if deadline, ok := req.Context().Deadline(); !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("request deadline missing")
				}
				headers := http.Header{}
				status := 200
				records := []map[string]any{statusRaw(2, "success")}
				if calls == 1 {
					headers.Set("Link", "<"+statusEndpoint(config, observed.Pair.Head, 2)+">; rel=\"next\"")
				} else {
					records = []map[string]any{statusRaw(1, "failure")}
				}
				if variant == "foreign next" {
					headers.Set("Link", "<https://evil.test/>; rel=\"next\"")
				}
				if variant == "duplicate conflict" && calls == 2 {
					records[0]["id"] = 2
				}
				if variant == "page error" && calls == 2 {
					status = 403
				}
				body, _ := json.Marshal(records)
				if variant == "malformed" {
					body = []byte(`[{"id":1,"id":2}]`)
				}
				if variant == "null page" {
					body = []byte("null")
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
			}).AnyTimes()
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			used := uint64(10)
			budget := uint64(1 << 20)
			if variant == "budget" {
				budget = 11
			}
			results, err := client.acquireStatuses(context.Background(), config, "secret", observed, budget, &used)
			if variant != "complete" {
				if err == nil || results != nil {
					t.Fatal("partial status coverage accepted", results, err)
				}
				return
			}
			if err != nil || len(results) != 2 || !results[0].Selected || results[1].Selected || results[0].ID != "2" || results[1].ID != "1" || used <= 10 {
				t.Fatal(results, err)
			}
		})
	}
}

func TestStatusResponseErrorsAreRedacted(t *testing.T) {
	for _, test := range []struct {
		status     int
		headers    http.Header
		body, code string
	}{
		{401, nil, `{"message":"secret"}`, "authentication_rejected"},
		{403, nil, `{"message":"secret"}`, "access_denied"},
		{403, http.Header{"X-Ratelimit-Remaining": []string{"0"}}, `{"message":"secret"}`, "rate_limit"},
		{403, nil, `{"message":"secondary rate limit secret"}`, "rate_limit"},
		{404, nil, `{"message":"secret"}`, "not_found_or_inaccessible"},
		{410, nil, `{"message":"secret"}`, "unsupported_api"},
		{429, nil, `{"message":"secret"}`, "rate_limit"},
		{500, nil, `{"message":"secret"}`, "service"},
		{302, http.Header{"Location": []string{"https://evil.test"}}, `secret`, "redirect_rejected"},
	} {
		t.Run(test.code, func(t *testing.T) {
			config, observed := acquisitionFixture()
			ctrl := gomock.NewController(t)
			transport := NewMockRoundTripper(ctrl)
			transport.EXPECT().RoundTrip(gomock.Any()).Return(&http.Response{StatusCode: test.status, Header: test.headers, Body: io.NopCloser(strings.NewReader(test.body))}, nil)
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			used := uint64(0)
			_, _, err := client.statusPage(context.Background(), config, "secret", observed.Pair.Head, 1, 1<<20, &used)
			var failed *application.Failure
			if !errors.As(err, &failed) || failed.Code != test.code || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}

func TestReadSignalPRSelectedOrigin(t *testing.T) {
	config, observed := acquisitionFixture()
	ctrl := gomock.NewController(t)
	transport := NewMockRoundTripper(ctrl)
	transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
		var request struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if req.URL.String() != "https://api.github.com/graphql" || req.Header.Get("Authorization") != "Bearer secret" || request.Variables["owner"] != "acme" || request.Variables["number"] != float64(17) || !strings.Contains(request.Query, "baseRef { id target { oid } }") {
			t.Fatal("PR request missing selected fields or scope")
		}
		data := map[string]any{"data": map[string]any{"repository": map[string]any{"id": "repo", "pullRequest": map[string]any{"id": "pr", "url": "https://github.com/acme/widget/pull/17", "headRefOid": observed.Pair.Head, "state": "OPEN", "mergedAt": nil, "baseRepository": map[string]any{"id": "repo"}, "baseRefName": "main", "baseRefOid": observed.Pair.BaseSHA, "baseRef": map[string]any{"id": "ref", "target": map[string]any{"oid": observed.Pair.BaseSHA}}, "mergeable": "UNKNOWN", "isDraft": true}}}}
		body, _ := json.Marshal(data)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	})
	client := NewWithHTTPClient(&http.Client{Transport: transport})
	total := 0
	aggregate, result, err := client.readSignalPR(context.Background(), config, "secret", &total)
	if err != nil || aggregate.ID != "pr" || result.Pair != observed.Pair || !result.Draft || total == 0 {
		t.Fatal(aggregate, result, err)
	}
}

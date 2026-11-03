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
	"go.uber.org/mock/gomock"
)

func TestValidateSignalsAccess(t *testing.T) {
	for _, test := range []struct {
		name      string
		wantCode  string
		wantCalls int
	}{
		{"capabilities available", "", 3},
		{"no check suites", "", 3},
		{"missing discussion", "pagination_failure", 1},
		{"checks denied", "access_denied", 2},
		{"null latest runs", "pagination_failure", 2},
		{"statuses denied", "access_denied", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, observed := acquisitionFixture()
			head := observed.Pair.Head
			ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			transport := NewMockRoundTripper(gomock.NewController(t))
			calls := 0
			transport.EXPECT().RoundTrip(gomock.Any()).Times(test.wantCalls).DoAndReturn(func(req *http.Request) (*http.Response, error) {
				calls++
				if got, ok := req.Context().Deadline(); !ok || !got.Equal(deadline) {
					t.Fatal("request did not preserve registration deadline")
				}
				if req.URL.Host != "api.github.com" || req.Header.Get("Authorization") != "Bearer secret" {
					t.Fatal("request changed origin or credential")
				}
				if calls == 3 {
					if req.Method != "GET" || req.URL.String() != statusEndpoint(config, head, 1) {
						t.Fatal("wrong status probe")
					}
					if test.name == "statuses denied" {
						return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
					}
					header := http.Header{"Link": {"<" + statusEndpoint(config, head, 2) + ">; rel=\"next\""}}
					body := `[{"id":1,"context":"build","state":"success","created_at":null,"updated_at":null,"target_url":null,"description":null}]`
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
				}

				var call struct {
					Query     string
					Variables map[string]any
				}
				if req.Method != "POST" || json.NewDecoder(req.Body).Decode(&call) != nil {
					t.Fatal("invalid GraphQL request")
				}
				repo := map[string]any{"id": observed.RepositoryID}
				response := map[string]any{"data": map[string]any{"repository": repo}}
				if calls == 1 {
					for _, field := range []string{"reviews(first:1)", "comments(first:1)", "reviewRequests(first:1)", "reviewThreads(first:1)", "mergeable", "isDraft"} {
						if !strings.Contains(call.Query, field) {
							t.Fatalf("missing PR probe field %s", field)
						}
					}
					pr := map[string]any{"id": "pr", "url": "https://github.com/acme/widget/pull/17", "state": "OPEN", "headRefOid": head, "mergedAt": nil, "baseRepository": map[string]any{"id": "repo"}, "baseRefName": "main", "baseRefOid": observed.Pair.BaseSHA, "baseRef": nil, "mergeable": "UNKNOWN", "isDraft": true}
					for _, field := range []string{"reviews", "comments", "reviewRequests", "reviewThreads"} {
						pr[field] = connectionRaw([]map[string]any{}, false, nil)
					}
					if test.name == "missing discussion" {
						pr["reviews"] = nil
					}
					repo["pullRequest"] = pr
				} else {
					if call.Variables["head"] != head || !strings.Contains(call.Query, "checkSuites(first:1)") ||
						!strings.Contains(call.Query, "all:checkRuns(first:1,filterBy:{checkType:ALL})") ||
						!strings.Contains(call.Query, "latest:checkRuns(first:1,filterBy:{checkType:LATEST})") {
						t.Fatal("check probe did not pin the head and exercise both filters")
					}
					suites := []map[string]any{}
					if test.name != "no check suites" {
						runs := connectionRaw([]map[string]any{checkRaw("run", "SUCCESS")}, true, "next-run")
						suite := map[string]any{"id": "suite", "app": map[string]any{"id": "app"}, "commit": map[string]any{"oid": head}, "all": runs, "latest": runs}
						if test.name == "null latest runs" {
							suite["latest"] = nil
						}
						suites = append(suites, suite)
					}
					repo["object"] = map[string]any{"oid": head, "checkSuites": connectionRaw(suites, len(suites) > 0, "next-suite")}
					if test.name == "checks denied" {
						response["errors"] = []any{map[string]any{"type": "FORBIDDEN", "message": "private detail"}}
					}
				}
				body, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			client := NewWithHTTPClient(&http.Client{Transport: transport}).WithSignals(true)
			err := client.Validate(ctx, config, "secret")
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var failure *application.Failure
				if !errors.As(err, &failure) || failure.Code != test.wantCode {
					t.Fatalf("validation error = %v, want %s", err, test.wantCode)
				}
			}
		})
	}
}

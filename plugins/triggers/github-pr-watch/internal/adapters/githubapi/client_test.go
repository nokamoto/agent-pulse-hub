package githubapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"go.uber.org/mock/gomock"
)

//go:generate go run go.uber.org/mock/mockgen -destination=transport_mock_test.go -package=githubapi net/http RoundTripper

func TestRequestOriginBoundAndFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name, host, body string
		status           int
		headers          http.Header
		code             string
	}{{"public", "github.com", `{"data":{"repository":{}}}`, 200, nil, ""}, {"enterprise", "ghes.test", `{"data":{"repository":{}}}`, 200, http.Header{"X-Github-Enterprise-Version": []string{"3.19.8"}}, ""}, {"unsupported enterprise", "ghes.test", `{"data":{}}`, 200, nil, "unsupported_api"}, {"auth", "github.com", `{"message":"private token detail"}`, 401, nil, "authentication_rejected"}, {"concealed", "github.com", `{}`, 404, nil, "not_found_or_inaccessible"}, {"permission", "github.com", `{}`, 403, nil, "access_denied"}, {"rate", "github.com", `{"message":"API rate limit exceeded"}`, 403, http.Header{"X-Ratelimit-Remaining": []string{"0"}, "Retry-After": []string{"120"}}, "rate_limit"}, {"redirect", "github.com", `{}`, 302, http.Header{"Location": []string{"https://evil.test"}}, "redirect_rejected"}, {"partial errors", "github.com", `{"data":{},"errors":[{"type":"FORBIDDEN","message":"private body"}]}`, 200, nil, "access_denied"}, {"page bound", "github.com", strings.Repeat("x", (256<<10)+1), 200, nil, "coverage_capacity_exceeded"}} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			transport := NewMockRoundTripper(ctrl)
			transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
				want := "https://" + test.host + "/api/graphql"
				if test.host == "github.com" {
					want = "https://api.github.com/graphql"
				}
				if req.URL.String() != want || req.Header.Get("Authorization") != "Bearer secret" || req.Method != http.MethodPost {
					t.Fatalf("unexpected request origin/header")
				}
				if deadline, ok := req.Context().Deadline(); !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("request deadline missing")
				}
				return &http.Response{StatusCode: test.status, Header: test.headers, Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
			})
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			total := 0
			_, err := client.request(context.Background(), domain.WatchConfig{Host: test.host}, "secret", "query Safe {}", nil, &total)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			failure, ok := err.(*application.Failure)
			if !ok || failure.Code != test.code || strings.Contains(err.Error(), "private") {
				t.Fatalf("error=%v want=%s", err, test.code)
			}
		})
	}
}

func TestConnectionRejectsMissingPagesAndBadCursors(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		ok        bool
	}{{"complete", `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}`, true}, {"null", `null`, false}, {"missing nodes", `{"pageInfo":{"hasNextPage":false}}`, false}, {"missing flag", `{"nodes":[],"pageInfo":{}}`, false}, {"missing cursor", `{"nodes":[{"id":"a"}],"pageInfo":{"hasNextPage":true}}`, false}, {"empty intermediate", `{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"a"}}`, false}, {"null node", `{"nodes":[null],"pageInfo":{"hasNextPage":false}}`, false}} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := connection(json.RawMessage(test.raw))
			if (err == nil) != test.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGraphQLRateLimitPreservesRetryGuidance(t *testing.T) {
	ctrl := gomock.NewController(t)
	transport := NewMockRoundTripper(ctrl)
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	transport.EXPECT().RoundTrip(gomock.Any()).Return(&http.Response{StatusCode: 200, Header: http.Header{"Retry-After": []string{"120"}, "X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{"1793059380"}}, Body: io.NopCloser(strings.NewReader(`{"errors":[{"type":"RATE_LIMITED","message":"rate limited"}]}`))}, nil)
	client := NewWithHTTPClient(&http.Client{Transport: transport})
	client.now = func() time.Time { return now }
	total := 0
	_, err := client.request(context.Background(), domain.WatchConfig{Host: "github.com"}, "secret", "query Safe {}", nil, &total)
	failure, ok := err.(*application.Failure)
	if !ok || failure.Code != "rate_limit" || failure.RetryAt.Before(now.Add(120*time.Second)) {
		t.Fatalf("failure=%#v", err)
	}
	if delay := application.RetryDelay(time.Minute, err, 0, now, 0); delay < 120*time.Second {
		t.Fatalf("retry guidance ignored: %s", delay)
	}
}

func TestItemDraftExclusionAndUnsupportedReviewer(t *testing.T) {
	for _, test := range []struct {
		name, kind, raw string
		skip, fail      bool
	}{{"draft", "review", `{"id":"R1","state":"PENDING"}`, true, false}, {"unknown reviewer", "request", `{"id":"Q1","requestedReviewer":{"id":"X1","__typename":"Alien","login":"u"}}`, false, true}, {"team", "request", `{"id":"Q1","requestedReviewer":{"id":"T1","__typename":"Team","slug":"reviewers"}}`, false, false}, {"missing body", "conversation", `{"id":"C1","url":"https://example.test"}`, false, true}} {
		t.Run(test.name, func(t *testing.T) {
			var node map[string]json.RawMessage
			_ = json.Unmarshal([]byte(test.raw), &node)
			_, skip, err := parseItem(node, test.kind)
			if skip != test.skip || (err != nil) != test.fail {
				t.Fatalf("skip=%v err=%v", skip, err)
			}
		})
	}
}

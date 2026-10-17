package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

func TestResolveUserUsesSelectedHostAndExactIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" || r.Method != http.MethodGet {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Authorization") != "Bearer secret-marker" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("unexpected API headers: %v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"id":18446744073709551615,"login":"operator"}`)
	}))
	defer server.Close()
	client := NewWithHTTPClient(server.Client())
	user, err := client.ResolveUser(context.Background(), domain.WatchConfig{Host: "ghe.example.test", APIBase: server.URL + "/api/v3"}, "secret-marker")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != ^uint64(0) || user.Login != "operator" {
		t.Fatalf("user = %#v", user)
	}
}

func TestObserveCollectsEveryExpectedPage(t *testing.T) {
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/search/issues" || query.Get("q") != "is:pr is:open user-review-requested:@me" || query.Get("per_page") != "100" || query.Get("sort") != "created" || query.Get("order") != "asc" {
			t.Errorf("unexpected search request: %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		page, _ := strconv.Atoi(query.Get("page"))
		first, count := uint64(1), 100
		if page == 2 {
			first, count = 101, 1
		}
		writeSearch(t, w, 101, false, first, count)
	}))
	defer server.Close()
	config := domain.WatchConfig{Host: "github.com", APIBase: server.URL}
	matches, err := NewWithHTTPClient(server.Client()).Observe(context.Background(), config, "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 101 || requests != 2 || matches[1] != "https://github.com/owner/repo/pull/1" || matches[101] != "https://github.com/owner/repo/pull/101" {
		t.Fatalf("matches=%d requests=%d first=%q last=%q", len(matches), requests, matches[1], matches[101])
	}
}

func TestObserveRejectsIncompleteOrAmbiguousCollections(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   application.FailureKind
	}{
		{name: "incomplete flag", body: searchBody(1, true, []byteItem{item(1)}), want: application.FailureIncomplete},
		{name: "missing page items", body: searchBody(101, false, []byteItem{item(1)}), want: application.FailureIncomplete},
		{name: "duplicate IDs", body: searchBody(2, false, []byteItem{item(1), item(1)}), want: application.FailureIncomplete},
		{name: "invalid context URL", body: searchBody(1, false, []byteItem{itemWithURL(1, "https://evil.example.test/owner/repo/pull/1")}), want: application.FailureIncomplete},
		{name: "capacity", body: searchBody(1001, false, nil), want: application.FailureCapacity},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			_, err := NewWithHTTPClient(server.Client()).Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: server.URL}, "token")
			failure, ok := err.(*application.Failure)
			if !ok || failure.Kind != test.want {
				t.Fatalf("error=%#v want category=%s", err, test.want)
			}
		})
	}
}

func TestObserveClassifiesRateLimitsAndRejectsRedirects(t *testing.T) {
	t.Run("secondary rate limit honors Retry-After", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
		}))
		defer server.Close()
		now := time.Now()
		client := NewWithHTTPClient(server.Client())
		client.now = func() time.Time { return now }
		_, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: server.URL}, "token")
		failure, ok := err.(*application.Failure)
		if !ok || failure.Kind != application.FailureRateLimit || failure.RetryAt.Before(now.Add(119*time.Second)) {
			t.Fatalf("failure=%#v", err)
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		redirected := false
		destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
		defer destination.Close()
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}))
		defer server.Close()
		_, err := NewWithHTTPClient(server.Client()).Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: server.URL}, "token")
		failure, ok := err.(*application.Failure)
		if !ok || failure.Kind != application.FailureUnsupported || redirected {
			t.Fatalf("failure=%#v redirected=%v", err, redirected)
		}
	})
}

func TestRetryTimeUsesLatestRateLimitGuidance(t *testing.T) {
	now := time.Date(2026, time.October, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		headers http.Header
		want    time.Time
	}{
		{
			name: "reset later than retry-after",
			headers: http.Header{
				"Retry-After":           []string{"60"},
				"X-Ratelimit-Remaining": []string{"0"},
				"X-Ratelimit-Reset":     []string{strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)},
			},
			want: now.Add(10 * time.Minute),
		},
		{
			name: "retry-after later than reset",
			headers: http.Header{
				"Retry-After":           []string{"1200"},
				"X-Ratelimit-Remaining": []string{"0"},
				"X-Ratelimit-Reset":     []string{strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)},
			},
			want: now.Add(20 * time.Minute),
		},
		{
			name: "reset is ignored when quota remains",
			headers: http.Header{
				"Retry-After":           []string{"60"},
				"X-Ratelimit-Remaining": []string{"1"},
				"X-Ratelimit-Reset":     []string{strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)},
			},
			want: now.Add(time.Minute),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := retryTime(test.headers, now); !got.Equal(test.want) {
				t.Fatalf("retryTime() = %s, want %s", got, test.want)
			}
		})
	}
}

type byteItem struct {
	id  uint64
	url string
}

func item(id uint64) byteItem {
	return itemWithURL(id, fmt.Sprintf("https://github.com/owner/repo/pull/%d", id))
}

func itemWithURL(id uint64, rawURL string) byteItem { return byteItem{id: id, url: rawURL} }

func writeSearch(t *testing.T, w http.ResponseWriter, total uint64, incomplete bool, first uint64, count int) {
	t.Helper()
	items := make([]byteItem, 0, count)
	for id := first; id < first+uint64(count); id++ {
		items = append(items, item(id))
	}
	_, _ = fmt.Fprint(w, searchBody(total, incomplete, items))
}

func searchBody(total uint64, incomplete bool, items []byteItem) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, `{"total_count":%d,"incomplete_results":%t,"items":[`, total, incomplete)
	for index, item := range items {
		if index != 0 {
			builder.WriteByte(',')
		}
		encodedURL, _ := json.Marshal(item.url)
		fmt.Fprintf(&builder, `{"id":%d,"html_url":%s,"state":"open","pull_request":{}}`, item.id, encodedURL)
	}
	builder.WriteString(`]}`)
	return builder.String()
}

func TestQueryValuesAreURLEncoded(t *testing.T) {
	values := url.Values{}
	values.Set("q", "is:pr is:open user-review-requested:@me")
	if !strings.Contains(values.Encode(), "user-review-requested%3A%40me") {
		t.Fatalf("qualifier was not encoded: %s", values.Encode())
	}
}

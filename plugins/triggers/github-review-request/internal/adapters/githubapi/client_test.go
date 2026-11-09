package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	client := mockHTTPClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" || r.Method != http.MethodGet {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get("Authorization") != "Bearer secret-marker" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("unexpected API headers: %v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"id":18446744073709551615,"login":"operator"}`)
	})
	user, err := client.ResolveUser(context.Background(), domain.WatchConfig{Host: "ghe.example.test", APIBase: "https://ghe.example.test/api/v3"}, "secret-marker")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != ^uint64(0) || user.Login != "operator" {
		t.Fatalf("user = %#v", user)
	}
}

func TestObserveCollectsEveryExpectedPage(t *testing.T) {
	for _, total := range []uint64{0, 100, 101, 1000} {
		t.Run(strconv.FormatUint(total, 10), func(t *testing.T) {
			var requests uint64
			client := mockHTTPClient(func(w http.ResponseWriter, r *http.Request) {
				requests++
				query := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != "/search/issues" || query.Get("q") != "is:pr is:open user-review-requested:@me" ||
					query.Get("per_page") != "100" || query.Get("sort") != "created" || query.Get("order") != "asc" || query.Get("page") != strconv.FormatUint(requests, 10) {
					t.Errorf("unexpected search request: %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
				}
				offset := (requests - 1) * 100
				writeSearch(t, w, total, false, offset+1, int(min(total-offset, 100)))
			})
			matches, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
			if err != nil || len(matches) != int(total) || requests != max(1, (total+99)/100) {
				t.Fatalf("matches=%d requests=%d error=%v", len(matches), requests, err)
			}
			for id := uint64(1); id <= total; id++ {
				if matches[id] != item(id).url {
					t.Fatalf("invalid match for %d: %q", id, matches[id])
				}
			}
		})
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
		{name: "non-open PR", body: strings.Replace(searchBody(1, false, []byteItem{item(1)}), `"state":"open"`, `"state":"closed"`, 1), want: application.FailureIncomplete},
		{name: "zero issue ID", body: searchBody(1, false, []byteItem{itemWithURL(0, item(1).url)}), want: application.FailureIncomplete},
		{name: "invalid context URL", body: searchBody(1, false, []byteItem{itemWithURL(1, "https://evil.example.test/owner/repo/pull/1")}), want: application.FailureIncomplete},
		{name: "capacity", body: searchBody(1001, false, nil), want: application.FailureCapacity},
		{name: "capacity before item validation", body: `{"total_count":1001,"incomplete_results":false,"items":[null]}`, want: application.FailureCapacity},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := mockHTTPClient(func(w http.ResponseWriter, _ *http.Request) {
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				_, _ = fmt.Fprint(w, test.body)
			})
			_, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
			failure, ok := err.(*application.Failure)
			if !ok || failure.Kind != test.want {
				t.Fatalf("error=%#v want category=%s", err, test.want)
			}
		})
	}
}

func TestObserveDoesNotReturnPartialMatchesWhenLaterPageFails(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secondPage func(http.ResponseWriter)
		kind       application.FailureKind
	}{
		{name: "changed total", secondPage: func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, searchBody(102, false, []byteItem{item(101), item(102)}))
		}, kind: application.FailureIncomplete},
		{name: "failed request", secondPage: func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadGateway)
		}, kind: application.FailureService},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := mockHTTPClient(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Query().Get("page") {
				case "1":
					writeSearch(t, w, 101, false, 1, 100)
				case "2":
					tc.secondPage(w)
				default:
					t.Fatalf("unexpected page in %s", r.URL)
				}
			})
			matches, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
			failure, ok := err.(*application.Failure)
			if matches != nil || !ok || failure.Kind != tc.kind {
				t.Fatalf("matches=%d failure=%#v, want %s", len(matches), err, tc.kind)
			}
		})
	}
}

func TestObserveClassifiesRateLimitsAndRejectsRedirects(t *testing.T) {
	t.Run("secondary rate limit honors Retry-After", func(t *testing.T) {
		client := mockHTTPClient(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
		})
		now := time.Date(2026, time.November, 9, 0, 0, 0, 0, time.UTC)
		client.now = func() time.Time { return now }
		_, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
		failure, ok := err.(*application.Failure)
		if !ok || failure.Kind != application.FailureRateLimit || failure.RetryAt.Before(now.Add(119*time.Second)) {
			t.Fatalf("failure=%#v", err)
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		client := mockHTTPClient(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "api.github.com" {
				t.Fatalf("followed redirect to %s", r.URL.Host)
			}
			http.Redirect(w, r, "https://redirect.example.test", http.StatusFound)
		})
		_, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
		failure, ok := err.(*application.Failure)
		if !ok || failure.Kind != application.FailureUnsupported {
			t.Fatalf("failure=%#v", err)
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
			if got := rateLimitRetryTime(test.headers, now); !got.Equal(test.want) {
				t.Fatalf("rateLimitRetryTime() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestObserveClassifiesStatusAndRetryGuidanceAfterBodyRead(t *testing.T) {
	headersAt := time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC)
	readAt := headersAt.Add(5 * time.Second)
	date := headersAt.Add(120 * time.Second).Format(http.TimeFormat)
	expired := headersAt.Format(http.TimeFormat)
	reset := headersAt.Add(180 * time.Second)
	const rateMessage = `{"message":"API rate limit exceeded"}`
	const oversized = "oversized"
	tests := []struct {
		name       string
		status     int
		retryAfter string
		reset      time.Time
		body       string
		readError  bool
		kind       application.FailureKind
		retryAt    time.Time
		delay      time.Duration
	}{
		{name: "503 delta", status: 503, retryAfter: "120", kind: application.FailureService, retryAt: headersAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "503 date", status: 503, retryAfter: date, kind: application.FailureService, retryAt: headersAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "502 delta", status: 502, retryAfter: "120", kind: application.FailureService, retryAt: headersAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "503 invalid delta", status: 503, retryAfter: "invalid", kind: application.FailureService, delay: 30 * time.Second},
		{name: "503 ignores quota reset", status: 503, reset: reset, body: rateMessage, kind: application.FailureService, delay: 30 * time.Second},
		{name: "503 timeout retains delta", status: 503, retryAfter: "120", readError: true, kind: application.FailureService, retryAt: readAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "503 timeout retains date", status: 503, retryAfter: date, readError: true, kind: application.FailureService, retryAt: headersAt.Add(120 * time.Second), delay: 110 * time.Second},
		{name: "503 overflow retains delta", status: 503, retryAfter: "120", body: oversized, kind: application.FailureService, retryAt: readAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "503 overflow retains date", status: 503, retryAfter: date, body: oversized, kind: application.FailureService, retryAt: headersAt.Add(120 * time.Second), delay: 110 * time.Second},
		{name: "429 timeout retains delta", status: 429, retryAfter: "120", readError: true, kind: application.FailureRateLimit, retryAt: readAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "429 timeout retains date", status: 429, retryAfter: date, readError: true, kind: application.FailureRateLimit, retryAt: headersAt.Add(120 * time.Second), delay: 110 * time.Second},
		{name: "429 overflow retains delta", status: 429, retryAfter: "120", body: oversized, kind: application.FailureRateLimit, retryAt: readAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "429 overflow retains date", status: 429, retryAfter: date, body: oversized, kind: application.FailureRateLimit, retryAt: headersAt.Add(120 * time.Second), delay: 110 * time.Second},
		{name: "401 preserves status through timeout", status: 401, retryAfter: "120", readError: true, kind: application.FailureAuthentication, delay: 5 * time.Minute},
		{name: "200 timeout", status: 200, readError: true, kind: application.FailureNetwork, delay: 30 * time.Second},
		{name: "200 overflow", status: 200, body: oversized, kind: application.FailureIncomplete, delay: 30 * time.Second},
		{name: "403 complete rate-limit error despite timeout", status: 403, body: rateMessage, readError: true, kind: application.FailureRateLimit, delay: time.Minute},
		{name: "403 truncated JSON", status: 403, body: rateMessage[:len(rateMessage)-1], readError: true, kind: application.FailurePermission, delay: 5 * time.Minute},
		{name: "403 oversized JSON", status: 403, body: oversized, kind: application.FailurePermission, delay: 5 * time.Minute},
		{name: "403 retry header", status: 403, retryAfter: "120", kind: application.FailureRateLimit, retryAt: headersAt.Add(120 * time.Second), delay: 115 * time.Second},
		{name: "403 zero remaining", status: 403, reset: reset, kind: application.FailureRateLimit, retryAt: reset, delay: 175 * time.Second},
		{name: "429 zero delta", status: 429, retryAfter: "0", kind: application.FailureRateLimit, retryAt: headersAt, delay: 30 * time.Second},
		{name: "429 elapsed reset", status: 429, reset: headersAt.Add(-time.Second), kind: application.FailureRateLimit, retryAt: headersAt.Add(-time.Second), delay: 30 * time.Second},
		{name: "429 expired date", status: 429, retryAfter: expired, readError: true, kind: application.FailureRateLimit, delay: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := headersAt
			body := tc.body
			if body == oversized {
				body = rateMessage + strings.Repeat(" ", maxResponse-len(rateMessage)+1)
			}
			headers := make(http.Header)
			headers.Set("Retry-After", tc.retryAfter)
			if !tc.reset.IsZero() {
				headers.Set("X-RateLimit-Remaining", "0")
				headers.Set("X-RateLimit-Reset", strconv.FormatInt(tc.reset.Unix(), 10))
			}
			reader := strings.NewReader(body)
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(readFunc(func(p []byte) (int, error) {
					if tc.readError || tc.body == oversized {
						now = readAt
					}
					n, err := reader.Read(p)
					if tc.readError {
						err = context.DeadlineExceeded
					}
					return n, err
				}))}, nil
			})
			client := NewWithHTTPClient(&http.Client{Transport: transport})
			client.now = func() time.Time { return now }
			matches, err := client.Observe(context.Background(), domain.WatchConfig{Host: "github.com", APIBase: "https://api.github.com"}, "token")
			failure, ok := err.(*application.Failure)
			if matches != nil || !ok || failure.Kind != tc.kind || !failure.RetryAt.Equal(tc.retryAt) {
				t.Fatalf("matches=%v failure=%#v, want %s at %s", matches, err, tc.kind, tc.retryAt)
			}
			if delay := application.RetryDelayAt(30*time.Second, failure, 0, now.Add(5*time.Second)); delay != tc.delay {
				t.Fatalf("retry delay = %s, want %s", delay, tc.delay)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func mockHTTPClient(handler http.HandlerFunc) *Client {
	return NewWithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler(recorder, r)
		return recorder.Result(), nil
	})})
}

type readFunc func([]byte) (int, error)

func (fn readFunc) Read(p []byte) (int, error) { return fn(p) }

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

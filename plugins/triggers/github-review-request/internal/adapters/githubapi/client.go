package githubapi

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

const (
	requestTimeout = 10 * time.Second
	maxResponse    = 8 << 20
	apiVersion     = "2022-11-28"
)

type Client struct {
	http *http.Client
	now  func() time.Time
}

func New() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
		if tlsConfig.MinVersion == 0 || tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	transport.TLSClientConfig = tlsConfig
	return &Client{
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}
}

func NewWithHTTPClient(client *http.Client) *Client {
	if client == nil {
		return New()
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copyClient, now: time.Now}
}

func (c *Client) ResolveUser(ctx context.Context, config domain.WatchConfig, token string) (application.User, error) {
	body, err := c.get(ctx, config, token, "/user")
	if err != nil {
		return application.User{}, err
	}
	value, err := jsonvalue.Parse(body, maxResponse)
	if err != nil || value.Kind() != jsonvalue.Object {
		return application.User{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "invalid authenticated-user response"}
	}
	idValue, idExists := value.Get("id")
	idText, idOK := idValue.Text()
	if !idExists || idValue.Kind() != jsonvalue.Number || !idOK {
		return application.User{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "invalid authenticated-user identifier"}
	}
	userID, validID := domain.ParsePositiveID(idText)
	loginValue, loginExists := value.Get("login")
	login, loginOK := jsonvalue.ParseString(loginValue)
	if !validID || !loginExists || !loginOK || login == "" {
		return application.User{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "invalid authenticated-user identity"}
	}
	return application.User{ID: userID, Login: login}, nil
}

func (c *Client) Observe(ctx context.Context, config domain.WatchConfig, token string) (map[uint64]string, error) {
	observationCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	matches := make(map[uint64]string)
	var expectedTotal uint64
	for page := uint64(1); page <= 10; page++ {
		query := url.Values{}
		query.Set("q", "is:pr is:open user-review-requested:@me")
		query.Set("per_page", "100")
		query.Set("sort", "created")
		query.Set("order", "asc")
		query.Set("page", strconv.FormatUint(page, 10))
		body, err := c.get(observationCtx, config, token, "/search/issues?"+query.Encode())
		if err != nil {
			return nil, err
		}
		response, err := parseSearch(body)
		if err != nil {
			return nil, err
		}
		if response.Incomplete {
			return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "GitHub reported incomplete search results"}
		}
		if page == 1 {
			expectedTotal = response.Total
			if expectedTotal > domain.MaxMatches {
				return nil, &application.Failure{Kind: application.FailureCapacity, Reason: "search result count exceeds 1,000"}
			}
		} else if response.Total != expectedTotal {
			return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search result count changed during pagination"}
		}
		want := expectedPageCount(expectedTotal, page)
		if uint64(len(response.Items)) != want {
			return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search page has an unexpected item count"}
		}
		for _, item := range response.Items {
			if !item.IsPullRequest || item.State != "open" {
				return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search returned a non-open pull request"}
			}
			id, valid := domain.ParsePositiveID(item.ID)
			if !valid || !domain.ValidatePullURL(item.URL, config.Host) {
				return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search returned an invalid pull request record"}
			}
			if _, duplicate := matches[id]; duplicate {
				return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search returned a duplicate issue ID"}
			}
			matches[id] = item.URL
		}
		if page == pageCount(expectedTotal) {
			return matches, nil
		}
	}
	return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "search pagination exceeded the supported page count"}
}

func (c *Client) get(ctx context.Context, config domain.WatchConfig, token, suffix string) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	base, err := url.Parse(config.APIBase)
	if err != nil {
		return nil, &application.Failure{Kind: application.FailureUnsupported, Reason: "invalid configured API origin"}
	}
	relative, err := url.Parse(suffix)
	if err != nil || !strings.HasPrefix(relative.Path, "/") || relative.IsAbs() || relative.Host != "" {
		return nil, &application.Failure{Kind: application.FailureUnsupported, Reason: "invalid API request path"}
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + relative.Path
	base.RawQuery = relative.RawQuery
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, &application.Failure{Kind: application.FailureUnsupported, Reason: "invalid API request"}
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, &application.Failure{Kind: application.FailureNetwork, Reason: "GitHub request failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
		return nil, classifyResponse(response, body, c.now())
	}
	reader := io.LimitReader(response.Body, maxResponse+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, &application.Failure{Kind: application.FailureNetwork, Reason: "GitHub response read failed"}
	}
	if len(body) > maxResponse {
		return nil, &application.Failure{Kind: application.FailureIncomplete, Reason: "GitHub response exceeds 8 MiB"}
	}
	return body, nil
}

type searchResponse struct {
	Total      uint64
	Incomplete bool
	Items      []searchItem
}

type searchItem struct {
	ID            string
	URL           string
	State         string
	IsPullRequest bool
}

func parseSearch(body []byte) (searchResponse, error) {
	value, err := jsonvalue.Parse(body, maxResponse)
	if err != nil || value.Kind() != jsonvalue.Object {
		return searchResponse{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "invalid search response"}
	}
	totalValue, totalExists := value.Get("total_count")
	totalText, totalOK := totalValue.Text()
	total, totalErr := strconv.ParseUint(totalText, 10, 64)
	incompleteValue, incompleteExists := value.Get("incomplete_results")
	incomplete, incompleteOK := incompleteValue.Bool()
	itemsValue, itemsExists := value.Get("items")
	if !totalExists || totalValue.Kind() != jsonvalue.Number || !totalOK || totalErr != nil || strconv.FormatUint(total, 10) != totalText || !incompleteExists || !incompleteOK || !itemsExists || itemsValue.Kind() != jsonvalue.Array {
		return searchResponse{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "search response fields are invalid"}
	}
	items := make([]searchItem, 0, len(itemsValue.Items()))
	for _, value := range itemsValue.Items() {
		if value.Kind() != jsonvalue.Object {
			return searchResponse{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "search item is invalid"}
		}
		idValue, hasID := value.Get("id")
		idText, idOK := idValue.Text()
		urlValue, hasURL := value.Get("html_url")
		rawURL, urlOK := jsonvalue.ParseString(urlValue)
		stateValue, hasState := value.Get("state")
		state, stateOK := jsonvalue.ParseString(stateValue)
		pullValue, hasPull := value.Get("pull_request")
		if !hasID || idValue.Kind() != jsonvalue.Number || !idOK || !hasURL || !urlOK || !hasState || !stateOK || !hasPull || pullValue.Kind() != jsonvalue.Object {
			return searchResponse{}, &application.Failure{Kind: application.FailureIncomplete, Reason: "search item fields are invalid"}
		}
		items = append(items, searchItem{ID: idText, URL: rawURL, State: state, IsPullRequest: true})
	}
	return searchResponse{Total: total, Incomplete: incomplete, Items: items}, nil
}

func expectedPageCount(total, page uint64) uint64 {
	if total == 0 || page > pageCount(total) {
		return 0
	}
	remaining := total - (page-1)*100
	if remaining > 100 {
		return 100
	}
	return remaining
}

func pageCount(total uint64) uint64 {
	if total == 0 {
		return 1
	}
	return (total + 99) / 100
}

func classifyResponse(response *http.Response, body []byte, now time.Time) *application.Failure {
	status := response.StatusCode
	failure := &application.Failure{StatusCode: status, Kind: application.FailureService}
	if status >= 300 && status < 400 {
		failure.Kind = application.FailureUnsupported
		failure.Reason = "GitHub redirects are not followed"
		return failure
	}
	switch status {
	case http.StatusUnauthorized:
		failure.Kind = application.FailureAuthentication
		failure.Reason = "GitHub rejected the selected credential"
	case http.StatusForbidden:
		if isRateLimited(response.Header, body) {
			failure.Kind = application.FailureRateLimit
			failure.RetryAt = retryTime(response.Header, now)
		} else {
			failure.Kind = application.FailurePermission
		}
	case http.StatusTooManyRequests:
		failure.Kind = application.FailureRateLimit
		failure.RetryAt = retryTime(response.Header, now)
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		failure.Kind = application.FailureUnsupported
	default:
		if status >= 500 {
			failure.Kind = application.FailureService
		} else if status >= 400 {
			failure.Kind = application.FailurePermission
		}
	}
	return failure
}

func isRateLimited(headers http.Header, body []byte) bool {
	if headers.Get("Retry-After") != "" || headers.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	value, err := jsonvalue.Parse(body, maxResponse)
	if err != nil || value.Kind() != jsonvalue.Object {
		return false
	}
	messageValue, exists := value.Get("message")
	message, ok := jsonvalue.ParseString(messageValue)
	if !exists || !ok {
		return false
	}
	message = strings.ToLower(message)
	return strings.Contains(message, "rate limit") || strings.Contains(message, "secondary rate") || strings.Contains(message, "abuse detection")
}

func retryTime(headers http.Header, now time.Time) time.Time {
	var latest time.Time
	consider := func(candidate time.Time) {
		if candidate.After(latest) {
			latest = candidate
		}
	}
	if text := headers.Get("Retry-After"); text != "" {
		if seconds, err := strconv.ParseInt(text, 10, 64); err == nil && seconds >= 0 {
			const maxDuration = time.Duration(1<<63 - 1)
			if seconds > int64(maxDuration/time.Second) {
				consider(now.Add(maxDuration))
			} else {
				consider(now.Add(time.Duration(seconds) * time.Second))
			}
		} else if date, err := http.ParseTime(text); err == nil && date.After(now) {
			consider(date)
		}
	}
	if text := headers.Get("X-RateLimit-Remaining"); text == "0" {
		if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			consider(time.Unix(reset, 0))
		}
	}
	return latest
}

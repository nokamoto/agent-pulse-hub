package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	DefaultHost         = "github.com"
	DefaultPollInterval = time.Minute
	MaxMatches          = 1_000
	StateSchema         = "github-review-request-v1"
)

var pullURLPath = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)$`)

type WatchConfig struct {
	Host         string
	APIBase      string
	PollInterval time.Duration
}

// ParseWatchConfig validates only the public watch arguments. It performs no
// external calls, so malformed registrations are rejected before credentials
// or network access are considered.
func ParseWatchConfig(args *jsonvalue.Value) (WatchConfig, error) {
	if args == nil || args.Kind() != jsonvalue.Object {
		return WatchConfig{}, errors.New("watch arguments must be an object")
	}
	for key := range args.Fields() {
		if key != "host" && key != "poll_interval" {
			return WatchConfig{}, fmt.Errorf("unknown watch argument %q", key)
		}
	}
	host := DefaultHost
	if value, exists := args.Get("host"); exists {
		var ok bool
		host, ok = jsonvalue.ParseString(value)
		if !ok {
			return WatchConfig{}, errors.New("host must be a string")
		}
		host, ok = CanonicalHost(host)
		if !ok {
			return WatchConfig{}, errors.New("host must be an ASCII DNS hostname without a scheme, port, or path")
		}
	}
	interval := DefaultPollInterval
	if value, exists := args.Get("poll_interval"); exists {
		text, ok := jsonvalue.ParseString(value)
		if !ok {
			return WatchConfig{}, errors.New("poll_interval must be a duration string")
		}
		parsed, err := time.ParseDuration(text)
		if err != nil || parsed <= 0 {
			return WatchConfig{}, errors.New("poll_interval must be a positive Go duration")
		}
		interval = parsed
	}
	apiBase := "https://" + host + "/api/v3"
	if host == DefaultHost {
		apiBase = "https://api.github.com"
	}
	return WatchConfig{Host: host, APIBase: apiBase, PollInterval: interval}, nil
}

// CanonicalHost accepts an ASCII DNS name and returns its lowercase form.
func CanonicalHost(host string) (string, bool) {
	if host == "" || len(host) > 253 || !utf8.ValidString(host) || strings.HasSuffix(host, ".") {
		return "", false
	}
	for i := range host {
		if host[i] >= 0x80 {
			return "", false
		}
	}
	canonical := strings.ToLower(host)
	if net.ParseIP(canonical) != nil {
		return "", false
	}
	for _, label := range strings.Split(canonical, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-') {
				return "", false
			}
		}
	}
	// Reject dotted decimal forms even when a component would not parse as an IP.
	if looksLikeIPv4(canonical) {
		return "", false
	}
	return canonical, true
}

func looksLikeIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func ParsePositiveID(text string) (uint64, bool) {
	value, err := strconv.ParseUint(text, 10, 64)
	return value, err == nil && value > 0 && strconv.FormatUint(value, 10) == text
}

// ValidatePullURL requires the exact URL shape allowed for event context.
func ValidatePullURL(raw, webHost string) bool {
	if raw == "" || len([]byte(raw)) > 8_192 || !utf8.ValidString(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return false
	}
	if !strings.EqualFold(u.Hostname(), webHost) || u.Port() != "" || u.Host != u.Hostname() && !strings.EqualFold(u.Host, webHost) {
		return false
	}
	if !pullURLPath.MatchString(u.EscapedPath()) || u.Path != u.EscapedPath() {
		return false
	}
	return true
}

func ParsePullURL(raw, webHost string) (uint64, bool) {
	if !ValidatePullURL(raw, webHost) {
		return 0, false
	}
	match := pullURLPath.FindStringSubmatch(mustPath(raw))
	if len(match) != 4 {
		return 0, false
	}
	return ParsePositiveID(match[3])
}

func mustPath(raw string) string {
	u, _ := url.Parse(raw)
	return u.Path
}

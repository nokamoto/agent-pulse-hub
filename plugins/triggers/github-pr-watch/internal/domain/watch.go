package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const StateSchema = "github-pr-watch-v1"

type WatchConfig struct {
	Host             string `json:"host"`
	Owner            string `json:"owner"`
	Repository       string `json:"repository"`
	PullNumber       int64  `json:"pull_number"`
	WaitingReason    string `json:"waiting_reason"`
	RepresentedActor string `json:"represented_actor"`
}

func ValidHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ToLower(host) != host || net.ParseIP(host) != nil {
		return false
	}
	numeric := true
	for _, c := range host {
		if c != '.' && (c < '0' || c > '9') {
			numeric = false
		}
	}
	if numeric {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

func ParseWatch(args *jsonvalue.Value, host string) (WatchConfig, error) {
	var c WatchConfig
	if args == nil || args.Kind() != jsonvalue.Object || len(args.Marshal()) > 4096 {
		return c, errors.New("invalid watch arguments")
	}
	fields := args.Fields()
	if len(fields) != 6 {
		return c, errors.New("watch requires exactly six fields")
	}
	for _, entry := range []struct {
		name   string
		target *string
		max    int
	}{{"host", &c.Host, 253}, {"owner", &c.Owner, 100}, {"repository", &c.Repository, 100}, {"waiting_reason", &c.WaitingReason, 512}, {"represented_actor", &c.RepresentedActor, 128}} {
		value, ok := fields[entry.name]
		if !ok {
			return c, fmt.Errorf("missing %s", entry.name)
		}
		text, ok := jsonvalue.ParseString(value)
		if !ok || strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > entry.max {
			return c, fmt.Errorf("invalid %s", entry.name)
		}
		*entry.target = text
	}
	number, ok := fields["pull_number"]
	if !ok {
		return c, errors.New("missing pull_number")
	}
	parsed, err := strconv.ParseInt(string(number.Marshal()), 10, 32)
	if err != nil || parsed < 1 {
		return c, errors.New("invalid pull_number")
	}
	c.PullNumber = parsed
	if !ValidHost(c.Host) || c.Host != host {
		return c, errors.New("watch host must match configured DNS hostname")
	}
	for _, segment := range []string{c.Owner, c.Repository} {
		decoded, err := url.PathUnescape(segment)
		if err != nil || strings.Trim(segment, ".") == "" || strings.ContainsAny(segment, "/\\") || strings.ContainsAny(decoded, "/\\") {
			return c, errors.New("invalid repository path segment")
		}
		for _, char := range segment {
			if unicode.IsControl(char) {
				return c, errors.New("invalid repository path segment")
			}
		}
	}
	return c, nil
}

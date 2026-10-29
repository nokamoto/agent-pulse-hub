package githubapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

func signalRaw(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestStatusLinkOriginAndPagination(t *testing.T) {
	config := domain.WatchConfig{Host: "github.com", Owner: "acme", Repository: "widget"}
	head := strings.Repeat("a", 40)
	valid := statusEndpoint(config, head, 2)
	for _, test := range []struct {
		name, link string
		next       uint64
		bad        bool
	}{
		{"none", "", 0, false},
		{"next", "<" + valid + ">; rel=\"next\"", 2, false},
		{"last", "<" + valid + ">; rel=\"last\"", 0, false},
		{"foreign", "<" + strings.Replace(valid, "api.github.com", "evil.test", 1) + ">; rel=\"next\"", 0, true},
		{"userinfo", "<" + strings.Replace(valid, "https://", "https://token@", 1) + ">; rel=\"next\"", 0, true},
		{"fragment", "<" + valid + "#x>; rel=\"next\"", 0, true},
		{"downgrade", "<" + strings.Replace(valid, "https:", "http:", 1) + ">; rel=\"next\"", 0, true},
		{"head", "<" + strings.Replace(valid, head, strings.Repeat("b", 40), 1) + ">; rel=\"next\"", 0, true},
		{"path", "<" + strings.Replace(valid, "/statuses", "/status", 1) + ">; rel=\"next\"", 0, true},
		{"size", "<" + strings.Replace(valid, "per_page=100", "per_page=1", 1) + ">; rel=\"next\"", 0, true},
		{"repeated", "<" + statusEndpoint(config, head, 1) + ">; rel=\"next\"", 0, true},
		{"leading zero", "<" + strings.Replace(valid, "page=2", "page=02", 1) + ">; rel=\"next\"", 0, true},
		{"unknown query", "<" + valid + "&token=secret>; rel=\"next\"", 0, true},
		{"duplicate query", "<" + valid + "&page=3>; rel=\"next\"", 0, true},
		{"missing relation", "<" + valid + ">", 0, true},
		{"malformed", valid, 0, true},
		{"duplicate relation", "<" + valid + ">; rel=\"next\", <" + valid + ">; rel=\"next\"", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := nextStatusPage(test.link, config, head, 1)
			if (err != nil) != test.bad || next != test.next {
				t.Fatalf("next=%d err=%v", next, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("private link leaked")
			}
		})
	}
	config.Host = "ghes.test"
	if next, err := nextStatusPage("<"+statusEndpoint(config, head, 2)+">; rel=\"next\"", config, head, 1); err != nil || next != 2 {
		t.Fatal(next, err)
	}
}

func TestSignalProviderParsing(t *testing.T) {
	check := `{"id":"K1","name":"test","status":"COMPLETED","conclusion":"FAILURE","startedAt":null,"completedAt":null,"detailsUrl":null,"url":null}`
	r, err := parseCheck(signalRaw(t, check), "repo", "head", "suite", "app")
	if err != nil || r.ID != "K1" || r.DetailsURL != nil {
		t.Fatal(r, err)
	}
	for _, field := range []string{"id", "name", "status", "conclusion", "startedAt", "completedAt", "detailsUrl", "url"} {
		raw := signalRaw(t, check)
		delete(raw, field)
		if _, err := parseCheck(raw, "repo", "head", "suite", "app"); err == nil {
			t.Fatal("missing field accepted", field)
		}
	}
	status := `{"id":9007199254740993,"context":"build","state":"failure","created_at":null,"updated_at":null,"target_url":null,"description":null}`
	r, err = parseStatus(signalRaw(t, status), "repo", "head")
	if err != nil || r.ID != "9007199254740993" || r.DetailsURL != nil {
		t.Fatal(r, err)
	}
	for _, id := range []string{"0", "-1", "1.5", "1e2", `"12"`, "18446744073709551616"} {
		raw := signalRaw(t, status)
		raw["id"] = json.RawMessage(id)
		if _, err := parseStatus(raw, "repo", "head"); err == nil {
			t.Fatal("invalid ID accepted", id)
		}
	}
}

func TestSignalPRPairConsistency(t *testing.T) {
	body := `{"repository":{"id":"repo","pullRequest":{"id":"pr","url":"https://github.com/acme/widget/pull/17","state":"OPEN","headRefOid":"` + strings.Repeat("a", 40) + `","mergedAt":null,"baseRepository":{"id":"repo"},"baseRefName":"main","baseRefOid":"` + strings.Repeat("b", 40) + `","baseRef":{"id":"ref","target":{"oid":"` + strings.Repeat("b", 40) + `"}},"mergeable":"UNKNOWN","isDraft":true}}}`
	_, observed, err := parseSignalPR(signalRaw(t, body))
	if err != nil || !observed.BaseExists || !observed.Draft || observed.Mergeable != "UNKNOWN" {
		t.Fatal(observed, err)
	}
	missing := strings.Replace(body, `"baseRef":{"id":"ref","target":{"oid":"`+strings.Repeat("b", 40)+`"}}`, `"baseRef":null`, 1)
	_, observed, err = parseSignalPR(signalRaw(t, missing))
	if err != nil || observed.BaseExists {
		t.Fatal(observed, err)
	}
	unequal := strings.Replace(body, `"target":{"oid":"`+strings.Repeat("b", 40), `"target":{"oid":"`+strings.Repeat("c", 40), 1)
	if _, observed, err := parseSignalPR(signalRaw(t, unequal)); err != nil || observed.Pair.BaseSHA != strings.Repeat("c", 40) {
		t.Fatal(observed, err)
	}
	for _, mutation := range []string{strings.Replace(body, `"isDraft":true`, `"isDraft":null`, 1), strings.Replace(body, `"mergeable":"UNKNOWN"`, `"mergeable":"BEHIND"`, 1), strings.Replace(body, `"baseRepository":{"id":"repo"}`, `"baseRepository":null`, 1)} {
		if _, _, err := parseSignalPR(signalRaw(t, mutation)); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}

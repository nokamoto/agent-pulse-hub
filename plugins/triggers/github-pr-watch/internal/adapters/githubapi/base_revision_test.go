package githubapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"go.uber.org/mock/gomock"
)

func baseRevisionPR(state string, unequal bool) map[string]any {
	associated, target := strings.Repeat("b", 40), strings.Repeat("b", 40)
	if unequal {
		target = strings.Repeat("c", 40)
	}
	var mergedAt any
	if state == "MERGED" {
		mergedAt = "2026-10-29T12:50:38Z"
	}
	return map[string]any{"id": "pr", "url": "https://github.com/acme/widget/pull/17", "headRefOid": strings.Repeat("a", 40), "state": state, "mergedAt": mergedAt, "baseRepository": map[string]any{"id": "repo"}, "baseRefName": "main", "baseRefOid": associated, "baseRef": map[string]any{"id": "ref", "target": map[string]any{"oid": target}}, "mergeable": "UNKNOWN", "isDraft": true}
}

// Exercise the whole HTTP acquisition with mocked external capability only.
func observeBaseRevision(t *testing.T, state string, unequal bool, changeRead int, mutate func(map[string]any, map[string]any)) (domain.Observation, error) {
	t.Helper()
	config, _ := acquisitionFixture()
	transport := NewMockRoundTripper(gomock.NewController(t))
	reads := 0
	transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
		body := []byte(`[]`)
		if req.Method == http.MethodPost {
			var request struct{ Query string }
			if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			pr := baseRevisionPR(state, unequal)
			repo := map[string]any{"id": "repo", "pullRequest": pr}
			switch {
			case strings.Contains(request.Query, "PullRequestMetadata"), strings.Contains(request.Query, "PullRequestSignals"):
				reads++
				if reads == changeRead {
					mutate(repo, pr)
				}
			case strings.Contains(request.Query, "PullRequestCheckSuites"):
				repo["object"] = map[string]any{"oid": strings.Repeat("a", 40), "checkSuites": connectionRaw([]map[string]any{}, false, nil)}
			default:
				for _, category := range []string{"reviews", "comments", "reviewRequests", "reviewThreads"} {
					pr[category] = connectionRaw([]map[string]any{}, false, nil)
				}
			}
			body, _ = json.Marshal(map[string]any{"data": map[string]any{"repository": repo}})
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	}).AnyTimes()
	return NewWithHTTPClient(&http.Client{Transport: transport}).WithSignals(true).Observe(context.Background(), config, "secret")
}

func TestBaseRevisionLifecycleMatrix(t *testing.T) {
	for _, state := range []string{"OPEN", "CLOSED", "MERGED"} {
		for _, unequal := range []bool{false, true} {
			t.Run(state+map[bool]string{false: "/equal", true: "/unequal"}[unequal], func(t *testing.T) {
				observed, err := observeBaseRevision(t, state, unequal, 0, nil)
				want := strings.Repeat("b", 40)
				if unequal {
					want = strings.Repeat("c", 40)
				}
				if err != nil || observed.PR.State != state || observed.Signals == nil || observed.Signals.Pair.BaseSHA != want || observed.Signals.Mergeable != "UNKNOWN" {
					t.Fatal(observed, err)
				}
			})
		}
	}
}

func TestBaseRevisionAcquisitionFence(t *testing.T) {
	mutations := map[string]func(map[string]any, map[string]any){
		"repository":      func(repo, pr map[string]any) { repo["id"] = "other" },
		"PR":              func(repo, pr map[string]any) { pr["id"] = "other" },
		"head":            func(repo, pr map[string]any) { pr["headRefOid"] = strings.Repeat("d", 40) },
		"lifecycle":       func(repo, pr map[string]any) { pr["state"] = "CLOSED" },
		"mergedAt":        func(repo, pr map[string]any) { pr["mergedAt"] = "2026-10-29T12:51:00Z" },
		"base repository": func(repo, pr map[string]any) { pr["baseRepository"] = map[string]any{"id": "other"} },
		"retarget":        func(repo, pr map[string]any) { pr["baseRefName"] = "release" },
		"associated":      func(repo, pr map[string]any) { pr["baseRefOid"] = strings.Repeat("e", 40) },
		"ref identity":    func(repo, pr map[string]any) { pr["baseRef"].(map[string]any)["id"] = "recreated" },
		"target": func(repo, pr map[string]any) {
			pr["baseRef"].(map[string]any)["target"] = map[string]any{"oid": strings.Repeat("f", 40)}
		},
		"deletion": func(repo, pr map[string]any) { pr["baseRef"] = nil },
	}
	for name, mutate := range mutations {
		for _, read := range []int{1, 2, 3} {
			t.Run(name+string(rune('0'+read)), func(t *testing.T) {
				if _, err := observeBaseRevision(t, "OPEN", true, read, mutate); err == nil || err.Error() != "observation_changed_during_acquisition" {
					t.Fatal("changed acquisition accepted", err)
				}
			})
		}
	}
	// Final readiness and mergeability are authoritative after a stable fence.
	observed, err := observeBaseRevision(t, "OPEN", true, 3, func(repo, pr map[string]any) { pr["mergeable"] = "CONFLICTING"; pr["isDraft"] = false })
	if err != nil || observed.Signals.Mergeable != "CONFLICTING" || observed.Signals.Draft {
		t.Fatal(observed, err)
	}
}

func TestBaseRevisionRequiredFields(t *testing.T) {
	for _, key := range []string{"id", "url", "headRefOid", "state", "mergedAt", "baseRepository", "baseRefName", "baseRefOid", "baseRef", "mergeable", "isDraft"} {
		for _, missing := range []bool{false, true} {
			t.Run(key+map[bool]string{false: "/null", true: "/missing"}[missing], func(t *testing.T) {
				_, err := observeBaseRevision(t, "MERGED", true, 3, func(repo, pr map[string]any) {
					if missing {
						delete(pr, key)
					} else {
						pr[key] = nil
					}
				})
				// Nullable timestamps and an explicitly unavailable ref are valid;
				// their change still fails the acquisition fence.
				if err == nil {
					t.Fatal("required field accepted", key)
				}
			})
		}
	}
	for _, ref := range []any{map[string]any{"id": "", "target": map[string]any{"oid": strings.Repeat("c", 40)}}, map[string]any{"id": "ref", "target": nil}, map[string]any{"id": "ref", "target": map[string]any{"oid": "bad"}}} {
		if _, err := observeBaseRevision(t, "OPEN", true, 3, func(repo, pr map[string]any) { pr["baseRef"] = ref }); err == nil || err.Error() != "incomplete_retrieval" {
			t.Fatal("malformed ref accepted", err)
		}
	}
}

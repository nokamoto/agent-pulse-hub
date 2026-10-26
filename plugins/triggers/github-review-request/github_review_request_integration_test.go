//go:build integration

package main

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("GitHub review request acceptance", Serial, func() {
	It("selects credentials across actual environment and executable boundaries", Label("case:GRR-V01-AUTH"), func() {
		t := GinkgoT()
		noGH := grrNoGHDirectory(t)
		fixture := newGRRFixture(t, map[string]uint64{"token-high": 71, "token-low": 72})
		host := startGRRPluginHost(t, noGH, fixture, map[string]string{
			"PATH": noGH, "GH_TOKEN": "token-high", "GITHUB_TOKEN": "token-low",
			"GH_ENTERPRISE_TOKEN": "enterprise-poison", "GITHUB_ENTERPRISE_TOKEN": "enterprise-poison",
			"GH_HOST": "wrong.example.test",
		})
		DeferCleanup(host.cleanup)
		accepted := host.watch(t, "high-priority", "grr-auth-high", grrWatchArgs("", "1h"), jsonvalue.NewNull())
		if accepted.Accepted == nil || !*accepted.Accepted {
			t.Fatalf("GH_TOKEN watch result = %+v", accepted)
		}
		calls := fixture.authSnapshot()
		if len(calls) != 1 || calls[0].Token != "token-high" || calls[0].Host != "api.github.com" || calls[0].Path != "/user" {
			t.Fatalf("GH_TOKEN precedence requests = %+v", calls)
		}

		rejectedFixture := newGRRFixture(t, map[string]uint64{"token-low": 72})
		rejectedHost := startGRRPluginHost(t, noGH, rejectedFixture, map[string]string{
			"PATH": noGH, "GH_TOKEN": "rejected-high", "GITHUB_TOKEN": "token-low",
		})
		DeferCleanup(rejectedHost.cleanup)
		rejected := rejectedHost.watch(t, "reject-high", "grr-auth-rejected", grrWatchArgs("", "1h"), jsonvalue.NewNull())
		if rejected.Accepted == nil || *rejected.Accepted || !strings.Contains(rejected.Error, "validation failed") {
			t.Fatalf("rejected higher-priority token result = %+v", rejected)
		}
		if calls := rejectedFixture.authSnapshot(); len(calls) != 1 || calls[0].Token != "rejected-high" || rejectedFixture.searchCountSnapshot() != 0 {
			t.Fatalf("rejected token fell back or started search: auth=%+v search=%d", calls, rejectedFixture.searchCountSnapshot())
		}

		githubTokenFixture := newGRRFixture(t, map[string]uint64{"token-low": 72})
		githubTokenHost := startGRRPluginHost(t, noGH, githubTokenFixture, map[string]string{"PATH": noGH, "GH_TOKEN": "", "GITHUB_TOKEN": "token-low"})
		DeferCleanup(githubTokenHost.cleanup)
		githubTokenResult := githubTokenHost.watch(t, "github-token", "grr-auth-github-token", grrWatchArgs("", "1h"), jsonvalue.NewNull())
		if githubTokenResult.Accepted == nil || !*githubTokenResult.Accepted {
			t.Fatalf("GITHUB_TOKEN-only result = %+v", githubTokenResult)
		}
		if calls := githubTokenFixture.authSnapshot(); len(calls) != 1 || calls[0].Token != "token-low" {
			t.Fatalf("GITHUB_TOKEN request = %+v", calls)
		}

		ghDirectory, err := os.MkdirTemp(grrSuiteDirectory, "saved-gh-")
		if err != nil {
			t.Fatal(err)
		}
		ghPath := filepath.Join(ghDirectory, "gh"+grrExecutableSuffix())
		if err := copyGRRFile(grrGHBInary, ghPath); err != nil {
			t.Fatalf("install gh process fixture: %v", err)
		}
		oldPath, hadPath := os.LookupEnv("PATH")
		if err := os.Setenv("PATH", ghDirectory); err != nil {
			t.Fatal(err)
		}
		resolvedGH, lookupErr := exec.LookPath("gh")
		if hadPath {
			_ = os.Setenv("PATH", oldPath)
		} else {
			_ = os.Unsetenv("PATH")
		}
		if lookupErr != nil || !strings.EqualFold(resolvedGH, ghPath) {
			t.Fatalf("isolated gh lookup=%q error=%v want=%q", resolvedGH, lookupErr, ghPath)
		}
		directJournal := filepath.Join(ghDirectory, "direct-gh-journal.json")
		direct := exec.Command(ghPath, "auth", "token", "--hostname", "ghes.example.test")
		direct.Env = grrEnvironment(map[string]string{
			"PATH": ghDirectory, "GH_TOKEN": "", "GITHUB_TOKEN": "", "GH_ENTERPRISE_TOKEN": "", "GITHUB_ENTERPRISE_TOKEN": "", "GH_HOST": "",
			"APH_GRR_FIXTURE_JOURNAL": directJournal,
		})
		directOutput, directErr := direct.CombinedOutput()
		if directErr != nil || string(directOutput) != "fixture-saved-token\n" {
			t.Fatalf("direct gh fixture did not run: output=%q error=%v", directOutput, directErr)
		}
		journal := filepath.Join(ghDirectory, "gh-journal.json")
		enterpriseFixture := newGRRFixture(t, map[string]uint64{"fixture-saved-token": 73})
		lookupJournal := filepath.Join(ghDirectory, "plugin-path-lookup.txt")
		startedJournal := filepath.Join(ghDirectory, "gh-started.txt")
		lookupEnvironment := grrTestEnvironment(enterpriseFixture, map[string]string{
			"PATH": ghDirectory, "GH_TOKEN": "", "GITHUB_TOKEN": "", "GH_ENTERPRISE_TOKEN": "enterprise-poison",
			"GITHUB_ENTERPRISE_TOKEN": "enterprise-poison", "GH_HOST": "wrong.example.test",
		})
		lookupEnvironment["APH_GRR_LOOKUP_MODE"] = "1"
		lookupEnvironment["APH_GRR_LOOKUP_JOURNAL"] = lookupJournal
		lookup := exec.Command(grrPluginBinary)
		lookup.Env = grrEnvironment(lookupEnvironment)
		if output, err := lookup.CombinedOutput(); err != nil {
			t.Fatalf("plugin process PATH probe failed: %v output=%s", err, output)
		}
		lookupText := readGRRFile(lookupJournal)
		if !strings.Contains(strings.ToLower(lookupText), strings.ToLower("path="+ghPath)) {
			t.Fatalf("plugin process resolves a different gh executable: %s", lookupText)
		}
		enterpriseHost := startGRRPluginHost(t, ghDirectory, enterpriseFixture, map[string]string{
			"PATH": ghDirectory, "GH_TOKEN": "", "GITHUB_TOKEN": "",
			"GH_ENTERPRISE_TOKEN": "enterprise-poison", "GITHUB_ENTERPRISE_TOKEN": "enterprise-poison",
			"GH_HOST": "wrong.example.test", "APH_GRR_FIXTURE_JOURNAL": journal, "APH_GRR_FIXTURE_STARTED_FILE": startedJournal,
		})
		DeferCleanup(enterpriseHost.cleanup)
		enterpriseResult := enterpriseHost.watch(t, "saved-gh", "grr-auth-saved-gh", grrWatchArgs("ghes.example.test", "1h"), jsonvalue.NewNull())
		if enterpriseResult.Accepted == nil || !*enterpriseResult.Accepted {
			t.Fatalf("saved GHES credential result = %+v plugin-log=%s auth=%+v gh-journal=%s started=%s path-probe=%s", enterpriseResult, readGRRFile(enterpriseHost.logPath), enterpriseFixture.authSnapshot(), readGRRFile(journal), readGRRFile(startedJournal), lookupText)
		}
		enterpriseHost.send(t, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "grr-auth-saved-gh"})
		search := grrWaitSearch(t, enterpriseFixture, 1, 3*time.Second)
		if search.Host != "ghes.example.test" || search.Path != "/api/v3/search/issues" || search.Token != "fixture-saved-token" {
			t.Fatalf("GHES search request = %+v", search)
		}
		if calls := enterpriseFixture.authSnapshot(); len(calls) != 1 || calls[0].Host != "ghes.example.test" || calls[0].Path != "/api/v3/user" || calls[0].Token != "fixture-saved-token" {
			t.Fatalf("GHES identity request = %+v", calls)
		}
		var child struct {
			Args           []string        `json:"argv"`
			Blocked        map[string]bool `json:"blocked_environment_present"`
			PromptDisabled string          `json:"prompt_disabled"`
		}
		journalData, err := os.ReadFile(journal)
		if err != nil || json.Unmarshal(journalData, &child) != nil {
			t.Fatalf("read saved gh fixture journal: %v", err)
		}
		if !reflect.DeepEqual(child.Args, []string{"auth", "token", "--hostname", "ghes.example.test"}) || child.PromptDisabled != "1" {
			t.Fatalf("saved gh invocation = %+v", child)
		}
		for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
			if present, ok := child.Blocked[key]; !ok || present {
				t.Errorf("saved gh inherited %s: %+v", key, child.Blocked)
			}
		}
		enterpriseHost.send(t, pluginprotocol.Frame{Type: "checkpoint", RequestID: "safe-state", SubscriptionID: "grr-auth-saved-gh", Quiesce: false})
		checkpoint := enterpriseHost.receiveType(t, "checkpoint_result", "safe-state", 2*time.Second)
		if checkpoint.Accepted == nil || !*checkpoint.Accepted || strings.Contains(string(checkpoint.State.Marshal()), "fixture-saved-token") || strings.Contains(string(checkpoint.State.Marshal()), "enterprise-poison") {
			t.Fatalf("credential appeared in checkpoint: %+v", checkpoint)
		}
		pluginLog := readGRRFile(enterpriseHost.logPath)
		for _, marker := range []string{"fixture-saved-token", "enterprise-poison", "wrong.example.test"} {
			if strings.Contains(pluginLog, marker) {
				t.Errorf("credential marker %q appeared in plugin diagnostics", marker)
			}
		}
	})

	It("observes complete request sets through the standalone public input", Label("case:GRR-V02-OBSERVATION"), func() {
		t := GinkgoT()
		noGH := grrNoGHDirectory(t)
		urlA := "https://github.com/owner-a/repo-a/pull/11"
		urlB := "https://github.com/owner-b/repo-b/pull/22"
		fixture := newGRRFixture(t, map[string]uint64{"observation-token": 81})
		fixture.holdSearchResponses()
		fixture.setSearch([]grrItem{{ID: 101, URL: urlA}, {ID: 202, URL: urlB}}, "complete", "1")
		host := startGRRPluginHost(t, noGH, fixture, map[string]string{"PATH": noGH, "GH_TOKEN": "observation-token", "GITHUB_TOKEN": ""})
		DeferCleanup(host.cleanup)
		watchResult := host.watch(t, "observation", "grr-observation", grrWatchArgs("", "40ms"), jsonvalue.NewNull())
		if watchResult.Accepted == nil || !*watchResult.Accepted {
			t.Fatalf("observation watch result = %+v", watchResult)
		}
		// A checkpoint is an ordered protocol barrier while the accepted watch
		// is still dormant; it must perform no source observation or emission.
		host.send(t, pluginprotocol.Frame{Type: "checkpoint", RequestID: "before-activation", SubscriptionID: "grr-observation"})
		prepared := host.receiveType(t, "checkpoint_result", "before-activation", 2*time.Second)
		if prepared.Accepted == nil || !*prepared.Accepted || fixture.searchCountSnapshot() != 0 {
			t.Fatalf("dormant watch performed source work: checkpoint=%+v searches=%d", prepared, fixture.searchCountSnapshot())
		}
		host.send(t, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "grr-observation"})
		host.send(t, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: "grr-observation"})
		firstRequest := grrWaitSearch(t, fixture, 1, 3*time.Second)
		query, err := url.ParseQuery(firstRequest.Query)
		if err != nil || query.Get("q") != "is:pr is:open user-review-requested:@me" || query.Get("per_page") != "100" || query.Get("sort") != "created" || query.Get("order") != "asc" || firstRequest.Host != "api.github.com" || firstRequest.Token != "observation-token" {
			t.Fatalf("GitHub search request = %+v query=%v error=%v", firstRequest, query, err)
		}
		grrReleaseSearch(t, firstRequest)
		eventA := host.event(t, 3*time.Second)
		if eventA.Context != urlA || eventA.EventID != "1" {
			t.Fatalf("first event = %+v", eventA)
		}
		host.send(t, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "grr-observation", EventID: eventA.EventID, Status: "accepted"})
		eventB := host.event(t, 3*time.Second)
		if eventB.Context != urlB || eventB.EventID != "2" {
			t.Fatalf("second event = %+v", eventB)
		}
		host.send(t, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "grr-observation", EventID: eventB.EventID, Status: "accepted"})

		unchanged := grrWaitSearch(t, fixture, 2, 3*time.Second)
		grrReleaseSearch(t, unchanged)
		fixture.setSearch([]grrItem{{ID: 202, URL: urlB}}, "complete", "1")
		missing := grrWaitSearch(t, fixture, 3, 3*time.Second)
		host.assertNoEvent(t, 25*time.Millisecond)
		grrReleaseSearch(t, missing)
		absent := grrWaitSearch(t, fixture, 4, 3*time.Second)
		host.assertNoEvent(t, 25*time.Millisecond)
		grrReleaseSearch(t, absent)
		fixture.setSearch([]grrItem{{ID: 101, URL: urlA}, {ID: 202, URL: urlB}}, "complete", "1")
		reappearedObservation := grrWaitSearch(t, fixture, 5, 3*time.Second)
		grrReleaseSearch(t, reappearedObservation)
		reappearedCommitted := grrWaitSearch(t, fixture, 6, 3*time.Second)
		reappeared := host.event(t, 3*time.Second)
		if reappeared.Context != urlA || reappeared.EventID != "3" {
			t.Fatalf("reappearance event = %+v", reappeared)
		}
		host.send(t, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: "grr-observation", EventID: reappeared.EventID, Status: "accepted"})

		fixture.setSearch([]grrItem{{ID: 202, URL: urlB}}, "incomplete", "1")
		grrReleaseSearch(t, reappearedCommitted)
		incomplete := grrWaitSearch(t, fixture, 7, 3*time.Second)
		if incomplete.Mode != "incomplete" {
			t.Fatalf("expected an incomplete search, got %+v", incomplete)
		}
		grrReleaseSearch(t, incomplete)
		incompleteRetry := grrWaitSearch(t, fixture, 8, 3*time.Second)
		if incompleteRetry.Mode != "incomplete" {
			t.Fatalf("incomplete retrieval was not retried: %+v", incompleteRetry)
		}
		grrReleaseSearch(t, incompleteRetry)
		fixture.setSearch([]grrItem{{ID: 202, URL: urlB}}, "rate-limit", "1")
		rateLimited := grrWaitSearch(t, fixture, 9, 3*time.Second)
		if rateLimited.Mode != "rate-limit" {
			t.Fatalf("expected rate limit, got %+v", rateLimited)
		}
		retryFrom := grrReleaseSearch(t, rateLimited)
		rateLimitedRetry := grrWaitSearch(t, fixture, 10, 3*time.Second)
		if rateLimitedRetry.Mode != "rate-limit" || rateLimitedRetry.At.Sub(retryFrom) < 900*time.Millisecond {
			t.Fatalf("rate-limit retry occurred before server guidance: response=%s retry=%s", retryFrom, rateLimitedRetry.At)
		}
		grrReleaseSearch(t, rateLimitedRetry)
		fixture.setSearch([]grrItem{{ID: 101, URL: urlA}, {ID: 202, URL: urlB}}, "complete", "1")
		recovered := grrWaitSearch(t, fixture, 11, 4*time.Second)
		if recovered.Mode != "complete" {
			t.Fatalf("search did not recover: %+v", recovered)
		}
		grrReleaseSearch(t, recovered)
		recoveryCommitted := grrWaitSearch(t, fixture, 12, 3*time.Second)
		grrReleaseSearch(t, recoveryCommitted)
		host.assertNoEvent(t, 150*time.Millisecond)

		host.send(t, pluginprotocol.Frame{Type: "checkpoint", RequestID: "observation-state", SubscriptionID: "grr-observation", Quiesce: false})
		checkpoint := host.receiveType(t, "checkpoint_result", "observation-state", 2*time.Second)
		if checkpoint.Accepted == nil || !*checkpoint.Accepted {
			t.Fatalf("observation checkpoint = %+v", checkpoint)
		}
		checkpointJSON := string(checkpoint.State.Marshal())
		if !strings.Contains(checkpointJSON, `"entries":[["101",2],["202",2]]`) || strings.Contains(checkpointJSON, urlA) || strings.Contains(checkpointJSON, "observation-token") {
			t.Fatalf("checkpoint does not contain only confirmed progress: %s", checkpointJSON)
		}
		logText := readGRRFile(host.logPath)
		if !strings.Contains(logText, "github_review_request_coverage_limit") || !strings.Contains(logText, "Search covers at most 4,000 GitHub-selected repositories; coverage beyond that limit cannot be detected or guaranteed.") || !strings.Contains(logText, `"category":"incomplete retrieval"`) || !strings.Contains(logText, `"category":"rate-limit"`) || !strings.Contains(logText, `"http_status":429`) {
			t.Fatalf("coverage or retrieval diagnostics are missing: %s", logText)
		}
		for _, marker := range []string{"observation-token", urlA, urlB} {
			if strings.Contains(logText, marker) {
				t.Errorf("diagnostics contain sensitive marker %q", marker)
			}
		}
	})

	It("restores accepted progress through the actual hub runtime-state facility", Label("case:GRR-V03-RESTART"), func() {
		runGRRRestartAcceptance(GinkgoT())
	})
})

func copyGRRFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o700)
}

func readGRRFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

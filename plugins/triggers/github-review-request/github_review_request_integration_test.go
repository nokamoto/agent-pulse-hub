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

	PIt("establishes a silent baseline and observes later request transitions through the public input", Label("case:GRR-V02-OBSERVATION"), func() {
		t := GinkgoT()
		const subscriptionID = "grr-observation"
		const token = "observation-token"
		noGH := grrNoGHDirectory(t)
		a := grrItem{ID: 101, URL: "https://github.com/owner-a/repo-a/pull/11"}
		b := grrItem{ID: 202, URL: "https://github.com/owner-b/repo-b/pull/22"}
		c := grrItem{ID: 303, URL: "https://github.com/owner-c/repo-c/pull/33"}
		d := grrItem{ID: 404, URL: "https://github.com/owner-d/repo-d/pull/44"}
		fixture := newGRRFixture(t, map[string]uint64{token: 81})
		fixture.holdSearchResponses()
		fixture.setSearch(nil, "incomplete", "1")
		host := startGRRPluginHost(t, noGH, fixture, map[string]string{"PATH": noGH, "GH_TOKEN": token, "GITHUB_TOKEN": ""})
		DeferCleanup(host.cleanup)
		watchResult := host.watch(t, "observation", subscriptionID, grrWatchArgs("", "40ms"), jsonvalue.NewNull())
		if watchResult.Accepted == nil || !*watchResult.Accepted {
			t.Fatalf("observation watch result = %+v", watchResult)
		}
		checkpoint := func(requestID string) string {
			host.send(t, pluginprotocol.Frame{Type: "checkpoint", RequestID: requestID, SubscriptionID: subscriptionID})
			result := host.receiveType(t, "checkpoint_result", requestID, 2*time.Second)
			if result.Accepted == nil || !*result.Accepted || result.State == nil {
				t.Fatalf("observation checkpoint rejected: %+v", result)
			}
			return string(result.State.Marshal())
		}
		checkpoint("before-activation")
		if fixture.searchCountSnapshot() != 0 {
			t.Fatalf("dormant watch performed %d searches", fixture.searchCountSnapshot())
		}
		host.send(t, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: subscriptionID})
		host.send(t, pluginprotocol.Frame{Type: "activate_watch", SubscriptionID: subscriptionID})
		request := grrWaitSearch(t, fixture, 1, 3*time.Second)
		query, err := url.ParseQuery(request.Query)
		if err != nil || query.Get("q") != "is:pr is:open user-review-requested:@me" || query.Get("per_page") != "100" || query.Get("sort") != "created" || query.Get("order") != "asc" || request.Host != "api.github.com" || request.Token != token {
			t.Fatalf("GitHub search request = %+v query=%v error=%v", request, query, err)
		}
		// Each request holds a snapshot of the fixture response. The next request
		// is a barrier for the preceding observation, which has already committed
		// or failed before polling can resume.
		advance := func(items []grrItem, mode string) time.Duration {
			fixture.setSearch(items, mode, "1")
			released := grrReleaseSearch(t, request)
			request = grrWaitSearch(t, fixture, request.Sequence+1, 4*time.Second)
			if request.Mode != mode {
				t.Fatalf("expected %s search, got %+v", mode, request)
			}
			return request.At.Sub(released)
		}
		expectEvent := func(item grrItem, eventID string) {
			event := host.event(t, 3*time.Second)
			if event.Context != item.URL || event.EventID != eventID || event.SubscriptionID != subscriptionID {
				t.Fatalf("expected event %s for %s, got %+v", eventID, item.URL, event)
			}
		}
		acceptEvent := func(eventID string) {
			host.send(t, pluginprotocol.Frame{Type: "delivery_notice", SubscriptionID: subscriptionID, EventID: eventID, Status: "accepted"})
		}

		advance([]grrItem{a}, "complete")
		host.assertNoEvent(t, 25*time.Millisecond)
		if state := checkpoint("after-incomplete"); !strings.Contains(state, `"baseline_established":false`) || !strings.Contains(state, `"entries":[]`) {
			t.Fatalf("incomplete observation established a baseline: %s", state)
		}
		abc := []grrItem{a, b, c}
		advance(abc, "complete")
		host.assertNoEvent(t, 25*time.Millisecond)
		if state := checkpoint("after-baseline"); !strings.Contains(state, `"baseline_established":true`) || !strings.Contains(state, `"entries":[["101",3]]`) {
			t.Fatalf("complete observation did not record a silent baseline: %s", state)
		}

		grrReleaseSearch(t, request)
		firstEvent := host.event(t, 3*time.Second)
		if (firstEvent.Context != b.URL && firstEvent.Context != c.URL) ||
			firstEvent.EventID != "1" || firstEvent.SubscriptionID != subscriptionID {
			t.Fatalf("expected first event for B or C, got %+v", firstEvent)
		}
		remaining := b
		if firstEvent.Context == b.URL {
			remaining = c
		}
		// An observation must not supersede the remaining event before emission.
		select {
		case next := <-fixture.searchCalls:
			t.Fatalf("started search %d with %s still unsent", next.Sequence, remaining.URL)
		case <-time.After(125 * time.Millisecond):
		}
		acceptEvent("1")
		expectEvent(remaining, "2")
		request = grrWaitSearch(t, fixture, request.Sequence+1, 3*time.Second)
		acceptEvent("2")
		bc := []grrItem{b, c}
		advance(bc, "complete") // Commit the unchanged observation.
		host.assertNoEvent(t, 25*time.Millisecond)
		advance(abc, "complete") // Commit absence before A reappears.
		host.assertNoEvent(t, 25*time.Millisecond)
		advance(bc, "incomplete")
		expectEvent(a, "3")
		acceptEvent("3")
		advance(bc, "rate-limit")
		host.assertNoEvent(t, 25*time.Millisecond)
		if delay := advance(bc, "rate-limit"); delay < time.Second {
			t.Fatalf("rate-limit retry after %s violated Retry-After", delay)
		}
		abcd := []grrItem{a, b, c, d}
		advance(abcd, "complete")
		host.assertNoEvent(t, 25*time.Millisecond)
		advance(abcd, "complete")
		expectEvent(d, "4")
		acceptEvent("4")
		host.assertNoEvent(t, 25*time.Millisecond)

		state := checkpoint("observation-state")
		if !strings.Contains(state, `"entries":[["101",2],["202",2],["303",2],["404",2]]`) {
			t.Fatalf("checkpoint did not preserve accepted progress: %s", state)
		}
		logText := readGRRFile(host.logPath)
		if !strings.Contains(logText, "github_review_request_coverage_limit") || !strings.Contains(logText, "Search covers at most 4,000 GitHub-selected repositories; coverage beyond that limit cannot be detected or guaranteed.") || !strings.Contains(logText, `"category":"incomplete retrieval"`) || !strings.Contains(logText, `"category":"rate-limit"`) || !strings.Contains(logText, `"http_status":429`) {
			t.Fatalf("coverage or retrieval diagnostics are missing: %s", logText)
		}
		for _, marker := range []string{token, a.URL, b.URL, c.URL, d.URL} {
			if strings.Contains(logText, marker) || strings.Contains(state, marker) {
				t.Errorf("sensitive marker %q appeared in diagnostics or checkpoint", marker)
			}
		}
	})

	PIt("restores silent baseline and accepted progress through the actual hub runtime-state facility", Label("case:GRR-V03-RESTART"), func() {
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

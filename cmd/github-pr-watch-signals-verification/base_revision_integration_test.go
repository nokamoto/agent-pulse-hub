//go:build integration

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// Governed by docs/design/github-pr-watch-signals.md, GPWS-I03.
// Reuse the suite's actual plugin/public Session, verified loopback TLS,
// streamed checkpoint and restore helpers; substitute only GitHub responses.
// Extend case-local fixture fields during implementation to distinguish the
// associated base OID, current target OID, state and mergedAt. Existing query
// dispatch and round counters can script different first/final PR responses.
var _ = Describe("PR base revision and lifecycle continuity", Serial, func() {
	// Register acme/widget#17 already MERGED with H1, associated B1, existing
	// target B2, UNKNOWN and unchanged successful CI/discussion. Watch must be
	// accepted; activate and repeat a complete silent baseline, then checkpoint.
	// Separately baseline OPEN H1/B1. Set MERGED with associated B1 and target
	// B2, but return B3 on the final PR read for one candidate round. Assert
	// observation_changed_during_acquisition, no event, and unchanged complete
	// comparison state, journal and positions by inspecting streamed checkpoints.
	// Recover with stable B2: lifecycle notification identifies MERGED, H1,
	// current target B2 and raw UNKNOWN; no conflict or inferred resolution.
	// Accept, rewait, then change only the H1 CI check from success to failure.
	// Assert one independent CI event despite MERGED and unequal base OIDs.
	// Accept, rewait and repeat unchanged: no event. Checkpoint, stop normally,
	// restore the same objects through public Session, activate and repeat.
	// Assert no accepted-event duplicate and preserved pair periods/positions.
	// All contexts remain <=8192 UTF-8 bytes and carry no credential markers.
	// Use round/checkpoint barriers, await children and close the TLS fixture.
	// Budget: 10 seconds excluding shared builds; no new runtime/dependencies.
	It("preserves lifecycle and independent signals with stable unequal base OIDs", Label("case:GPWS-I03"), signalBaseRevisionCase)
})

func signalBaseRevisionCase() {
	t := GinkgoT()
	f := newGPWFixture(t)
	f.changeSignals(func(s *signalFixtureState) {
		*s = signalFixtureState{CI: 1, Base: 2, AssociatedBase: 1, Lifecycle: "MERGED", Conflict: "UNKNOWN"}
	})
	h := startGPWHost(t, gpwSuite.newCase(t), "github.com", f, nil)
	if result := h.watch(t, "merged", "github.com"); result["accepted"] != true {
		t.Fatal("stable unequal merged watch rejected", result)
	}
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "merged", "generation": "merged-1"})
	signalWaitState(t, h, "merged", func(s map[string]any) bool { return s["baseline"] == true })
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	merged := h.checkpoint(t, "merged", "merged-baseline")
	header := gpwState(t, merged)
	if header["sequence"] != float64(1) || header["version"] != float64(3) || header["signal_version"] != float64(1) {
		t.Fatal("merged baseline reset or emitted changes", header)
	}
	gpwRequireContains(t, merged, signalBase(2), "UNKNOWN", "MERGED")
	h.close()

	f.changeSignals(func(s *signalFixtureState) {
		*s = signalFixtureState{CI: 1, Base: 1, AssociatedBase: 1, Lifecycle: "OPEN", Conflict: "UNKNOWN"}
	})
	h = startGPWHost(t, gpwSuite.newCase(t), "github.com", f, nil)
	if result := h.watch(t, "continuity", "github.com"); result["accepted"] != true {
		t.Fatal(result)
	}
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "continuity", "generation": "continuity-1"})
	signalWaitState(t, h, "continuity", func(s map[string]any) bool { return s["baseline"] == true })
	// Hold unsuccessful reads before taking the pre-race cut, so an in-flight
	// successful baseline repeat cannot change its observation timestamps.
	f.changeSignals(func(s *signalFixtureState) { s.Failure = "pr-read" })
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(h.log), "service") }) {
		t.Fatal("failure barrier missing")
	}
	before := h.checkpoint(t, "continuity", "before-race")
	f.changeSignals(func(s *signalFixtureState) {
		s.Failure = ""
		s.Lifecycle = "MERGED"
		s.Base = 2
		s.FinalBase = 3
	})
	if !waitMVP(t, time.Second, func() bool { return strings.Contains(readMVPFile(h.log), "observation_changed_during_acquisition") }) {
		t.Fatal("racing target not rejected", readMVPFile(h.log))
	}
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	after := h.checkpoint(t, "continuity", "after-race")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed acquisition advanced complete state", before, after)
	}
	f.changeSignals(func(s *signalFixtureState) { s.FinalBase = 0 })
	event := h.receive(t, "state_event", "")
	context := gpwContext(t, event["context"])
	changes := context["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["kind"] != "merged" || changes[0].(map[string]any)["item"].(map[string]any)["kind"] != "pull_request" {
		t.Fatal("merge did not independently notify", context)
	}
	current := context["current"].(map[string]any)
	conflict := current["conflict"].(map[string]any)
	if conflict["state"] != "UNKNOWN" || conflict["confirmed_in_period"] != false || conflict["pair"].(map[string]any)["base_sha"] != signalBase(2) {
		t.Fatal("invented conflict or wrong current target", context)
	}
	gpwRequireContains(t, context, "MERGED", signalHead(), signalBase(2), "UNKNOWN")
	position := context["confirmation_position"].(string)
	signalResume(t, h, "continuity", event, position, 2)
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	f.changeSignals(func(s *signalFixtureState) { s.Failed = true })
	event = h.receive(t, "state_event", "")
	context = gpwContext(t, event["context"])
	changes = context["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["category"] != "ci_check" {
		t.Fatal("CI failed to notify independently on merged PR", context)
	}
	gpwRequireContains(t, context, "MERGED", signalHead(), signalBase(2), "FAILURE", "K000")
	position = context["confirmation_position"].(string)
	signalResume(t, h, "continuity", event, position, 3)
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	saved := h.checkpoint(t, "continuity", "saved")
	header = gpwState(t, saved)
	if header["sequence"] != float64(3) || header["confirmed"] != float64(3) {
		t.Fatal("positions advanced beyond represented changes", header)
	}
	gpwRequireNoSecrets(t, h.log, "private-token-marker", "private-conflict-marker", "Bearer high")
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "Bearer high") {
		t.Fatal("credential marker in checkpoint")
	}
	h.close()
	h = startGPWHost(t, gpwSuite.newCase(t), "github.com", f, nil)
	if result := h.restore(t, "continuity", "github.com", saved["schema"].(string), saved["chunks"].([]any)); result["accepted"] != true {
		t.Fatal("stream restore rejected", result)
	}
	if restored := h.checkpoint(t, "continuity", "restored"); !reflect.DeepEqual(saved, restored) {
		t.Fatal("restore changed retained state", saved, restored)
	}
	h.send(t, map[string]any{"type": "activate_watch", "subscription_id": "continuity", "generation": "continuity-1"})
	f.waitRounds(t, f.count()+2)
	h.noEvent(t)
	repeated := h.checkpoint(t, "continuity", "repeated")
	// Successful unchanged observations may update their observation counter and
	// timestamps, but cannot change pairs, periods, journal, or retained positions.
	for _, cut := range []map[string]any{saved, repeated} {
		state := gpwState(t, cut)
		delete(state, "started_at")
		delete(state, "ended_at")
		delete(state, "observation_sequence")
	}
	if !reflect.DeepEqual(saved, repeated) {
		t.Fatal("unchanged restored watch lost continuity", saved, repeated)
	}
	gpwRequireNoSecrets(t, h.log, "private-token-marker", "Bearer high")
	h.close()
}

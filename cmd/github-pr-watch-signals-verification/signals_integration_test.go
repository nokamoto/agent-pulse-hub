//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/github-pr-watch-signals.md. Pending defers product
// wiring, not the observable contract. Reuse the established verified-loopback
// TLS and command-launcher technique from github-pr-watch-verification; replace
// GitHub responses and timing only, keeping actual plugin/hub/action behavior.
var _ = Describe("Independent PR signals", Serial, func() {
	// GPWS-I01: Launch the real github-pr-watch plugin through its public Session
	// with selected PR acme/widget#17, head H1, base B1, draft=true, one successful
	// check, one successful commit status, and unchanged review/comment data.
	// Use the default signals-v3 profile and negotiate runtime_state_stream_v1;
	// inspect a v3 header and signal_version=1 after a complete checkpoint.
	// Authenticate against controlled HTTPS with hostname/certificate verification.
	// Return a paginated CI acquisition error before completing the initial round:
	// assert no baseline/event. Then complete and repeat the baseline: no event.
	// Run each following branch from that baseline with an active wait, leaving
	// head and all discussion data unchanged:
	// (a) Complete check K1 with FAILURE, then separately status S1 with failure.
	// (b) Move only the base to B2, return UNKNOWN, then CONFLICTING for H1/B2.
	// (c) Set isDraft=false.
	// Each branch must independently emit one bounded state_event identifying the
	// PR, qualifying change, current state, observation times and confirmation
	// position. CI carries source/identity/name/result/head/link; conflict carries
	// compared head/base refs and OIDs; readiness carries previous/current state.
	// With acceptance notices and explicit rewait through the public control,
	// repeat identical observations and assert no new event. Old-head CI must not
	// appear as current failure. UNKNOWN alone must not report confirmed conflict.
	// Fail a required status page and, independently, the conflict query after a
	// baseline; assert a safe diagnostic and no committed coverage/position.
	// Recover with the outstanding qualifying change and observe its notification.
	// Inspect the event's decoded UTF-8 size <=8192, external-data marker, and
	// unchanged registered waiting context. No credentials enter event/diagnostic.
	// Close Session and await child exit on success or failure.
	It("notifies each new category independently and preserves coverage on acquisition failure", Label("case:GPWS-I01"), signalObservationCase)

	// GPWS-I02: Launch the real hub, client, plugin and model-output action with
	// a temporary core state directory and controlled GitHub HTTPS, signals-v3.
	// Start with 128 CI records (256-byte names/descriptions and distinct links),
	// failed CI/conflict/Ready already present: complete baseline is silent.
	// Require a checkpoint >32768 bytes using multiple objects, each <=32768,
	// and complete wire frames <=65536. No private signal store is substituted.
	// Observe recovery, conflict resolution and Ready-to-Draft: no notification.
	// Add comment C1 to obtain accepted position P1 at registered destination A.
	// While consumed, repeat three cycles: new failed run, a new base-only
	// confirmed conflict pair, Draft-to-Ready; then complete recovery/resolution/
	// Ready-to-Draft. This retains exactly nine eligible signal records after P1.
	// Use small required changed metadata so eight fit one 8192-byte context;
	// current CI details are explicitly omitted with counts/PR link as necessary.
	// No notification is admitted while consumed. Freeze external data, rewait
	// from P1 with the real client. Expect eight ordered records, through=P1+8,
	// has_more=true, remaining_changes=1, observed_through=P1+9, operation=rewait.
	// Covered C1 is absent and destination B untouched. Accept that prefix,
	// checkpoint and stop normally. Restart with frozen source: exactly the
	// ninth record reaches A, not the accepted eight; has_more=false and position
	// P2=P1+9. Rewait P2 unchanged: no event. Then observe another failed result,
	// conflict and Draft-to-Ready: fresh episodes are eligible.
	// Assert same registration/nonce, no inferred agent assessment, and no plugin
	// private state files. Independently restore an old payload without signal
	// history (both v1 and discussion-only v2): the affected watch fails before
	// activation with incompatible-state
	// diagnostics, keeps the saved payload, and cannot accept its old position as
	// signal coverage. An unrelated retained registration is not reset.
	// Scope the fixtures to signals-v3; the existing GPW-I01..I05 regression suite
	// explicitly uses discussion-v2, including its unchanged v1 migration branch.
	// Stop and await hub/plugin/action; remove temporary data.
	It("retains independent signals through rewait and normal restart without repeated unchanged work", Label("case:GPWS-I02"), signalRestartCase)
})

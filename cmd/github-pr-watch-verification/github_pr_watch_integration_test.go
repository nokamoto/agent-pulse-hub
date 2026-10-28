//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/github-pr-watch.md. The separate feasibility probe
// qualifies fixture mechanics only; these cases execute actual product paths.
var _ = Describe("GitHub PR watch acceptance", Serial, func() {
	// GPW-I01: Launch the actual plugin through the public framework/Session,
	// with only GitHub HTTPS, credentials, and time replaced by local controls.
	// Select github.com PR acme/widget#17 with reviewer waiting context; select
	// ghes.example.test PR acme/widget#17 independently with author context.
	// Use GH_TOKEN=high and GITHUB_TOKEN=low with no gh on PATH: observe only
	// high at the selected origin. Reject high with low still valid: no fallback.
	// With environment tokens absent, run a controlled gh executable and inspect
	// auth token --hostname ghes.example.test and its sanitized child environment.
	// Before activation, permit authentication validation but no observation.
	// Fail the first observation's second comments page: no baseline or event.
	// Complete all pages with H1, comment C1, review R1, thread T1 and reply C1r:
	// baseline produces no event; repeating it produces no event. Then expose
	// C2, H2, R1=DISMISSED, T1=resolved, and changed requested-reviewer/lifecycle
	// state in one complete preparation. Expect one grouped state_event retaining
	// item/review/reply/thread identities, provenance, current state, waiting
	// context, and an opaque confirmation position. Caller supplies notices.
	// In that changed observation use a multibyte body/change set exceeding 8192
	// bytes and an empty body:
	// context remains <=8192 decoded UTF-8 bytes, distinguishes empty/shortened,
	// names omitted items/categories and links/continuation, and does not advance
	// coverage over unreported changes. Failed pagination and GraphQL errors with
	// HTTP 200 preserve complete progress and produce safe visible diagnostics;
	// recovery unchanged invents neither disappearance nor ordinary change.
	// Assert auth/PR/position/progress isolation across the two selected hosts,
	// no secret/private-body markers in diagnostics, and external text cannot
	// change the destination or authority. Stop Session; await process exit.
	It("observes a complete baseline and bounded changes through the public plugin with host-scoped authentication", Label("case:GPW-I01"), gpwObservationCase)

	// GPW-I02: Use the actual daemon/client/plugin and model-output action, with
	// controlled HTTPS only. Register acme/widget#17 to output A; leave output B
	// unregistered. Establish H1/C1, then add C1b and receive position P1 at A.
	// Hold the preceding delivery's terminal notice/checkpoint fence while the
	// real client issues a same-registration rewait from P1. Expect rewait_busy
	// with no progress/admission mutation before that fence completes.
	// While the agent works, expose C2 and H2 and complete a source observation.
	// Complete the fence and retry rewait; make no subsequent source change. Expect
	// exactly one next context containing C2/H2 and current state at A, using the
	// same subscription, waiting context and immutable destination. Covered P1 changes
	// and superseded wait admission do not cause duplicate output; B stays empty.
	// Repeat the identical rewait operation ID: one operation result/admission.
	// Submit foreign-host/PR, malformed, incompatible, and expired positions:
	// actionable continuity errors, with no reset, route change or new admission.
	// Keep monitor polling after its admitted notification, but admit no second
	// ordinary notification until another explicit rewait. Observe through public
	// commands, model-output records and bounded fixture journals, then stop and
	// await daemon/plugin/action exits. No test-only cancellation replaces rewait.
	It("rewaits on the same route from P1 without losing intervening C2 and H2 or overlapping the previous wait", Label("case:GPW-I02"), gpwRewaitCase)

	// GPW-I03: Use the actual daemon/client/plugin/model-output, temporary state,
	// and controlled GitHub. Accept a C2/H2 notification, wait for its accepted
	// notice and successful checkpoint, stop normally, and await all processes.
	// Restart the retained registration with unchanged C2/H2: no repeat solely
	// from restart. Expose fresh C3: restart rearming permits one notification
	// with C3 and current state, same registration and destination. It does not
	// claim that the agent assessed either accepted context.
	// In a separate branch deny a runtime save using a deterministic local
	// filesystem obstruction, leaving source/action healthy. Expect the exact
	// common continuity warning; after explicit rewait expose fresh C4 and observe
	// continued monitoring/notification. Do not call unsaved progress durable.
	// With every process stopped, replace the newest saved payload with an
	// incompatible plugin schema: restart blocks before observation/delivery,
	// identifies registration/reason, and never falls back to baseline. Restore
	// a preserved valid tree for other branches. Delete one stopped registration
	// through documented recovery and recreate equivalent arguments without an
	// explicit position: new ID and fresh baseline, no implicit old checkpoint.
	// Keep another retained registration distinct throughout. Verify plugin owns
	// no state-file/service operations. Cleanup only after every child exits.
	It("preserves confirmed progress across normal restart and exposes save, restore and registration-isolation boundaries", Label("case:GPW-I03"), gpwRestartCase)

	// GPW-I04: Extend the existing TLS fixture with generic first:25 cursor
	// pages and observable completed-round markers; build the actual plugin once
	// in BeforeSuite and use startGPWHost's public Session. Establish a silent
	// complete baseline of 1,000 short items: 994 conversation comments, review
	// R1, thread T1, its root RC1 and reply RC2, and requests Q1/Q2. Assert the
	// RC2->RC1/R1/T1 relationships survive, then repeat the unchanged round.
	// Add C1001, edit C0001, remove C0002, and change head H1 to H2. Independently
	// fail a later comments page and a nested thread-comments page before the
	// round completes: no event, false disappearance, sequence commit, or silent
	// reset; restoring those pages reports exactly the outstanding transitions.
	// Complete the public delivery-notice fence. Require runtime_state_v2 and
	// runtime_state_stream_v1. Send the ordinary checkpoint request; collect
	// checkpoint_stream_begin, checkpoint_stream_chunk and checkpoint_stream_end
	// through Session and retain the complete opaque snapshot in the test host.
	// Close the first process; start another actual plugin and restore that exact
	// stream using state_stream_watch, indexed state_stream_chunk objects and
	// state_stream_end to the same registration before activation. Indices/counts
	// are canonical uint64 decimal strings; positive watch_result follows the
	// complete stream, and no observation runs before activation. An unchanged
	// complete round emits no duplicate; one new old-item edit remains reportable.
	// Measure each full wire message <=65,536 bytes and each compact chunk object
	// <=32,768 bytes; require a logical snapshot larger than 32,768 bytes
	// so this cannot pass through the legacy envelope alone. No plugin file API,
	// live service, production bypass, or new shared test harness is required.
	// The 31/32/33/100/1,000 sizes and conversation-only/mixed combinations belong
	// to the cheaper unit matrix; 1,000 mixed items retain the assembled boundary.
	// Budget 10 seconds excluding shared builds; one local TLS server and at most
	// one live input process. Close Session, await exit, and remove temporary data.
	It("preserves a thousand-item paginated observation and streamed plugin progress through recovery and restore", Label("case:GPW-I04"), Pending, func() {})

	// GPW-I05: Reuse gpwNewDaemon, the real register/rewait/stop commands, actual
	// PR-watch plugin and model-output action, and a temporary core state tree.
	// Establish a complete 100-item baseline, deliver one seed change at P0, then
	// consume that wait. While it is consumed, observe 40 new comments D01-D40
	// in one complete round, exceeding both former eight-change and 32-record
	// bounds: D01 has >8,192 UTF-8 body bytes, D02 is genuinely empty, and D03-D40
	// have distinct short bodies. This retains more than 32 small changes.
	// Fail one later acquisition page first, then recover it; freeze all source
	// content after the successful round. Rewait from P0 and then each returned
	// position until all 40 changes have been represented. Assert every context
	// <=8,192 decoded bytes, unchanged registration/destination, increasing covered
	// sequences without gaps, no duplicate transition, explicit body shortening
	// and digest/link, empty-body distinction, and visible continuation remaining.
	// Each returned position covers only represented transitions; at least one
	// intermediate context must leave outstanding work without new activity.
	// After the first continuation context receives accepted delivery_notice,
	// stop normally, await all children, and preserve that successfully committed
	// streamed snapshot tree. Restart without registration: the reopened wait
	// exposes only remaining outstanding changes, then rewait to exhaust them.
	// Inspect bounded protocol journals and core-owned chunk/manifest files to
	// require at least two chunks and establish multi-message save/restore and
	// atomic generation association.
	// In a separate migration branch, create a stopped registration tree with a
	// valid v1 .json runtime fixture: github-pr-watch-v1 payload, <=32 fingerprints,
	// retained nonce/journal, confirmed progress and an issued gpw1 position Pold.
	// Restart the real daemon with the new plugin; observe state_stream_watch
	// restoring that legacy schema and no baseline reset or unchanged duplicate.
	// Rewait from Pold, report its outstanding change, accept delivery and stop.
	// Require a higher .stream generation using github-pr-watch-v2, with retained
	// registration identity and Pold still accepted while its history is retained.
	// Return a preserved valid older .json fixture copy to the stopped tree after
	// successful-save cleanup. Restart: the higher .stream generation is selected,
	// no duplicate arises, and a new change continues from retained progress.
	// In isolated stopped copies of the preserved tree, remove a referenced chunk
	// and separately replace one chunk with truncated invalid JSON. Each restart
	// must identify watch/reason, fail before that watch's source work/delivery,
	// preserve files, and never
	// fall back to a baseline or an older generation. Restore the valid tree.
	// Separately obstruct this watch's runtime directory with a regular file as
	// in GPW-I03; after another small change, expect the common continuity warning
	// and continued rewait/delivery without claiming unsaved progress durable.
	// Broad malformed/missing/reordered stream inputs and commit fault points are
	// unit cases; this case retains actual command/process/file/restart wiring.
	// Budget 20 seconds excluding shared builds; one hub/input/output/TLS server
	// at a time, no fixed sleeps, and cleanup only after all children exit.
	It("continues beyond forty outstanding changes through bounded rewait contexts and core-owned streamed restart", Label("case:GPW-I05"), Pending, func() {})
})

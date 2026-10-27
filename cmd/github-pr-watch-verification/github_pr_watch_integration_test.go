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
})

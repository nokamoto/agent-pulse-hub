//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/notification-delivery.md. Reuse builtAcceptance,
// startPublicAction, controlled Codex argv journal and process cleanup. The
// actual action is the system under test; no daemon or live Codex is required.
var _ = Describe("Codex Queue notification profile acceptance", Serial, func() {
	It("reports the actual queue launch boundary through its optional public profile", Label("case:ND-V04-ACTION"), Pending, func() {
		By("starting the built action with --notification-delivery through the public go-plugin v2 Session and a copied controlled Codex executable; ready advertises notification_delivery_v1")
		By("validating registered conversation A, deleting the executable only after successful prerequisite validation, then sending notification_deliver with fixed notification/attempt UUIDs and the original external-data envelope")
		By("observing exactly correlated unsubmitted proof for failure to start, bounded reason/message and retryable false for the missing executable; the process journal contains zero queue invocations, and the action never schedules retries itself")
		By("restoring the executable and sending the same notification binding with a fresh attempt UUID; an accepted ACK naming A yields accepted with the exact request/delivery IDs and optional opaque receipt; inspect the journal for one queue --thread A --message invocation and preserved untrusted context")
		By("using independent notification IDs for controlled started-process nonzero exit without ACK, zero exit without ACK and an ACK naming B; each yields unknown rather than unsubmitted/rejected/accepted, and none starts another queue process")
		By("holding a controlled process past the existing real child deadline through the already governed CQ-V02-OUTCOMES case; preserve its unknown result and child-reaping assertion rather than adding another twenty-second wait here")
		By("checking each result preserves the exact notification/attempt IDs and binds the ACK to that request's original destination; identity-conflict rejection is verified in core units, without adding an action-side deduplication cache")
		By("checking diagnostics exclude context, credentials, complete arguments and raw process output, then closing the Session and proving the configured action and every controlled child were reaped")
		By("retaining default invocation without --notification-delivery under existing CQ-V01/CQ-V02 checks: legacy ready/validate/deliver behavior remains one attempt and advertises no new capability")
	})
})

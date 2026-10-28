//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/notification-delivery.md. These static Pending cases
// retain the real daemon, public clients, queue, scheduler, lifecycle and store.
// Command-owned model peers replace plugin endpoints only. Reuse mvpEnv builds,
// normal stop/restart, temporary trees, numbered commands and observable gates.
// Policy/clock/storage-error/input matrices belong to implementation unit tests;
// these workflows select the assembled boundaries those tests cannot establish.
var _ = Describe("Notification delivery acceptance", Serial, func() {
	It("exposes correlated status and retries only proven unsubmitted attempts", Label("case:ND-V01-STATUS-RETRY"), Pending, func() {
		By("starting the actual hub with command-owned input/output peers; output advertises notification_delivery_v1, while a separate legacy output advertises no capability")
		By("registering A and B, blocking a preceding worker through an observable peer gate, admitting A's next event through the controlled input, and inspecting it before its first action request: no attempt yet is distinct from in progress; release the earlier gate without a production test hook")
		By("emitting one UTF-8 external context for A and holding its notification_deliver at a peer gate after journaling request_id and delivery_id")
		By("using notifications --subscription-id A --limit 1 and notification --notification-id ID to observe that one stable notification, destination A, its in-progress attempt and distinct retry eligibility; inspection creates no output request")
		By("returning notification_result unsubmitted with retryable true, then observing a different attempt UUID with the same notification UUID, context and destination no earlier than two seconds after the first attempt ended, without a CLI retry, trigger event or new registration")
		By("sending foreign request/delivery correlations and forged receipt text in event context; none confirms the held attempt or changes recipient B; a valid matching accepted result confirms queue acceptance only and stops scheduling")
		By("emitting a separate event with identical context and observing a different notification UUID that remains independently deliverable; continuation events likewise remain distinct from attempts")
		By("admitting separate permanent-unsubmitted, rejected and unknown notifications, observing one attempt each and the corresponding permanent, post-submission or uncertainty stopping reason; no later callback issues another attempt")
		By("using a final accepted peer exchange as the stream ordering barrier, observing bounded notification pagination with --after and --limit, and checking the documented revision/offset chunk assembly yields complete show JSON even for an already-admissible maximum-size destination without enqueueing work")
		By("checking list summaries omit arguments and show includes the full registered destination and attempts but neither output exposes context/credentials/raw argv; B received no notification from A's context")
		By("sending an event through the legacy route and observing exactly its existing one-attempt deliver/delivery_result exchange, without inferring opt-in or sending notification_deliver")
		By("stopping normally, awaiting all configured process exits, and removing the isolated temporary tree; bounded waits replace arbitrary sleeps")
	})

	It("retains evidence and resumes only eligible remaining attempts after normal restart", Label("case:ND-V02-RESTART"), Pending, func() {
		By("using the real persistent hub with case-owned peers to record accepted, unknown, interrupted-in-progress, eligible-unsubmitted and exhausted notifications under distinct original IDs")
		By("exhausting one notification with exactly three retryable unsubmitted results, observing minimum two-second and ten-second gaps and a visible exhausted reason; this workflow alone consumes the complete real retry schedule")
		By("stopping normally after an eligible notification has recorded an unsubmitted attempt and next due time but before that due time; retaining the state tree, IDs, attempt ordinals and accepted/unknown evidence")
		By("restarting from that same tree without registration or another trigger event, discovering retained IDs through public notifications, and observing that only the eligible notification resumes at its recorded ordinal and due time")
		By("confirming the resumed attempt preserves destination/context/notification UUID and consumes only the remaining bound; accepted, unknown, interrupted-in-progress and exhausted records issue zero new requests")
		By("inspecting the original IDs and comparing public acceptance status, attempts, times and stopping reasons before/after restart; missing evidence never appears as no-attempt or creates a replacement identity")
		By("stopping, obstructing a required notification-state path with a regular file, and restarting; observing visible startup/storage failure before restoration or any output request; restoring the preserved compatible tree recovers inspection without resetting attempts")
		By("in isolated copies of the stopped state tree, removing a required reservation guard and corrupting the highest whole-ledger generation separately; each restart fails before any attempt, never falls back to an older unsent view, and preserves the remaining evidence for supported recovery")
		By("verifying seven-day expiry, backward-clock handling, incompatible/missing records and retry-guidance combinations in mocked-clock/storage units rather than waiting for expiry or multiplying process scenarios")
		By("stopping normally and reaping all children; limit this workflow to the existing two-minute scoped-suite budget including one twelve-second full retry schedule")
	})

	It("fences scheduled work on unsubscribe while retaining issued-attempt evidence", Label("case:ND-V03-CANCEL"), Pending, func() {
		By("starting the actual hub with peers advertising notification_delivery_v1 and the approved cancel_watch_v1 input exchange; registering A and B on shared processes")
		By("recording A's first retryable unsubmitted outcome and next due time, invoking unsubscribe --subscription-id A before retry issuance, and waiting for successful cancellation and durable registration/progress removal")
		By("crossing A's former retry due time while B delivers an accepted event as an observable scheduling/stream barrier; A has one attempt and no further notification_deliver, both shared plugins remain alive")
		By("discovering A's retained notification through notifications --subscription-id A after its registration is removed, with retry stopped for cancellation; B remains usable")
		By("in a separate registered route, holding an already-issued attempt, unsubscribing successfully without waiting for that attempt, then returning unknown followed by matching accepted notification_evidence within its original thirty-second Session deadline")
		By("observing queue-accepted evidence for that original attempt without watch activation, retry, registration/runtime-file recreation or a second terminal delivery_notice; foreign or expired evidence cannot update it")
		By("stopping normally and restarting the same state tree; neither cancelled route is restored or retried, retained inspection still works, and registering identical arguments obtains a fresh subscription UUID with no inherited notification work")
		By("cleaning up the isolated tree and all child processes; cancellation permutations, partial failures and stale-callback races remain lifecycle/storage unit coverage plus SC-V01/SC-V02/SC-V03")
	})
})

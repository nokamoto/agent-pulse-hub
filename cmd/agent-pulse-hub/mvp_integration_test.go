//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Contracts, methods and budgets: docs/design/mvp.md#selected-command-workflows.
// Static Pending records the revised design; it is not product acceptance.
var _ = Describe("MVP daemon and registration public behavior", Serial, func() {
	It("rejects truncated configuration and a missing executable before plugin startup", Pending, Label("case:MVP-V01-INVALID-CONFIG"), func() {
		By("invoking both invalid files and observing exit one, diagnostics, no plugin start and no work request")
	})
	It("activates an independent language-neutral watch only after acknowledgement", Pending, Label("case:MVP-V02-REGISTER"), func() {
		By("holding the Python Session result while registration stays pending and delivery remains absent")
		By("releasing a positive result followed by an immediate event; observing one subscription and exact A delivery")
		By("rejecting another watch without active state and stopping through standard go-plugin lifecycle")
	})
	It("routes an atomic manual trigger after an idle interval", Pending, Label("case:MVP-V03-ROUTE"), func() {
		By("registering A twice with the same ID and observing one declared second without work requests")
		By("publishing PULSE-ROUTE through a sibling rename and observing one exact A delivery, no B call, and removed trigger/claim")
		By("retaining receipt/attempt times, permission wrapper and manual-plugin idle source inspection")
	})
	It("rejects invalid events and isolates an exited emitter", Pending, Label("case:MVP-V04-INVALID-EVENT"), func() {
		By("rejecting malformed JSON, unknown ID and B-owned ID from A while admitting a later valid event")
		By("ending A, rejecting subsequent A registration/events, and retaining B registration and delivery")
	})
	It("reports launched nonzero and actual deadline outcomes without retry", Pending, Label("case:MVP-V04-DELIVERY-OUTCOME"), func() {
		By("emitting to a nonzero child then one held past 30 seconds; observing unknown, one attempt each and terminated held child")
	})
	It("rejects stale watches after restart", Pending, Label("case:MVP-V05-RESTART"), func() {
		By("registering, stopping, restarting, and observing old ID rejection and one fresh-ID delivery")
	})
	It("deduplicates response loss and concurrent registrations without deduplicating events", Pending, Label("case:MVP-V10-DUPLICATE"), func() {
		By("closing the first response, issuing two concurrent equivalent requests, releasing one watch result and repeating publicly")
		By("observing one watch/same IDs, then two identical events with distinct delivery IDs and ordered once-only attempts")
	})
	It("stops configured plugin processes through the standard lifecycle", Pending, Label("case:MVP-V01-SHUTDOWN"), func() {
		By("starting a fixture and manual plugin, requesting public stop, observing acknowledgement, listener closure, exit zero and configured-child exit")
	})
	It("registers locally and rejects a conflicting listener", Pending, Label("case:MVP-V08-LOCAL-CONTROL"), func() {
		By("registering on loopback, rejecting a second daemon before plugin startup, and proving the first remains usable")
		By("rejecting non-loopback address before any connect or listen")
	})
})

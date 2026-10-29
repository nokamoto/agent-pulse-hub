//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

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
	It("preserves lifecycle and independent signals with stable unequal base OIDs", Label("case:GPWS-I03"), Pending, func() {})
})

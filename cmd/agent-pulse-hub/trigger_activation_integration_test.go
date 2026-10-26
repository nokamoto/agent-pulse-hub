//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/trigger-runtime-state.md. Product wiring follows
// design approval and merge; the existing implemented TRS cases remain unchanged.
var _ = Describe("Runtime-state v2 activation", Serial, func() {
	It("waits for complete restoration before granting prepared watches", Pending, Label("case:TRS-V04-ACTIVATION"), func() {
		// Use actual hub/client, model output and command-local storage-free v2
		// input fixture. Register two routes and stop after successful snapshots.
		// Sort retained IDs by filename; restart with fresh U2 already available.
		// The fixture accepts/applies the first state_watch but holds the later
		// watch_result at a file-controlled barrier. Observe its prepared journal
		// row and the later request barrier; no activate_watch, source read,
		// state_event or output is permitted before release. Successful release
		// must finish all watch results before grants, then deliver U2 once on
		// the saved ID with accepted notice, no re-registration or source retry.
		// Stop and restart a preserved valid tree, rejecting the held later watch:
		// no grant/source read/event/delivery/readiness for either route, records
		// retained, all configured processes exit. Clean temporary trees on failure.
		// Unit tests own grant duplicates, invalid IDs, dormant checkpoints,
		// quiesce-before-grant, storage/send/stop races and unchanged v1 handling.
	})
})

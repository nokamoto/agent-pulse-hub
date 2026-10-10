//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Static Pending defers product wiring; the design fixes these small real-file
// workflows and assigns other content, path and I/O branches to unit tests.
var _ = Describe("MVP manual plugin public protocol", func() {
	It("preserves the context bound and rejects representative invalid files", Label("case:MVP-V04-MANUAL-INVALID-CONTENT"), Pending, func() {
		By("Inputs: real manual-plugin.exe over stdin/stdout, accepted absolute absent trigger_file in a writable local directory, and separate UTF-8 file contents: an exactly 8192-byte valid multibyte context, 8193 bytes, and invalid UTF-8.")
		By("Action: observe ready and accepted watch_result, atomically publish each file, and collect event frames, bounded stderr and target/claimed file state. Await each consumption observation before publishing the next input; send shutdown and await exit.")
		By("Expected: exact valid bound emits once with unchanged decoded context and removes the consumed file; oversized/invalid encoding emits no event, reports bounded diagnostics with the claimed path, and never truncates or retries the claimed file. Empty content and claim/read/removal failure branches use mocked filesystem unit tests.")
		By("Budget: 5 seconds including setup and cleanup after suite-level build, one real plugin and bounded temporary files; no ACL mutation or race-dependent failure injection.")
	})

	It("rejects an equivalent watched path and an existing trigger", Label("case:MVP-V02-MANUAL-PATHS"), Pending, func() {
		By("Inputs: real manual-plugin.exe, an absent absolute file under a writable existing local parent, a slash/dot spelling referring to that same parent/file, and a second already-existing trigger file.")
		By("Action: accept the first watch, submit distinct request/subscription IDs for its equivalent spelling and the existing file, then atomically publish one recognizable valid context to the accepted location and shut down.")
		By("Expected: both later watch_result frames have accepted:false and a nonempty error; one event belongs only to the first accepted ID. Parent-identity lookup, junction aliases, ordinal case and unsupported path-form permutations use unit tests.")
		By("Budget: 5 seconds including setup and cleanup, one real plugin and a small temporary directory; no junction, account, network or permission setup.")
	})
})

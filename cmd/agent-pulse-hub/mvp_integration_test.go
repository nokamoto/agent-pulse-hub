//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Static Pending defers product wiring, not the verification decisions. Each case
// specifies a bounded public workflow and the fixtures owned by docs/design/mvp.md.
var _ = Describe("MVP daemon and registration public behavior", func() {
	It("rejects representative invalid startup inputs before starting plugins", Label("case:MVP-V01-INVALID-CONFIG"), Pending, func() {
		By("Inputs: built public daemon, one malformed absolute UTF-8 config file and one otherwise valid config naming an absent plugin executable; a local Codex substitute records --version and queue invocations.")
		By("Action: invoke daemon --config <file> for each variant and collect exit status, stderr and plugin process-start records.")
		By("Expected: exit 1 with a visible bounded diagnostic and no plugin process or queue call. Configuration, version-probe and CLI parsing permutations are unit obligations, not more process scenarios.")
		By("Budget: 4 seconds including setup and cleanup after the suite builds binaries once; at most the daemon and one version-probe substitute are live.")
	})

	It("activates a language-neutral watch only after acknowledgement", Label("case:MVP-V02-REGISTER"), Pending, func() {
		By("Inputs: real daemon/client and the reviewed fixed PowerShell plugin-v1 source in testdata. Read its body into one configured argv: powershell.exe -NoLogo -NoProfile -NonInteractive -Command <fixed script body>. Set fixture-root paths only in the daemon child environment; matching CODEX_THREAD_ID/CODEX_SESSION_ID = 11111111-1111-4111-8111-111111111111 belongs only to register clients. The fixture uses UTF-8 JSON lines/file gates and imports no Go code or agent API; no script-file execution or execution-policy change is required, and event data is never script source.")
		By("Action: invoke register with an absolute watch-args JSON file; wait for the fixture's watch record while holding its ACK gate and confirm the client has not succeeded. Release accepted watch_result and emit an event immediately afterward. In the same daemon, submit one watch that the fixture explicitly rejects.")
		By("Expected: success only after the matching acknowledgement, exit 0 with exact success JSON plus LF, and one correctly targeted event attempt. Rejected watch exits 1 with the exact bounded daemon error JSON and never becomes active. The independent non-Go executable runs unchanged through the configured public protocol.")
		By("Budget: 5 seconds including cleanup, one daemon, one PowerShell fixture, one register client and at most one queue substitute. Readiness/watch timeout and blocked-write branches use mocked-time unit tests.")
	})

	It("routes an atomic manual trigger after an idle interval", Label("case:MVP-V03-ROUTE"), Pending, func() {
		By("Inputs: real daemon/client/manual-plugin binaries, a writable local directory with an absent trigger_file, registered UUID A = 11111111-1111-4111-8111-111111111111, unregistered UUID B = 33333333-3333-4333-8333-333333333333, and a Codex substitute recording argument arrays and exact acknowledgements separately from --version probes.")
		By("Action: observe plugin readiness and registration_available; register A through public register and repeat the identical registration. Observe a declared one-second no-event interval, then atomically rename a temporary UTF-8 file containing the fixed decoded text probe + LF + quoted \"data\" C:\\probe to trigger_file. Record receipt and attempt times, event count and target/claimed file state.")
		By("Expected: identical registration returns the same ID; zero work requests occur during idle, followed by exactly one queue --thread A --message <envelope> and accepted outcome. The envelope preserves decoded source/subscription/context, marks external data and grants no extra permission; trigger and claimed files are removed. No queue invocation targets B or requests creation of a conversation. Retain source-review evidence that the bundled plugin has no independent Codex work capability; this substitute does not prove real Desktop acknowledgement or isolation.")
		By("Budget: 4 seconds including the one-second idle interval and cleanup after suite-level builds; at most daemon, manual plugin, client and one queue substitute.")
	})

	It("recovers a malformed frame and isolates invalid or exited emitters", Label("case:MVP-V04-INVALID-EVENT"), Pending, func() {
		By("Inputs: real daemon, two instances A/B of the same small PowerShell plugin-v1 fixture, accepted subscriptions for each, and the recording Codex substitute.")
		By("Action: fixture A emits one bounded malformed newline-terminated frame then a valid event; also emit an unknown subscription ID and B's ID from A. Exit A, attempt another registration on it, then register and emit from B.")
		By("Expected: malformed/unknown/foreign inputs make zero attempts; the following valid A event is processed once. A becomes unavailable and new registration fails; B still accepts registration and delivers once. Strict encoding, size, pre-activation, partial-EOF, late-result and buffered post-exit permutations are unit obligations.")
		By("Budget: 5 seconds including cleanup; at most daemon, two PowerShell fixtures, a register client and one queue substitute.")
	})

	It("terminates an uncertain delivery without retrying", Label("case:MVP-V04-DELIVERY-OUTCOME"), Pending, func() {
		By("Inputs: real daemon and PowerShell plugin-v1 fixture, plus a controlled Codex executable in nonzero-exit and held-until-cancelled modes. The substitute records invocation identity and PID before either response.")
		By("Action: admit one event in each mode; for held mode observe the actual fixed 30-second deadline, retain its process handle and await termination. Check daemon outcomes, invocation count and cleanup.")
		By("Expected: both launched outcomes are unknown with known plugin/subscription identifiers, exactly one invocation per admitted event and no retry. The held process is no longer alive after cancellation. ROUTE covers accepted delivery; prelaunch failure, parsing/output variants, command-length bounds and interrupted-delivery decisions use unit tests.")
		By("Budget: 35 seconds including the single actual 30-second timeout, all setup and cleanup; at most daemon, one protocol fixture and one queue substitute, with a short-lived client.")
	})

	It("rejects stale watches after restarting in-memory state", Label("case:MVP-V05-RESTART"), Pending, func() {
		By("Inputs: real daemon with one accepted PowerShell fixture watch, its recorded old ID, and the recording Codex substitute.")
		By("Action: normally stop using the case-owned console driver and restart with the same configuration. Emit the old ID, then register anew and emit its fresh ID.")
		By("Expected: old ID makes zero attempts; new registration yields a distinct usable ID and one attempt. No old registration or queued event is replayed. The healthy fixture acknowledges shutdown and exits immediately; forced five-second cleanup is exercised only in SHUTDOWN.")
		By("Budget: 4 seconds including both runs and cleanup; each run has one daemon, one healthy fixture and at most one queue substitute; the console driver is also tracked.")
	})

	It("deduplicates committed registration despite response loss and concurrent repeats", Label("case:MVP-V10-DUPLICATE"), Pending, func() {
		By("Inputs: real daemon, one gated PowerShell plugin-v1 fixture recording watch frames, same plugin/session/watch_args, raw same-user control clients and the recording Codex substitute.")
		By("Action: send one registration and close its control connection before reading a response. Hold the recorded watch acknowledgement; send two equivalent concurrent requests and confirm no success while pending, then release ACK and collect their responses. Explicitly repeat through public register, then emit two identical valid event frames.")
		By("Expected: one total watch, one committed ID shared by both concurrent responses and the explicit repeat, and two distinct ordered delivery IDs/attempts for the two frames. Closing the first response path neither rolls back commit nor adds a watch. Invalid response handling, decimal/key canonicalization and capacity boundaries remain unit obligations.")
		By("Budget: 6 seconds including cleanup, one daemon, one PowerShell fixture, at most two concurrent control clients and one sequential queue substitute; requests use one pipe connection each.")
	})

	It("cleans the owned process tree after a normal Windows Ctrl+C", Label("case:MVP-V01-SHUTDOWN"), Pending, func() {
		By("Inputs: real daemon and a dedicated hidden-console driver fixture under this command's testdata, plus a protocol fixture and tracked descendant that ignore Ctrl+C and remain alive after receiving shutdown. Product binaries and fixture executables use existing dependencies and are built once.")
		By("Action: driver creates an isolated console, starts the daemon there, and records process handles and readiness. Its own handler consumes Ctrl+C without passing an inherited ignore attribute to the daemon. Call GenerateConsoleCtrlEvent(CTRL_C_EVENT, 0) in that console and observe the plugin's shutdown frame, fixed five-second grace and all process handles.")
		By("Expected: daemon closes admission, sends shutdown, exits 0 and terminates remaining owned plugin/descendant processes after the five-second grace. Driver exits and no tracked process remains alive. Forced kill, pipe closure or CTRL_BREAK do not substitute for this signal observation. Partial-startup, pending-watch and in-flight cancellation permutations use unit tests.")
		By("Budget: 15 seconds including startup, the one five-second grace and complete cleanup; at most console driver, daemon, protocol fixture and one descendant. No account, credential, network or installed shared harness is required.")
	})

	It("enforces the current-user pipe policy and rejects a second daemon", Label("case:MVP-V08-SAME-USER"), Pending, func() {
		By("Inputs: ready real daemon/client, a healthy PowerShell plugin-v1 fixture and known current user SID. Open the actual local pipe handle and obtain its security descriptor with GetSecurityInfo; retain server process SID and semantic ACE records.")
		By("Action: register successfully as the same user, inspect a present non-null protected DACL with only an allow ACE for that user SID, then start a second daemon under the same SID. After its rejection, register and deliver through the first daemon again.")
		By("Expected: actual server SID equals current user SID, DACL grants only the intended user, and same-user public control works. Second daemon exits 1 before starting its plugins; first remains usable. Client mismatched/unverifiable SID branches use unit tests. Review pinned go-winio remote-reject creation and the Windows contract separately; no different-user or remote connection test, account or network setup is required or claimed passed.")
		By("Budget: 8 seconds including cleanup, at most first daemon, its fixture, second daemon and one client or queue substitute. Actual descriptor inspection replaces dedicated account setup, not the local-only security contract.")
	})
})

//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Static Pending preserves design acceptance scope until implementation wires
// these cases to the public product commands and controlled external processes.
// These concrete plans do not establish product behavior or harness feasibility.
var _ = Describe("MVP daemon and registration public behavior", func() {
	It("starts the configured product and reports plugin readiness", Label("case:MVP-V01-STARTUP"), Pending, func() {
		By("Inputs: a strict UTF-8 config.json with one manual plugin, absolute executable paths and args: [], and a local Codex substitute returning codex-cli probe-local for --version.")
		By("Action: build and start agent-pulse-hub.exe daemon --config <absolute file>; collect stderr and owned plugin process IDs.")
		By("Expected: version diagnostic, plugin readiness and registration_available appear; no queue request occurs before an admitted event. Missing ready reaches the fixed 10-second deadline and marks only that plugin unavailable.")
	})

	It("rejects invalid startup inputs before starting children", Label("case:MVP-V01-INVALID-CONFIG"), Pending, func() {
		By("Inputs: config variants with duplicate keys, unknown fields, duplicate names, relative executable paths, malformed JSON, missing plugin executable or missing Codex executable.")
		By("Action: start the public daemon command separately with each invalid absolute config file; observe exit status and substitute process-start records.")
		By("Expected: exit 1 and bounded stderr diagnostic; no owned plugin starts. A failed or invalid Codex version probe also exits 1 before starting plugins.")
	})

	It("activates a subscription only after a matching accepted watch result", Label("case:MVP-V02-REGISTER"), Pending, func() {
		By("Inputs: running real daemon and a protocol-only plugin substitute; matching CODEX_THREAD_ID and CODEX_SESSION_ID = 11111111-1111-4111-8111-111111111111; watch args {\"topic\":\"probe\"}.")
		By("Action: invoke register --plugin fixture --session-id <UUID> --watch-args-file <absolute file>; hold then release the plugin's matching accepted watch_result; submit its first valid event immediately afterward.")
		By("Expected: no success before acknowledgement; afterward exit 0 with exactly {\"ok\":true,\"subscription_id\":\"<nonempty ID>\"} plus LF on stdout, no stderr; event reaches the registered thread exactly once.")
	})

	It("rejects identity and unavailable or rejected registration inputs", Label("case:MVP-V02-REGISTER-REJECTION"), Pending, func() {
		By("Inputs: missing, conflicting or mismatching identity; unknown plugin; unavailable plugin; valid fixture rejecting watch_args with a nonempty error; no daemon listener.")
		By("Action: invoke public register for each variant; record stdout, stderr, status and plugin watch/queue calls.")
		By("Expected: local identity/connect-before-send failures exit 1 with stderr and empty stdout; daemon rejections exit 1 with the exact bounded error JSON on stdout; no active registration or queue call. A blocked write or absent matching watch result never extends the 10-second watch deadline and stops only the uncertain plugin.")
	})

	It("preserves committed identity after response loss", Label("case:MVP-V02-UNCERTAIN-REPEAT"), Pending, func() {
		By("Inputs: a real daemon, protocol-only plugin substitute recording watch calls, and a raw same-user control client that closes after sending a valid registration before reading its result.")
		By("Action: allow watch acceptance, repeat the equivalent registration through public register, and submit one event; also send malformed, truncated and duplicate-field response variants through an explicitly controlled client-response boundary.")
		By("Expected: repeat returns the committed nonempty subscription ID with one total watch and one delivery attempt; lost/invalid response after possible commit is uncertain, never definite rejection, and does not authorize a second watch or retry.")
	})

	It("routes external context to the registered recipient", Label("case:MVP-V03-ROUTE"), Pending, func() {
		By("Inputs: plugin fixture A and B; registered UUID A = 11111111-1111-4111-8111-111111111111 and UUID B = 33333333-3333-4333-8333-333333333333; Codex process substitute recording full queue arguments.")
		By("Action: register both, then fixture A emits an accepted event with context 'probe\\nquoted \"data\"'.")
		By("Expected: exactly one queue --thread A --message <envelope>; message identifies A's source/subscription, labels untrusted external data, grants no extra permission, and preserves escaped context. No queue call targets B or creates a conversation. This substitute observation does not establish a real Desktop reply.")
	})

	It("rejects malformed and unauthorized events without delivery", Label("case:MVP-V04-INVALID-EVENT"), Pending, func() {
		By("Inputs: accepted subscriptions in two distinct plugin processes; malformed/oversized frames, empty/8193-byte context, unknown ID, another plugin's ID, event before watch_result and buffered post-exit events.")
		By("Action: drive each invalid input through the configured plugin's stdout and then a valid bounded frame where recovery is permitted.")
		By("Expected: zero queue calls for invalid events; size-bounded terminated malformed frames are discarded and the next valid frame is processed. Over-limit/partial-EOF output marks that plugin unavailable; no post-exit event is admitted and other plugins continue.")
	})

	It("classifies each delivery attempt without retries", Label("case:MVP-V04-DELIVERY-OUTCOME"), Pending, func() {
		By("Inputs: an admitted valid event and a Codex process substitute in modes: exact matching queue acknowledgement with exit 0; missing executable at delivery; nonzero exit; 30-second timeout; mismatched thread; ambiguous, lost or oversized stdout; verbose stderr.")
		By("Action: emit one event per mode and observe substitute launch count and bounded daemon outcome logs.")
		By("Expected: exact matching successful response is accepted; prelaunch failure is failed; launched nonzero/timeout/ambiguous/mismatched/oversized response is unknown. Every admitted event has at most one launch attempt, no retry; stderr verbosity alone does not invalidate valid acknowledgement.")
	})

	It("keeps unaffected plugin registrations and events working", Label("case:MVP-V04-PLUGIN-ISOLATION"), Pending, func() {
		By("Inputs: two ready protocol-only plugin processes with active registrations and recorded process IDs.")
		By("Action: exit one plugin while it has pending and active watches; attempt new registration on it and emit events from both processes.")
		By("Expected: failed plugin becomes unavailable, its subscriptions invalidate and no new/buffered event is admitted; another plugin still registers and delivers. Already admitted events retain their one-attempt outcome.")
	})

	It("loses in-memory watches and rejects stale subscriptions after restart", Label("case:MVP-V05-RESTART"), Pending, func() {
		By("Inputs: a daemon with a registered fixture and recorded old subscription ID; local Codex substitute.")
		By("Action: stop and restart the product with the same configuration, emit the old ID, then register a fresh watch and emit its event.")
		By("Expected: restarted registry is empty; old event makes zero queue calls; fresh registration is required and yields a fresh usable ID. Lost registrations/events are not replayed and delivery is never retried.")
	})

	It("makes no work request while idle", Label("case:MVP-V06-IDLE"), Pending, func() {
		By("Inputs: ready real daemon and bundled manual plugin, one accepted watch, Codex process substitute separating --version probes from queue calls, and monotonic timestamps. Retain the evaluated plugin source revision and evidence that its idle path has no independent Codex request capability.")
		By("Action: observe one declared second with no event, then emit one valid event and observe admission/attempt timestamps.")
		By("Expected: neither daemon nor bundled manual plugin issues Codex work during the idle interval; the controlled endpoint records zero queue requests, then one attempt after the event. Daemon adapter counts alone do not establish plugin coverage. Record receipt/attempt times without inventing a numeric latency target or claiming a real conversation response.")
	})

	It("accepts a language-neutral independent plugin", Label("case:MVP-V07-INDEPENDENT-PLUGIN"), Pending, func() {
		By("Inputs: a minimal PowerShell fixture using only plugin-v1 JSON-line frames and a config entry invoking it through absolute powershell.exe plus -NoLogo -NoProfile -NonInteractive -File <fixture>.")
		By("Action: run the real daemon, register through its public command, accept a watch, emit context and respond to shutdown.")
		By("Expected: readiness, accepted registration and one queue call through real product components; fixture imports no Go package and receives no Codex identity, credentials, GitHub schema or agent API. Required PowerShell absence is an environment failure, never runtime Skip.")
	})

	It("preserves wire encoding limits and conformance recovery", Label("case:MVP-V07-PROTOCOL-BOUNDARIES"), Pending, func() {
		By("Inputs: a protocol-only process fixture; valid LF/CRLF frames at 65536 total bytes; invalid UTF-8/unpaired surrogate/BOM/blank lines/duplicate nested keys/wrong direction/version/unknown fields; context of 8192 and 8193 decoded UTF-8 bytes; late/duplicate/unknown watch results.")
		By("Action: send the conformance variants over actual child stdio, including malformed terminated frame followed by valid frame and partial frame followed by EOF.")
		By("Expected: otherwise-valid exact limits and escaped newline survive unchanged; over-limit or empty context never truncates or delivers; invalid bounded delimited frames recover, over-limit/partial EOF makes only that plugin unavailable. Ready/watch deadlines remain 10 seconds; readiness and activation order cannot be bypassed.")
	})

	It("preserves the public command file and stdout contracts", Label("case:MVP-V09-CLI"), Pending, func() {
		By("Inputs: built product commands, existing absolute strict UTF-8 config/watch JSON files, matching identity, and an actual daemon with controlled protocol plugin.")
		By("Action: invoke successful/rejected register and representative invalid argument, missing/relative file, BOM/non-object/malformed file variants through the executable; invoke manual-plugin.exe with an extra argument.")
		By("Expected: public status/stdout/stderr contracts match the design; local failures exit 1 with empty stdout, daemon rejection preserves exact error JSON, success emits only exact success JSON. Invalid input is rejected before a request is sent; no shell expansion occurs.")
	})

	It("deduplicates equivalent registration and preserves repeated event identity", Label("case:MVP-V10-DUPLICATE"), Pending, func() {
		By("Inputs: accepted plugin recording watch frames; same plugin and session with object keys reordered, equivalent JSON number spellings and nested arrays retaining order; differing session/plugin/array order variants.")
		By("Action: submit equivalent registrations sequentially and concurrently through public control/CLI, then emit one event and two repeated identical valid event frames.")
		By("Expected: equivalent registrations share one watch and one subscription ID; distinct registrations remain distinct; one event gives one attempt, while two repeated admitted frames give two distinct ordered attempts.")
	})

	It("enforces registry queue and rendered command capacity", Label("case:MVP-V10-ADMISSION-BOUNDS"), Pending, func() {
		By("Inputs: controlled accepted plugin; registration count at 1024 and 1025, event count at 128 queued-or-in-flight and 129 while Codex substitute holds the worker, outgoing watch frame at 65536/65537 bytes, rendered Codex command line at 24000/24001 UTF-16 units.")
		By("Action: fill registration capacity through raw public control requests using one connection per request, avoiding 1024 CLI process launches; drive event capacity through real plugin stdout, hold/release local substitute deterministically, then submit one over-bound request/event.")
		By("Expected: exact allowed boundary is accepted when other rules permit; excess registration/watch is resource_exhausted without watch write; excess event/command is rejected or failed without invocation, never truncated/retried. Single worker preserves admission order across sessions.")
	})

	It("stops owned processes on normal interruption and partial startup", Label("case:MVP-V01-SHUTDOWN"), Pending, func() {
		By("Inputs: real daemon and protocol-only plugin spawning a tracked descendant; one variant remains silent before ready; another has pending watch and queued/in-flight event.")
		By("Action: deliver a genuine Windows Ctrl+C to the foreground daemon; separately interrupt during partial plugin startup; observe daemon, direct-child and descendant process handles.")
		By("Expected: normal completion exits 0, closes admission, emits shutdown and allows the fixed five-second grace then terminates remaining descendants; partial startup also leaves no owned process alive. Interrupted delivery is unknown and queued state is lost. Closing pipes alone is insufficient evidence.")
	})

	It("admits the same Windows user through the restricted control pipe", Label("case:MVP-V08-SAME-USER"), Pending, func() {
		By("Inputs: real daemon and same-user public register command; capture daemon pipe ACL and server process SID.")
		By("Action: register a valid watch through the named pipe and inspect explicit DACL and client server-SID validation.")
		By("Expected: same-user request succeeds, DACL grants access to the intended user SID, and a server whose process SID does not match is rejected before request bytes are sent.")
	})

	It("denies a different standard-user token", Label("case:MVP-V08-DIFFERENT-USER"), Pending, func() {
		By("Inputs: running real daemon under standard user A and a control-pipe client running with a distinct standard-user B token; deterministic credentials/token setup is a CI prerequisite.")
		By("Action: attempt to open and send a registration to A's known pipe from B; collect OS error and plugin/queue call records.")
		By("Expected: pipe access denied, no accepted request/watch/delivery, A's existing registration remains usable. Missing second-user setup fails required verification; never Skip or substitute an ACL string assertion for access denial.")
	})

	It("denies a remote control-pipe connection", Label("case:MVP-V08-REMOTE"), Pending, func() {
		By("Inputs: running real daemon and a remote named-pipe client that genuinely exercises Windows remote-client rejection, with required network/token setup explicitly provided by CI.")
		By("Action: attempt remote open/registration to the daemon's pipe and record OS rejection plus daemon/plugin observations.")
		By("Expected: remote connection denied before any watch or event reaches application; existing same-user local control still succeeds. Unavailable remote-client environment remains unverified, not passed or skipped.")
	})

	It("prevents a second daemon from taking over the active pipe", Label("case:MVP-V08-SECOND-DAEMON"), Pending, func() {
		By("Inputs: first ready real daemon and accepted watch under one Windows SID; second daemon started with the same SID and independent plugin process recorder.")
		By("Action: start the second daemon, then register again and deliver through the first.")
		By("Expected: second daemon exits 1 because it cannot reserve the pipe, before starting its plugins; first remains the server and continues registration/delivery; no takeover or stale endpoint succeeds.")
	})
})

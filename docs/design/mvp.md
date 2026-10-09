---
type: Design
title: Minimal external event delivery MVP
description: A Windows daemon routes language-neutral plugin events to existing Codex conversations without agent polling.
sources:
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Minimal external event delivery MVP
---

# Minimal external event delivery MVP

## Context and scope

This design implements [requirements/mvp](../requirements/mvp.md), FR-001 through
FR-007, NFR-001 through NFR-004, and AC-001 through AC-010. The source is
[requirements PR #4](https://github.com/nokamoto/agent-pulse-hub/pull/4), head
`a2b0ce2d114d17a6a751554bb040b73c5ea6f756`, merged as
`e48ed3238abbc56deb0da52a4edf797a69e56616`. The maintainer confirmed on
2026-10-09 that their merge constituted approval of this revision. This explicit
approval is the exception to the usual separate PR-review record; the source
document's pending-status text has not been changed in this design-only change.

The repository currently contains development tooling, not a running event hub.
The new system runs in the foreground on Windows as one local user. It consists
of a Go daemon and registration client, a Go manual-test plugin, and a Codex
skill. All subscriptions and undelivered events live only in memory. Stop and
restart loses them. No service integration, retries, plugin restart, persistence,
individual cancellation, GUI, or service installation is introduced.

## Architecture and responsibilities

```text
User / Codex registration skill
          | registration CLI; local user-restricted named pipe
          v
Daemon: configuration -> plugin supervisor -> subscription registry
                              | stdin/stdout JSON lines
                              v
                       configured plugin processes
                              | event(subscription_id, context)
                              v
Daemon: validation -> bounded delivery queue -> Codex delivery adapter
                                                  | codex queue
                                                  v
                                     existing registered conversation
```

The daemon owns recipient identity, subscription identifiers, plugin process
lifecycle, and delivery outcomes. A plugin owns argument validation and watching
its source. It never receives Codex identity or credentials. The adapter owns
Codex-specific command construction and response interpretation. A future agent
adapter can replace it without changing the plugin protocol.

The registration client has no registry of its own. It reports the daemon's
result to the skill and exits. The skill obtains the current conversation's
identity from the supported Codex execution environment and fails when that
identity is missing or ambiguous. It never chooses a recent conversation or
creates a conversation as a fallback.

## Interfaces and data

### Configuration and local control

The foreground command accepts a UTF-8 JSON configuration with a `plugins` array.
Each entry has a unique nonempty `name`, absolute `executable` path, and an
`args` array of strings. The configuration also has a top-level absolute
`codex_executable`. No shell
command strings or environment-variable expansion are interpreted. Duplicate
keys, unknown fields, duplicate names, relative executable paths, and malformed
values are errors. Validate the whole configuration before starting children.

The daemon and client use a versioned Windows named pipe whose name is derived
from the current user's SID (Windows security identifier). Creation must require
the first pipe instance, use an explicit DACL (access-control list) granting
access only to that SID, and reject remote clients. The client uses the local
pipe namespace, verifies that the server process has the same user SID, and
rejects an unverifiable server. A second daemon fails without sharing state.
No TCP listener or bearer token is needed. Administrators taking ownership and
malicious programs already running as the same user are outside the isolation
boundary. Plugin executables are explicitly trusted by the user.

One connection carries one newline-terminated JSON request and response, then
closes. Requests contain `version: 1`, `op: "register"`, `plugin`, `session_id`,
and `watch_args` (a JSON object). Successful responses contain `ok: true` and
`subscription_id`; errors contain `ok: false`, a stable `code`, and a bounded
human-readable `message`. Invalid JSON/version/fields, missing identity, unknown
or unavailable plugin, rejected arguments, and resource exhaustion have distinct
error codes. A connection failure is a client error, never registration success.

Session IDs must be UUIDs; session-name lookup is not supported.
The CLI never discovers a session from plugin data. If the response connection
is lost after registration commits, the client reports an uncertain result;
repeating the identical registration returns the existing identifier.

### Plugin protocol version 1

Each child receives one private stdin/stdout pair. Both directions use UTF-8
newline-delimited JSON objects; stdout is protocol only and stderr is diagnostics.
No Go types or agent-specific values are required. Every frame has `version: 1`
and `type`. The following fields are required in addition to those two fields.

| Direction / type | Fields | Meaning |
| --- | --- | --- |
| Plugin -> daemon, `ready` | None | Plugin can accept watches; exactly once before registration. |
| Daemon -> plugin, `watch` | `request_id`, `subscription_id`, `watch_args` | Proposed watch; identifiers are opaque strings. |
| Plugin -> daemon, `watch_result` | `request_id`, `accepted` | Boolean acceptance; rejection also includes string `error`. |
| Plugin -> daemon, `event` | `subscription_id`, `context` | Context is a nonempty UTF-8 string, never a routing instruction. |
| Daemon -> plugin, `shutdown` | None | Stop watching, release resources, and exit. |

For example, a plugin event is
`{"version":1,"type":"event","subscription_id":"opaque-id","context":"manual check 1"}`.
The emitting process establishes plugin identity; a frame cannot supply or
override plugin name or recipient. Unknown fields, duplicate object keys,
invalid versions/types, and missing or mistyped fields are rejected. A malformed
event is logged and discarded; its next valid framed event may still be read.
An oversized or unterminated frame that prevents safe framing makes the plugin
unavailable. A duplicate/unsolicited `watch_result` or event before acceptance
is rejected without changing the active registry.

Watches are multiplexed over the process. The plugin must send an accepted
`watch_result` before its first event for that watch. The daemon processes a
plugin's frames in read order, activating the registration before processing
the next event. It never waits for the client to consume the success response
before accepting the event. Request identifiers are unique for the process
lifetime and must match a pending request.

The registry holds `(plugin instance, subscription_id, session_id, watch_args,
state)` plus an index for equivalent registrations. IDs are randomly generated
and never intentionally reused across daemon runs. Active registrations compare
plugin name, session ID, and recursively canonicalized JSON arguments: object
key order is ignored, array order is preserved, and numbers are compared by
their exact decimal value rather than floating-point conversion. The same tuple
returns its active ID without sending another watch. Concurrent identical requests
share one pending operation and receive the same result. Plugin rejection removes
the pending operation and leaves no active subscription.

A watch timeout or failed write could leave a plugin watching without confirmed
acceptance. Mark that plugin unavailable, invalidate its registrations, and stop
its process instead of silently permitting orphan watches. Other plugins remain
available. This trades that plugin's availability for a definite registration
state without adding cancellation or retries.

### Bounds and event acceptance

Protocol limits are implementation choices for bounded resource use, not
performance targets: 64 KiB per wire frame, 8 KiB UTF-8 event context, 1,024 active
or pending registrations, and 128 queued or in-flight delivery events globally.
The Codex adapter also rejects a rendered command line over 24,000 UTF-16 code
units before launching it. Error messages and captured child output are bounded.
Oversized inputs are rejected with identifiers when recoverable; no truncation
is forwarded as if it were the original event.

Startup readiness and watch acknowledgement each have a 10-second deadline;
delivery commands have a 30-second deadline. Normal shutdown gives plugin
processes up to 5 seconds to exit before terminating them. These are initial
operational defaults to document and test, not latency guarantees.

An event becomes accepted only when its frame, plugin availability, subscription
ownership, context, and queue capacity have passed validation and it is admitted
to the delivery queue. Capacity exhaustion rejects and logs the event without
an attempt. Each admitted event gets a daemon-local delivery ID and one attempt;
identical event frames are separate events and are not deduplicated. A single
delivery worker preserves admission order and avoids simultaneous Codex commands.
It can delay other sessions during a slow command, bounded by the command
deadline. No durability is implied by admission.

### Codex delivery and event envelope

The adapter runs the configured Codex executable directly as the same Windows
user, using an argument array equivalent to
`codex queue --thread <session_id> --message <envelope>`. It must not invoke a
shell, start a new conversation, interrupt a turn, or fall back to another API.
The initial supported CLI is `codex-cli 0.162.0-alpha.2`, the version recorded
in PR #4 and confirmed by local `--version` and `queue --help` on 2026-10-09.
PR #4 records successful idle and busy conversation demonstrations. A different
version requires adapter verification before support is claimed; startup rejects
an unsupported version without invoking queue. Queue acceptance means Codex accepted work, not that
the conversation completed or acknowledged it.

The message has a fixed instruction wrapper identifying the plugin and
subscription, followed by a JSON-encoded `context` value. The wrapper says that
the content is external data, may be untrusted, and must only be used under the
user's existing instructions and permissions. JSON encoding preserves content
without allowing it to escape the data field; it is not a claim to prevent all
prompt injection. The skill repeats this boundary and does not authorize
actions merely because an event requests them.

Delivery outcomes are `accepted`, `failed`, or `unknown`. Only a response that
positively confirms queue acceptance for the intended thread, with successful
process termination, is `accepted`: exit status zero and exactly the recognized
stdout line `Queued message <message UUID> for thread <target UUID>.`, allowing
a final line ending, with valid UUIDs and target equal to the registered session.
The [OpenAI source snapshot](https://github.com/openai/codex/blob/0ada5d8806cdad498230d5b1b2924091e04c8feb/codex-rs/tui/src/session_queue_commands.rs)
returns this line after a typed queue acknowledgement. This snapshot is supporting
evidence, not proof of binary identity; implementation must capture fixtures and
repeat the real demonstration on the supported executable. Capture stdout and
stderr separately. Failure to start the process is `failed`; the initial adapter
does not assume that a general nonzero exit proves non-acceptance. Timeout, lost output, inconsistent
response, or any post-start error without a definite rejection is `unknown`.
An unrecognized CLI response is never inferred to be success from exit code
alone. None of these outcomes causes an automatic retry. Once an event has been
accepted by Codex, daemon shutdown cannot retract it.

### Manual test plugin and skill

The bundled plugin accepts `watch_args` with exactly one field, `trigger_file`,
an absolute path to a currently absent file in a writable local directory. It
rejects a second watch of the same normalized path in that plugin, so two
subscriptions cannot race to consume one file. The developer writes context to
a temporary UTF-8 file in the same directory and atomically renames it to the
registered path without overwriting an existing file. The plugin checks for that
file periodically, claims it by renaming to a unique path in the same directory,
reads at most the context limit, removes the claimed file, and emits context through the public `event`
frame. It does not poll Codex. Failed reads, oversized/invalid content, or failed
removal are reported on stderr with the claimed file path and are not emitted.
The plugin does not retry a claimed file. The developer removes that file after
inspecting the error and creates a new trigger to try again. A crash between consumption and emission can lose the
event, consistent with the MVP's lack of guaranteed delivery.

The skill asks for the plugin and its watch arguments, requires both
`CODEX_THREAD_ID` and `CODEX_SESSION_ID` to be valid UUIDs with the same value,
and invokes registration with that value from the top-level conversation.
PR #4 demonstrated this environment for the supported CLI; subagents can have
different values and must not be used for this registration step. A missing,
conflicting, or unsupported identity fails with an explanation; an agent must
not delegate registration to a subagent with a different conversation identity.
The skill reports the returned subscription ID and tells the user how stopping
the daemon ends the watch. For the demonstration, the user authorizes the current
conversation to acknowledge the next matching context before triggering it.
This advance instruction enables continuation without an additional user message.

## Normal and failure scenarios

1. Startup validates configuration and reserves the user-specific control pipe,
   then starts configured children. Registration becomes available once startup
   results are known. A plugin that misses readiness or exits is unavailable;
   successful plugins remain usable, and all startup failures are visible.
2. Registration validates control input, coalesces duplicate requests, asks the
   selected plugin to watch, and activates only on positive acknowledgement.
   Rejection produces no active subscription. No Codex request is made here.
3. An accepted event captures its fixed recipient from the registry. The worker
   attempts delivery once and records an outcome. Busy-thread behavior uses
   Codex's queue; rejection is reported without retry or interruption.
4. Plugin exit invalidates all of that instance's registrations and logs their
   IDs. New and buffered unread frames from that plugin are rejected. Events
   already admitted before the exit retain their captured destination and their
   one delivery attempt; this avoids silently reclassifying accepted events.
   Other plugins are unaffected. A failed plugin is not automatically restarted.
5. Ctrl+C closes registration and event admission, signals `shutdown`, then
   terminates remaining children after the grace period. Queued events are
   discarded with a shutdown reason. An interrupted in-flight delivery is
   unknown. Restart begins with an empty registry and rejects old IDs.

Daemon-owned processes must be tracked from creation, including partial startup.
Use Windows Job Objects with kill-on-close and assign newly created suspended
children before resuming them, so their descendants cannot escape normal-shutdown
cleanup through a start/assignment race. Containment setup failure fails that
child's startup and terminates it. Pipe closure alone is not evidence that a process exited. Abnormal
termination may lose all pending state and cannot provide delivery guarantees.

## Decisions and alternatives

| Decision | Alternative | Rationale and tradeoff |
| --- | --- | --- |
| SID-restricted local named pipe | Loopback HTTP with secret | Windows supplies the user boundary without token storage; requires Windows-specific transport and access tests. |
| JSON lines over child stdio | Per-plugin HTTP servers or Go RPC | Language-neutral with process-bound source identity and no plugin network endpoint; strict framing and serialized writes are required. |
| In-memory registry and one delivery worker | Database and parallel workers | Meets explicit MVP scope and makes ordering visible; restart loses state and slow delivery delays other sessions. |
| Codex CLI queue adapter | UI automation or undocumented direct database writes | Queue is the tested existing-conversation entry point; isolates version-sensitive behavior and avoids mutating Codex storage. |
| File-triggered manual plugin | Daemon-specific test injection operation | Exercises the same plugin protocol as future sources; requires documented atomic file creation and permits event loss on crash. |
| Stop a plugin after uncertain watch acceptance | Leave an unconfirmed watch running | Keeps active-registration semantics definite without adding cancellation; all watches on that plugin are lost. |

## Quality and operations

The daemon logs UTC timestamps, lifecycle changes, request and subscription IDs,
plugin name, event receipt, admission/rejection, delivery attempt, and outcome.
Idle operation emits no Codex work request. Logs contain bounded reason codes and
sanitized diagnostics, not raw event context or command lines by default. Context
is still disclosed to the target Codex conversation, and subprocess command-line
arguments may be observable to programs with sufficient local access. Trigger
files and any diagnostic logs saved by the developer have user-managed retention.
The daemon does not retain event payloads after completion or shutdown.

The configuration and plugin paths are trusted local inputs. Output bytes must
not be interpolated into shell commands or interpreted as terminal control
instructions. Per-plugin readers drain stderr and bound diagnostic capture so a
noisy child cannot block another plugin. A frame that is malformed but correctly
delimited is rejected without crashing the daemon. Capacity limits and deadlines
prevent unbounded pending operations; they do not promise throughput or uptime.

Protocol v1 is strict: incompatible versions fail visibly. Future additions must
update the contract deliberately rather than allowing plugins to guess fields.
There is no persisted schema to migrate. Implementation documentation must include
the exact wire examples, error codes, limits, Windows commands, expected output,
Codex version, and recovery by re-registering after restart.

## Requirement coverage

All identifiers below belong to `requirements/mvp`; no requirement is deferred
to another design. Verification entries V01-V10 are defined in the next section.

| Requirement | Acceptance | Design responsibility | Verification |
| --- | --- | --- | --- |
| FR-001 | AC-001 | Configuration, supervisor, readiness and shutdown | V01 |
| FR-002 | AC-002 | Local control, pending/active registry and watch acceptance | V02 |
| FR-003 | AC-003, AC-004 | Process identity, fixed recipient and event envelope | V03, V04 |
| FR-004 | AC-001, AC-003 | Manual plugin over public protocol | V01, V03 |
| FR-005 | AC-002, AC-003 | Current-session skill and advance user instruction | V02, V03 |
| FR-006 | AC-004, AC-005 | Once-only worker, outcome classification and invalidation | V04, V05 |
| FR-007 | AC-010 | Canonical registration identity and Codex queue | V10 |
| NFR-001 | AC-006 | No Codex request until event admission | V06 |
| NFR-002 | AC-007 | Language-neutral child protocol and delivery adapter | V07 |
| NFR-003 | AC-008 | Local pipe security and external-data boundary | V08 |
| NFR-004 | AC-005, AC-009 | Documented Windows lifecycle and state loss | V05, V09 |

## Verification strategy

These are implementation acceptance plans, not executed acceptance results.
Automated fixtures can replace plugins and the Codex executable to test process
and failure behavior; they do not replace the real Codex demonstration.

| ID / source acceptance | Method and decisive expected result | Evidence and evaluator |
| --- | --- | --- |
| V01 / AC-001 | Windows process integration: valid configuration reaches ready; malformed config and missing executable show errors; Ctrl+C and partial startup leave no owned child or descendant alive. | Automated process IDs, exit checks and logs; maintainer reviews failures. |
| V02 / AC-002 | Client/protocol tests plus real skill registration: success only after plugin acceptance. Missing/conflicting identity, unknown/unavailable plugin, rejected args and inability to connect before sending the request create no active entry. Response loss after commit is uncertain and an identical repeat returns the existing ID. Timeout stops the uncertain plugin. | Automated registry and process assertions; maintainer records skill command and ID. |
| V03 / AC-003 | Real Desktop conversations A and B: register A via skill, authorize an acknowledgement, atomically create the trigger file, and observe A's reply with source and recognizable context. B receives nothing and no new conversation appears. | Maintainer records commands, versions, daemon logs, A/B identifiers and visible conversation results. |
| V04 / AC-004 | Fixtures submit malformed/oversized frames, unknown IDs, cross-plugin IDs and post-exit frames: zero delivery calls. Simulate launch failure, nonzero exit, timeout, mismatched target and ambiguous output: correct failed/unknown result, one attempt, no retry. Exit one plugin; another continues to register and deliver. | Automated invocation counts, identifiers and lifecycle logs; maintainer inspects results. |
| V05 / AC-005 | Restart integration: old ID is rejected, registry is empty, and a fresh watch is needed. Inspect instructions for registration/event loss and no retries. | Automated restart trace and documentation review by maintainer. |
| V06 / AC-006 | Declare an idle observation period, instrument every adapter call, and observe zero calls. Trigger an event; record receipt and attempt timestamps and the real conversation response time. No periodic agent check participates. | Automated idle assertion plus maintainer's timestamped real demonstration; no numeric latency threshold. |
| V07 / AC-007 | Review the public JSON contract and replace the configured plugin executable with an independent fixture using that contract. No daemon code, Go import, Codex identity, GitHub schema or agent API is needed by the plugin. | Contract review and replacement transcript; maintainer evaluates portability. |
| V08 / AC-008 | Windows tests show same-user control works; a different standard-user token and remote pipe connection are denied; second daemon cannot take over the pipe. Check explicit DACL and server SID validation. Inspect envelope and skill for external-data labeling and absence of additional authorization. | Automated transport tests where available plus maintainer-run account/network checks and message inspection. Unavailable checks remain incomplete. |
| V09 / AC-009 | From a clean Windows build, follow documented build/start/skill-register/file-trigger/acknowledge/stop steps. Record exact Codex version and all failures; inspect documented limits and reset behavior. | Maintainer's reproducible command transcript and lifecycle logs. |
| V10 / AC-010 | Repeat and concurrently submit equivalent registrations: one watch and same ID; one event yields one attempt. In a real busy session, queue a recognizable event and observe current work uninterrupted followed by the event, or explicit rejection with no retry. | Automated canonicalization/concurrency tests and maintainer's busy-conversation trace. |

Additional boundary tests cover escaped/newline context, exact size limits,
queue overflow, response loss after registration, immediate event after watch
acceptance, and shutdown during a delivery. Assertions distinguish plugin event
rejection from an admitted event's delivery result.

## Change and rollout impact

Implementation adds the daemon/client commands, plugin supervisor and protocol,
in-memory registry, Windows control transport, Codex adapter, manual plugin,
registration skill, Windows usage documentation, and integration fixtures. No
existing application code or design requires migration. Development tooling is
retained. Only `docs/design/` changes in this design phase.

The maintainer builds and runs the foreground binaries manually. Rollback means
stopping them and returning to the prior build; registration must be repeated.
Stopping cannot cancel work already accepted by Codex. Delivery failures must be
inspected before manually triggering a new event because an unknown outcome may
already have reached Codex. Implementation begins after design approval and
merge under the [AIDD playbook](../aidd/README.md).

## Open questions and risks

| Question or risk | Impact | Owner | Resolve before | Status |
| --- | --- | --- | --- | --- |
| Codex queue response and current-session identity contract | Incorrect interpretation could misreport acceptance or select the wrong conversation. | Implementer | Implementation acceptance | Design resolved: support the observed CLI version, matching environment UUIDs and strict acknowledgement grammar; capture executable fixtures and repeat the real demonstration. |
| CLI changes after the tested version | Delivery may become unavailable or unknown. | Maintainer | Each supported-version release | Mitigated by strict adapter recognition and real idle/busy demonstrations; no fallback or retry. |
| Local control security tests require another Windows user and remote client | Missing environment could leave AC-008 unverified. | Maintainer | Implementation acceptance | Planned; report missing evidence rather than assuming isolation. |
| Memory-only state and once-only delivery | Crash or shutdown can lose accepted events; unknown delivery can already have reached Codex. | Maintainer | Design approval | Accepted MVP tradeoff for human review. |

No unresolved product decision blocks implementation after design approval.
The rows above identify required implementation verification and human risk
review; they do not add persistence or retry behavior outside the approved scope.

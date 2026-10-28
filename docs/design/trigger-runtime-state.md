---
type: Design
title: Trigger runtime state persistence
description: Restore optional trigger-owned progress through core-managed coordinated checkpoints without changing nonparticipating v2 triggers.
sources:
  - id: runtime-requirements
    resource: requirements/trigger-runtime-state.md
  - id: registration-design
    resource: design/state-persistence.md
  - id: routing-design
    resource: design/mvp.md
  - id: runtime-stream-design
    resource: design/runtime-state-stream.md
---

# Trigger runtime state persistence

## Context and scope

This design fulfills [trigger runtime state requirements](../requirements/trigger-runtime-state.md).
The core stores opaque, trigger-produced checkpoints alongside immutable saved
registrations. A participating trigger applies its checkpoint before accepting
the restored watch. At normal stop the core reports confirmed delivery outcomes,
requests a final checkpoint, and saves it before stopping plugins. Successful
persistence prevents a completed update from being notified again solely because
of restart. Recipient processing, unfinished notifications, crash recovery,
automatic retries, and collection of updates while stopped remain outside this
guarantee. Actions have no new interface or state obligations.

Participation is an optional capability in application/framework version 2.
`runtime_state_v2` adds explicit activation once route admission is ready. The
host also supports `runtime_state_v1` unchanged: accepted watches may start
before hub-wide readiness and their early events can be discarded. A v1 input
cannot assume preservation of fresh first-observation notifications across
that gap. Inputs needing this coordination select v2, including the
[GitHub review request trigger](github-review-request.md).
An unchanged v2 input continues using its existing `ready`, `watch`, and `event`
messages and receives no progress guarantee. Supporting an unchanged old input
on the new host does not imply that a new participating input works on an old
host: an old strict decoder can reject the capability field and new messages.

## Requirement coverage

All unqualified IDs below belong to `requirements/trigger-runtime-state`.

| Requirements | Acceptance | Design | Verification |
| --- | --- | --- | --- |
| FR-001, FR-002, NFR-001, NFR-002 | AC-001 | Optional public capability; opaque state; core filesystem ownership | TRS-V01-RESTART-ISOLATION; protocol/value unit tests; source inspection |
| FR-003, FR-004 | AC-001, AC-006 | Outcome fence and final coordinated checkpoint | TRS-V01-RESTART-ISOLATION; lifecycle unit tests; existing MVP-V05-RESTART |
| FR-005, NFR-003 | AC-002 | Registration/trigger ownership; offline deletion; fresh UUID | TRS-V01-RESTART-ISOLATION; association unit tests |
| FR-006, NFR-003 | AC-003 | Fail-closed restoration; explicit offline reset | TRS-V02-RESTORE-RECOVERY; read/validation/application unit tests; TRS-D01 |
| FR-007 | AC-004 | Visible unsuccessful checkpoint/save; continued monitoring | TRS-V03-SAVE-FAILURE; filesystem/protocol unit tests |
| FR-008, NFR-001 | AC-005 | Legacy v2 message path and old registration compatibility | Existing SP-V01-RESTORE; unchanged bundled-model cases; protocol inspection |
| FR-009 | AC-006 | Trigger-owned stopped-period policy; no hub replay | Lifecycle unit tests; fixture policy below; existing MVP-V05-RESTART |
| FR-010, NFR-001–NFR-003 | AC-007 | Author contract and offline operator procedure | TRS-D01; contract/source and readability review |

The existing [MVP](mvp.md), [registration persistence](state-persistence.md), and
[bundled-plugin](bundled-plugins.md) checks retain their expected behavior.
These cases do not establish a future service trigger's correctness.

## Architecture and responsibilities

The hub owns registration identity, recipient capture, delivery acceptance,
checkpoint scheduling, storage, and startup/shutdown ordering. The input owns
its state schema, compatibility checks, applying returned state, extracting a
restorable snapshot, and deciding which completed updates that snapshot prevents
from being emitted again. It performs no state-file access or persistence-service
calls. The action retains sole authority to report delivery acceptance.

Inputs may additionally select the [repeated-wait profile](pr-watch-rewait.md)
alongside runtime-state v2. Its notification generations and operation receipts
are daemon-owned Session control, distinct from plugin-owned logical progress.
It retains this storage format and checkpoint contract; the profile governs
preparation/grant ordering against checkpoints and automatic rearming after
restart. Inputs without that capability retain the behavior below unchanged.

For a participating watch the core correlates each admitted event with its
event ID and sends its terminal outcome back to the input. The input consumes
these notices in host-stream order before producing a requested checkpoint.
Only `accepted` proves a completed notification. The state must retain the
progress necessary to suppress every such update. A scan cursor that moves past
unfinished updates is not evidence of their successful notification.

The core neither understands service cursors nor inspects payload fields to
choose destinations. Existing captured-recipient routing and one-attempt
delivery remain unchanged. State is distinct from watch/destination arguments.

## Interfaces and data

### Optional protocol and author contract

[Plugin v2](plugin-v1.md#optional-trigger-runtime-state) owns the schemas,
directions, correlations, event counters and deadlines. Participating inputs
advertise exactly one of `runtime_state_v1` or `runtime_state_v2`, receive
`state_watch` with null or saved state,
emit `state_event`, consume terminal `delivery_notice`, and answer `checkpoint`.
They validate/apply state before positive `watch_result`; incompatible state
is a negative result, never initial-state fallback. A v2 watch stays dormant
until the host's one-way `activate_watch` grant. V1 retains its original start
after positive acceptance. Runtime-state revision and application version 2
are separate version axes; the wire specification owns their exact schemas.
The transport event counter may restart in a new Session; it is not logical
progress. The core stores only its last value and bounded outstanding work.

The state envelope contains exactly `schema` and `payload`. `schema` is a
nonempty string of at most 128 decoded UTF-8 bytes; `payload` is a JSON object.
For inputs without `runtime_state_stream_v1`, the complete compact-encoded
envelope is at most 32,768 bytes. Existing strict
[JSON, Unicode, and exact-number rules](plugin-v1.md#application-service-and-encoding)
apply. The core validates shape and
size; only the trigger validates the service meaning and schema compatibility.
The plugin must bound its state to this limit and document any service-specific
history limit. Exceeding it is a checkpoint failure, never silent truncation.
Use the same compact encoder, with HTML escaping disabled and exact decimal
values preserved, for size measurement, stored state and outbound messages.
Before accepting a new participating registration, reserve the full 32,768-byte
state allowance in its complete encoded `state_watch`, including IDs and watch
arguments, within the 65,536-byte application bound. Reject an oversized route
before dispatching its watch; nonparticipating limits stay unchanged. During
restoration verify both that reservation and the actual saved-state message
before dispatch. A previously saved oversized route switched to participation
fails visibly rather than accepting an unrestorable checkpoint. Validate every
outbound message again at send time; never truncate state or arguments.

Authors document initial state, state schema/version compatibility, which
confirmed updates the snapshot suppresses, and whether stopped-period updates
are retrieved or skipped. They implement restoration, ordered delivery-notice
handling, and snapshot production; no storage location or filesystem contract
belongs in the plugin. No additional RPC, service SDK, or Go dependency is
required by this language-neutral JSON/gRPC extension.

Inputs combining `runtime_state_v2` and `runtime_state_stream_v1` instead use
[streamed opaque state](runtime-state-stream.md). That optional specification
owns the ordered-object representation, conditional restore/checkpoint wire
schemas, byte budget and complete multi-chunk generation format. Each object
remains at most 32,768 bytes and every message remains at most 65,536 bytes;
there is no single-envelope total-state reservation for this profile. Its
ten-second watch/batch/final-stop windows and delivery-notice fence are unchanged.
Legacy runtime-state v1/v2 inputs retain all envelope behavior above.

### Storage and compatibility

Retain the [registration v1 format](state-persistence.md#record-format-and-compatibility)
without adding mutable fields. Under the same locked state root add `runtime/`.
Create it after acquiring exclusion for a new or valid older registration-only
tree; its absence in an older tree means no saved runtime checkpoints. An
existing reserved entry must be a real directory, never a symlink. The core
continues creating owner-only requested directories/files using Go APIs.

Each `runtime/<subscription_id>/` contains committed generations named as a
20-digit, zero-padded unsigned decimal greater than zero, followed by `.json`.
For example, `00000000000000000001.json` contains:

```json
{"version":1,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71","input_plugin":"cursor","generation":"1","state":{"schema":"cursor-v1","payload":{"confirmed":["U1"]}}}
```

The record has exactly the shown fields. The version is the integer token `1`;
generation is the canonical decimal string matching the filename; identity and
configured input name must match the owning registration. Read at most 49,152
bytes per file. Reuse strict parsing, exact decimal values, and payload secrecy.
An existing per-registration directory must contain at least one committed
generation; an empty directory is an invalid incomplete save, not implicit reset.
Unknown entries, invalid filenames, symlinks, nonregular files, read errors,
unsupported versions, or owner mismatch fail restoration. For multiple valid
generation filenames load the highest only and validate it completely; corrupt
newest state never falls back to older progress.

The optional [stream format and migration](runtime-state-stream.md#core-managed-generation-storage)
add `<generation>.stream/` directories only for stream-capable inputs. Select
the highest generation across both forms; equal-number forms, an invalid newest
generation or unsupported stream capability fail closed. The stream specification
governs staging and atomic directory commit for that form. Legacy files retain
their exact format and limits. A stream-capable host hands a legacy envelope
to a compatible plugin as one opaque payload object with the original schema;
only a later successful checkpoint creates a higher stream generation.

To save, select one more than the highest generation, exclusively create/write/
close a temporary file in existing `staging/`, then rename to the previously
absent final generation path on the same filesystem. A successful rename is
the commit point. Do not replace a file, require fsync, or claim crash durability.
After committing, remove older generations; retain the newest even if cleanup
fails. Report cleanup errors with affected registration/path/reason. Do not
retain history intentionally. Keep staging creation and generation operations
serialized per registration. Generation exhaustion is a visible save failure.

A first failed save must remove its newly created empty per-registration
directory when possible; report cleanup failure if it remains. Older committed
state is never overwritten on failure. No generation is created for inactive
uncommitted registration tombstones. Runtime directories for IDs absent from
the committed registration set are never loaded or assigned to another route;
offline deletion removes them. Validate their entry names/types without
interpreting their payloads as live registrations.

A missing per-registration directory means initial state, including an explicit
offline reset. An existing saved checkpoint requires a participating input;
changing configuration to a nonparticipating input fails restoration instead
of ignoring progress. Saved runtime state does not change a registration's
destination or canonical duplicate key.

## Normal and failure scenarios

### Registration and startup

Keep destination validation before watch acceptance and commit/activation
serialization. New legacy-profile participating watches receive `state: null`;
stream-profile watches receive null schema and zero chunks followed by end.
A repeated
equivalent active registration returns its existing ID without another watch,
reset, or checkpoint. Registration storage failure retains the existing inert
tombstone behavior and does not schedule runtime checkpoints for that route.
A v2 accepted watch remains dormant on commit failure; send no grant. After a
successful commit install routing/runtime correlations and make the route
admissible under lifecycle serialization, then dispatch its grant. Client
success means a committed active route, not grant receipt or completed polling.
A post-commit grant failure retains the record and reports plugin unavailability,
not storage rollback. Equivalent active registration sends no second grant.

During startup validate registration records and runtime file envelopes before
plugin launch. Negotiate capabilities, then restore each destination and watch
with its saved ID and optional state. A trigger checks/applies state before
acceptance; v2 retains a dormant watch. Keep every route staged until all
restorations succeed. Under lifecycle serialization recheck all referenced
instances and stop state, install every active route and runtime correlation,
then open the shared registration/event gate. This is startup commit. Emit
`daemon_ready` with restored count at commit before dispatching one
`activate_watch` per accepted v2 watch. Readiness asserts admission, not grant
receipt or source readiness. V1 retains legacy early emission; no v2 grant can
precede the complete-set restoration barrier.
Any runtime read, validation, capability, or application failure identifies the
input, registration, path when applicable, and bounded reason; abort the whole
startup and perform existing cleanup. No affected watch starts with fallback
state and no earlier staged route delivers. A failure or stop before commit
sends no grant, retains committed records and performs failed-start cleanup
without `daemon_ready`.

Bound activation dispatch by the existing 1,024-watch capacity: at most one
pending grant per watch and no retry queue. Serialize grants with other host
messages per input; dispatch different inputs concurrently. One ten-second
per-input batch deadline covers queuing, flow control and all pending grant
sends. Remove unsent grants if stop wins ordering or their input fails/expires.
A failed/expired send makes that input unavailable under normal cleanup; this
is a running-daemon failure after commit, not failed restoration. Retain saved
records and healthy routes. An earlier granted watch may already deliver when
a later send fails; activation is not an atomic all-watch delivery transaction.
Do not retract readiness or roll back grants. Stop first prevents further
dispatch; already dispatched grants can race with admission closure, using
ordinary discard/notices and final checkpoint fencing. Quiescence is permanent
until Session closure, even if a grant arrives late. No ACK, source-ready
promise, replay, grant retry or post-commit loss recovery is introduced.

A v1 event emitted after positive watch acceptance but before activation/readiness
receives `discarded`. A v2 input must not emit before its grant; a valid fresh
event from a dormant runtime-state v2 watch is discarded and diagnosed with a
terminal notice; it is not buffered or replayed. Events from tombstones or
closed admission are also discarded, with a notice when their participating
watch and fresh ID are valid. An input must resolve this outcome without
treating it as successful notification or waiting forever for acceptance.
Foreign, repeated, malformed or pre-watch event IDs do not obtain a second
notice or change progress. Start periodic checkpointing only after readiness.

### Periodic checkpoints and save failures

Request the first periodic batch five seconds after readiness, and subsequent
batches five seconds after the previous periodic batch finishes;
only one batch per input and one checkpoint per watch may be outstanding.
For each batch, send all prior terminal notices before that watch's checkpoint
request on the same serialized host stream. The snapshot includes those notices
even if the input's event-producing task runs concurrently. A nonquiescing
snapshot permits monitoring and later events to continue.

Pipeline the bounded set of at most 1,024 watch requests, and run input-instance
batches concurrently. A single ten-second protocol deadline covers notice
transmission, all checkpoint requests, and results for the batch, rather than
ten seconds serially per watch. Bound outbound pending entries by registry and
existing event capacity. Save returned envelopes with at most four concurrent
filesystem workers and per-registration serialization. Filesystem operations
have the existing local-storage latency boundary; no portable hard filesystem
deadline is promised. Do not accumulate another unbounded persistence queue.

Negative, malformed, oversized, missing, or timed-out checkpoint results and
write/close/rename errors are unsuccessful persistence. Diagnose the trigger,
registration, operation/path when applicable, reason, and exact warning
`runtime continuity at next restart cannot be guaranteed`. Do not log payloads
or report unsuccessful progress as saved. Continue monitoring and notifications
for healthy watches. A checkpoint timeout alone does not kill the input;
this is an explicit exception to the ordinary destination/watch timeout rule.
Expire its request correlation and ignore late results. The next scheduled
batch may try a new snapshot, without retrying any event/delivery. A successful
later checkpoint restores the persisted boundary and receives a success record.
Actual stream/send failure still invokes existing plugin-unavailability rules.
At batch expiry remove checkpoint messages not yet sent from the bounded
outbound queue, expire every batch correlation, and report affected watches.
Already transmitted requests cannot be recalled; ignore their late results.
Never reuse request IDs or let an expired queued request enter a later batch.

### Normal stop and confirmed-delivery fence

Stop/Ctrl+C use the same coordinator. Under lifecycle serialization close new
registration/event admission and establish the delivery-result cutoff. A valid
`accepted` result processed before this cutoff is confirmed and its notice must
precede the final checkpoint. Results processed after the cutoff cannot change
an already final unknown outcome. This makes an acceptance/stop race explicit.

Wait for any serialized registration commit already in progress. Discard queued
work with `discarded` notices, cancel unconfirmed in-flight delivery work and
record `unknown`; neither outcome authorizes advancing successful-notification
progress. Cancel pending registrations. Keep participating input Sessions alive
for the final coordination instead of cancelling every Session immediately.
Finish/expire a periodic checkpoint correlation before issuing the final one;
a periodic result cannot substitute for a later final snapshot.

Concurrently for each participating input, send all remaining terminal notices
and `checkpoint` requests with `quiesce: true`. Quiescing prevents further event
emission for that watch before returning its snapshot and lasts until Session
closure. Use one ten-second protocol deadline shared by this final phase across
all instances; stop interrupts any periodic wait so it does not add another
ten seconds. Persist each successful final snapshot before plugin cleanup.
An accepted notice that cannot be sent/applied, a failed/expired final snapshot,
or failed save produces the same visible persistence warning; the corresponding
normal-restart guarantee is unavailable. Continue cleanup rather than hanging
on a nonresponsive input. A healthy input's negative/expired periodic checkpoint
does not stop other watches or notifications.

Then cancel Sessions, use standard `Client.Kill`, await configured-process exit,
and release state exclusion only after all storage work and plugin cleanup.
Stop acknowledgement still precedes process exit; the operator waits for exit.
There is no new process-shutdown RPC, automatic delivery retry, event replay, or recipient
processing claim. The final protocol phase is bounded; existing filesystem and
framework-cleanup limits remain separately observable. Successful checkpoint
diagnostics identify the registration and committed generation without payloads.

### Offline reset, deletion, and recreation

The [operation guide](windows-operations.md#trigger-runtime-state-recovery)
gives the reusable procedure. Stop every owner, wait for configured-process exits,
confirm no live owner, and preserve the complete state tree before any change.
Do not edit a live tree or remove a live `.lock/`.

To reset one retained watch deliberately, remove its entire
`runtime/<subscription_id>/` directory from the stopped tree, leaving its saved
registration untouched. Restart uses the same ID/destination with `state: null`
and the trigger's documented initial behavior. This may notify earlier updates
again; the explicit choice relinquishes their continuity. Do not delete only the
latest generation, since that would silently roll back to older progress.

To delete a registration, remove both `registrations/<subscription_id>.json`
and its entire runtime directory while stopped. Recreate by normal registration
after restart; it receives a new UUID and initial state even with identical
arguments. Removing only a registration file never transfers its orphan runtime
directory to the recreated ID. Restore a preserved valid tree/configuration for
repair instead of resetting when continuity is required. No live removal API or
automatic corrupt-state salvage is added.

## Specification documents

This document is authoritative for coordination, scheduling, storage, ownership,
recovery, and runtime-state acceptance. [Plugin v2](plugin-v1.md) is the separate
authoritative wire specification for authors in any language; its optional
extension derives from this design. [Registration persistence](state-persistence.md)
retains immutable registration storage/commit/exclusion authority and incorporates
this mutable runtime extension by reference. [MVP](mvp.md) retains recipient,
delivery and configuration authority. The [operation guide](windows-operations.md)
derives reusable operator procedures; [bundled design](bundled-plugins.md) retains
unchanged model-plugin artifacts and its verification authority. No additional
specification is deferred. The optional [stream specification](runtime-state-stream.md)
owns its capability, conditional wire/storage contracts and resource controls;
it derives ownership and coordination from this design. Internal package/helper names and temporary suffixes
remain routine implementation choices constrained by these contracts.

## Decisions and alternatives

| Decision | Alternatives | Rationale and tradeoff |
| --- | --- | --- |
| Runtime-state v2 one-way activation; preserve v1 | Silently strengthen v1; reject v1; add activation ACK or all-watch transaction | Explicit opt-in preserves existing authors and binary shapes; rejecting v1 expands migration scope. A grant suffices once admission is installed. An ACK cannot undo earlier delivery if a later watch fails; post-commit failure uses ordinary unavailability. |
| Optional v2 capability and distinct participating messages | Mandatory wire version change; adding fields to every old message | Unchanged old inputs retain their artifacts and message shapes; new participating inputs require a new host. |
| Ordered outcome notices plus coordinated snapshots | Snapshot after cancelling input; event receipt as successful progress; core service-cursor interpretation | Preserves confirmed delivery at normal stop while keeping service meaning inside the trigger. |
| Per-registration bounded envelope, or optional stream | Per-process shared state; arbitrary unbounded blobs | Ownership and reset are explicit. Legacy inputs retain their allocation; the stream profile separates message size from total state under an operator byte budget and existing time limits. |
| Immutable generation commit and old-generation cleanup | Replace an existing file; retain every checkpoint forever | Portable unique-path rename preserves the previous commit on failure without intentional storage growth. |
| Fail closed on incompatible state | Automatic initial state; silently ignore state on opt-out | Continuity requires an explicit repair or reset decision. |
| Periodic plus final snapshots | Shutdown-only saves; plugin-owned files | Periodic failures become visible during monitoring; final coordination covers later accepted notifications. |

## Quality and operations

Use Go standard filesystem/process APIs and the existing gRPC service; no
host-specific storage or language requirement is introduced. The dedicated tree
and exclusion share existing trusted-local-operator limitations and best-effort
owner permissions. State may contain sensitive service identifiers: protect
backups like configuration and never print payloads in errors. State and notices
grant no authority to change a recipient or execute external instructions.

State size, registered watch count, one batch per input, and four filesystem
workers bound resource usage. Local storage latency can delay checkpoints and
shutdown; diagnostics distinguish committed progress from unsaved progress.
Generation cleanup failure may retain old files until offline repair; this is
visible rather than an intentional history feature. A successful save promises
normal stop/restart only, never process-image or power-loss restoration.

## Verification strategy

Use mocked filesystem/plugin capabilities in unit tests for strict value/schema
and exact-number matrices, all read/write/close/rename errors, identity/capability
mismatches, generation selection/cleanup, duplicate and exhausted counters,
late/malformed/negative/expired checkpoint responses, and acceptance/stop races.
Add v1/v2/nonparticipant negotiation and incompatible capability combinations;
no grant on storage/restore/availability failure; route/correlation installation
before grant; duplicate/foreign/pre-accept grants; dormant checkpoints;
quiesce-before-grant; stop before/after startup commit; blocked/failed grant
send, batch expiry and cleanup. Mock stream/clock/storage boundaries to assert
commit ordering, retained records, no v1 grant, unchanged v1 early-event discard,
and no retry/restart.
Assert a final snapshot follows every pre-cutoff accepted notice, while queued,
unknown, failed, startup-discarded and tombstone events cannot establish success.
Cover bounded batching, timeout continuation, opaque payload diagnostics,
unchanged destination and old-state initialization without process-heavy matrices.

The three existing command cases cover file/process/restart and recovery.
One additional case, TRS-V04-ACTIVATION, covers the public complete-restoration
barrier with an actual daemon. Broad failure/race matrices remain in unit tests;
unchanged v1 cases retain their implemented status and expectations.
Combine restoration/isolation/duplicate/deletion into one case; combine deliberate
reset with failed startup; retain save failure as a separate running-daemon case.
Reuse SP-V01-RESTORE for unchanged nonparticipating artifacts and MVP-V05-RESTART
for volatile work. Repeating every format or storage fault through subprocesses
adds maintenance/runtime without stronger boundary evidence. Unit and integration
results remain separate. Static Pending and feasibility probes prove no product
acceptance; implementation must pass all scoped approved cases.

### CI acceptance cases

The governed files are
`cmd/agent-pulse-hub/trigger_runtime_state_integration_test.go`,
`cmd/agent-pulse-hub/trigger_activation_integration_test.go`, and
`cmd/agent-pulse-hub/testdata/trigger-runtime-state-cases.json`, using the existing
`integration` tag and `TestIntegrationMVP` suite. Independent mechanism probes live in
`cmd/agent-pulse-hub/trigger_runtime_state_feasibility_integration_test.go` and
`cmd/agent-pulse-hub/testdata/trigger-runtime-state-probe.py`. They check protocol/process
and generation-file feasibility outside the scoped acceptance inventory; they
do not establish product acceptance. Verification-only fixtures stay
under that command's `testdata/`; they may substitute the external source and
observable action, but run the actual daemon/client, public gRPC, filesystem and
shutdown coordinator. The participating fixture reads source updates, maintains
confirmed logical IDs in memory, consumes notices, and returns checkpoints; it
performs no runtime-state file operations. Its stopped-period policy deliberately
offers source IDs again after restart so restored confirmed IDs suppress U1 and
fresh U2 remains observable. This fixture policy is not a common service promise.

| Stable ID | Requirements / acceptance | Inputs and actions | Observable results and boundary |
| --- | --- | --- | --- |
| TRS-V01-RESTART-ISOLATION | FR-001–FR-005, FR-009; NFR-001–NFR-003; AC-001, AC-002, AC-006 | Two registrations A/B with distinct sources/destinations. Just after a periodic batch completes, notify U1/V1, observe accepted delivery notices, repeat equivalent A, and stop before the next periodic checkpoint. Await exit, restart without registration, present U1/V1 then U2/V2. Stop, remove A registration/runtime directory by the offline procedure, restart and recreate identical A arguments. | Fixture journal proves no intervening periodic snapshot and final quiesce snapshot includes U1/V1. Same retained IDs and distinct state; U1/V1 are suppressed after restart, U2/V2 each notify once to their own destinations. Equivalent A retains ID/progress and one watch. Recreated A has a fresh UUID/null state and initial behavior; B retains progress. Source fixture performs no runtime-file operations; no recipient change or replay. |
| TRS-V02-RESTORE-RECOVERY | FR-002, FR-006, FR-010; NFR-003; AC-003 | Save a checkpoint, stop; in copies corrupt the latest runtime file and separately use a syntactically valid incompatible schema. Start each copy, then explicitly reset by removing the complete runtime directory while stopped and restart. | Failed starts identify input/ID/reason without payload, no ready/affected watch/fallback delivery; files retained. Explicit reset permits initial behavior with the original ID/destination. Unreadable-file errors are deterministically injected in unit tests rather than relying on OS permissions. |
| TRS-V04-ACTIVATION | FR-002–FR-004; NFR-001, NFR-002; AC-001; common coordination for GitHub review request FR-008/AC-005 | Actual daemon/client/model output and storage-free v2 input fixture. Retain two routes, order IDs by filename. On restart apply/accept the first watch; hold the later result with a fixture-controlled file barrier, offering fresh U2 to the first source while held. Release success. Repeat from stopped saved state, rejecting the held later watch. | Before release the journal shows prepared first watch, no grant/source read/event/output. Successful release grants only after all results; U2 delivers once with accepted notice on retained ID. Later rejection yields no grants/source reads/events/delivery/readiness, retained files and all configured-process exits. Unit tests own stop/send/storage/duplicate matrices. |
| TRS-V03-SAVE-FAILURE | FR-007; AC-004 | Start a participating route, confirm U1; while running obstruct its runtime directory with a regular file as controlled fault injection; await a periodic failed save, then offer U2. Restore storage only after stopping. | Diagnostic identifies input/ID/path/reason and warns continuity cannot be guaranteed. U2 is still monitored and accepted once; failed operation has no successful generation/continuity claim. Fault injection is not supported live operator editing. |

Each new case needs integration because mocked components cannot demonstrate
wire ordering with actual process stop, state-directory ownership, and a second
daemon process. The cases use isolated temporary trees and explicit output/ready/
checkpoint observations rather than fixed sleeps. The command's existing Python
3.12 and pinned gRPC tooling, Go build and Ginkgo/Gomega dependencies suffice;
no live services, credentials, new CI jobs or shared harness change is required.
Implement the fixture through the existing verification-only plugin-tooling
mechanism. Limit files/logs, supervise at most six direct children, and stop or
kill/await every child and remove temporary trees even on assertion failure.

Use `go run build/mage.go -d build -w . test` for unit results and
`go run build/mage.go -d build -w . integrationAll` for separate tagged results.
The scoped Ginkgo focus `case:TRS-` registers three existing IDs and the new
static Pending TRS-V04-ACTIVATION; include
SP-V01-RESTORE and MVP-V05-RESTART as retained regression evidence. The new scoped
budget is 90 seconds after dependency cache warm-up: at most 30 seconds fixture/
binary preparation, 40 seconds combined scenario runtime, ten seconds setup and
ten seconds cleanup; peak six children. Existing acceptance ceilings remain.
TRS-V04 has a separate 30-second scenario and five-second cleanup allowance,
with shared 30-second warm build/setup and peak six children. Prepare two saved
routes in one daemon, restart successfully, then reject restoration in a third
launch. Extend only the command-local fixture with v2 mode, grant journaling,
first-source-read journaling and a later-watch result barrier. Use journal rows
and file releases, not fixed sleeps. Existing gRPC/process and file-barrier
patterns plus the v2 standalone probe establish mechanisms without modifying
shared harness, CI or dependency versions. The fixture's producer starts on
grant, making absence of premature source work observable. Probe with
`go test -tags=integration ./cmd/agent-pulse-hub -run '^TestTriggerRuntimeStateActivationFeasibility$' -count=1 -v`.
These mechanisms do not establish the future product barrier.
Compile/register every new static Pending case with the tag enabled and execute
minimal real gRPC/process and generation-file feasibility probes before design
readiness. Record actual commands, environment, times and outcomes in the PR;
neither those probes nor Pending registration establish implemented behavior.

### Delivery quality checks

| Check | Conditions and expected results | Evidence and evaluator |
| --- | --- | --- |
| TRS-D01 | Maintainer follows finalized author/operator instructions using the service-free participating fixture and an observable action. Record U1 acceptance, normal stop, saved generation, restored same ID, no second U1 and one U2; inspect two-registration isolation and deliberate recreation; demonstrate failed load, explicit reset, and visible save failure with continued U2. Separately run unchanged model instructions/SP-D01. | Delivery maintainer records exact revision/commands, environment, redacted paths/IDs, terminal outcomes, checkpoint notices and saved/restored schema/progress observations, output records, recovery choices and configured-process exits. Inspect fixture source for absence of state-file access and confirm documented initial/stopped-period semantics. |

This check is designed here and executed during delivery; it does not replace
implementation CI or any existing real Codex/external-service checks. No new
external-service trigger or live-service delivery case is introduced. Existing
service-plugin quality evidence remains with its governing design.

## Change and rollout impact

Implement optional wire capability, outcome correlation, checkpoint lifecycle,
runtime storage, diagnostics, and operator procedures together after approval.
The original v1-to-v2 activation revision changes no persistent format: registration v1 records,
runtime generation version 1, state envelopes and plugin schemas stay identical.
The separately opted-in [stream rollout](runtime-state-stream.md#migration-and-rollout) adds
stream generation version 2, preserves legacy readback and requires matching
stream-capable binaries after a stream generation is committed.
A compatible v1-to-v2 trigger upgrade loads the same saved checkpoint without
rewrite. Do not persist/infer the negotiated capability from progress. New v2
inputs require a v2-capable host; older strict hosts reject its unknown capability
visibly. Use matching binaries and stopped-tree preservation for rollback. V1
on the new host remains supported with its explicit pre-readiness discard limit.
Old registration v1 files need no rewrite; absent runtime directories mean
initial state. Manual/model plugins remain nonparticipating and unchanged.
Existing pure-registration storage failure semantics remain intact.

Stop before replacing binaries and preserve the entire tree. Old daemons reject
the added reserved `runtime/` directory. For deliberate rollback, preserve that
directory separately and remove it only from a stopped working copy, retaining
registration files; use nonparticipating compatible triggers or their prior
artifacts. The old daemon cannot maintain runtime progress. Returning to new
binaries must explicitly restore an appropriate preserved state or reset it;
never claim continuity across operation by an old daemon. No automatic downgrade
or schema migration is provided, and output already accepted cannot be undone.

## Open questions and risks

No unresolved implementation decision remains. Human design review covers the
optional capability, normal-stop cutoff, opaque-state bound, periodic schedule,
and offline reset. Slow storage, failed snapshot production, plugin loss, or
generation cleanup can prevent or delay continuity; visible diagnostics and
explicit recovery define those limits without adding crash or retry guarantees.

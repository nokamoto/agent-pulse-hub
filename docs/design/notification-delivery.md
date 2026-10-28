---
type: Design
title: Notification evidence and safe automatic retry
description: Separate notification evidence from attempts and define the safety prerequisites for retrying delivery to the registered Codex conversation.
sources:
  - id: notification-requirements
    resource: requirements/notification-delivery.md
  - id: cancellation-requirements
    resource: requirements/subscription-cancellation.md
  - id: codex-design
    resource: design/codex-queue.md
  - id: runtime-design
    resource: design/trigger-runtime-state.md
---

# Notification evidence and safe automatic retry

## Context and scope

This design proposal addresses [notification delivery](../requirements/notification-delivery.md)
FR-001–009, NFR-001–003 and AC-001–008. A notification is an admitted event
bound to one immutable route and context. An attempt is one submission of that
notification. Queue acceptance for the registered conversation confirms delivery;
conversation receipt and task completion are separate and are not tracked.

The design cannot be approved until the receiver contract in
[Safety prerequisites](#safety-prerequisites) and the integration decisions in
[Open decisions](#open-decisions) are resolved. Blocking unsafe submissions is
required behavior, but does not fulfill the requirement to deliver through a
supported safe-retry route. This proposal does not define a fictitious Codex API.

The extension applies only to explicitly participating routes. Existing
one-attempt routes keep the [Codex Queue](codex-queue.md) and
[MVP](mvp.md) contracts. Previously unrecorded notifications are not recovered.
Registration retry, source-event suppression, new conversations, plugin restart,
manual retry and downstream exactly-once execution remain excluded.

## Architecture and responsibilities

The core owns notification identity, evidence, retry eligibility, scheduling
and persistent records. The action owns Codex capability validation, submission
and translation of authoritative queue evidence. Triggers retain ownership of
source observation and logical progress; they receive neither Codex state nor
responsibility for storing attempts.

The intended path is event admission, durable notification creation, durable
attempt reservation, action submission, correlated evidence recording, then
either completion or automatic scheduling of the same notification. Persistence
failure before submission prevents that submission. Failure after submission
preserves uncertainty and prevents further submission until safety is established.

The action must not acknowledge queue acceptance from event text, a message
marker, another conversation's ACK, or merely a successfully started process.
A trusted destination response must identify the notification and original
destination. An attempt-specific request correlation alone cannot reconcile an
accepted notification after that request's response is lost.

## Safety prerequisites

The existing `queue --thread ... --message ...` profile returns a generated
queue token and target conversation. It does not define a caller-supplied
idempotency key, an atomic duplicate check, or an acceptance lookup keyed by
notification identity. Repeating an identity inside the message body does not
change that boundary. Its one-attempt behavior therefore cannot be upgraded to
safe retry solely by adding a sender-side ledger.

Codex CLI `0.165.0-alpha.1.2` exposes an alternative candidate in its experimental
App Server schema: `thread/queue/add` accepts `clientUserMessageId`, `input`
and `threadId`, and returns a `queuedSubmission` containing
`clientUserMessageId`, `id` and `input`. `thread/queue/list` returns queued
submissions for a thread with pagination. These request/response types are not
included in that CLI's schema generation without `--experimental`.
The types establish a possible correlation surface, not atomic deduplication,
conflict rejection or retention after consumption/restart. Absence from a queue
listing cannot prove non-acceptance. Selecting this candidate requires a
supported connection to the selected conversation's shared server as well as
all guarantees below; `turn/start` or `turn/steer` is not a queue substitute.

A compatible receiver must establish all of these properties:

1. Bind a stable caller identity to the original conversation and immutable
   payload before or atomically with acceptance. Concurrent equal submissions
   return evidence for one acceptance; conflicting payload or target reuse is
   rejected without another enqueue.
2. Correlate an acceptance response or read-only reconciliation result to that
   identity, conversation and accepted payload. An absent record is not proof
   of non-acceptance unless the receiver contract makes it authoritative.
3. Preserve deduplication across the supported sender restart and receiver
   lifecycle. Define the safety record's lifetime, expiry reference clock,
   namespace, reset conditions and behavior after expiry.
4. Prevent a delayed or already-issued request from enqueueing after its safety
   record is forgotten. A sender-local expiry timer is insufficient: the
   receiver must reject an expired request, or retain protection until all
   requests that could still arrive are covered by a demonstrated bound.
5. Expose unsupported capability, permanent rejection and retry guidance without
   relying on arbitrary diagnostic prose or falling back to ordinary queueing.

Both retrying an accepted-but-unconfirmed request and querying its acceptance
are viable only with a documented receiver guarantee. A local proxy that writes
a record and calls the existing non-idempotent queue still has an acceptance/
recording gap. It does not satisfy these prerequisites. Removing that gap by
changing Codex or its queue service is not authorized product work in this design.

The Codex adapter's concrete API, versions and safety lifetime must be selected
and evidenced before the public plugin schema and acceptance fixtures are
finalized. Help output or a controlled fake cannot establish actual receiver
semantics. Real-service quality execution remains a delivery responsibility.

## Notification and attempt model

These are proposed logical records, not a finalized wire or storage schema.
Use a fresh opaque notification ID for every admitted event, including separate
events with identical context. Preserve the original subscription ID, configured
input/output names, destination arguments and exact decoded context. Store a
separate attempt ID and ordinal for every issued submission; never make an
attempt by registering again, replaying a trigger event or replacing the ID.

Keep acceptance evidence separate from scheduling state:

| Dimension | Values and interpretation |
| --- | --- |
| Acceptance | `not_attempted`, `in_progress`, `not_accepted`, `unknown`, `queue_accepted`. An earlier ambiguous issued attempt keeps the aggregate unknown even if a later attempt is rejected, unless authoritative evidence resolves the original uncertainty. |
| Scheduling | Eligible/waiting, stopped after acceptance, exhausted, permanently rejected, blocked for safety, or cancelled. Stopping retries does not turn unknown into failure or success. |
| Attempt evidence | ID, ordinal, reservation/start/end times, correlated outcome, bounded reason and optional destination retry guidance. A reserved attempt without a conclusive result is uncertain on recovery. |
| Safety evidence | Receiver identity namespace, immutable binding, validated capability profile, receiver retention/expiry evidence and remaining submission bounds. Exact representation depends on the receiver contract. |

Matching acceptance is monotonic: stale timeout or failure results cannot erase
it. A matching delayed ACK may resolve unknown even after exhaustion or
cancellation, but cannot reopen scheduling. Reject a mismatched destination,
notification, payload binding or unknown attempt correlation. A receipt from a
conversation is not authoritative queue evidence.

Inspection must enumerate retained notification IDs for an explicit subscription
and inspect one ID without submitting work. The response includes destination,
both status dimensions, attempt outcomes/times and actionable stop reasons;
routine output excludes context, credentials and raw process arguments. Results
must be bounded and paginated under the existing control message limit. Exact
commands, response schemas and pagination stability remain an approval blocker,
along with the plugin capability negotiation and late-evidence messages.

## Retry and lifecycle proposal

Propose at most three submissions in total: the original and two automatic
retries, with minimum waits of two and ten seconds after the preceding attempt
ends. Persist the consumed ordinal before issuing work; normal restart never
refunds it. This small fixed policy bounds load and remains inexpensive to test.
It is a proposal for design review, not an existing product setting.

Schedule only when the route is participating, active and not cancelling, no
accepted evidence exists, a submission remains, and the receiver safety boundary
covers the next request. A single core serialization point reserves attempts
and arbitrates scheduled callbacks, acknowledgement, cancellation and stop.
Recheck eligibility at issue time; a stale timer cannot authorize submission.
Receiver deduplication remains necessary despite this serialization.

Honor destination retry guidance by taking the later of the local delay and
the destination's minimum time. If that time lies outside established safety,
stop as safety-blocked. Permanent configuration or compatibility errors stop
automatic submissions. Uncertainty is retryable only with established receiver
safety. Do not restart an unavailable action automatically.

Persist notification evidence independently of registration arguments and
trigger checkpoints, under the existing state-directory exclusion. Normal stop
closes admission and scheduling, resolves issued work to accepted or unknown,
and saves the remaining evidence before releasing ownership. Restart validates
records before resuming still-eligible work with the same identity and remaining
bounds. Invalid, missing, incompatible or insufficient records never create a
fresh identity for an old notification.

The proposed operator evidence-retention period is seven days after admission,
with automatic retries limited to the receiver-proven interval within that
period. The local retention period is not a promise that Codex deduplicates for
seven days. Expiry stops submissions; it must not erase protection for delayed
requests. The final storage schema must specify safe compaction, tombstones,
missing-record detection, clock rollback handling and receiver expiry before
this policy is implementable. Forced termination and power-loss recovery remain
outside the guarantee. Restoring an older backup or deleting safety records is
not a supported way to retry.

## Shared contracts and cancellation

[Individual cancellation](../requirements/subscription-cancellation.md)
FR-002–004 and FR-006 require success to fence every new attempt. The retry
scheduler must participate in the same lifecycle serialization as unsubscribe.
A reservation that has not been issued must be stopped when cancellation wins;
an already-issued attempt may finish. Cancellation never recalls or retries it.
Normal restart must not revive cancelled work.

Keep notification evidence and required deduplication protection after deleting
the registration and trigger progress. Inspection by the original subscription
ID must remain possible during notification retention even when that subscription
is no longer registered. A delayed matching ACK can update that evidence only;
it cannot recreate a watch, checkpoint, rewait grant or retry task. A later
registration of identical arguments has a new subscription ID and inherits none
of the cancelled notification work. Cancellation partial failure must leave
scheduling fenced until explicit supported recovery establishes a safe state.

The exact persistent cancellation marker and commit ordering must be reconciled
with the separately governed cancellation design. These constraints derive
from approved cancellation requirements; they do not select that design's API
or claim an unapproved proposal as a dependency already satisfied.

The [trigger runtime protocol](plugin-v1.md#optional-trigger-runtime-state)
sends one terminal `delivery_notice` per `state_event`. Per-attempt unknown must
not be sent as a terminal notice and later followed by a second accepted notice.
During same-Session retry, retain the notification's outstanding source
correlation until its terminal outcome. A final unknown at shutdown remains
terminal for that Session under [runtime coordination](trigger-runtime-state.md).
Safe retry after restart therefore needs an explicit design decision about
durable source correlation and checkpoint progress; an old Session `event_id`
cannot simply be sent to a new Session. This is an unresolved common contract,
not permission to reinterpret a PR-watch confirmation position as a receipt.

Capacity-driven continuation notifications represent distinct admitted events.
They must remain distinct from these same-notification retries. This design does
not add capacity, source pagination, readiness notifications or cancellation
functionality. Any change to common checkpoint semantics must be integrated
against the latest approved design before dependent implementation.

## Specification documents and compatibility

This document contains the proposed policy and unresolved prerequisites. It does
not supersede existing specifications until its exact contracts and their
corresponding revisions are included and approved together. No separate new
specification directory is needed.

The completed design must revise [MVP routing/control](mvp.md),
[plugin wire schema](plugin-v1.md), [Codex action](codex-queue.md),
[registration persistence](state-persistence.md), [trigger coordination](trigger-runtime-state.md)
and [operator procedures](windows-operations.md) where affected. These remain
authoritative for existing routes. Define explicit opt-in, old-host/action
compatibility, migration of valid older state, rollback refusal when newer
safety state exists, and diagnostics for unsupported participating routes.
Do not infer opt-in from a Codex executable's help or replay legacy volatile work.

Implementation may choose internal Go package/helper names and ordinary data
structures. It may not invent the missing external API, wire schema, migration,
source correlation or retention semantics. Operator/plugin-author instructions
derive from the approved contracts and are finalized and exercised in delivery.

## Verification selection and requirement coverage

Use unit checks for identity conflicts, evidence monotonicity, stale scheduling,
retry bounds/guidance, cancellation races, clock behavior, redaction and storage
failure injection. These decisions do not need a real process for every input.
Retain assembled verification only for public status/control, plugin boundaries,
durable restart and actual destination-subprocess behavior. Combine overlapping
response-loss, delayed-ACK and duplicate-prevention observations into one workflow.
Keep the existing bundled manual/model-output MVP suite independent of Codex.

The following coverage groups are candidates, not approved Ginkgo cases. A
completed design must supply concrete schemas, fixtures, static Pending cases,
an inventory and measured feasibility before it can claim selection is complete.

| Requirements | Acceptance | Proposed verification and unresolved dependency |
| --- | --- | --- |
| FR-001–003, NFR-002 | AC-001, AC-007 | Unit identity/evidence/adversarial matrices; real hub plus bundled model plugins, public list/inspect during admission, held submission and acceptance. Status never enqueues. Needs final control and output profiles. |
| FR-001, FR-004, FR-007 | AC-002 | Real scheduler and model action: fail before acceptance, retry automatically, then stale timer; separately deliver equal context under a new ID. Needs model safe-retry profile. |
| FR-003–006 | AC-003, AC-004 | Actual Codex action through public Session with controlled external receiver; drop an accepted response, overlap submissions and delay ACK, assert one receiver notification and conflict rejection. Unit tests cover callback permutations. Needs real receiver contract to model faithfully. |
| FR-006–008 | AC-005, AC-006 | Policy/storage unit matrices plus actual hub normal stop/restart with accepted-but-unconfirmed, accepted and exhausted records; public status and receiver counts retain identities and bounds. Needs persistence and source-checkpoint decisions. |
| NFR-001–003 | AC-007 | Source/contract review, independent model suite, standalone action suite, diagnostic assertions and separately identified delivery evidence. No trigger-owned Codex retry state. |
| FR-002, FR-006–009, NFR-001 | AC-008 | Documentation review and delivery walkthrough for discovery, inspection, automatic retry, blocked/exhausted reasons, normal restart and expiry; no manual retry command. Needs finalized operating contracts. |

Intended case placement is `cmd/agent-pulse-hub/*_integration_test.go` for hub
behavior and `plugins/actions/codex-queue/*_integration_test.go` for independent
action behavior, with `//go:build integration` and command/plugin-local
`testdata/` fixtures. Use existing Go/Ginkgo dependencies and Ubuntu CI; add no
shared runner, dependency or CI change. Final case IDs, inventory and per-suite
cost allocation depend on resolved methods. The existing two-minute scoped
suite limit is a ceiling, not feasibility evidence.

Use separate Mage `test` and `integrationAll` results, plus the eventual scoped
`integration` inventory for implementation completion. Compilation, dry-run and
Pending registration do not establish acceptance. No hypothetical fixture
proving only its own idempotency can establish Codex compatibility.

### Delivery checks

The delivery maintainer must retain product revision, Codex executable/server
version, environment, configuration, notification/attempt IDs, fault stimulus,
queue evidence and independently observed destination notification counts.

| Check | Required observation |
| --- | --- |
| Queue confirmation: AC-001/008 | Submit one admitted notification to existing A, inspect matching queue acceptance without requiring an agent receipt; B receives none and inspection adds no work. |
| Response loss and overlap: AC-003/004 | At a supported boundary, lose the response after actual acceptance, allow automatic same-ID retry/reconciliation and a delayed ACK; A has one notification, B zero. A marker in message text alone is insufficient counting evidence. |
| Restart and expiry: AC-005/006 | Persist uncertainty, stop normally, restart within supported safety and remaining bounds; retain one destination notification. Confirmed/exhausted records do not resend. Unsupported/expired evidence blocks with an actionable reason and no queue fallback. |
| Instructions: AC-007/008 | Follow finalized operator and plugin-author instructions, including one-attempt compatibility, retention and blocked recovery; distinguish trigger progress, queue acceptance, conversation receipt and work completion. |

Execute these in delivery after implementation approval and merge. The precise
fault-control/counting procedure must be established in this design before
approval; inability to demonstrate it remains an open verification decision.

## Decisions and alternatives

| Proposal | Alternative and tradeoff |
| --- | --- |
| Receiver-enforced identity with durable core attempts | Sender-only dedup cannot close acceptance-before-save or concurrent-send gaps. It is insufficient, even with a local proxy. |
| Separate acceptance and scheduling status | A single failed/success flag hides unknown after exhaustion or cancellation and can erase delayed evidence. |
| Three submissions with fixed minimum delays | Unbounded retries violate finite bounds; configurable policies increase validation and test combinations without a current requirement. Exact bounds remain subject to receiver safety. |
| Retain evidence independently of registration | Deleting all related files on unsubscribe loses diagnosis and potentially protection for issued requests. Retention costs local storage and needs explicit safe compaction. |
| Unit matrices plus a few assembled workflows | A subprocess for every policy combination adds cost; unit-only coverage misses real protocol, persistence and process failures. |

## Quality and operations

Keep service-neutral decisions in Go core layers and Codex-specific behavior in
the action. Public plugin contracts remain language neutral. Use portable file,
clock and process APIs under the existing trusted-local-operator boundary.
External context cannot choose a recipient or authenticate evidence. Store
context only where needed for exact retry; protect state like configuration.
Default diagnostics identify notification/attempt, destination and bounded reason
without dumping context, credentials, raw service output or full argv.

No numerical performance target or crash durability is introduced. Bound status
pages, attempts, evidence size and retained state in the final schema; retention
must not cause unsafe reuse. An expired, damaged or unsupported record requires
preserving evidence and restoring compatible state/capabilities, not deleting
records and resubmitting under a new ID.

## Open decisions

| Decision | Impact and owner | Resolve before |
| --- | --- | --- |
| Select and evidence an actual Codex idempotent submission/reconciliation contract, including retention and late-request fencing. If none is available, obtain an upstream requirement decision rather than substitute best effort. | Design owner and responsible maintainer; blocks FR-003–006, safe retry and meaningful standalone fixtures. | Design approval; all dependent implementation. |
| Finalize public status commands, output capability/messages, exact data schemas, route opt-in and migration/rollback. | Design owner; needed for plugin authors, status clients and implementers. | Design approval. |
| Reconcile cancellation commit/fence and retained safety records against the approved cancellation design. | Both design owners; no further attempt may start after successful unsubscribe. | Shared-contract approval and dependent implementation. |
| Resolve one-terminal-notice source correlation across normal restart, final checkpoints and later accepted evidence. | Notification/runtime design owners; prevents duplicate terminal notices and incorrect source progress. | Design approval; check capacity-design dependencies if common checkpoints change. |
| Finalize retention/expiry, missing-record detection, clock handling, safe compaction and storage format. | Design owner with receiver evidence; seven-day local retention does not establish receiver safety. | Design approval. |
| Complete economic case selection, static Pending artifacts, executable fixture feasibility and bounded delivery fault/counting procedures. | Design owner; coverage groups alone cannot establish readiness. | Design approval. |

---
type: Requirement
title: Individual subscription cancellation
description: Cancel one registered watch using its existing subscription identifier without stopping other watches.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: state-persistence
    resource: state-persistence.md
    title: Local daemon state persistence
  - id: trigger-runtime-state
    resource: trigger-runtime-state.md
    title: Trigger runtime state
---

# Individual subscription cancellation

## Problem and goals

An operator needs to stop one registered watch without stopping the daemon or
interrupting unrelated watches. A subscription connects a trigger watch to an
action destination and has a `subscription_id`, returned by registration.
Cancellation must use that same identifier rather than require the operator
to repeat the watch arguments or discover another identifier.

## Sources and decisions

The user requested an unsubscribe operation alongside the existing registration
workflow and explicitly selected passing the same subscription ID for removal.
These conversation inputs have no separate linkable source. Automatic selection
based on conversation identity is not part of this request.

The remaining policies below are proposals for human review: cancellation also
removes restart restoration and saved trigger progress; a well-formed unknown
or already-cancelled ID returns a not-found failure; success stops future
delivery attempts but cannot retract an attempt already issued to an action.
They make cancellation specific and persistent without introducing output
recall or a cancellation-history service.

This extension supersedes the individual-cancellation exclusion in the
[MVP requirements](mvp.md). It extends
[registration persistence](state-persistence.md) and
[trigger runtime state](trigger-runtime-state.md) to cover live deletion.
Their other routing, restoration, isolation, and failure guarantees remain.

## Scope

- In scope: explicit single-ID unsubscribe through local control and the
  existing agent-facing registration workflow; selective watch termination;
  removal from restart restoration; saved-progress cleanup; visible results
  and failure handling; operator instructions.
- Out of scope: bulk cancellation, listing/searching subscriptions, automatic
  conversation-based selection, remote control, output recall, delivery retry,
  new OS restrictions, or stronger forced-termination/power-loss guarantees.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | Operator or coding agent submits unsubscribe with a registration's returned ID | Only that subscription stops, without daemon restart | Other watches in the same input process continue |
| UC-002 | Operator restarts normally after successful unsubscribe | Cancelled registration stays absent | Other saved registrations restore normally |
| UC-003 | Caller supplies an invalid or absent ID, or cancellation cannot complete | A visible failure identifies the cause without claiming success | No unrelated registration is changed |

## Functional requirements

All requirements below are proposed as required for this extension, subject to
human approval of this requirements revision.

| ID | Requirement | Rationale / use case |
| --- | --- | --- |
| FR-001 | Expose an operation named `unsubscribe` accepting exactly one explicit `subscription_id`, using the identifier returned by registration unchanged. Make it available through local control and alongside registration in the existing bundled agent-facing workflow. Do not infer a target from the caller's conversation or arguments. | UC-001; user's selected interaction |
| FR-002 | On successful cancellation, terminate the target watch and prevent further delivery attempts for that subscription. Discard its queued events and reject later events from the cancelled watch. Keep the daemon, shared plugins, and other subscriptions running. | UC-001 |
| FR-003 | Success must also remove the target from saved registration restoration and remove its saved trigger progress, including associated repeated-wait state when present. Normal shutdown must not recreate cancelled state. Normal restart must not restore the cancelled watch. | UC-002 |
| FR-004 | Report success with the target ID only after both the live cancellation and persistent removal obligations are satisfied. If a plugin or storage failure prevents completion, return an identifiable failure and do not claim successful cancellation. Preserve unrelated registrations and their progress. | UC-003 |
| FR-005 | Reject missing or malformed IDs before changing subscriptions. A well-formed ID with no saved or live registration, including an already-cancelled ID, returns a distinguishable not-found failure. Permit cancellation of a saved registration whose live route has become inactive. | UC-003 |
| FR-006 | An action attempt already issued before cancellation takes effect may finish and is not recalled or retried by cancellation. A successful response must guarantee that no further attempts for the cancelled subscription can begin. Concurrent registration, repeated-wait requests, event processing, and normal shutdown must not reactivate or restore a successfully cancelled subscription. | UC-001, UC-002 |
| FR-007 | A later explicit registration using identical watch and destination arguments is a new registration with a new ID and no inherited cancelled progress. Cancellation must release the former watch's resource reservation so that it does not itself prevent the new registration. | UC-001, UC-002 |
| FR-008 | Provide operator and agent-workflow instructions showing how to retain a registration's ID, submit unsubscribe with it, interpret success and failure, and understand in-flight delivery and restart limits. | UC-001–UC-003 |

## Nonfunctional requirements

| ID | Requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Preserve the existing trusted local-control boundary and external-data restrictions. Event content must not initiate cancellation or select its target. No additional service credentials are required for local cancellation. | Administrative changes remain explicit caller operations | Control-boundary inspection and adversarial input checks |
| NFR-002 | Cancellation and its persistent state changes must isolate subscriptions even when they share an input or output plugin. | Selective removal is the purpose of the feature | Shared-plugin and saved-state checks |

No new latency target, OS support matrix, or crash-durability guarantee is
introduced; these are not needed to define selective cancellation.

## Constraints and dependencies

Keep the Go implementation and OS-neutral product contracts required by the
MVP. Reuse existing identifiers and local control rather than introduce a
service-specific identity or ownership mechanism. Existing registrations must
be cancellable without re-registration or access to original argument files.

Design must define the plugin cancellation contract and compatibility handling,
local command/API syntax, synchronization boundary, storage-failure recovery,
and coordination with repeated waiting and normal shutdown. Partial failure
may leave the target stopped live but still saved; this must be reported as
failure with actionable recovery guidance, never as successful unsubscribe.
Design must prevent saved-progress writes or late callbacks from resurrecting
a successfully removed registration. Protocol or state-format changes require
explicit compatibility and migration instructions before implementation.

## Acceptance criteria

These describe required observations, not test execution results. Design
selects the least costly sufficient verification and separates service-free
implementation checks from any actual-agent delivery walkthrough.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, NFR-002 | Register A and B through a shared input plugin; retain their returned IDs. Submit unsubscribe for A through local control, then trigger A and B. Include a queued A event. | Response identifies A; A's queued/later events cause no new attempts; B still delivers and the daemon and shared plugins remain running. | Assembled local daemon/client/plugin verification with controlled event ordering |
| AC-002 | FR-003, FR-007, NFR-002 | Give A and B distinct saved progress, cancel A, stop normally and restart. Explicitly register A's former arguments again. | Only B restores with its original ID and progress. New registration has a new ID, no inherited A progress, and no reservation conflict left by cancellation. | Local restart/state verification and state-association unit checks |
| AC-003 | FR-004, FR-005 | Submit missing, malformed, unknown, and already-cancelled IDs. Separately cancel a saved inactive route and inject target-plugin and storage failures. | Invalid inputs change nothing; absent IDs yield not-found. Inactive saved route can be removed. Incomplete cancellation reports failure with cause and recovery guidance, never success; unrelated state is preserved. | Validation/failure unit matrices and local boundary checks where plugin or file behavior matters |
| AC-004 | FR-002, FR-003, FR-006 | Control an A action attempt already issued, a queued event, and overlapping repeated-wait, equivalent registration, or normal-stop requests around cancellation. | The issued attempt may finish once; no further A attempt begins after success. No concurrent work recreates A's cancelled registration or saved progress. | Controlled concurrency/unit checks and assembled lifecycle checks where process ordering matters |
| AC-005 | NFR-001 | Supply event content requesting cancellation of B while A and B are registered. | Neither registration is cancelled by that content; only an explicit local administrative request can cancel the selected ID. | Control-boundary inspection and adversarial event unit checks |
| AC-006 | FR-001, FR-008 | Follow finalized operator instructions and the bundled agent-facing workflow from registration through ID-based unsubscribe, then inspect results and restart behavior. | The maintainer records returned ID, cancellation request/result, continued unrelated delivery, and absence after restart; instructions explain failures and already-issued output. | Documentation review and maintainer walkthrough; actual-agent evidence allocated during design to delivery |

## Open questions and assumptions

No additional user decision is required to review this proposal. Unknown-ID
failure, persistent deletion, in-flight semantics, and the other proposed
policies require human approval of this revision rather than being represented
as prior user decisions. The design owner resolves protocol, concurrency,
partial-failure recovery, and compatibility details before design approval;
these decisions must preserve the success guarantees above.

## Change impact

This new concept defines FR-001–FR-008, NFR-001–NFR-002, and AC-001–AC-006.
No existing requirement ID is retired. Adoption replaces the MVP's individual
cancellation exclusion and extends live deletion beyond the offline recovery
procedure in trigger runtime state FR-005. Registration persistence FR-004
continues to restore retained registrations only; cancelled ones are removed.
Repeated waiting must not become a route to reviving a cancelled subscription.

After requirements approval and merge, update the
[routing design](../design/mvp.md),
[plugin protocol](../design/plugin-v1.md),
[registration persistence design](../design/state-persistence.md),
[runtime-state design](../design/trigger-runtime-state.md),
[repeated-wait design](../design/pr-watch-rewait.md), and
[operation guide](../design/windows-operations.md). Review the
[Codex Queue design](../design/codex-queue.md) for the agent-facing workflow.
Design defines compatibility, developer contracts, and acceptance cases;
implementation and delivery remain separate phase PRs.

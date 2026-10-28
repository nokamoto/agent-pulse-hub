---
type: Requirement
title: Observable notification delivery and safe retry
description: Track delivery to the selected Codex conversation and retry the same notification without creating duplicate notifications.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: codex-queue
    resource: codex-queue.md
    title: Codex Queue action plugin
  - id: registrations
    resource: state-persistence.md
    title: Local daemon state persistence
  - id: trigger-progress
    resource: trigger-runtime-state.md
    title: Trigger runtime state persistence
---

# Observable notification delivery and safe retry

## Problem and goals

An operator needs to determine whether a notification reached its registered
Codex conversation and retry it when it did not. A missing response can also
mean that Codex accepted the notification but its acknowledgement was lost.
Sending a new notification in that situation can notify the conversation twice.

Success means the operator can inspect target-specific queue acceptance and
the daemon automatically retries an eligible notification without a second
notification at the destination. Queue acceptance is the required confirmation
stage. Receipt in the conversation and completion of work are separate
observations and are not required for delivery confirmation.

## Sources and decisions

The user requested target-Codex delivery confirmation, retry after non-delivery,
and prevention of duplicate notifications when delivery is uncertain. The user
prioritized the simpler confirmation implementation and selected automatic
retry only. Use the existing target-specific queue acceptance acknowledgement
as the confirmation stage; do not require an additional receiving-agent receipt.
These conversation inputs have no separate linkable source. Q-001 and Q-002
record the resolved decisions.

The [MVP](mvp.md) and [Codex Queue](codex-queue.md) establish the earlier
one-attempt behavior. This extension replaces that behavior only for routes
that support the safe-retry contract. Other routes retain one attempt and no
retry. [Registration persistence](state-persistence.md) and
[trigger progress](trigger-runtime-state.md) retain their existing ownership;
neither is evidence that a particular notification reached Codex.

## Terms and operator workflow

The daemon **admits an event** when it accepts a valid trigger event for an
active registered subscription into its delivery queue, before submission to
the action and destination.
A subscription connects a trigger watch to its registered action and destination.

- A **notification** is one admitted event bound to its registered action,
  destination, source, subscription, and context. Separate admitted events
  remain separate notifications even if their context is identical.
- An **attempt** is one submission of that notification. Retrying creates an
  attempt for the same notification, rather than a new notification.
- **Queue accepted** means the destination service confirmed acceptance for
  the selected conversation. It does not establish conversation receipt.
- **Conversation received** means evidence from the selected conversation
  confirms receipt of that notification. It does not establish task completion.
- **Unknown** means the available evidence cannot establish whether a submission
  was accepted. A timeout or an absent acknowledgement alone is not non-delivery.
- **Safe retry** means repeated or concurrent submissions of the same
  notification produce at most one destination notification within a documented
  safety boundary. It does not promise successful delivery or exactly-once
  execution of subsequent agent work.

The operator identifies the admitted notification and inspects its queue
acceptance status and attempts. The daemon automatically retries eligible
non-accepted or uncertain notifications within the retry policy, using the
original identity, context, and destination. Confirmed queue acceptance stops
further submissions. If safety cannot be established, the daemon reports why
retry is blocked instead of making another submission. No manual retry operation
is provided by this extension.

## Scope

In scope are per-notification status inspection, target-specific queue acceptance,
automatic same-notification retry, duplicate prevention for retries, and the
Codex action capabilities needed to establish those guarantees. Status inspection
must be usable without manually interpreting daemon log chronology.

Out of scope are general suppression of distinct source events, changing a
notification's recipient during retry, new conversations, automatic plugin
restart, historical notifications that were not recorded under this extension,
reconstruction of events occurring while stopped, manual retry operations,
conversation-receipt or task-completion tracking, and guarantees that an agent
executes downstream side effects exactly once. No GUI, remote hosting, or
numerical performance target is required.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator checks an admitted notification | Status identifies its destination, attempts, and confirmed delivery stage | Missing or unavailable evidence remains unknown rather than successful |
| UC-002 | Submission fails before acceptance | Automatic retry can deliver the same notification | A permanent configuration or compatibility error is visible |
| UC-003 | Codex accepts a notification but its response is lost | Automatic reconciliation or safe retry confirms the original queue acceptance without adding another notification | Absence of a response is not proof of non-delivery |
| UC-004 | Automatic retry scheduling or a delayed acknowledgement overlaps an existing attempt | One destination notification and a consistent final status | Earlier errors cannot overwrite later confirmed evidence |
| UC-005 | Operator restarts normally or required state becomes unavailable | Retained evidence and automatic retry safety remain consistent, or retry is visibly blocked | Missing safety records must not create a new notification identity |

## Functional requirements

All requirements below are necessary for observable queue acceptance and safe
automatic retry.

| ID | Requirement |
| --- | --- |
| FR-001 | Assign each admitted notification a stable identity and preserve its registered action, destination, source, subscription, and context across all attempts. Distinguish each attempt from the notification. Different admitted events must not be merged solely because their context matches. |
| FR-002 | Provide a supported way to identify and inspect notifications for a subscription and inspect one notification by identity. Show its registered destination, current queue acceptance status, attempt outcomes and times, and actionable failure or uncertainty reasons. Distinguish no attempt yet, in progress, confirmed non-acceptance, queue acceptance, and unknown acceptance. Confirmed target-specific queue acceptance satisfies delivery confirmation; do not report it as conversation receipt or work completion. Do not require another notification to inspect status. |
| FR-003 | Advance confirmed status only from evidence correlated to the notification and registered destination. An acknowledgement for another notification or conversation, or unverified event text claiming receipt, must not confirm delivery. A delayed matching acknowledgement may resolve uncertainty; earlier timeouts or failed attempts must not erase confirmed evidence. |
| FR-004 | Automatically retry an eligible notification using its original identity, context, and registered destination, without an operator retry request or an agent polling turn. Never implement retry by re-registering a watch, asking a trigger to emit another event, publishing the same context as a new notification, or assigning a fresh notification identity. Confirmed queue acceptance stops retries; stale automatic retry work must not add another destination notification. Do not provide a manual retry operation. |
| FR-005 | Prevent duplicate destination notifications when acceptance occurs but its response is lost, a process fails after submission, an acknowledgement is delayed, or retries overlap. Checking local status before sending is insufficient on its own: the destination submission boundary must preserve the same notification identity and prevent repeated acceptance, or provide an equivalent demonstrated guarantee. Detect a conflicting reuse of an identity with different context or destination and reject it. |
| FR-006 | Preserve unknown as distinct from confirmed non-acceptance. When safety cannot be established, including unsupported Codex capabilities, missing or invalid safety records, or an expired safety boundary, block further submission and explain the reason. Never fall back to a non-idempotent queue operation or silently reset an unknown notification to unsent. |
| FR-007 | Define finite automatic retry bounds and visible stopping conditions. Retry policy must distinguish retryable non-acceptance or uncertainty from permanent errors, respect destination retry guidance when provided, and stop unnecessary submissions after confirmed queue acceptance. Exhaustion stops retry without falsely converting an uncertain outcome into confirmed failure or success. Exact retry bounds and timing are design decisions. |
| FR-008 | Preserve the notification evidence and safety records needed for status inspection and automatic retry through successful persistence and normal stop/restart. Automatically resume still-eligible retries only within the documented safety boundary and remaining retry bounds; unavailable, incompatible, or insufficient records block unsafe retries visibly. Normal restart must not reset exhausted bounds or resend confirmed notifications. Forced termination or power-loss recovery is not promised, but loss of evidence must never authorize an unsafe resend. Design must define retention, expiry, and supported recovery. |
| FR-009 | Provide operator and plugin-author instructions for identifying notifications, interpreting queue acceptance, observing same-identity automatic retry, diagnosing blocked or exhausted retries, supported Codex prerequisites, normal restart, and retention/expiry limits. Explain that manual retry is outside scope, compatibility with routes that retain the one-attempt contract, and the separation from trigger progress, conversation receipt, and recipient work completion. |

## Nonfunctional requirements

| ID | Requirement and evaluation |
| --- | --- |
| NFR-001 | Keep integration-independent identity, status, and retry decisions in the core and Codex-specific submission/evidence handling in the action. Triggers must not implement Codex receipt tracking or retry storage. Preserve Go implementation, English documentation, OS-neutral procedures, language-neutral public plugin contracts, and the existing daemon-managed plugin lifecycle. Verify contracts and source. |
| NFR-002 | Bind status updates and retry operations to the original notification and destination under the existing trusted-local-operator boundary. External event content cannot select a recipient, forge authoritative confirmation, or grant permissions. Routine diagnostics must not dump context, credentials, or full process arguments. Verify adversarial inputs, routing isolation, and diagnostics. |
| NFR-003 | Verify common status/retry behavior without real Codex or live trigger services, preserving the [MVP hub end-to-end suite using bundled model plugins](mvp.md#acceptance-criteria). Verify the Codex integration independently through its public action boundary and retain separate real-Codex delivery evidence. Mocks cannot establish actual Codex queue acceptance or duplicate prevention. |

## Constraints and dependencies

The existing Codex action's queue acknowledgement confirms queue acceptance
only. Reusing a hub delivery ID in message text does not by itself make
`codex queue` idempotent. Use queue acceptance for the simpler confirmation
implementation. Design must still establish compatible Codex submission,
status, and queue-acceptance correlation that meet FR-003–FR-006; unsupported
capabilities must remain a visible blocker rather than weaken duplicate safety.

Protocol shape, command names, state schema, storage layout, concurrency control,
retry timing, and retention bounds are design decisions. Design must specify
the receiver-side safety boundary and its interaction with the normal-restart
contract, including notification acceptance before sender confirmation is saved.
No additional database service or particular transport is selected here.

Trigger progress may advance when the action confirms acceptance under the
[existing trigger-progress contract](trigger-runtime-state.md#functional-requirements).
Tracking or retrying the resulting notification must not fabricate another
trigger event, reinterpret a
[PR-watch confirmation position](github-pr-watch.md#terms-and-workflow) marking
covered source changes as a delivery receipt, or claim recipient work
completion.

## Acceptance criteria

Design must select economical unit and command-level assembled checks for the
following observations. Real Codex checks belong to the
[delivery phase](../aidd/README.md#4-delivery). The maintainer responsible for
that verification evaluates the results and retains the product revision, Codex version,
environment, configuration, notification/attempt identities, fault stimulus,
status evidence, and observed destination notification count. No live GitHub
trigger is needed to establish Codex delivery behavior.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001–FR-003 | Admit one event for conversation A, inspect it before submission, during submission, and after matching queue acknowledgement. Supply mismatched notification and destination confirmations separately. Do not supply a receiving-agent receipt. | Inspection identifies one stable notification, its destination and attempts, and queue acceptance as confirmed delivery. Mismatched evidence does not confirm it. Conversation receipt and task completion are not claimed or required; inspection submits no new work. | Identity/evidence unit checks and public status-control assembled verification; real queue-acceptance check during delivery |
| AC-002 | FR-001, FR-004, FR-007 | Fail before destination acceptance and allow the retry policy to run without operator retry requests or agent polling. After confirmed queue acceptance, present stale scheduled retry work. Separately admit a distinct event with identical context. | Automatic retry preserves identity, context, and destination and produces one destination notification. Confirmation stops retries and stale work adds none. The distinct admitted event remains independently deliverable. No manual retry operation, re-registration, or trigger replay is used. | Retry-decision unit checks and selected assembled automatic-retry checks |
| AC-003 | FR-003–FR-006 | Accept a submission at A but drop its response. Allow automatic reconciliation or safe retry, then deliver a delayed original confirmation. | A has exactly one notification for that identity. Unknown is visible until correlated queue-acceptance evidence resolves it. Automatic reconciliation and delayed evidence leave a consistent confirmed status without a second notification or an operator retry request. | Controlled assembled response-loss case; independent real-Codex response-loss and destination-count evidence during delivery |
| AC-004 | FR-001, FR-003–FR-006 | Overlap two automatic retry scheduling callbacks with an in-progress original submission; separately reuse its identity with changed context or destination B. | At most one notification reaches A; B receives none. Attempts remain correlated, conflicts are rejected, and stale errors cannot overwrite confirmed queue acceptance. No manual retry interface is needed to exercise the overlap. | Concurrency/conflict unit checks and assembled checks where process boundaries matter |
| AC-005 | FR-006, FR-007 | Leave acceptance uncertain and exhaust the designed retry bound. Separately supply a permanent rejection, supply destination retry guidance, remove required destination capabilities, or expire required safety evidence. | Exhaustion is visible and stops retries while preserving unknown. Permanent rejection prevents further attempts and reports its reason. Otherwise eligible retries respect supplied destination retry guidance. Unsupported or expired safety conditions block submissions with actionable reasons; no ordinary queue fallback occurs. | Controlled policy/clock, capability, and expiry unit checks and selected public-action failure checks |
| AC-006 | FR-005, FR-006, FR-008 | Accept a submission but lose sender confirmation; successfully persist retained records, stop normally, restart, inspect, and allow automatic retry to resume. Separately restart with confirmed or exhausted notifications, and make required records missing, unreadable, or incompatible. | With valid safety records, still-eligible retries resume automatically within remaining bounds; A retains one notification and its identity. Confirmed or exhausted notifications are not resubmitted solely due to restart. Invalid or insufficient records visibly block unsafe retries instead of treating the notification as new. No operator retry request or crash-durability claim is made. | Assembled normal-restart verification and storage-failure unit checks; design-selected real-Codex safety-boundary evidence during delivery |
| AC-007 | NFR-001–NFR-003 | Exercise status/retry with controlled external boundaries, inspect adversarial context and diagnostics, and run the existing bundled-model suite. | Core policy is service-neutral; triggers gain no Codex retry obligations; context cannot retarget or forge confirmation; diagnostics expose no secret/context dump. Service-free, standalone action, and real-Codex results remain separately identified. | Source/contract review, focused unit checks, selected assembled checks, existing model suite, and separate delivery evidence |
| AC-008 | FR-002, FR-006–FR-009, NFR-001 | A maintainer follows finalized instructions to find a notification, inspect queue-acceptance evidence, observe automatic retry, stop/restart normally, and diagnose a blocked or exhausted retry. | Instructions establish queue acceptance as sufficient delivery confirmation and demonstrate automatic retry without a manual retry operation. They explain retention, unsupported capabilities, and the absence of conversation-receipt/task-completion guarantees, and preserve one-attempt behavior for nonparticipating routes. | Maintainer walkthrough and operator/plugin-author documentation review with recorded environment and outcomes |

## Open questions and assumptions

| ID | Decision | Impact if unresolved | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Required confirmation stage | Determines evidence source, status stages, and completion criteria. | Responsible user; design owner applies the simplicity preference | Requirements approval | Resolved: the user prioritized implementation simplicity. Use existing target-specific queue acceptance, without conversation-receipt or work-completion tracking. |
| Q-002 | Retry initiation | Determines scheduling, controls, and acceptance cases. | Responsible user | Requirements approval | Resolved by the user: automatic retry only; no manual retry operation. |

No unresolved user scope questions remain. The design owner must resolve actual
Codex support, queue-acceptance correlation, receiver-side duplicate safety,
automatic retry/retention bounds, migration, and verification feasibility before
design approval. Infeasible support must be raised for an upstream decision
rather than replaced with sender-only best-effort duplicate suppression.

## Change impact

This is a new requirement concept. No existing local IDs are retired. On
adoption, it qualifies the following earlier decisions for participating routes:

| Concept and affected IDs | Revised boundary and downstream impact |
| --- | --- |
| [MVP](mvp.md): FR-006, NFR-004, AC-004, AC-005, AC-010 and retry/persistence exclusions | Replace one attempt/no retry and volatile-only notification state for safe-retry routes. Preserve existing behavior for other routes, role ownership, and service-free verification. |
| [Codex Queue](codex-queue.md): FR-005, FR-006, FR-008, AC-003, AC-004, AC-008 | Add inspectable queue-acceptance evidence and safe same-identity automatic retry. Revisit submission capabilities for duplicate prevention; registration identity and the prohibition on automatically retrying uncertain registration remain unchanged. Idle status inspection must not create new work. |
| [Registration persistence](state-persistence.md): scope, FR-006, AC-005 | Retained notification records are governed here rather than restored registration arguments. Allow only the separately established safe-retry recovery; do not replay legacy volatile hub work. |
| [Trigger progress](trigger-runtime-state.md): scope, FR-004, FR-009, AC-006 | Its no-retry exclusion describes trigger-progress recovery, not an obstruction to core-owned safe retry. Confirmed-action-acceptance progress remains distinct from conversation receipt. |
| [GitHub review requests](github-review-request.md): scope, FR-008, AC-007; [PR watch](github-pr-watch.md): scope, FR-007, AC-005 | Source observation/deduplication contracts are unchanged. Their no-added-retry and uncertain-delivery limits do not override this extension for participating actions; trigger plugins do not resend notifications themselves. |

Adoption must define how existing routes opt in or remain on their earlier
contract. Old unrecorded notifications must not be reconstructed and submitted
as supposedly safe retries. Revise the governing
[routing design](../design/mvp.md), [public plugin specification](../design/plugin-v1.md),
[Codex action design](../design/codex-queue.md),
[persistence design](../design/state-persistence.md),
[trigger-progress coordination](../design/trigger-runtime-state.md), and
[operator guide](../design/windows-operations.md) as affected. Design owns the
necessary command, plugin, storage, and evidence contracts and verification
inventory. Implementation and delivery remain separate phase changes.

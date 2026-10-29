---
type: Requirement
title: Observable notification delivery and safe retry
description: Track delivery to the selected Codex conversation, retry only proven unsubmitted notifications, and stop when acceptance is unknown without experimental API dependencies.
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
the daemon automatically retries only when every earlier attempt is proven not
to have submitted the notification. An attempt that may have submitted it stops
automatic retry, even if delivery remains unconfirmed. Queue acceptance is the required confirmation
stage. Receipt in the conversation and completion of work are separate
observations and are not required for delivery confirmation.

## Sources and decisions

The user selected target-specific queue acceptance as the simpler confirmation
stage and automatic retry only. The user also selected no dependency on
experimental Codex APIs and prioritized duplicate prevention: retry only when
non-submission is certain, and stop when acceptance is unknown. Do not require
an additional receiving-agent receipt or receiver-side idempotent resubmission.
These conversation inputs have no separate linkable source. Q-001–Q-004 record
the resolved decisions.

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
- An **attempt** is one effort to submit that notification, including one that
  fails before submission. Retrying creates an attempt for the same notification,
  rather than a new notification.
- **Proven unsubmitted** means authoritative evidence establishes that an
  attempt did not cross the action's destination-submission boundary. For the
  existing Codex CLI action, a failure to start the queue process can establish
  this; a started process with no acknowledgement cannot. Confirmed rejection
  after submission is non-acceptance, but is not proven non-submission.
- **Queue accepted** means the destination service confirmed acceptance for
  the selected conversation. It does not establish conversation receipt.
- **Conversation received** means evidence from the selected conversation
  confirms receipt of that notification. It does not establish task completion.
- **Unknown** means the available evidence cannot establish whether a submission
  was accepted. A timeout or an absent acknowledgement alone is not non-delivery.
- **Safe retry** means issuing a further attempt only after every earlier
  attempt is proven unsubmitted, with no overlapping submission. It prevents
  retries from adding duplicate destination notifications by stopping once
  submission may have occurred. It does not promise recovery from unknown
  acceptance, successful delivery or exactly-once execution of subsequent work.

The operator identifies the admitted notification and inspects its queue
acceptance status and attempts. The daemon automatically retries eligible
proven-unsubmitted notifications within the retry policy, using the original
identity, context, and destination. Confirmed queue acceptance or any uncertainty
about prior submission stops further attempts. Missing confirmation may remain
unknown indefinitely; status inspection and normal restart do not authorize a
resend. The daemon reports the stopping reason. No manual retry operation is
provided by this extension.

## Scope

In scope are per-notification status inspection, target-specific queue acceptance,
automatic same-notification retry before submission, duplicate prevention through
conservative stopping, and non-experimental Codex integration. Status inspection
must be usable without manually interpreting daemon log chronology.

Out of scope are general suppression of distinct source events, changing a
notification's recipient during retry, new conversations, automatic plugin
restart, historical notifications that were not recorded under this extension,
reconstruction of events occurring while stopped, manual retry operations,
resubmission after possible submission, receiver-side deduplication or acceptance
lookup, experimental Codex API dependencies,
conversation-receipt or task-completion tracking, and guarantees that an agent
executes downstream side effects exactly once. No GUI, remote hosting, or
numerical performance target is required.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator checks an admitted notification | Status identifies its destination, attempts, and confirmed delivery stage | Missing or unavailable evidence remains unknown rather than successful |
| UC-002 | An attempt is proven unsubmitted | Automatic retry can deliver the same notification within finite bounds | A permanent configuration or compatibility error stops retry visibly |
| UC-003 | Codex accepts a notification but its response is lost | Unknown is visible and no further attempt is issued; a later valid matching acknowledgement may confirm the original attempt | Without that evidence the notification stays unknown; no automatic reconciliation or resend is required |
| UC-004 | Automatic retry scheduling or a delayed acknowledgement overlaps an existing attempt | One destination notification and a consistent final status | Earlier errors cannot overwrite later confirmed evidence |
| UC-005 | Operator restarts normally or required state becomes unavailable | Retained evidence and automatic retry safety remain consistent, or retry is visibly blocked | Missing safety records must not create a new notification identity |

## Functional requirements

All requirements below are necessary for observable queue acceptance and safe
automatic retry.

| ID | Requirement |
| --- | --- |
| FR-001 | Assign each admitted notification a stable identity and preserve its registered action, destination, source, subscription, and context across all attempts. Distinguish each attempt from the notification. Different admitted events must not be merged solely because their context matches. |
| FR-002 | Provide a supported way to identify and inspect notifications for a subscription and inspect one notification by identity. Show its registered destination, current queue acceptance status, attempt outcomes and times, and actionable failure or uncertainty reasons. Distinguish no attempt yet, in progress, proven non-submission, confirmed non-acceptance after submission, queue acceptance, and unknown acceptance. Show retry eligibility or its stopping reason separately from acceptance. Confirmed target-specific queue acceptance satisfies delivery confirmation; do not report it as conversation receipt or work completion. Do not require another notification to inspect status. |
| FR-003 | Advance confirmed status only from evidence correlated to the notification and registered destination. An acknowledgement for another notification or conversation, or unverified event text claiming receipt, must not confirm delivery. A delayed matching acknowledgement may resolve uncertainty; earlier timeouts or failed attempts must not erase confirmed evidence. |
| FR-004 | Automatically retry an eligible notification only when every earlier attempt is proven unsubmitted and no attempt is in progress. Use its original identity, context, and registered destination, without an operator retry request or an agent polling turn. Never implement retry by re-registering a watch, asking a trigger to emit another event, publishing the same context as a new notification, or assigning a fresh notification identity. Confirmed acceptance, possible prior submission or confirmed post-submission rejection stops retries; stale retry work cannot issue another attempt. Do not provide a manual retry operation. |
| FR-005 | Prevent retries from creating duplicate destination notifications by serializing attempt issuance and stopping further attempts once submission may have occurred, including response loss, process failure after launch or a delayed acknowledgement. A local timeout, missing acknowledgement, process exit or absence from a queue listing does not prove non-submission. Detect conflicting reuse of an identity with different context or destination and reject it before submission. Receiver-side idempotency is not required and must not be assumed. |
| FR-006 | Preserve unknown as distinct from proven non-submission and confirmed non-acceptance. Unknown is never retryable. When required evidence is missing, invalid, incompatible or expired, block further attempts and explain why; never silently reset a notification to unsent. Do not use an experimental Codex API or weaken this stop rule as a fallback. A delayed matching acknowledgement may update evidence under FR-003 but cannot reopen retry eligibility. |
| FR-007 | Define finite automatic retry bounds and visible stopping conditions. Distinguish retryable pre-submission failures from permanent configuration or compatibility errors. Respect supplied retry guidance only for an otherwise eligible attempt; guidance never authorizes resubmission after possible submission. Exhaustion stops further attempts and preserves the observed acceptance status. Unknown stops immediately rather than consuming a retry budget. Exact bounds and timing are design decisions. |
| FR-008 | Preserve notification evidence, prior-attempt submission eligibility and consumed retry bounds through successful persistence and normal stop/restart. Automatically resume only recorded, still-eligible work whose prior attempts are all proven unsubmitted, within retained evidence and remaining bounds. Records that cannot rule out prior submission must remain blocked, including interrupted in-progress attempts. Restart must not reset exhausted bounds or resend unknown, confirmed-accepted or post-submission-rejected notifications. Forced termination or power-loss recovery is not promised, but loss of evidence must never authorize an unsafe resend. Design must define retention, expiry and supported recovery; expiry cannot make old notifications eligible again. |
| FR-009 | Provide operator and plugin-author instructions for identifying notifications, interpreting queue acceptance, observing same-identity pre-submission retry, diagnosing blocked or exhausted retries, non-experimental Codex prerequisites, normal restart, and retention/expiry limits. Explain that unknown stops retry and may remain unresolved, manual retry is outside scope, and submitting the same context as a new event is not a safe recovery procedure. Explain compatibility with one-attempt routes and the separation from trigger progress, conversation receipt and recipient work completion. |

## Nonfunctional requirements

| ID | Requirement and evaluation |
| --- | --- |
| NFR-001 | Keep integration-independent identity, status, and retry decisions in the core and Codex-specific submission/evidence handling in the action. Triggers must not implement Codex receipt tracking or retry storage. Preserve Go implementation, English documentation, OS-neutral procedures, language-neutral public plugin contracts, and the existing daemon-managed plugin lifecycle. Verify contracts and source. |
| NFR-002 | Bind status updates and retry operations to the original notification and destination under the existing trusted-local-operator boundary. External event content cannot select a recipient, forge authoritative confirmation, or grant permissions. Routine diagnostics must not dump context, credentials, or full process arguments. Verify adversarial inputs, routing isolation, and diagnostics. |
| NFR-003 | Verify common status/retry behavior without real Codex or live trigger services, preserving the [MVP hub end-to-end suite using bundled model plugins](mvp.md#acceptance-criteria). Verify the Codex integration independently through its public action boundary and retain separate real-Codex delivery evidence. Mocks cannot establish actual Codex queue acceptance or duplicate prevention. |

## Constraints and dependencies

Use supported, non-experimental Codex interfaces. Do not require experimental
App Server operations, caller-supplied receiver idempotency keys or acceptance
lookup. This restriction concerns the interface relied on, not a requirement
that every compatible executable's release name be non-alpha. Design and delivery
must establish compatibility for the selected executable and shared server.

The existing Codex action's queue acknowledgement confirms queue acceptance
only. Reusing a hub delivery ID in message text does not make `codex queue`
idempotent. Correlate a target-specific ACK through its original action attempt;
no receiver echo of a hub notification ID is required. Design must establish
that correlation and trustworthy non-submission evidence. For the current CLI
boundary, once the queue process starts, its unconfirmed outcome is unknown and
is not retryable. A nonzero exit or timeout cannot establish non-submission.

Protocol shape, command names, state schema, storage layout, concurrency control,
retry timing, and retention bounds are design decisions. Design must specify
the pre-submission boundary and preserve its evidence across normal restart,
including conservative stopping when an attempt may have been issued before
its outcome was saved. No recovery of receiver acceptance after response loss
is required; inspection may continue to show unknown.
No additional database service or particular transport is selected here.

Trigger progress may advance when the action confirms acceptance under the
[existing trigger-progress contract](trigger-runtime-state.md#functional-requirements).
Tracking or retrying the resulting notification must not fabricate another
trigger event, reinterpret a
[PR-watch confirmation position](github-pr-watch.md#terms-and-workflow) marking
covered source changes as a delivery receipt, or claim recipient work
completion.

When combined with [individual cancellation](subscription-cancellation.md),
successful unsubscribe prevents further attempts, including scheduled retries.
An already-issued attempt may finish and its matching evidence may be recorded,
but it cannot restart the watch or retry scheduling. Removing registration or
trigger progress must not turn retained notifications into new or eligible work.
Notification retention remains distinct from source-progress retention. For
[PR-watch continuation](github-pr-watch.md#functional-requirements), events
carrying outstanding changes that did not fit an earlier notification remain
separate notifications, not retry attempts.

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
| AC-002 | FR-001, FR-004, FR-007 | Prove an attempt did not submit, allow retry without an operator request or agent polling, then let a subsequent attempt succeed. After acceptance, present stale retry work. Separately admit a distinct event with identical context. | Automatic retry preserves identity, context and destination, and produces one destination notification. No attempt overlaps another; acceptance stops retry. The distinct event remains independently deliverable. No manual retry, re-registration or trigger replay is used. | Retry-decision unit checks and selected assembled automatic-retry checks |
| AC-003 | FR-003–FR-006 | Accept a submission at conversation A but drop its response. Let scheduled retry work run. In one variant no further evidence arrives; in another deliver a delayed valid matching confirmation. Separately start submission but return a nonzero exit or timeout without confirmation. | No further attempt is issued after possible submission. A retains one notification in the accepted-response-loss variant. Unknown stays visible without confirmation; a matching late ACK may change it to queue accepted without reopening scheduling. Nonzero exit or timeout does not become proven unsubmitted. | Controlled assembled response-loss and launch-boundary checks; independent real-Codex response-loss, no-resubmission and destination-count evidence during delivery |
| AC-004 | FR-001, FR-003–FR-006 | Overlap two automatic retry callbacks with an in-progress submission to A; separately reuse its identity with changed context or destination B. | The hub issues no overlapping attempt; at most one submission reaches the destination boundary. B receives none, conflicts are rejected before submission, and stale errors cannot overwrite accepted evidence. | Concurrency/conflict unit checks and assembled checks where process ordering matters |
| AC-005 | FR-006, FR-007 | Repeatedly prove non-submission until the retry bound is exhausted. Separately produce unknown, permanent pre-submission failure, confirmed post-submission rejection, retry guidance, unsupported interfaces or expired evidence. | Exhausted pre-submission attempts show non-submission and a stopping reason. Unknown stops immediately and remains unknown; a post-submission rejection is non-acceptance but cannot be retried. Permanent errors stop. Guidance affects only otherwise eligible retries. Unsupported or expired evidence blocks, with no experimental fallback. | Controlled policy/clock/evidence unit checks and selected public-action failure checks |
| AC-006 | FR-005, FR-006, FR-008 | Persist a retryable proven-unsubmitted notification, stop normally, restart and allow retry. Separately persist unknown after response loss, an interrupted in-progress attempt, accepted, post-submission-rejected and exhausted notifications. Make required records missing, unreadable, incompatible or expired in separate variants. | Only retained eligible unsubmitted work resumes, with the same identity and remaining bounds. Unknown/in-progress recovery never authorizes another attempt; accepted, rejected and exhausted work is not retried. Insufficient records block visibly without making a new identity. No crash-durability claim is made. | Assembled normal-restart checks and storage-failure unit checks; real-Codex delivery verifies no resubmission of unknown work after normal restart |
| AC-007 | NFR-001–NFR-003 | Exercise status/retry with controlled external boundaries, inspect adversarial context and diagnostics, and run the existing bundled-model suite. | Core policy is service-neutral; triggers gain no Codex retry obligations; context cannot retarget or forge confirmation; diagnostics expose no secret/context dump. Service-free, standalone action, and real-Codex results remain separately identified. | Source/contract review, focused unit checks, selected assembled checks, existing model suite, and separate delivery evidence |
| AC-008 | FR-002, FR-006–FR-009, NFR-001 | A maintainer follows instructions to identify/inspect a notification, observe proven-unsubmitted automatic retry, stop/restart, and diagnose unknown, blocked or exhausted work using non-experimental interfaces. | Queue acceptance is sufficient confirmation. Instructions demonstrate retry only before submission, explain that unknown may remain unresolved and cannot be retried, and do not recommend republishing the same context as safe recovery. Retention limits, one-attempt compatibility and the absence of conversation-receipt/work-completion guarantees are explicit. | Maintainer walkthrough and operator/plugin-author documentation review with recorded environment and outcomes |

AC-006 also covers these variants when individual cancellation is available:
cancel a subscription with retained eligible work and a scheduled but unissued
retry, then execute the stale callback and restart normally. No new attempt may
begin after cancellation succeeds, and retained records must not recreate the
registration or retry eligibility. In a separate variant, cancel after an
attempt has been issued, then deliver its delayed matching acknowledgement.
Recording that evidence must not reactivate the watch or retry scheduling.
Use lifecycle unit checks and selected assembled cancellation/restart boundaries;
this does not add the cancellation feature to the notification implementation scope.

## Open questions and assumptions

| ID | Decision | Impact if unresolved | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Required confirmation stage | Determines evidence source, status stages, and completion criteria. | Responsible user; design owner applies the simplicity preference | Requirements approval | Resolved: the user prioritized implementation simplicity. Use existing target-specific queue acceptance, without conversation-receipt or work-completion tracking. |
| Q-002 | Retry initiation | Determines scheduling, controls, and acceptance cases. | Responsible user | Requirements approval | Resolved by the user: automatic retry only; no manual retry operation. |
| Q-003 | Experimental dependencies | Determines the supported Codex interface scope. | Responsible user | Requirements approval | Resolved by the user: do not depend on experimental Codex APIs. |
| Q-004 | Retry after uncertain acceptance | Determines duplicate risk and the recovery guarantee. | Responsible user | Requirements approval | Resolved by the user: prioritize duplicate prevention, retry only proven-unsubmitted attempts, and stop when acceptance is unknown. |

No unresolved user scope questions remain. The design owner must resolve actual
non-experimental Codex compatibility, queue-acceptance correlation, proof of non-submission,
automatic retry/retention bounds, migration, and verification feasibility before
design approval. Infeasible support must be raised for an upstream decision
rather than treating unknown as retryable or introducing experimental dependencies.

## Change impact

This revision preserves all local IDs. FR-002 and FR-004–009, UC-002/003 and
AC-002–006/008 change from recovery through receiver-protected resubmission to
retry only when every earlier attempt is proven unsubmitted, with unknown
stopping immediately.
FR-003's evidence correlation remains required without a receiver-side ID echo.
Q-003/004 record the new user decisions. FR-001 and NFR-001–003 retain their
identity, ownership, security and verification requirements.
On adoption, the concept qualifies the following earlier decisions:

| Concept and affected IDs | Revised boundary and downstream impact |
| --- | --- |
| [MVP](mvp.md): FR-006, NFR-004, AC-004, AC-005, AC-010 and retry/persistence exclusions | Replace one attempt/no retry and volatile-only notification state for safe-retry routes. Preserve existing behavior for other routes, role ownership, and service-free verification. |
| [Codex Queue](codex-queue.md): FR-005, FR-006, FR-008, AC-003, AC-004, AC-008 | Add inspectable queue evidence and same-identity retry only when no prior attempt submitted. Preserve conservative unknown after a started, unconfirmed queue process; use non-experimental interfaces. Registration identity and no automatic uncertain-registration retry remain unchanged. Idle status inspection creates no work. |
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

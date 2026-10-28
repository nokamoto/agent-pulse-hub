---
type: Design
title: Individual subscription cancellation
description: Fence one subscription, terminate its watch, and remove its retained registration and trigger progress without interrupting other subscriptions.
sources:
  - id: cancellation-requirements
    resource: requirements/subscription-cancellation.md
  - id: routing
    resource: design/mvp.md
  - id: persistence
    resource: design/state-persistence.md
  - id: runtime
    resource: design/trigger-runtime-state.md
  - id: notification-design
    resource: design/notification-delivery.md
---

# Individual subscription cancellation

## Context and scope

This design fulfills [individual cancellation](../requirements/subscription-cancellation.md).
The caller retains the UUID returned by registration and explicitly cancels that
one subscription. Success means that its watch has stopped, no new action
attempt can start, and its registration and trigger progress cannot restore.
Already-issued attempts can finish; cancellation does not recall their output.
Other subscriptions, including those sharing either plugin, remain usable.

This extension introduces no bulk removal, discovery command, conversation-based
selection, delivery retry, or crash-durability guarantee. It retains the
[local-control boundary](mvp.md#configuration-and-local-control). External event
text is data and cannot invoke administrative operations.

## Requirement coverage

All unqualified IDs refer to `requirements/subscription-cancellation`.

| Requirement | Acceptance | Design decision | Verification |
| --- | --- | --- | --- |
| FR-001 | AC-001, AC-006 | Explicit ID CLI/API and bundled skill workflow | SC-V01-SELECTIVE; client unit matrix; SC-D01 |
| FR-002, NFR-002 | AC-001, AC-004 | Per-subscription fence and selective input cancellation | SC-V01-SELECTIVE; concurrency units |
| FR-003 | AC-002, AC-004 | Durable intent, save drain, deletion before success | SC-V02-RESTART; SC-V03-FAILURE; lifecycle units |
| FR-004, FR-005 | AC-003 | Validation, visible partial failure, saved inactive removal | SC-V03-FAILURE; validation/storage units |
| FR-006 | AC-004 | Attempt-start serialization and rewait/stop fence | SC-V01-SELECTIVE; lifecycle units |
| FR-007 | AC-002 | Resource release and fresh UUID after removal | SC-V02-RESTART; identity units |
| FR-008 | AC-006 | Operation guide and agent skill contract | SC-D01; document review |
| NFR-001 | AC-005 | Explicit trusted local request, opaque event data | Adversarial event units and control-boundary inspection |

## Architecture and responsibilities

The daemon owns target lookup, admission and attempt-start fences, storage,
and lifecycle ordering. The input owns stopping one monitor and releasing its
watch-specific reservation. The action needs no cancellation message and
retains any already-issued attempt. Local clients validate an explicit ID;
they never infer it from a conversation, argument file, or event.

Use one per-subscription cancellation state alongside the existing lifecycle
coordinator. Internal type names are implementation choices. The states are
retained, cancelling, and removed. A cancelling route remains addressable for
an explicit retry and counts against the existing registration limit until
its registration is removed. Do not retain an unbounded cancelled-ID history.

## Local interface

Add the following command and exact request/response shapes using existing
loopback framing, version 1, limits, deadlines and exit/stdout rules:

```sh
agent-pulse-hub unsubscribe --subscription-id <registered-id> [--address <address>]
```

```json
{"version":1,"op":"unsubscribe","subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71"}
{"ok":true,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71"}
```

Require exactly one ID, unchanged from registration, matching the persisted
lowercase UUID syntax. Reject missing/duplicate flags, positional arguments,
extra fields, nonstrings and malformed IDs before mutation with the existing
local input failure or `invalid_request` result. Preserve strict JSON errors.

| Error code | Meaning and recovery |
| --- | --- |
| `unknown_subscription` | No live or saved registration, including an already-removed ID. No mutation. Check the retained ID; do not select another subscription automatically. |
| `unsubscribe_unsupported` | A still-running input lacks `cancel_watch_v1`. No fence or storage change. Upgrade the input and daemon while stopped, then use the same saved ID. |
| `unsubscribe_busy` | Cancellation for this ID is already executing. No second exchange; retry explicitly after its result. |
| `cancellation_rejected` | Input reports failure to stop/release the target. The target stays fenced and saved; correct the cause and retry the same ID. |
| `plugin_unavailable` | Cancellation cannot be confirmed because of timeout, send failure or unavailable cleanup. Target stays fenced. Retry when termination can be confirmed, or stop and restart to complete the saved intent. |
| `storage_error` | Intent commit or removal failed. Report the stage and target, keep the target fenced, and repair storage before explicit same-ID retry or normal restart. |

Errors retain the exact existing `ok:false,code,message` envelope and bounded
message. Include whether a durable intent exists and whether restart will
complete cancellation or may restore the saved watch. Do not expose arguments
or checkpoint payloads. Before durable intent commit, failure leaves the saved
registration intact: normal restart may restore it. After commit, startup must
complete deletion before readiness. Transport loss is an uncertain response,
not success; an explicit same-ID retry can return success or not-found, but
not-found alone cannot prove which caller removed the subscription.

## Cancellation ordering

1. Validate the request and find a live or saved registration. A saved route
   inactive because its output failed still needs input cancellation. Bypass
   the exchange only when the owning configured input's termination and cleanup
   are confirmed; mere Session closure is insufficient. For a running legacy
   input reject unsupported cancellation before mutation.
2. Under lifecycle serialization mark the target cancelling. Close its event,
   rewait, grant, checkpoint and new-attempt admission; remove unsent grants,
   preparations and queued deliveries. Equivalent registration now returns
   `unsubscribe_busy` while cancellation is incomplete. Unrelated requests
   continue normally. Expire pending rewait preparation correlations and finish
   their callers unsuccessfully; a late result cannot commit a generation.
3. Drain any target save already admitted to the filesystem, and prevent every
   subsequent save commit. A checkpoint callback must check the same fence at
   commit, not just at snapshot collection. Remove pending checkpoint requests
   and ignore late snapshots. This also applies to final shutdown snapshots.
4. Commit the durable intent described below. Failure returns `storage_error`;
   keep the live fence but do not pretend the saved route was removed.
   For notification-profile work, durably set every retained target retry state
   to `cancelled`, preserving acceptance and evidence, before persistent
   removals or successful unsubscribe. The
   [notification design](notification-delivery.md#source-outcomes-shutdown-and-cancellation)
   owns that save. Its failure returns `storage_error`, retains the fence and
   intent, and cannot report success or allow a retry callback to issue work.
5. Send selective cancellation and await positive completion, or establish the
   confirmed-input-termination condition from step 1. Negative/timeout results
   preserve the fence and intent for retry. Never kill a healthy shared input
   solely to cancel one subscription.
6. Under per-target storage serialization remove its runtime directory and
   registration file, in that order. Validate paths and types; do not follow
   symlinks. Missing entries are idempotent during recovery. A failure preserves
   the intent and returns `storage_error`. Remove its live route and release
   the core capacity/duplicate-key reservation only after both removals succeed.
7. Remove the intent and return the same-ID success. If only intent housekeeping
   fails, success is still valid: the watch, registration and progress are gone.
   Emit a bounded cleanup diagnostic; startup retries housekeeping. The orphan
   intent is neither a registration nor permission to reuse the ID.

The attempt-start boundary is issuance of the action request to the ordered
output sender. Fence checks and that issuance must serialize with step 2,
including work already dequeued or awaiting an output send. Work not yet issued
is discarded. Already-issued work keeps its original correlation and deadline
and may finish once. Cancellation itself neither retries it nor waits for its
30-second action deadline. Late outcomes must not recreate trigger progress.
For stateful inputs, serialize terminal notices already owed before cancel;
after positive cancellation remove the watch correlation and do not issue new
notices/checkpoints for it. Late action results remain action outcomes only.

Stop closes admission to new unsubscribe calls and waits for admitted cancellation
transactions to finish or fail before releasing storage exclusion. It must not
hold a lifecycle lock while waiting for plugin responses. Cancelling targets
are excluded from final checkpoint collection and save. If stop wins before an
unsubscribe is admitted, the client receives failure/transport uncertainty, not
a successful cancellation. Restart completion uses the committed intent.

Keep completed rewait operation receipts until their existing expiry: replaying
a receipt returns its historical result without another grant. New rewait
intents for a removed ID fail. Do not erase replay-safety records merely because
the monitor and its logical progress are removed.

## Persistent intent and restart

Add reserved directory `cancellations/` to the existing locked state tree.
Create it after exclusion in a valid older tree; absence means no intents.
Use existing owner-permission requests and strict JSON rules. Each regular
`<subscription_id>.json` contains exactly:

```json
{"version":1,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71","input_plugin":"manual"}
```

Version is integer token 1. Validate filename/UUID agreement and a nonempty
configured input name, matching the registration when present. Bound each read
to 128 KiB, matching registration admission bounds. Reject unknown entries,
symlinks, unsupported versions, malformed records or ownership mismatch. A new
intent uses exclusive staging creation, full write, close and rename to its
previously absent final path. Rename commits the operator's cancellation intent;
it does not yet mean successful cancellation. Reuse a valid existing intent on
explicit retry. No fsync, crash atomicity or new database is required.

At startup, after acquiring exclusion and before plugin launch or restoration,
collect and validate all intent records and their referenced file types. Load
and validate any existing notification store and durably cancel their target
notification retry states before finishing runtime/registration deletion and
intent cleanup. Only then launch plugins or restore registrations. A missing
registration also cancels retained work, including after intent housekeeping
has completed. Fail startup visibly if notification saving or
necessary deletion cannot complete; never restore a marked target with initial
or old progress. This is completion of an explicit operator request, not
automatic repair of an arbitrary corrupt registration. An intent without a
registration is housekeeping only and is removed without interpreting it as a
watch. Runtime payloads for cancelled targets need not be interpreted; inspect
types before deletion. Other registrations retain normal full validation.

The intent records no arguments, payload, notification history or completed
operation receipt. Successful deletion permits a later equivalent registration
with a fresh UUID and initial state; it cannot inherit this intent or old
progress. Input positive completion must release its equivalent-watch resource
reservation before success, independently of the core duplicate key.

## Specification documents and compatibility

This document owns cancellation ordering, local operation and intent storage.
[Plugin protocol](plugin-v1.md#optional-individual-watch-cancellation) owns the
language-neutral `cancel_watch_v1` schemas and deadlines derived here.
[Persistence](state-persistence.md), [runtime state](trigger-runtime-state.md),
[rewait](pr-watch-rewait.md) and [routing](mvp.md) reference these boundaries.
[Operations](windows-operations.md) and [Codex Queue](codex-queue.md) derive
operator and bundled skill instructions. No specification is deferred to
implementation; package names and helper signatures remain routine choices.

Keep application/framework version 2 and registration/runtime record formats.
The optional input capability composes with either runtime-state revision and
with valid rewait profiles. Inputs without it still register and monitor; live
cancellation fails explicitly. Ship all bundled inputs with the capability in
the cancellation implementation. Upgrade daemon and input artifacts together
while stopped; existing registrations use the same ID without original argument
files or re-registration. Old strict hosts may reject the new capability.

Preserve the entire stopped state tree before rollback. Older daemons reject
the new reserved directory; complete every intent using the new daemon first,
stop normally, and preserve/remove only the now-empty `cancellations/` directory
from the stopped working copy when deliberately reverting. Never discard a
pending intent to make an older daemon start: that could restore a cancelled
watch. Rollback cannot restore removed watches or recall outputs.

## Decisions, quality and dependencies

| Decision | Alternative | Reason and cost |
| --- | --- | --- |
| Optional per-watch cancellation | Kill the input process | Preserves shared watches; legacy inputs need an explicit upgrade. |
| Durable deletion intent | Delete runtime then registration without a marker | Prevents partial deletion from restoring initial progress; adds one small record and startup cleanup. |
| Fence before plugin exchange | Wait for plugin before closing admission | Prevents concurrent issuance and rewait from escaping cancellation; failures can leave the target stopped but saved. |
| Retain existing operation safety receipts | Delete all ID-associated data | Avoids replaying accepted operations; monitoring progress is still deleted. |

Use portable Go filesystem and process APIs; no OS-specific permission-failure
test is required. Retain existing local trust, payload secrecy, storage latency
and control-deadline limits. Keep plugin requests bounded by the existing
1,024 registrations and at most one cancellation request per target. Slow
storage can exceed the client deadline and yields an uncertain client result.

The [capacity requirements](../requirements/trigger-runtime-state.md)
may change the checkpoint envelope or reservation. Cancellation does not select
those changes: it drains the current per-target save and deletes the entire
owned runtime directory regardless of payload size. Reconcile any approved
replacement checkpoint contract before dependent implementation.

The separate [notification-delivery design](notification-delivery.md)
owns automatic retry, status and evidence retention. Cancellation closes
all future attempt-start paths, including its automatic scheduler; an
already-issued attempt and its delayed matching ACK remain distinct from a new
attempt. Never delete notification evidence or duplicate-prevention records as
trigger-progress cleanup, and never let an ACK reactivate a cancelled watch.
This design adds no independent retry schema, timing, status API or retention
policy. The notification design's durable cancellation save composes with this
intent transaction across normal restart; its verification inventory owns the
cross-feature cases. Notification records remain separate from deleted source
progress and cannot recreate a cancelled registration.

## Verification strategy

Prefer unit tests for strict validation, UUID/not-found matrices, capability
combinations, every filesystem error, idempotent cleanup, startup intent
validation, replay receipts, adversarial event text and interleaving decisions.
Mock storage and plugin ports for deterministic fence/commit races: hold a
save after snapshot, a dequeued attempt before send, rewait before commit, and
normal stop before final snapshot; cancellation must win or lose ordering
explicitly with no post-success resurrection. This costs less than duplicating
every interleaving with processes or wall-clock sleeps.

Three assembled cases cover the remaining command/process/stream/file defects.
They are static Pending in `cmd/agent-pulse-hub/subscription_cancellation_integration_test.go`,
registered in `cmd/agent-pulse-hub/testdata/subscription-cancellation-cases.json`.
Use the existing tagged suite and fixtures under that command's `testdata/`;
implementation may complete case-owned fixture scripts there. Do not change
shared harnesses, dependencies, CI or product code in this design phase.

| Case | Inputs and action | Required observations and method |
| --- | --- | --- |
| SC-V01-SELECTIVE | Register A/B on one controlled input/output. Hold A1 after the action journals receipt, admit queued A2, then unsubscribe A. Release A1, inject late A3 and B1. | CLI returns A, the input journals one-watch stop, A1 may finish, A2/A3 never reach action, B1 does, processes stay live. Existing action gate/source-command fixture provides ordering; real daemon/client/routing and streams are exercised. |
| SC-V02-RESTART | Give A/B distinct runtime progress; cancel A, normal stop and restart with the same state. Register A's exact old arguments anew. | A registration/runtime absent, only B restored with original ID/progress, fresh A ID and null initial state, same resource accepted. Reuse runtime input fixture, real model action, state helpers and normal process lifecycle. |
| SC-V03-FAILURE | Reject/withhold input cancellation, explicitly retry; separately obstruct target runtime deletion after intent commit, stop and restart after removing the obstruction; cancel a saved inactive route after confirmed input termination. | Failures identify stage/cause, never success, B is preserved; retained intent prevents A restoration, repaired cleanup completes before readiness; inactive saved A is removable. Use a target-owned nonregular entry and a cancel-result gate, not permission changes. Error matrices and timeout duration use cheaper units. |

Use source/journal barriers and bounded polling, not assumed sleeps. The target
runtime obstruction is a test-controlled nonregular entry rejected by deletion
validation; remove it before restart so the marker-driven cleanup can finish.
Only external plugins/source behavior are substituted; real control, lifecycle,
filesystem adapter and configured process cleanup remain under test. A fixture
ACK proves only fixture compliance, not a real service's behavior.

Budget the scoped suite within the existing two-minute ceiling: warmed build
20 seconds, V01 15, V02 20, V03 25, cleanup 5, reserve 35. Peak six children;
one temporary state tree per scenario, bounded journals, gate release and
awaited process exits on failure. Reuse existing Ubuntu/Python 3.12 CI setup;
record local OS and cold-cache time separately. No new service credential,
runtime pin, runner or infrastructure prerequisite is introduced.

```sh
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/subscription-cancellation-cases.json <new-report-directory>
```

Design verification records tagged compilation, Ginkgo registration, existing
process/stream and filesystem feasibility probes separately from product
acceptance. Pending is not acceptance and scoped execution must fail until all
three cases are implemented. Keep existing MVP and runtime-state coverage;
these cases do not replace them or establish actual Codex behavior.

### Delivery quality check

SC-D01 covers FR-001/FR-008 and AC-006. The delivery maintainer builds and follows
the finalized operator guide and bundled Codex skill with a real compatible
daemon/input/action/Codex installation. Retain registration A's returned ID,
register unrelated B, explicitly unsubscribe A through the skill's direct hub
command, publish distinct A/B stimuli, and observe continued B delivery and no
new A attempt. Stop normally, wait for exits, restart and verify A absent and B
retained. Record revision, environment, commands, redacted IDs/results, outputs,
restart evidence and failure/issued-output explanation. The skill must not
derive the ID from conversation identity or automatically retry uncertainty.
This check runs during delivery, separately from service-free implementation CI.

## Open questions and risks

The cancellation decisions above define the selective-removal contract.
No capacity design is assumed approved. Its eventual shared-contract
integration must be reviewed before combined implementation; each design owner
owns reconciliation of its changed contract. Slow storage and legacy input
compatibility remain explicit operating limits, not weaker success conditions.

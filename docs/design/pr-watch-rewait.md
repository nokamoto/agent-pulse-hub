---
type: Design
title: Continuous notification on a retained trigger registration
description: Automatically continue bounded notifications on one route while preserving coverage, terminal outcomes and cancellation fences.
sources:
  - id: pr-watch-requirements
    resource: requirements/github-pr-watch.md
  - id: notification-requirements
    resource: requirements/notification-delivery.md
  - id: cancellation-requirements
    resource: requirements/subscription-cancellation.md
  - id: runtime-design
    resource: design/trigger-runtime-state.md
---

# Continuous notification on a retained trigger registration

## Purpose and authority

This stable path replaces the retired repeated-wait contract. It implements
[PR-watch](../requirements/github-pr-watch.md) FR-006/FR-010/FR-012,
AC-004/AC-008/AC-012/AC-014 and resolves Q-004's common-control decisions.
It owns automatic admission, public compatibility and the restart safety fence.
[Capacity](github-pr-watch-capacity.md) owns coverage and migration;
[signals](github-pr-watch-signals.md) owns category-specific comparison.
[Routing](mvp.md), [runtime state](trigger-runtime-state.md),
[notification delivery](notification-delivery.md) and
[cancellation](subscription-cancellation.md) retain their respective authority.
These specifications serve implementers and verification authors; no additional
specification is deferred to implementation.

The daemon owns service-neutral admission and the fixed destination. The input
owns observation, journal selection and opaque progress. The action alone
reports delivery acceptance. None of these facts proves recipient assessment.

## Continuous admission

One registration has one monitor and at most one emitted event awaiting its
terminal notice. Observation continues while delivery or recipient work runs.
The daemon grants initial activation after the existing registration/startup
commit barrier. A valid event consumes that grant; after serializing its terminal
notice the daemon automatically grants the next event. No recipient command,
position, assessment acknowledgement, registration or restart participates.
`accepted`, `failed`, `unknown` and `discarded` all release this admission slot
while the route remains available. Only `accepted` advances accepted evidence.
`discarded` proves no action attempt for that valid event and restores its
provisional range to the unselected suffix; it does not claim delivery.

The input selects the oldest unselected contiguous journal prefix, at most
eight changes and 8,192 decoded context bytes. It marks that range selected
provisionally when emitting, before another checkpoint can snapshot it. A later grant selects
only the remaining suffix or newly observed changes. It never reselects a failed
or unknown range as a new event. Retrying an admitted notification belongs
exclusively to the core policy, under its original notification ID.
Intermediate retryable unsubmitted outcomes are not terminal and issue no grant.
A terminal failure does not disable later distinct changes on a healthy route.

### Scheduling and ordering

Serialize event admission, notices, grants, checkpoints, cancellation and stop
per registration. Each terminal notice queues at most one fresh grant after it
on the same ordered host stream. Bound pending grants by the existing 1,024
registrations, with one pending/in-flight grant per watch. Use the existing
ten-second per-input dispatch deadline and unavailability handling. Schedule
ready watches fairly; never build an unbounded grant or notification queue.
Existing admitted-work capacity (128) and the single delivery worker still
apply. A valid event rejected before action admission receives `discarded`. Restore
its provisional selection and delay the next automatic grant five seconds,
using one coalesced timer per watch. This avoids capacity/storage-error hot loops
without requiring new source activity. It retries no admitted notification.

On each matching grant the input first checks retained eligible work, even
during retrieval failure and without another GitHub observation. With none,
keep that grant idle until a complete round supplies changes. Repeated identical
grants are no-ops. New complete rounds group changes available when the next
context is prepared; every distinct observed transition retains its sequence.
No intentional delay is added between bounded prefixes. Polling/backoff remains
independent and never overlaps source rounds. This is no latency promise.

A periodic checkpoint may precede or follow a queued grant, but cannot split
the input's selected-range mutation from its emitted event. Snapshots are
immutable and include earlier notices and already selected ranges. A checkpoint
in progress need not delay observation or automatic notification.
Normal stop closes automatic grants first and retains event/notice handling
until quiescence. A continuous input receiving `checkpoint(quiesce:true)` stops
emitting and cancels acquisition immediately, then waits for any emitted event's
terminal notice before sending its immutable final snapshot. Keep receiving
notices while waiting; the core discards late fresh events after admission
closes and sends their notices, even during this final phase. A discarded
unadmitted suffix is therefore restored before final saving. Preserve the common
ten-second final protocol budget; unresolved correlation fails the snapshot
and leaves the active-run marker. A late grant cannot restart a quiesced watch.
Unsubscribe checks the same fence at final dispatch and attempt issuance:
remove unsent grants/queued events, reject late events, cancel retained retries
and delete runtime progress before success. Previously issued action work may
finish once; late evidence cannot restore a watch or issue another grant.

## Public interface and compatibility

Remove the CLI `rewait` command, its flags/help and local-control operation.
An old invocation fails usage validation before connecting; an old wire request
returns existing `invalid_request`, with no route, coverage or activation mutation.
Do not retain a no-op success alias. Remove operation receipts, position-file
parsing and resume preparation. Registration identity, equivalent `register`,
`unsubscribe`, inspection, framing, addresses, deadlines and error envelopes
are otherwise unchanged.

Replace input capability `rewait_v1` with `continuous_watch_v1`, allowed only
with `runtime_state_v2`. It may compose with `runtime_state_stream_v1` and
`cancel_watch_v1`; the bundled PR watcher requires all four. Reject duplicate
or unknown capabilities, runtime-state v1 combinations and any `rewait_v1`
advertisement before activation. Older strict hosts reject the new capability:
upgrade daemon and input together while stopped. Nonparticipating inputs keep
their existing runtime-state and multi-event behavior. Output capabilities
and application protocol version 2 are unchanged.

Only these conditional fields extend the public runtime-state messages:

| Direction / type | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to input / `activate_watch` | `subscription_id`, `generation`: nonempty strings | Initial or automatic single-event grant, using a fresh daemon UUID. |
| Input to host / `state_event` | Existing `subscription_id`, `event_id`, `context`; `generation`: nonempty string | Attribute one selected range to the current grant. |

Every message includes existing `version:2` and `type`, strict encoding and
the 65,536-byte bound. Event IDs strictly increase per watch within a Session;
they are not coverage positions. `resume_watch` and `resume_result` are removed
and become protocol errors. The input applies a fresh grant only initially or
after its matching terminal notice; a fresh grant before that notice is a
protocol error. A grant never resets its selection cursor.

The daemon verifies ownership, fresh event ID, current generation and an
unconsumed grant atomically with admission. Valid fresh stale-generation or
consumed-grant events receive one diagnosed `discarded` notice, without
creating a grant or advancing acceptance. Such invalid events cannot release
the actual outstanding event. Repeated/malformed IDs keep the no-second-notice
rule. Stream restore, watch acceptance, notices and checkpoints keep their
exact schemas. Generations and correlations are Session-only.

## Progress and restart safety

Keep three plugin facts separate: latest complete observation, highest contiguous
range selected for notification, and accepted delivery evidence. Coverage means
represented changes; neither selection nor acceptance says omitted text was
read or the context assessed. Successful normal save retains selection and the
unselected suffix. Restore continues after selection even if an earlier result
was failed or unknown. Late results or resumed retries update the ledger only,
never a new Session's cursor or grant.
[Capacity migration](github-pr-watch-capacity.md#legacy-migration) defines
old-state interpretation without inventing acceptance.

A stale checkpoint must not cause known unknown work to receive a new ID.
Use one core-owned **active-run marker** per continuous watch. After dormant
watch acceptance and before the registration/startup commit barrier, exclusively
create and close
`runtime/<subscription_id>/continuous-active.json` containing exactly
`version:1`, `subscription_id` and `session_id` (both UUID strings). Bound it
to 1,024 bytes; validate identity, strict JSON, regular-file type and ownership.
The runtime directory may contain this reserved file beside generations;
it is not a generation. A marker-only directory is valid only in its owning
live run, never as restorable initial progress.

Marker creation failure prevents activation through existing startup/registration
failure handling. Equivalent register does not recreate it. Failed pre-commit
cleanup removes only this run's newly created marker and any newly created empty
runtime directory, since no grant was dispatched. Once created it remains through periodic saves, so save
failure still permits healthy live monitoring and notifications with the common
continuity warning. After final quiescence, earlier terminal notices, a successful
final runtime commit and notification-store saves, remove it under per-watch
storage serialization before releasing exclusion. Failure leaves it present
and reports unsuccessful continuity. Cancellation deletes it with the entire
runtime directory. Before activation, a newly created marker may be removed
only when no grant was dispatched.

At startup complete cancellation intents first. A surviving marker on any
remaining registration blocks restoration before grants or retained retry
issuance, with `continuous_progress_unverified`, registration/path and bounded
reason. Never delete it automatically or fall back to an older generation.
This conservative rule also covers interrupted runs; it adds no crash-recovery
guarantee. Operators preserve the stopped tree and repair from a verified clean
backup, or follow offline deletion/recreation accepting continuity loss.
Removing the marker alone or republishing unknown context is unsupported.
Successful normal stop/restart needs no re-registration or intervention.
A clean backup must include selection covering all earlier possibly submitted
work; an older checkpoint alone does not establish that fact.
One marker per run avoids synchronous progress saves on every event and keeps
service coverage opaque to the core.

## Verification and decisions

The parent [verification plan](github-pr-watch.md#verification-strategy) governs
stable GPW/GPWS cases, inventories, harnesses and delivery checks.
GPW-I02 covers consecutive automatic notifications, failed/unknown outcomes,
retired requests and cancellation dispatch fencing. GPW-I03 covers clean restart,
save warning and marker-blocked restart. GPW-I05 and GPWS-I02 cover frozen-source
continuation, migration and restart between accepted prefix and suffix.
Unit tests cover capability/message shapes, grant duplication/order, bounded
fair dispatch, full-capacity rejection, terminal-notice races, marker
write/close/remove failures and immutable snapshots. Existing notification
and cancellation suites retain their distinct policy coverage.

| Decision | Alternative | Rationale and tradeoff |
| --- | --- | --- |
| Automatically replenish single-event grants | Unbounded emission or recipient rearming | Reuses admission fences and bounds outstanding work; terminal delivery can delay context, recipient work cannot. |
| Selection independent of acceptance | Resume from accepted cursor | Prevents failed/unknown prefix replay; failed work has no eventual-delivery promise. |
| New capability and versioned state | Reinterpret old capability/decoder | Makes mixed-version failure explicit and preserves unrelated plugins. |
| Active-run marker | Per-event durable transaction or stale-checkpoint replay | Constant per-run cost and continued live operation on save failure; incomplete final saves require deliberate recovery. |

No unresolved implementation decision remains. Human review evaluates these
compatibility and availability tradeoffs. Live GitHub/GHES and receiving-agent
evidence belongs to delivery; design approval and merge precede implementation.

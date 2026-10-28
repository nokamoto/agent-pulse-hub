---
type: Design
title: Repeated waiting on a retained trigger registration
description: Rearm one continuing registration through daemon-owned notification generations without changing its arguments or destination.
sources:
  - id: pr-watch-requirements
    resource: requirements/github-pr-watch.md
  - id: mvp-requirements
    resource: requirements/mvp.md
  - id: runtime-state-requirements
    resource: requirements/trigger-runtime-state.md
  - id: subscription-cancellation-requirements
    resource: requirements/subscription-cancellation.md
  - id: notification-design
    resource: design/notification-delivery.md
---

# Repeated waiting on a retained trigger registration

## Purpose and authority

This design supplies the common control needed by
[GitHub PR watch](../requirements/github-pr-watch.md) FR-006, FR-010,
AC-004 and AC-008. It governs notification lifetime, explicit rewaiting,
admission fencing and compatibility. The
[routing design](mvp.md) continues to own registration identity, destinations
and local control. The [public plugin specification](plugin-v1.md) incorporates
the optional message profile below. The
[runtime-state design](trigger-runtime-state.md) continues to own checkpoint
storage and shutdown. A plugin-specific design owns confirmation-position
meaning, bounded history, observation and notification content.

The daemon owns service-independent operations and notification admission,
preserving MVP C-002. Plugins perform requested monitoring and position
comparison. A rewait never cancels a subscription, deletes a registration,
replaces a route, changes its destination, or transfers another registration's
saved state. Explicit deletion instead follows
[subscription cancellation](subscription-cancellation.md), including its
coordination with this profile. No delivery retries or recipient-processing
guarantee are introduced by rewait. The independent
[notification-delivery design](notification-delivery.md) owns retained evidence
and safe retry for participating outputs; it does not rearm source generations
or reinterpret confirmation positions as receipts.

## Chosen lifetime

A participating registration retains one continuing monitor. The daemon grants
one **notification generation** at initial activation, at each successful
explicit rewait, and at activation after daemon restart. One valid admitted
event consumes that generation, regardless of its eventual delivery outcome.
The monitor continues acquiring data and retaining eligible differences after
consumption, but cannot admit another notification until rearmed. A generation
is a daemon-owned admission token, not an assessment position or GitHub cursor.

The observable rule is **one admitted notification per explicit wait or daemon
run**. An equivalent `register` request still returns the existing subscription
ID and does not rearm it. Users normally receive context, assess or perform
work, then explicitly rewait with the position they assessed. Restart rearms
the retained monitor automatically, using saved delivery progress to suppress
previously confirmed notifications. This restart behavior is intentional and
must be explained in plugin usage materials.

The plugin keeps these facts separate:

- Its latest complete observation and bounded differences still eligible for
  an explicitly supplied position.
- Confirmed delivery progress used to prevent restart-only repetition.
- The position supplied by the caller for the current explicit wait.

Delivery acceptance can advance the second fact; it never proves assessment
or changes the third. An earlier supplied confirmation position can therefore
include changes observed while the agent worked even if a separate delivery
had been accepted. The
plugin must reject positions outside its supported retention or compatibility
boundary instead of silently advancing them. A consumed generation is not
restored: each new Session gets a fresh generation and retains logical service
progress through the existing checkpoint contract.

## Local client contract

Use the existing loopback control connection, strict version-1 JSON framing,
65,536-byte record bound, 25-second read/write deadlines and stdout/exit rules
from [MVP local control](mvp.md#commands). Add this exact request shape:

```json
{"version":1,"op":"rewait","subscription_id":"<registered ID>","operation_id":"<unix-ms>:<32 lowercase hex digits>","confirmation_position":"<opaque position>"}
```

Both IDs and the position are nonempty strings. The position is at most 4,096
decoded UTF-8 bytes. The daemon checks shape and bounds, never interprets the
position or derives a destination from it. Success is exactly:

```json
{"ok":true,"subscription_id":"<same registered ID>","operation_id":"<same operation ID>"}
```

Add the command:

```sh
agent-pulse-hub rewait --subscription-id <id> --operation-id <id> --confirmation-position-file <absolute-file> [--address <address>]
```

The position file contains exactly one strict UTF-8 JSON string, without BOM,
not a watch-argument object. Apply the existing absolute regular-file and
flag rules. The client validates the operation-ID syntax and position bound
before connecting. It never retries automatically or substitutes `register`.
It does not need the original argument files, a new destination, or agent APIs.

Failures use the existing exact error object and 1,024-byte message bound.
Existing JSON/version/structure, resource and unavailable-plugin codes apply.
Additional codes are:

| Code | Meaning and effect |
| --- | --- |
| `unknown_subscription` | No retained active route with this ID; no state is borrowed from another route. |
| `rewait_unsupported` | The input did not advertise this profile; its existing operation is unchanged. |
| `wait_active` | The current generation has not been consumed; do not supersede an active admission token. |
| `rewait_busy` | A previous admitted event has no terminal outcome, its notice has not been serialized, a checkpoint is outstanding, or another rewait is pending. No preparation was sent. |
| `position_rejected` | The trigger rejected the supplied position, including foreign, malformed, incompatible or expired positions. No reset or activation occurs. |
| `operation_expired` | The operation ID falls outside the Session/time window below. No preparation is sent. |
| `operation_conflict` | A retained operation ID was reused for a different subscription or position. No preparation is sent. |

Use actionable bounded messages without positions, context bodies or secrets.
The public operation cannot change waiting context: it remains the immutable
registration's supplied context. A different context requires a separately
selected registration and does not supersede this registration.

### Explicit retry and bounded receipts

An operation ID contains a canonical nonnegative Unix-millisecond integer,
colon, and 128 random bits written as 32 lowercase hexadecimal digits. It is
caller-generated and identifies one explicit intent, not a plugin event.
The time must be strictly later than this daemon Session's start millisecond,
no later than the current millisecond, and no more than 300,000 milliseconds
old. Clock changes can reject an operation visibly; never infer a new ID.

Keep at most 1,024 operation receipts across the daemon, retaining each through
its five-minute validity window. A receipt records the ID, subscription ID,
SHA-256 of the exact decoded position bytes, and terminal response. Reserve a
slot before preparation. If none is available, reject with `resource_exhausted`
without changing the watch. Expire receipts only when their IDs can no longer
pass time validation. A duplicate pending request shares its eventual response;
an identical completed request returns its receipt without a second generation
or plugin operation. Conflicting reuse fails. Pre-preparation validation/busy
errors need no receipt and can be retried explicitly with the same ID.

Receipts are volatile. After restart, prior operation IDs fail the Session-start
test; they must not silently execute again. This supplies bounded explicit
same-Session retry without another persistent format. A lost response can be
resolved by an explicit same-ID request within this window. Outside the window,
the result remains uncertain; a new ID is a deliberate new wait, subject to
`wait_active`/`rewait_busy`, rather than recovery of an old response. No automatic
retry or exactly-once guarantee across failures is claimed.

Unsubscribe does not prematurely expire these safety receipts. Retain terminal
responses through their existing validity window even after removal of the
associated route, and resolve an identical existing receipt before requiring
a live route. A replay of an earlier successful rewait reports that historical
commit only; it issues no grant and does not assert that the subscription is
still active. A new operation ID for a cancelled subscription returns
`unknown_subscription`. Expiry and conflicting reuse retain their normal rules.

## Optional public input profile

Add capability `rewait_v1`, permitted only alongside `runtime_state_v2`:

```json
{"version":2,"type":"ready","role":"input","capabilities":["runtime_state_v2","rewait_v1"]}
```

Array order is immaterial. The optional `runtime_state_stream_v1` follows
[stream capability rules](runtime-state-stream.md#capability-and-compatibility).
The independent `cancel_watch_v1` may also accompany this profile, including
its streamed-state combination, under [subscription cancellation](subscription-cancellation.md).
Duplicate, unknown or otherwise invalid combinations remain malformed.
Each optional profile changes only its own conditional messages; inputs without
it retain the corresponding existing message shapes. Rewait does not change
output readiness, which follows the independent profiles in the
[public plugin specification](plugin-v1.md). An older strict host may reject the new capability;
there is no fallback that weakens this profile's lifetime.

For this capability only, replace these additional-field schemas. Every
message retains `version: 2`, existing ownership/encoding rules and the
65,536-byte application bound:

| Direction / type | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to input / `activate_watch` | `subscription_id`, `generation`: nonempty strings | Grant initial, restored or prepared activation for this generation. |
| Input to host / `state_event` | Existing `subscription_id`, `event_id`, `context`; `generation`: nonempty string | Attribute an event to one granted generation. Event IDs remain strictly increasing across generations in this Session. |
| Host to input / `resume_watch` | `request_id`, `subscription_id`, `generation`, `confirmation_position`: nonempty strings | Prepare the supplied position for a future generation without activating it. |
| Input to host / `resume_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: bounded nonempty string only when false | Accept or reject preparation. A positive result does not grant event emission. |

The daemon generates a fresh opaque UUID generation for each grant, and a fresh
request ID for each preparation. Plugins never manufacture these tokens.
Rewait itself leaves `state_watch`, `watch_result`, delivery notices and
checkpoint shapes unchanged. An input additionally selecting
`runtime_state_stream_v1` uses that profile's state-transfer replacements;
its delivery notices and `watch_result` stay unchanged. Initial/restored
`activate_watch` uses a fresh generation after the
existing complete registration/startup commit barrier. Rewait grants follow
the ordering below. Only one generation is current; no generation history or
second poller is required in the daemon.

## Rewait ordering and failure atomicity

Serialize this state machine with admission, terminal delivery outcomes,
checkpoints, individual cancellation, stop and plugin unavailability for the
affected registration:

1. Validate the local request, capability and retained route. Resolve an existing
   receipt before checking `wait_active`. Require a consumed generation and no
   admitted event without a terminal outcome. Intermediate retryable unsubmitted
   results in notification mode leave that outstanding event pending and keep
   rewait busy until its notification receives a terminal outcome. Serialize that event's terminal
   notice before preparation. Do not withdraw queued/in-flight deliveries to
   make a rewait possible; return `rewait_busy` instead.
2. Reserve the operation receipt and a new generation. The old generation is
   already consumed and cannot admit another event. Defer periodic checkpoints
   for this watch while preparation is pending. Send `resume_watch` after all
   earlier notices. Other registrations remain usable.
3. The input validates the explicit position against the immutable watch and
   its supported retained observations. It stages the new comparison position
   without replacing its current logical progress, starting another monitor,
   or emitting an event. Finish sending `resume_result` before applying a grant.
4. On a negative result, discard the staged position, retain the consumed old
   generation/progress and record `position_rejected`. On an accepted result,
   recheck stop, the cancellation fence and both plugins under lifecycle
   serialization, install the new admission token, record success in the receipt,
   then dispatch exactly one
   matching `activate_watch`. This installation is the rewait commit point.
5. On its matching grant the input applies the staged position, discards the
   preparation and reconciles already retained/observable changes without
   requiring another GitHub stimulus. It resumes event eligibility through the
   same monitor. Repeated identical grants are no-ops. Stale/foreign grants or
   events are diagnosed and cannot create a generation or reset progress.

Use one ten-second preparation deadline, including send and result. A negative
result is an ordinary rejection. A timeout, failed send or lost Session makes
that input unavailable under existing cleanup: otherwise a late prepared result
could conflict with a later operation. Retain the registration and old saved
progress. Record an unavailable/uncertain response; never claim rollback of an
unconfirmed plugin exchange. Stop before commit cancels the preparation and
permits no grant. After commit, grant-send failure follows the existing
post-commit input-unavailability rule and keeps the receipt's committed result;
client success promises installation, not successful source polling or delivery.

If cancellation wins before a preparation commits, finish that pending receipt
with an identifiable failure, expire its request correlation and issue no grant.
If rewait commits first, cancellation still closes the newly installed token
and prevents any not-yet-issued grant from restarting the watch. The plugin's
selective cancellation discards staged positions and permanently terminates
that watch; late resume results or grants cannot recreate it. A completed
receipt keeps its committed response under the replay rule above. Cancellation
does not discard other registrations' preparations or receipts.

Process `state_event` under the same admission ordering. Verify ownership,
fresh event ID, current generation and an armed token before admitting. Consume
the token atomically with admission, preserving one notification and the
registered recipient. A valid fresh stale-generation or consumed-token event
is discarded, diagnosed and receives the existing terminal `discarded` notice;
it neither opens a token nor advances confirmed delivery progress. Invalid or
repeated event IDs retain the existing no-second-notice rule. The input emits
at most one event for a grant, then keeps monitoring without event emission.
No notification queue or event replay is added by this profile.

## Checkpoints, restoration and operations

Use the existing opaque registration-bound runtime envelope and save schedule.
For an input also advertising `runtime_state_stream_v1`, use the optional
[ordered-object stream](runtime-state-stream.md) instead of a single envelope;
the save schedule, grant ordering and logical-state ownership remain unchanged.
No registration file is rewritten by rewait. A checkpoint contains sufficient
plugin-owned observation, retention and confirmed-delivery progress for its
declared continuity, plus any explicit comparison position whose meaning must
survive. Generations, pending preparations and operation receipts are Session
control and are not restored. A checkpoint never applies a staged ungranted
position. Checkpoints cannot overtake a pending preparation/grant; process
the grant send before the next checkpoint request on the same host stream.
The exception is final quiescence after stop cancels an uncommitted preparation:
expire its request correlation, serialize the quiescing checkpoint after any
already sent preparation, and ignore its late result. On quiescence the input
discards any staged ungranted position and snapshots only committed logical
progress. No grant or preparation can restart a quiesced watch.

After restoration the first new generation uses confirmed-delivery progress
to avoid repeating successfully saved accepted notifications. A fresh change
is eligible without client re-registration or rewait. An explicit older
position still means assessment through that supplied position; restoration
must not replace it with the
delivery cursor. Invalid restored state blocks startup under the existing
rule. Save failure reports the existing continuity warning and does not stop
monitoring; fresh notification requires an armed generation, including explicit
rewait after a prior generation was consumed. Rewait failure recovery itself
adds neither automatic delivery retries nor unfinished-notification guarantees.
Notification-profile retries resumed after restart update only their retained
ledger; they send no old Session notice or successful-progress checkpoint
update. The new Session still receives its ordinary fresh activation generation
and source deduplication behavior. Distinct new source events remain independent
notifications, not retries of the retained record.

Normal stop closes rewait admission along with registration/event admission,
finishes any already serialized commit decision, sends no further grants after
stop wins ordering, then follows the existing terminal-notice/final-checkpoint
shutdown. All consumed and armed generation tokens expire with the Session.
Offline reset/deletion remains an explicit operator procedure. A recreated or
unrelated registration receives no old progress; a position is useful only
when explicitly supplied and accepted by the plugin's position contract.
Live ID-based unsubscribe follows the cancellation design. It removes this
watch's staged and committed progress and notification token, while preserving
the bounded historical receipts needed to prevent replay. Normal stop excludes
the fenced registration from grants, checkpoint saves and restoration.
Cancellation also fences notification retry scheduling and durably cancels
retained work before removal under its governing design. A delayed ACK may
advance retained evidence only; it cannot resurrect a generation or watch.

Positions and checkpoints are trusted local opaque data, not routing authority.
Keep both out of diagnostics. External GitHub text cannot select a subscription,
operation, destination or permission. Existing local-user and trusted-plugin
boundaries remain unchanged.

## Verification and alternatives

The plugin-specific governed verification mapping supplies the command-level
cases for this profile. Combine a notification with position P1, a later
comment C2 and head H2 observed while the agent works, same-ID
rewait, no-further-stimulus delivery and restart/fresh-change observations in
the selected repeated-wait/restart scenarios. Observe one monitor, unchanged
registration/destination, generation fencing and no duplicate event admission.
Use unit tests for capability combinations, ID/time/receipt bounds, operation
reuse, busy/terminal ordering, malformed/foreign/stale generations, preparation
rejection/timeout, stop races, exact message limits and save-failure semantics.
Independent runtime-state and model cases remain required; this profile does
not establish real GitHub or receiving-agent acceptance.

One continuing registration avoids per-wait cancellation, state transfer and an
accumulation of superseded retained routes. Session-scoped receipts avoid a
new core persistence format. Continuous multi-notification generations would
need a more complex reconciliation policy for notifications already delivered
while the agent works. Per-wait registration replacement would require explicit
supersession/deletion and state association decisions. The selected simpler
lifetime instead makes its one-notification and restart-rearm behavior visible.

No shared CI, test harness, product implementation, new storage service or
delivery procedure is introduced by this design. Human approval of this design
and its plugin-specific verification precedes dependent implementation.

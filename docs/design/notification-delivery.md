---
type: Design
title: Notification evidence and conservative automatic retry
description: Persist notification evidence, retry only proven unsubmitted attempts, and stop unknown delivery without experimental Codex APIs.
sources:
  - id: requirements
    resource: requirements/notification-delivery.md
  - id: cancellation
    resource: design/subscription-cancellation.md
  - id: plugin
    resource: design/plugin-v1.md
  - id: codex
    resource: design/codex-queue.md
  - id: runtime
    resource: design/trigger-runtime-state.md
---

# Notification evidence and conservative automatic retry

## Context and authority

This design fulfills [notification delivery](../requirements/notification-delivery.md)
FR-001–009, NFR-001–003 and AC-001–008. A notification is one admitted event
bound to its original route and context; an attempt is one effort to submit it.
Queue acceptance confirms delivery, not conversation receipt or work completion.
Retry requires every earlier attempt to be proven unsubmitted. Once submission
may have occurred, stop. Unknown can remain unresolved indefinitely.

Use the existing non-experimental Codex queue command and target ACK. No receiver
deduplication, acceptance lookup, manual retry, registration retry, trigger replay,
automatic plugin restart or downstream exactly-once execution is introduced.
Separate admitted events remain distinct even with identical context. Legacy
routes retain their one-attempt, volatile-work contract.

The core owns identity, evidence, scheduling, status and storage; outputs translate
service boundaries into structured evidence. Inputs retain source progress, with
no Codex receipt or attempt-storage responsibility. This document owns policy,
local operations, records and verification. [Plugin v2](plugin-v1.md) owns the
language-neutral wire profile, [Codex Queue](codex-queue.md) its process/ACK mapping,
[MVP](mvp.md) routing, [persistence](state-persistence.md) state-root exclusion,
[runtime coordination](trigger-runtime-state.md) source outcomes,
[cancellation](subscription-cancellation.md) removal, and
[operations](windows-operations.md) derivative procedures. Their affected contracts
are revised together; no specification is deferred to implementation. Internal
Go package names, indexes and helper signatures remain implementation choices.

## Participation and evidence

An output explicitly advertises `notification_delivery_v1` as defined in the
wire specification. Bundled Codex Queue and model-output select it only through
the optional valueless `--notification-delivery` flag, at most once; default mode
stays legacy. The core uses readiness, never interprets plugin arguments.
Upgrade host and output together while stopped: old strict hosts may reject the
new capability. Retained notifications cannot silently downgrade to a legacy
output after restart.

In `notification_deliver`, `delivery_id` is a canonical non-nil lowercase
notification UUID; `request_id` is a fresh attempt UUID. Capture configured names,
subscription, destination arguments and exact decoded context at admission.
Reject conflicting reuse before issuance. The existing frame/context limits and
strict JSON/exact-number rules still apply. Do not derive routing from event text.

| Output result | Evidence | Core disposition |
| --- | --- | --- |
| `unsubmitted` | This attempt did not cross the destination-submission boundary. | Retry only with `retryable:true`, all predecessors unsubmitted, no current attempt and remaining bounds. |
| `rejected` | Authoritative rejection after submission. | Stop; non-acceptance is not non-submission. |
| `unknown` | Submission may have occurred; acceptance unconfirmed. | Stop immediately. |
| `accepted` | Original destination confirmed this attempt. | Stop; opaque receipt is not work-completion evidence. |

Codex submission begins when the queue process starts. Only recognized temporary
process-resource start failures are retryable; missing/invalid executable,
permission, destination and unclassified failures are permanent unsubmitted.
After successful start, nonzero exit, timeout, cancellation or invalid ACK is
unknown. The exact matching successful ACK supplies the queue token as receipt;
original request and target correlation suffice without a receiver UUID echo.
The [Codex mapping](codex-queue.md) is authoritative for classifications.

For participating model-output, the first write invocation is its submission
boundary. A recognized temporary resource failure opening the file before any
write is retryable unsubmitted; path, permission and unclassified errors are
permanent. Any write/close failure after invoking write is unknown. Full write
and close is accepted. Preserve its existing JSONL content, serial writes and
single-writer assumptions; do not add file deduplication or repair.

## State and scheduling

Acceptance is `not_attempted`, `in_progress`, `unsubmitted`, `rejected`, `unknown`
or `queue_accepted`. Independent retry state is `scheduled`, `in_progress`,
`accepted`, `permanent`, `unknown`, `exhausted`, `cancelled`, `expired` or `blocked`.
Never turn unknown into failure because retries stopped. Accepted evidence is
monotonic; an older error cannot erase it.

Allow three attempts total, with minimum waits of two seconds after the first
unsubmitted result and ten after the second. If supplied, `retry_after_ms`
(wire maximum 60,000) increases that wait. Guidance never makes unknown eligible.
No configurable retry policy is added. If the due time reaches expiry, stop.

Keep the existing single worker and 128 admitted-work slots. Scheduled retry
occupies a slot but does not block ready work; order ready candidates by due
time, admission time, then UUID. One timer covers the earliest due candidate.
All callbacks recheck eligibility under lifecycle serialization. At most 1,024
notifications are retained; full retention rejects new admission with
`notification_capacity_exceeded`, never evicts unexpired evidence. These resource
bounds are not throughput targets. Startup rejects more than 128 nonterminal
records rather than dropping work.

Before issuance require the original subscription active, both instances available,
no cancellation/stop fence, all earlier attempts unsubmitted, no attempt in flight,
nonexpired evidence and an ordinal at most three. Persist reservation and consumed
ordinal before handing the request to the ordered sender, the existing cancellation
attempt-start boundary. Reservation, checks and handoff serialize with that fence;
do not hold the lifecycle mutex while awaiting a response. A reservation that loses
the race cannot be issued after cancellation. Unless this running process can prove
handoff never occurred, a reserved interrupted attempt becomes unknown.

Save the result before scheduling retry. Save failure globally closes participating
admission/issuance with `notification_storage_error`, leaving durable reservations
conservative; legacy routing retains its prior contract. Inspection stays available
with `storage_blocked:true`. A memory-only unsubmitted result is not permission
to resume after restart. Bound each attempt by the existing 30-second outer deadline.

Persist `issued_at` with the next result save after actual sender handoff; it can
remain null when recovery has only the earlier reservation. That absence is not
proof of non-submission. A late accepted result changes acceptance, but preserves
an existing cancelled or expired scheduling state.

After an unknown result, a valid `notification_evidence` can confirm acceptance
only within that original deadline and Session, as specified by the wire profile.
Keep the attempt correlation until then; ignore wrong/expired correlations with
bounded reasons. Evidence updates the ledger, never reopens retries or resends a
terminal trigger notice. No polling or second queue process is introduced, and
no correlation is restored into a new Session. The Codex adapter does not invent
late evidence after cancelling an unconfirmed process.

## Read-only local interface

Use existing loopback framing, version 1, strict parsing, 65,536-byte frames,
address/exit conventions and 25-second I/O deadlines:

```sh
agent-pulse-hub notifications --subscription-id <uuid> [--after <uuid>] [--limit <1..20>] [--address <address>]
agent-pulse-hub notification --notification-id <uuid> [--address <address>]
```

Reject unknown/duplicate flags, positional arguments and invalid UUIDs before
connecting. The list request is exactly `version`, `op:"notifications"`,
`subscription_id`, optional `after` and optional integer `limit` (default 20).
Filter by subscription, sort retained UUIDs lexicographically and return IDs
greater than `after`. Listing includes cancelled subscriptions and does not
require a live registration. Response: `ok:true`, `subscription_id`,
`notifications`, `next_after` (UUID only if more matching records exist, otherwise
null). Each item has exactly `notification_id`, `created_at`, `acceptance`,
`retry_state`, integer `attempt_count`, and nullable reason code `reason`.
This is live pagination; new UUIDs can sort before the cursor. Restart listing
to discover them. Empty history is not proof of non-submission.

The single-notification CLI outputs one complete JSON object with exactly
`notification_id`, `subscription_id`, `input_plugin`, `output_plugin`,
`destination_args`, `created_at`, `expires_at`, `acceptance`, `retry_state`,
`reason`, `next_attempt_at`, `storage_blocked`, and `attempts`. UTC times use
RFC3339Nano strings; absent times/reasons are null. Each attempt has exactly
`attempt_id`, integer `ordinal`, `reserved_at`, nullable `issued_at` and
`finished_at`, `status`, nullable `reason`, `message`, `receipt`, `retryable` and
`retry_after_ms`. Its initial status is `in_progress`, becoming unknown on
uncertain recovery. Inapplicable fields are null. Never expose context, watch
arguments, environment or process argv. Explicit destination inspection is trusted
local routing access; keep credentials out of destination arguments. Codex
destination arguments contain only the registered thread UUID.

To preserve existing argument-size limits, the CLI assembles read-only chunks.
Request: `version`, `op:"notification"`, `notification_id`, optional integer
`offset` (default zero), optional `revision`. Initial reads omit revision and
require offset zero. Response: `ok:true`, `notification_id`, `revision` (canonical
positive ledger generation string), `offset`, `total_bytes`, `data` (base64 of at
most 16,384 bytes of UTF-8 JSON). Later reads supply the revision and next byte
offset. No per-client snapshot is allocated: if ledger generation changed,
return `notification_changed`. The CLI restarts once, then fails visibly rather
than presenting mixed/partial JSON. Bound assembled payload to 131,072 bytes
and the entire CLI operation to 25 seconds. All reads are non-submitting.

Use existing error envelopes with `invalid_request`, `notification_not_found`,
`notification_changed` or `notification_storage_error`. A missing/expired ID is
not-found, never unsent. Retry requests are invalid; there is no manual operation.

## Storage, commit and recovery

Use the existing state-directory exclusion and owner-only requested permissions.
Registration and trigger-runtime record formats remain unchanged. Add root
`notification-store.json` and directory `notifications/`, with `ledger/` and
`guards/`. The permanent marker is exactly `{"version":1,"store_id":"<uuid>"}`.
Initialize an empty store under exclusion only if both new entries are absent
in a new or otherwise valid legacy tree, after output negotiation but before
restoration/admission. Log `notification_history_initialized`; reconstruct no
legacy work. If either entry exists, both and all components are required.

Ledger filenames are positive, 20-digit zero-padded decimal generations ending
in `.json`, matching the runtime generation convention. Each complete snapshot
has exactly `version:1`, `store_id`, `generation` (canonical decimal string),
`saved_at` (UTC) and `notifications` (array sorted by notification UUID).
Each notification has the inspection fields except `storage_blocked`, plus
`context` and `binding_hash`. Hash the strict canonical tuple of notification ID,
subscription ID, input/output names, destination object and exact context with
SHA-256, encoded lowercase hex. Reuse canonical argument equality/number rules.
The hash detects mismatched records; it is not authentication.

Guard filenames are `<notification_id>.<ordinal>.json`: ordinal zero protects
admission, one through three protect reservations. Each has exactly `version:1`,
`store_id`, `notification_id`, integer `ordinal`, nullable `attempt_id` (null for
zero), `binding_hash`, `reserved_generation`, `created_at`, and `expires_at`.
Guard times are copied from notification admission/expiry, not extended on retry.
`reserved_generation` is a canonical positive decimal string no greater than
the containing ledger generation. Generation values fit uint64; exhaustion
blocks the store visibly, never wraps or overwrites an existing file.
Validate UUIDs, filenames, hashes, ordinals, legal state transitions and strict
JSON. Bound each notification/projection to 131,072 encoded bytes, a snapshot to
134,217,728 bytes/1,024 notifications, and each guard to 1,024 bytes (at most 4,096
guards). Validate before admission; no truncation. Reject symlinks, nonregular,
unknown, oversized, malformed, duplicate or unsupported entries.

One store coordinator serializes commits. Exclusively write/close a staging file
and rename to its previously absent final path on the same filesystem; no
overwrite, fsync or crash-atomicity guarantee. Commit guard zero before admission
snapshot; commit a fresh ordinal guard before reservation snapshot. A guard alone
never authorizes sending. After snapshot commit, remove all older generations
before allowing another commit or sender issuance. Cleanup failure blocks the
store and is actionable; it does not authorize fallback or unbounded accumulation.
Result saves retain guards even for unsubmitted outcomes and never reuse ordinals.

Load the highest generation and validate completely, never fall back on error.
Require the admission guard and every attempt guard, and cross-check every guard
against ledger membership, binding, ordinal and store identity. Missing guards,
advanced/orphan guards or an empty ledger directory block startup. A reservation
without saved conclusive evidence becomes unknown and is saved as stopped before
readiness. A newer reservation guard prevents a stale unsent snapshot from
authorizing reissue. Coherently rolling back or deleting marker, guards and ledger
together cannot be detected without an external authority; such editing/old-backup
restoration is unsupported and never a supported retry recovery procedure.

Admission snapshot commit precedes enqueueing. Failure means no admitted notification
or action issuance: report storage error and a discarded source outcome. A successful
record with no attempt survives stop. Validate all retained records and route
bindings before any restoration grant/retry. A missing registration cancels retained
work; a mismatched existing route or missing output capability fails startup.
Only retained scheduled records with all earlier outcomes unsubmitted resume,
preserving due times and consumed ordinals. No old Session correlation is restored.

### Retention, clock and compaction

Keep evidence seven days from admission. Before each issue check expiry. Use UTC
persisted times and monotonic waits within a process; if wall time regresses below
last saved/observed time, block issuance and expiry cleanup with `clock_regression`
until it catches up. Never extend expiry. Forward movement can expire work early
relative to elapsed real time; this sacrifices availability without allowing resend.
Keep an expired issued record until its bounded response/evidence window closes.
Expiry stops scheduling and never makes a notification fresh again.

Remove only expired, quiescent records. A temporary `notifications/compaction.json`
is exactly `version:1`, `store_id`, `generation` (removal snapshot) and `guards`
(array of exact guard filenames). Commit this receipt before the removal snapshot.
After committing/removing older snapshots, delete the listed guards and receipt;
finish this transaction before any new commit. At startup, a receipt whose target
generation was never committed is abandoned without removing guards. Otherwise
require that no listed guard is referenced by the loaded ledger, then complete
deletion idempotently. Missing listed guards are cleanup, not corruption; unlisted
orphan guards remain errors. Validate names/types and never follow symlinks.
Failed cleanup blocks the store with a reason. Not-found after expiry is a retention
limit, not proof that old work never reached its destination.

## Source outcomes, shutdown and cancellation

For a usable input Session send one terminal outcome per notification, not per
attempt. Intermediate retryable unsubmitted results keep the event and rewait
outstanding flag pending. Accepted sends accepted; unknown sends unknown;
permanent/exhausted/rejected/expired unaccepted work sends failed; pre-admission
rejection sends discarded. The finite policy plus attempt deadlines bounds waiting.

Normal stop closes admission/issuance. At the existing runtime cutoff, save known
outcomes, mark issued-unconfirmed reservations unknown, and retain queued/waiting
proven-unsubmitted work with its bounds. Send final failed for retained unsent work
and unknown for issued uncertain work, then perform the existing final input
checkpoint/save. Failed notification save is a visible continuity failure, not
restart safety. Await notification saves and configured process exits before lock
release. After stop, late evidence cannot change the completed trigger fence.

Resumed notification work has no old event ID in the new input Session. Its later
acceptance updates only the ledger, not an old terminal notice or checkpoint.
Likewise late accepted evidence after a terminal unknown does not send a second
notice. Existing source deduplication/failed-event recovery remains unchanged:
an input may later emit a distinct event for similar source data. General
suppression of distinct events is excluded. PR-watch continuation remains distinct.

Use [cancellation ordering](subscription-cancellation.md#cancellation-ordering)
and its sender-handoff attempt boundary. Its fence closes every retry path,
including dequeued/reserved-but-unissued work. After the live fence and before
registration/runtime deletion or successful cancellation, durably set all retained
target retry states to cancelled, preserving acceptance and evidence. Notification
save failure is `storage_error`, never success; leave the target fenced. The
cancellation intent continues to protect a partially completed removal.

At startup collect cancellation intents, validate the notification store, save
their target notifications as cancelled, then complete existing deletion/intent
cleanup before any plugin launch. A missing registration also cancels retained
work, including after intent housekeeping finished. New equivalent registrations
have fresh IDs and inherit nothing. Already-issued attempts may update evidence
under their deadline without reopening watches, source progress or retry. Never
delete notification records as trigger-progress cleanup or wait for output recall.

## Migration and operations

Preserve the whole stopped state tree before enabling the output flag and upgrading
host/action together. Existing registrations keep IDs/arguments. Only newly admitted
events and eligible retained work participate; no volatile history is reconstructed.
Before starting an older daemon, stop normally and preserve the newer state tree.
Older strict daemons reject the new entries: do not remove safety files to force
startup or restore an old backup to resume work. Rollback to an older binary is
unsupported for a state tree with notification records; use a new isolated state
only as a deliberate new deployment, never represent re-registration/republication
as safe recovery of an uncertain notification.

On corruption preserve the stopped tree and restore the original matching files
and configuration, not an older coherent snapshot. If evidence cannot be restored,
keep it blocked. There is no automated salvage, reset or manual retry command.
Capacity is released by normal expiry/cleanup, not eviction or operator deletion.

Operators retain the registration's subscription ID, list notifications, inspect
one ID and read acceptance separately from retry state. Explain temporary versus
permanent unsubmitted failures, unknown stopping, seven-day retention, capacity,
and stop/wait/restart beside these instructions. Plugin-author documentation
derives from the public profile, never treats legacy failed as retry permission.
Default diagnostics list IDs/times/reason codes only; protect context in storage
and destination details available through explicit local inspection. Never dump
credentials, raw service streams or argv. Use portable Go APIs and existing trust;
no database, OS matrix or stronger power-loss guarantee is selected.

## Decisions and alternatives

| Decision | Alternatives and tradeoff |
| --- | --- |
| Prove non-submission or stop | Receiver deduplication would require an excluded API dependency. Unknown recovery is intentionally sacrificed for duplicate prevention. |
| Optional output profile | Reinterpreting legacy failed silently changes safety and existing acceptance cases. Explicit opt-in preserves them. |
| Three attempts, two/ten-second delays | Unbounded retry violates requirements; configuration adds unneeded combinations. Only one acceptance workflow must consume the full twelve-second minimum. |
| Bounded complete ledger plus guards | Per-record mutable indexes reduce write amplification but add multi-record recovery. At maximum a 128 MiB snapshot is costly; serialized admission/backpressure makes that cost explicit. |
| Chunked read-only inspection | Reducing destination argument size would restrict existing routes. The CLI hides bounded transport assembly while retaining exact routing values. |
| Session-scoped source outcomes | Reusing old event IDs would corrupt restored input correlation. Notification truth persists separately without promising suppression of new source events. |

## Coverage and verification selection

All IDs below refer to `requirements/notification-delivery` unless qualified.

| Requirements | Acceptance | Verification |
| --- | --- | --- |
| FR-001–003, NFR-002 | AC-001/004/007 | ND-V01-STATUS-RETRY, ND-V04-ACTION; identity, correlation, chunk/JSON and redaction units. |
| FR-004/005/007 | AC-002/004/005 | ND-V01-STATUS-RETRY, ND-V02-RESTART; timer, rejection, guidance and stale-callback units. |
| FR-003/005/006 | AC-003/005 | ND-V04-ACTION, ND-V02-RESTART; late-evidence and unknown/permanent matrices; ND-D01/02. |
| FR-005/006/008 | AC-006 | ND-V02-RESTART, ND-V03-CANCEL; mocked storage/clock/compaction failures. |
| NFR-001–003 | AC-007 | Existing bundled-model MVP scope, ND cases, source/contract review, separate real Codex evidence. |
| FR-002/006–009, NFR-001 | AC-008 | ND-D03, operator/plugin-author instruction review. |

Prefer units with mocked file/process/time ports for every JSON/UUID/binding
combination, number canonicalization, pagination/revision change, failure at each
guard/snapshot/compaction write-close-rename/delete, clock/expiry, cancellation
interleaving and classification. They require no real processes. Retain four
assembled cases for actual control, Session, filesystem, process and restart
wiring. Combine overlapping observations rather than duplicate every matrix.
Reuse legacy CQ-V02's real 20-second timeout; the new action case uses immediate
started/nonzero/ACK-loss behavior plus timeout-classification units.

### Cases and method

The mixed inventory is `cmd/agent-pulse-hub/testdata/notification-delivery-cases.json`.
Hub cases live in `cmd/agent-pulse-hub/notification_delivery_integration_test.go`;
the standalone action case in
`cmd/codex-queue-verification/notification_delivery_integration_test.go`.
Use existing suites, `//go:build integration` on every case/probe, and command-local
`testdata/`. Pending defers product wiring only; methods and outcomes follow.

| Case | Inputs/actions and observable outcomes |
| --- | --- |
| ND-V01-STATUS-RETRY | Real daemon/client, gated public output: inspect admitted work before issuance, then in progress. Return retryable unsubmitted once and accept after the real two-second minimum. Preserve notification identity, target and context, with distinct attempts. Mismatched correlation/stale work cannot confirm or resend. Equal-context new event gets a new ID. List/show, including large destination chunks, never issue work. |
| ND-V02-RESTART | Controlled outputs produce eligible unsubmitted, unknown, accepted and exhausted records in real storage. Exhaust one with three unsubmitted results and real two/ten-second delays. Stop before another due retry and restart the same state: only eligible work resumes under original ID/remaining bounds. In stopped copies remove a guard and separately corrupt the highest ledger: no readiness, fallback or submission. |
| ND-V03-CANCEL | Schedule an unissued retry, successfully unsubscribe, execute stale callback and restart: no new attempt, retained cancelled evidence remains inspectable. Separately cancel an issued held attempt, then return unknown and matching accepted evidence before its original deadline: no revived watch/notice/registration/retry. Unrelated shared subscription continues. |
| ND-V04-ACTION | Public standalone caller launches the real participating action with controlled Codex. Remove executable after readiness: permanent unsubmitted and no queue start. Restore, return started nonzero, accept but suppress ACK, or wrong-target ACK: unknown with exactly one invocation and no action retry. A matching successful ACK returns correlated queue receipt. Context naming B cannot retarget A; children are reaped. |

V01–03 retain real hub, local clients, lifecycle, scheduler, store and streams;
only source timing and destination responses are substitutes. V04 retains real
action/domain/process code, replacing only external Codex. Use existing journal/
gate helpers, temporary isolated state, bounded condition waits and awaited owned
process exits. Never corrupt live operator state. Preserve the actual bundled
manual/model-output MVP suite separately; a controlled plugin is not that evidence.

Use pinned Go/Ginkgo, the existing Ubuntu job and approved Python fixture setup.
No dependency, CI, shared tooling or pin change is needed. Hub warmed budget is
30 seconds setup/build, 15 V01, 35 V02, 20 V03, 10 cleanup and 10 reserve (120 total),
at most six direct children. Action budget is 30 setup, 15 V04, 5 cleanup, 70 reserve
(120 total), at most two children. New raw-profile feasibility uses one Go helper
process bounded to ten seconds; its exact path is
`cmd/agent-pulse-hub/notification_delivery_feasibility_integration_test.go`.
It proves public frame transport and cleanup, not product policy. Existing Codex
feasibility proves process argv, capture and cancellation machinery. Record executed
times separately from forecasts and product acceptance in the PR.

Run Mage `test` and `integrationAll` separately; compile and register ND cases
with integration enabled and execute the minimal probe. Implementation completion:

```sh
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/notification-delivery-cases.json <new-report-directory>
```

All four IDs must pass; Pending/skipped/filtered/missing cases fail. Retain existing
MVP/CQ scope results independently. Design compilation/dry-run/feasibility cannot
establish notification behavior or actual Codex compatibility.

### Delivery quality checks

Use existing conversation A (registered target) and B (non-target). The maintainer
retains product/Codex versions, environment, configuration, non-secret commands,
notification/attempt IDs, stimuli, status and independent destination counts.

| Check | Procedure and required result |
| --- | --- |
| ND-D01: AC-001/002/008 | Follow final instructions using actual hub/manual/action and ordinary Codex queue. Publish a unique marker, list/show matching queue acceptance at A, and confirm B receives none. Inspection adds no work. Pre-launch transient failure is controlled in CI; do not claim a live transient occurred unless observed. |
| ND-D02: AC-003/006 | A delivery-only native wrapper forwards the exact queue argv once to the selected real Codex, privately records its successful target ACK, suppresses stdout and exits nonzero. Require unknown, one actual queue invocation, no later invocation throughout the retry window and after normal stop/restart. Independently count the marker at A through available queue/history/conversation evidence; wrapper counts alone do not prove destination counts. Missing counting or safe fault control leaves the check incomplete. |
| ND-D03: AC-007/008 | Follow list/show, permanent/unknown/exhausted diagnosis, stop/wait/restart and retention instructions; inspect the author contract and absence of experimental/manual retry paths. Distinguish unit-controlled expiry from any actually observed seven-day retention. |

The native delivery wrapper under `cmd/codex-queue-verification/testdata/` is
verification support only. It forwards native argv exactly once, passes help
and version requests through, bounds child output and records only the matching
queue ACK privately. It suppresses that ACK from the action and exits nonzero.
Its 18-second child bound plus one-second pipe cleanup precede the action's
unchanged 20-second bound; this fixture does not change product deadlines.
The controlled-executable probe establishes this fault mechanism, not real
Codex acceptance. Delivery supplies the selected real executable and private
evidence path, then removes the wrapper from configuration while stopped after
the run. Execute real-service checks only after implementation approval and
merge; a fake cannot replace their evidence.

## Open questions and risks

No product-policy choice is intentionally deferred to implementation. Human review
evaluates the retry/retention/capacity bounds, snapshot cost and conservative recovery.
Actual executable/server compatibility and independent destination observations
remain delivery checks. Unknown can remain unresolved and distinct source events
can produce similar notifications. Reconcile future approved checkpoint/capacity
changes before dependent implementation; unapproved proposals are not contracts.

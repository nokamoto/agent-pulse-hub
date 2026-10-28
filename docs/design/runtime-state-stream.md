---
type: Design
title: Streamed trigger runtime state
description: Transfer opaque registration-owned state in bounded messages and commit complete multi-chunk generations through core-managed storage.
sources:
  - id: pr-watch-requirements
    resource: requirements/github-pr-watch.md
  - id: runtime-requirements
    resource: requirements/trigger-runtime-state.md
  - id: runtime-design
    resource: design/trigger-runtime-state.md
  - id: plugin-specification
    resource: design/plugin-v1.md
---

# Streamed trigger runtime state

## Purpose and authority

This optional profile fulfills `requirements/github-pr-watch` FR-010/FR-012,
AC-008/AC-011/AC-012 and the ownership, restoration, failure and compatibility
requirements in `requirements/trigger-runtime-state` FR-001–FR-010 and
NFR-001–NFR-003. It removes the single-envelope allocation as a total-state
limit for participating inputs. It does not change event context capacity,
action delivery, registration identity or the meaning of a plugin's progress.

This document is authoritative for the stream capability, conditional wire
schemas, multi-chunk storage, migration and resource controls. It supplements
[the public plugin specification](plugin-v1.md) and
[the runtime-state design](trigger-runtime-state.md). Their unchanged transport,
strict JSON, delivery-notice, activation, checkpoint scheduling and normal-stop
rules remain authoritative. [Rewait](pr-watch-rewait.md) continues to own
notification generations and explicit comparison positions. Plugin-specific
designs own each state schema and how its ordered objects reconstruct progress.

## Capability and compatibility

An input opts in with both `runtime_state_v2` and `runtime_state_stream_v1`.
It may additionally advertise `rewait_v1` and the independent
`cancel_watch_v1` from [subscription cancellation](subscription-cancellation.md);
array order is immaterial. Their existing semantics remain unchanged. The stream
capability alone, with `runtime_state_v1`, duplicate capabilities or unknown
capabilities is invalid. Nonparticipating inputs, runtime-state v1/v2 without
the new capability, outputs and the application version `2` remain unchanged.
An older strict host rejects the new capability visibly; do not fall back to
a smaller state, a fresh baseline or plugin-owned files.

The stream profile changes only state handoff and storage. `delivery_notice`,
`state_event`, `activate_watch`, `resume_watch`, `resume_result` and their
generation rules retain their existing shapes. Existing `checkpoint` requests
also retain their shape. Participating plugins use the streamed success
messages below instead of positive `checkpoint_result`; a negative
`checkpoint_result` remains valid. Ordinary `state_watch` is replaced by
`state_stream_watch` only for this profile.

## State representation and bounds

A snapshot is a nonempty ordered sequence of JSON objects plus one nonempty
`schema` string of at most 128 decoded UTF-8 bytes. Each object has at most
32,768 compact-encoded UTF-8 bytes; each complete application message must still
fit 65,536 bytes. Objects obey the public strict JSON and exact-decimal rules.
There is no base64 application encoding. Object boundaries carry no common
service meaning: only the plugin schema defines ordering, reconstruction and
validation. The core does not merge objects or inspect service fields.

An empty service state is represented by one object, for example `{}`, not by
an absent saved snapshot. Null state is reserved for a registration without
saved runtime progress. State can contain fingerprints, relationship metadata,
pending journal records, positions and accepted-delivery progress across many
objects. No item count, journal count or total-state allocation is derived from
the 32-KiB object or 64-KiB message bounds.

All counters below are canonical unsigned decimal strings: `0` or a positive
decimal without leading zeroes, through uint64 maximum. Chunk indexes start at
`0` and advance by one. Counts are positive except the null-state restore
count, which is `0`. Counter exhaustion fails explicitly; it never wraps.
Every message contains exactly the listed fields plus `version: 2` and `type`.
All request/subscription IDs are nonempty strings with existing ownership and
uniqueness rules. Schema and data fields must leave room for the complete
message; reject a local oversized encoding before sending it.

## Restoration wire contract

The host knows a stored snapshot's count before transmission. After destination
validation it sends these messages in order on the existing Session:

| Direction / type | Additional fields | Meaning |
| --- | --- | --- |
| Host to input / `state_stream_watch` | `request_id`, `subscription_id`: nonempty strings; `watch_args`: object; `schema`: string or null; `chunk_count`: decimal string | Begin one dormant watch. Null schema requires count `0`; nonnull schema requires a positive count. |
| Host to input / `state_stream_chunk` | `request_id`, `subscription_id`: nonempty strings; `index`: decimal string; `data`: object | Supply exactly the next object for that pending watch. |
| Host to input / `state_stream_end` | `request_id`, `subscription_id`: nonempty strings; `chunk_count`: decimal string | Finish the handoff; count must equal the announced and received count. |
| Input to host / `watch_result` | Existing `request_id`, `accepted`, conditional `error` | Complete acceptance only after end and complete schema validation/application. |

A new or deliberately reset registration receives
null schema, count `0`, then end with count `0`. There are no chunk messages in
that case. A restored registration receives every stored object, then end.
The input stages state until end; it does not poll, emit, install partial
progress, accept the watch early or request storage operations. On successful
validation it installs the complete state and sends positive `watch_result`,
remaining dormant until the ordinary post-commit activation grant.

The existing ten-second watch deadline covers the entire request, chunks,
end, plugin validation and result; sending more chunks never extends it.
Missing, repeated, out-of-order, foreign or excess chunks, inconsistent count,
unexpected end, unsupported schema and invalid reconstructed progress reject
the watch with a bounded reason. A malformed host message follows the existing
invalid-host-message rule. No partially restored watch becomes eligible for
activation. Startup's all-watch barrier, saved-route failure and plugin-loss
behavior remain unchanged. Bounded invalid input cannot renew a deadline.

## Checkpoint wire contract and ordering

For a pending ordinary `checkpoint` request, the input returns exactly one
negative `checkpoint_result`, or this ordered successful stream:

| Direction / type | Additional fields | Meaning |
| --- | --- | --- |
| Input to host / `checkpoint_stream_begin` | `request_id`, `subscription_id`, `schema`: nonempty strings | Begin extraction of one snapshot for the matching pending request/watch. |
| Input to host / `checkpoint_stream_chunk` | `request_id`, `subscription_id`: nonempty strings; `index`: decimal string; `data`: object | Supply the next snapshot object. |
| Input to host / `checkpoint_stream_end` | `request_id`, `subscription_id`: nonempty strings; `chunk_count`: positive decimal string | Declare that all objects were supplied; count must equal the received count. |

Use the host's checkpoint request ID as the transfer correlation; do not add
plugin-generated transfer IDs or permit reuse across batches. The host permits
one transfer per pending watch. Different watches may interleave messages,
but each watch's begin/chunks/end order is strict. A begin is required before
any chunk; an end requires at least one chunk. One positive legacy
`checkpoint_result`, a second begin, repeated/missing indexes, owner mismatch,
count mismatch, excess data after end or negative result after begin fails the
affected snapshot. A negative result after begin is treated as an unsuccessful
transfer with its bounded reason, never as a completed snapshot.

Before extracting the snapshot the input applies all earlier delivery notices
on the host stream. The entire object sequence represents one immutable
logical cut, including every earlier accepted notice; reading live mutable
tables independently while emitting chunks is not sufficient. The plugin may
use a bounded immutable view or copy-on-write internally. For `quiesce: false`,
later observation/notification work may continue after that cut. For
`quiesce: true`, stop producing events before snapshot extraction and remain
quiescent through timeout, transfer failure and Session closure. No late grant
or rewait restarts a quiesced watch.

The existing single ten-second batch deadline covers notice/request sending,
begin, every chunk and end for all watches in that batch. It is not ten seconds
per chunk or watch. A chunk cannot renew the window. Final stop uses its same
shared ten-second protocol phase across instances, interrupts a periodic wait,
and preserves the pre-cutoff accepted-notice fence. No final snapshot starts
until any prior periodic correlation is completed or expired.

On a malformed, rejected, incomplete, over-budget or expired transfer, retain
the previous committed generation, discard staging, expire the correlation and
report the existing continuity warning. Ignore all subsequent frames for that
expired correlation; they cannot complete or contaminate a later transfer.
Unknown request IDs never create state. Drop unsent expired host messages and
plugin checkpoint frames from bounded outgoing queues. A checkpoint timeout
alone does not kill a healthy input; real transport failure retains the normal
unavailability behavior. Another scheduled batch may take a new snapshot,
without replaying delivery. No stream acceptance receipt or new delivery
acknowledgement is introduced.

## Core-managed generation storage

Keep registration v1 files and runtime ownership unchanged. New stream
generations occupy `runtime/<subscription_id>/<generation>.stream/`, where
generation is the existing 20-digit zero-padded positive uint64. This suffix
distinguishes them from legacy `<generation>.json` files. A generation number
cannot appear in both forms. The directory contains exactly `manifest.json`
and contiguous files `<index>.json`, with indexes formatted as 20-digit
zero-padded unsigned decimals starting at zero. Symlinks, nonregular files,
unexpected entries, missing indexes and mismatched owners fail validation.

Each chunk file contains the strict compact JSON object, without framing.
The core writes the same encoded bytes whose lengths it checked. The manifest
has exactly these fields:

```json
{"version":2,"subscription_id":"<ID>","input_plugin":"<name>","generation":"1","schema":"<plugin schema>","chunk_count":"2","data_bytes":"1234"}
```

`version` is number token `2`; `data_bytes` is the sum of chunk-file byte
lengths and uses the same canonical decimal-string rules. Bound manifest reads
to 16,384 bytes and every chunk read to 32,768 bytes. Validate the complete
selected generation for each retained route before any watch transmission:
process committed cancellation intents first under the cancellation contract,
without interpreting a removed target's payload. Retained-state validation
checks exact count, per-object
strict JSON, summed bytes, owner, schema shape and budget. Semantic validation
remains the input's job. This is trusted local storage; no authenticity or
crash-durability guarantee is added.

While holding the existing exclusive state-root ownership, create a unique
`runtime-stream-<UUID>` directory under root `staging/`. Serialize generation
work per registration and reserve a previously unused next generation number
above both filename forms. Write/close chunks as they arrive with backpressure.
After a valid end, close all files, write/close the manifest, then rename the
complete staging directory to the absent final `.stream` path on the same
filesystem. That rename is the single commit point; do not replace a populated
directory or commit any chunk separately. A save succeeds only after rename. Recheck the existing
[registration cancellation fence](trigger-runtime-state.md#coordination-with-individual-cancellation)
under the same per-registration ordering immediately before publishing it.
As with existing runtime generations, filesystem work has no portable hard
deadline or power-loss guarantee; protocol expiration before valid end forbids
commit even if queued filesystem work finishes later.

Use at most the existing four filesystem workers, per-registration ordering
and bounded queues. A worker can write a chunk and yield; do not buffer all
active snapshots or block the Session reader indefinitely behind one watch.
When a bounded write queue cannot accept more work within the protocol window,
fail that transfer and continue draining/discarding its expired correlation.
A save finalized after valid end may take the existing filesystem latency;
shutdown waits for that save before input cleanup and lock release. This does
not bypass the existing cancellation fence: expire a fenced target's stream
correlation without waiting for end, discard queued chunks and remove its
uncommitted staging. Apply the existing cleanup diagnostic if staging removal
fails; residue is never a committed generation. Drain
writes or a save that already won lifecycle ordering before target deletion;
no late end, result or filesystem callback may recreate its runtime directory.
Do not interpret this compatibility rule as adding cancellation behavior to
the PR-watch capacity scope; the cancellation design owns that feature.

On failure remove the unique staging directory and remove a newly created
empty runtime directory when possible; diagnose cleanup failure. Staging is
never a candidate generation. On next startup, while holding exclusion, remove
recognized abandoned `runtime-stream-<UUID>` directories after checking their
strict owned structure: a lowercase UUID name, a real directory, and only
regular `manifest.json` or 20-digit-index `.json` files. An abandoned directory
may lack the manifest, have index gaps or contain incomplete file contents;
these are uncommitted attempts and their contents are not parsed as progress.
Unknown entries or unsafe types fail startup. Never
follow symlinks or remove a computed path outside `staging/`. A failed cleanup
does not turn partial data into progress. Retain the previous commit on any
write/close/rename failure. After a successful commit remove older generations
in either format; cleanup failure leaves the newest selected and is visible.

For mixed valid filename forms, select the highest generation number once,
then validate it fully. A corrupt, unreadable, unsupported or over-budget newest
generation blocks restoration; do not fall back to an older stream or envelope.
Equal-number forms are invalid rather than a format-priority decision. Offline
reset/deletion removes the complete registration runtime directory as before.
Orphan state never transfers to another registration.

## Resource controls and operator instructions

The daemon accepts `--runtime-state-max-bytes <positive-decimal>` with default
`67108864` (64 MiB), measured as the sum of compact chunk bytes per streamed
snapshot. It applies only to stream-profile state; legacy state retains its
32,768-byte envelope limit. Accept positive values through uint64 maximum;
reject zero, signed, fractional, overflowing or malformed values before startup.
This is an operator resource budget, not a supported PR item count. Operators
may raise it for a larger saved state subject to available memory/disk and the
unchanged protocol deadlines. Do not derive a maximum PR lifetime item count
from the configured value or replace this setting with item-count limits.

Admission reserves one bounded stream correlation per watch, not the entire
state inside `state_stream_watch`; only that message's arguments/metadata must
fit the ordinary wire bound. Bound queued chunk data to four objects per active
filesystem worker and propagate backpressure. Plugin snapshot construction and
reconstruction must have declared byte budgets and avoid unchecked allocations.
Any inability to materialize/validate state within the configured budget or
ten-second window is an explicit resource/timeout failure, never permission to
truncate progress, emit a false no-change result or start over silently.

Before changing the byte setting or replacing binaries, stop the daemon, await
configured plugin exits, and preserve the entire state tree. Use the same
setting on restart or increase it when a saved generation exceeds a new lower
budget. Such a load fails visibly and preserves files; it does not fall back.
Save failure continues healthy monitoring with
`runtime continuity at next restart cannot be guaranteed`. A successful later
checkpoint restores the saved boundary for its own progress. Diagnose watch,
operation, byte budget/observed size when available and reason without payloads,
credentials, or discussion text. Existing trusted-local backup/reset instructions
apply to complete `.stream` directories as well as envelope files.

## Migration and rollout

A stream-capable host can load legacy runtime generation version `1` without
rewriting it. Deliver its envelope `schema` unchanged and its `payload` as one
object through the stream restore messages. The input must explicitly support
that legacy single-object schema; transport migration does not authorize a
service-schema reset. For `github-pr-watch-v1`, the plugin-specific migration
preserves nonce, logical sequences, issued positions, comparison, pending
changes, attempted and confirmed progress. The first successful stream save
commits a higher-numbered generation. A failed migration leaves the legacy
generation intact and blocks the affected restoration.

For a streamed generation, select and validate its format before checking
plugin schema compatibility. A configured input without the stream capability
cannot restore it, even if it advertises legacy runtime-state support. Older
hosts see the unknown committed `.stream` entry and fail closed. Do not flatten,
drop pending chunks or remove the directory to make rollback appear successful.
Preserve matching binaries and the stopped tree; deliberate offline reset
relinquishes continuity under the existing operator procedure. Legacy inputs
on the new host keep their existing envelope files and wire shapes.

## Decisions and verification

| Decision | Alternative | Reason and cost |
| --- | --- | --- |
| Optional ordered object stream with unchanged wire bound | Raise the shared 64-KiB message maximum | Separates total state from per-message allocation and preserves existing profiles; adds explicit transfer/commit lifecycle. |
| Plugin-owned schema, core-owned generation directory | Plugin files, service database, core interpretation of PR items | Preserves public language neutrality and storage ownership; plugins must validate ordered objects. |
| Single immutable generation commit | Commit each chunk, silently truncate, keep only a cursor | Partial progress cannot satisfy restart coverage; staging uses temporary disk and failure cleanup. |
| Byte budget and existing deadlines | A new fixed total PR item count or unlimited work | Resource failure remains observable without turning notification/state allocation into a lifetime item ceiling. |

Mocked protocol/clock/filesystem unit tests cover every capability combination,
exact byte boundaries, unknown/duplicate/foreign/late frames, zero/missing/excess
chunks, index/count exhaustion, deadlines without renewal, concurrent watches,
snapshot mutation, notice/cancellation fences and late-result non-resurrection,
write/close/rename/cleanup faults, mixed
format selection, invalid newest state, budget reduction, old-schema migration
and legacy-profile regression. They assert no activation, fallback, overwrite,
cross-registration application or payload disclosure on failure. Keep most
failure matrices at unit level rather than repeating subprocess scenarios.

The governed [PR-watch verification](github-pr-watch.md#verification-strategy)
uses GPW-I04 for large complete acquisition and public Session stream restore,
and GPW-I05 for bounded notification/rewait, core-owned multi-chunk save/restart,
remaining continuation and compatible
legacy-position migration through the real host/plugin/storage boundary.
These cases do not replace existing runtime-state or legacy-profile cases.
Case definitions, static Pending registration, feasibility, cost and commands
are owned by that design; use its separate unit and integration results.
Delivery follows its continuation/restart walkthrough and the existing
runtime-state operator checks. No live-service or receiving-agent success is
claimed by service-free storage verification.

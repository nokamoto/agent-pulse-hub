---
type: Design
title: GitHub PR watch capacity and continuation
description: Separate complete acquisition, retained progress and bounded notification delivery without a fixed lifetime item ceiling.
sources:
  - id: pr-watch
    resource: requirements/github-pr-watch.md
  - id: plugin-design
    resource: design/github-pr-watch.md
  - id: runtime-state
    resource: requirements/trigger-runtime-state.md
---

# GitHub PR watch capacity and continuation

## Scope and authority

This specification extends [GitHub PR watch](github-pr-watch.md) for
[FR-012 and AC-011/AC-012](../requirements/github-pr-watch.md). It governs
acquisition budgets, progress representation, legacy migration, notification
continuation and their verification. The parent design retains authentication,
original review/discussion categories, actor neutrality, scheduling, external
data protection and ordinary wait behavior. FR-013/AC-013/AC-014 CI failure,
conflict and readiness additions require another design revision. Cancellation
and action confirmation/retry are not introduced by capacity work.

The plugin owns GitHub semantics; the core owns persistence and routing.
[Streamed runtime state](runtime-state-stream.md) defines bounded messages,
complete generation commits and restoration. [Rewait](pr-watch-rewait.md)
defines one admitted event per explicit wait or daemon run. This specification
does not change either delivery acceptance into agent assessment or create
automatic delivery retries. Implementers and verification authors must read
these linked contracts; no additional specification is deferred.

## Acquisition and resource budgets

Enumerate all required category connections, including each thread's comments,
with the parent's `first: 25` cursor rules on every observation. There is no
item-count or page-count ceiling. A recent-item window or updated-since filter
alone cannot establish old edits or disappearances. Stage a candidate separately;
only complete enumeration atomically replaces the previous complete snapshot
and appends its changes. Failed pages never imply deletion or advance sequence.
The initial complete observation remains silent. A round is not an atomic
GitHub snapshot; preserve start/end times and the periodic-observation limit.

Keep ten-second request deadlines and the 256-KiB decoded page bound. Use a
300-second total round deadline, including in-round retry waiting. These replace
the original 30-second/1-MiB aggregate acquisition bounds. The plugin accepts:

| Option | Default | Meaning |
| --- | --- | --- |
| `--observation-byte-budget` | `67108864` (64 MiB) | Independently bounds accumulated decoded response bytes in a round and its compact canonical candidate snapshot. |
| `--history-byte-budget` | `67108864` (64 MiB) | Bounds the compact encoded retained change journal. |

Accept positive decimal bytes through uint64 maximum; reject signs, zero,
fractional values and overflow at process startup. Account before appending
or allocating. Previous snapshot, candidate and one immutable checkpoint view
may coexist; release failed candidates and completed views and never overlap
rounds or accumulate checkpoint copies. Resource failure preserves prior
progress and reports `observation_resource_exhausted` or
`history_resource_exhausted`, the relevant budget, measured bytes and option
without private content. Operators may raise a budget and restart from saved
progress. Never raise it automatically or reset the baseline. The daemon's
streamed-state byte budget is separate and must fit the complete saved state.

These configurable resource budgets are not supported PR item counts. The
31/32/33/100/1,000 regression inputs fit the defaults; 1,000 is not a maximum.
API/page limits and deadlines remain genuine visible acquisition failures.
Continue old-ID comparison regardless of age. Sort simultaneous differences
by category then stable ID before assigning monotonic change sequences, so
pagination order and duplicate unchanged fetches cannot create transitions.
Retain each distinct observed transition, including an edit later reverted.

## State representation and retention

Require `runtime_state_v2`, `rewait_v1` and `runtime_state_stream_v1` together.
Persist `github-pr-watch-v2` as an ordered sequence of JSON objects:

1. A header object has `kind: "header"`, `version: 2`, selected PR identity,
   registration nonce, observation/change sequences, latest complete observation
   times, scalar head/lifecycle metadata, attempted/confirmed delivery progress,
   committed comparison position and issued-position list. Include total item
   and change counts and first/last retained change sequences.
2. Objects with `kind: "items"` and an `items` array contain current snapshot
   records sorted by category then stable ID. Variable-length review/request/
   thread metadata belongs here, not in the header.
3. Objects with `kind: "changes"` and a `changes` array contain journal entries
   in ascending contiguous sequence order within the retained range.

Each object fits the stream's 32,768-byte compact payload bound. Empty item or
change sections may be absent; the header distinguishes a complete empty
observation from initial state. Reject duplicate IDs, unsupported kinds,
inconsistent counts, order, scope, sequence ranges or progress references
before applying restoration. Internal field names beyond those specified are
implementation choices, but these semantics and validation are mandatory.

Items retain identity/type, SHA-256 of full acquired content and relevant
semantic metadata, relationships and provenance sufficient to describe later
disappearance. Journal entries retain sequence/time, change kind, old/new state
where applicable, identity, provenance, relations, source link, full-body digest
and excerpt with explicit body status. Hash full content, never just excerpts;
unavailable attributes carry reasons. Excerpts are at most 512 UTF-8 bytes
before JSON encoding. Historical full text and deleted text may be unavailable;
identify that limitation with digest and source link. Never collapse distinct
transitions merely because they concern one ID. Shorten excerpts first if a
record exceeds a chunk. Mandatory record or header data that still cannot fit
causes `item_metadata_exhausted`; reject the entire candidate without advancing
snapshot or sequence. Never split one semantic record across objects.

Keep the existing `gpw1.` position: unpadded base64url of version byte 1,
16-byte registration nonce and eight-byte big-endian covered sequence.
Sequence exhaustion fails visibly. Positions are independent of Session event
IDs and generation IDs. Accept only an exactly issued retained position for
this registration. Preserve the latest four issued positions without a time
TTL and pin the active comparison position separately. An expired older
position yields `continuity_expired`, never substitution with the present.
Do not expire the newest position merely to fit new changes.

There is no 32-record journal bound. Retain every unreported transition and
history after the earliest retained issued position, committed comparison
position or confirmed delivery cursor. Prune only a prefix covered by accepted
delivery and no longer needed by those positions. If a candidate would exceed
the history budget, reject it and report the error. Existing continuation stays
available during acquisition failure. Successful checkpoint/restart preserves
all outstanding entries, positions and the nonce. Attempted, accepted and
explicitly assessed positions remain separate facts.

### Legacy migration

A stream-capable host returns a legacy envelope as one object with its original
`github-pr-watch-v1` schema. Validate the legacy readback contract below,
then convert in memory without changing nonce, sequences, issued positions,
attempted/confirmed/comparison progress or retained history. Conversion is
neither a new baseline nor a change. The next successful checkpoint writes v2;
failed migration leaves the legacy commit intact and blocks restoration.

No fallback to a smaller envelope is permitted. Older hosts/plugins cannot
consume new streamed v2 generations and fail closed. Preserve compatible
binaries and the stopped tree for rollback; restoring an older tree knowingly
loses intervening progress and is an explicit operator recovery decision.

### Legacy readback contract

For compatibility with existing saved bytes, the
[frozen v1 decoder and record declarations](https://github.com/nokamoto/agent-pulse-hub/blob/c71823fd817277a68f4860abc777e6f5487b6ece/plugins/triggers/github-pr-watch/internal/domain/tracker.go)
and its [item/aggregate declarations](https://github.com/nokamoto/agent-pulse-hub/blob/c71823fd817277a68f4860abc777e6f5487b6ece/plugins/triggers/github-pr-watch/internal/domain/observation.go)
define the legacy field layout and accepted readback forms. These fixed
references identify compatibility input bytes, not the authority for new v2
behavior. Do not substitute a later decoder or add new mandatory nested fields
to legacy state during migration. The following constraints retain the v1
validation boundary.

The normal saved representation contains required keys `scope` (six immutable watch arguments), `nonce` (16 bytes
encoded as hexadecimal), `sequence`, `confirmed`, `attempted` (uint64 JSON
numbers), `baseline` (boolean), `fingerprints`, `journal`, `positions`, `metadata`
(nonnull arrays), `aggregate` (object), and `started_at`/`ended_at` (strings).
`comparison` is an optional uint64 number or null. This type summary describes
normally emitted state, not additional migration rejection rules. Preserve all
forms accepted by the fixed decoder, including its null-to-zero-value handling
for non-array fields in uninitialized state; do not add stricter type checks.
Reject unknown fields under the legacy strict decoder. Scope must match the retained registration.
Confirmed/attempted/comparison cannot exceed sequence; confirmed greater than
one cannot exceed attempted. A completed baseline has positive sequence and
confirmed progress, valid aggregate identity/head/lifecycle and ordered
RFC3339Nano observation times. An uninitialized state has sequence zero and
empty fingerprints/journal/positions/metadata.

Journal sequences are positive, contiguous and end at sequence when sequence
exceeds one. History after the earliest confirmed/issued position must have no
leading gap. Fingerprints have unique `(kind,id)` keys and valid SHA-256
digests; metadata has exactly the same keys, empty bodies and valid full-body
digests. Issued positions have sequence at least two, no greater than attempted,
and exactly encode the retained nonce and sequence using `gpw1`.

| Legacy saved region | Original bound |
| --- | --- |
| Fingerprints | 32 records; each <=192 encoded bytes; array <=6,144 bytes. |
| Journal | 32 records; each <=640 encoded bytes; array <=20,480 bytes. |
| Issued positions | Four records; array <=1,024 bytes. |
| Remaining payload | <=4,096 bytes, including scope, nonce, aggregate, item provenance metadata, sequences/times and progress; measure the payload with fingerprints/journal/positions set to null. |
| Complete `{schema,payload}` envelope | <=32,768 bytes. |

Use the frozen v1 encoding/measurement behavior when validating existing
legacy bytes; the stream and v2 representation use their own compact encoding
rules. Oversized or inconsistent legacy state blocks restoration. The old
notification batch bound is not an additional saved-state validation rule.
Successful conversion preserves every retained transition and position; these
legacy bounds do not constrain new v2 acquisition or journal growth.

## Bounded continuation

Select a nonempty contiguous prefix of eligible journal entries after the
current comparison position, in sequence order. Keep at most eight entries per
notification and the existing 8,192 decoded UTF-8 byte context maximum.
Reserve identity, waiting context, times, current summary, position and
continuation metadata, then select as many entries as fit, shortening bodies
first. Eight is a message selection bound, not a lifetime/history limit.
A suffix that does not fit remains eligible rather than failing the batch.

The returned `confirmation_position` covers exactly the last represented entry.
The `continuation` object contains `has_more`, `remaining_changes`,
`through_sequence` (prefix end), `observed_through_sequence` (complete journal
tail at preparation), and `operation: "rewait"` only when more remain. Counts
refer to that preparation; subsequent observations may add entries. The caller
assesses the context and supplies its position to the existing rewait command
on the same subscription. The next prefix is available without another GitHub
activity or successful observation. Never automatically rearm or emit multiple
events for one generation. Restart rearms through confirmed delivery progress:
it may emit a remaining suffix, but cannot repeat a confirmed prefix solely
because of restart.

Current state comes from the latest complete observation, which can be newer
than the covered prefix; include its time and mark that distinction. Keep
scalar head/lifecycle state and bound review/request/thread lists with totals,
represented IDs, explicit omitted counts and the PR link. Truncation of current
lists never advances the change position. During acquisition failure label
state as the last complete observation, with its time and failed refresh reason,
not as newly complete data.

Each represented change keeps identity, provenance and relationships. Empty
body is `body_status: "empty"`; shortened body is `body_status: "shortened"`
with full-body digest, reason, source link and unavailable-history indication
when necessary. An explicitly represented change with such a body limitation
can advance its change position; this never asserts the omitted text was read
or assessed. Entirely omitted entries are a retained suffix outside the
position. If one entry's mandatory metadata plus minimal context cannot fit,
report `notification_metadata_exhausted`, retain the wait/progress and issue
no position. Do not skip it to send later entries. Allocation/encoding failure
has the same non-advancement rule.

## Decisions and tradeoffs

| Decision | Alternatives | Rationale and cost |
| --- | --- | --- |
| Complete paginated comparison | Recent window or updated-since cursor only | Detects old edits/removals across all required categories; API cost grows with current content. |
| Streamed core-owned state | Larger fixed envelope, compression only, plugin files | Fixed envelopes only postpone a ceiling; streams preserve ownership and bounded messages but add transfer/commit coordination. |
| Explicit prefix continuation | Automatic multi-event wait or reject large batches | Reuses admission fences and lets the agent assess each part; large backlogs require more explicit waits. |
| Configurable byte budgets | Fixed item maximum or unlimited resource use | Makes actual resource failures visible without treating checkpoint capacity as PR lifetime capacity. |

## Verification selection

All requirement IDs below refer to `requirements/github-pr-watch.md`.
FR-003–FR-010 and FR-012 map to AC-011 through full comparison, retention,
rewait and restart. FR-005–FR-008 and FR-012 map to AC-012 through prefix
coverage and durable continuation. NFR-001–NFR-004 remain applicable to external
data, secrecy, core ownership and separated evidence. The parent design's
other mappings and cases remain authoritative.

Use mocked unit matrix U02 for conversation-only and mixed 31/32/33/100/1,000
inputs, growth between sizes, old edits/removals and head changes. U03 covers
more than 32 changes, exact encoded-byte selection, body/current omissions,
position expiry and migration. U04 retains pagination, authentication and
retry checks. New U05 covers stream capability combinations, strict frame
validation, unknown/duplicate/foreign/late chunks, unchanged deadlines,
immutable snapshots/notice fences, budget reduction, mixed-format selection,
write/close/rename/cleanup failures and legacy compatibility using mocks.

Select only two additional process cases. One large mixed case catches real
pagination/serialization/restore wiring, avoiding subprocess permutations for
every size. One continuation/restart case combines overlapping notification,
control and filesystem boundaries. Unit mocks cannot establish those actual
boundaries. Preserve GPW-I01's existing non-capacity checks; its old expected
33-item and nine-change rejection is superseded by I04/I05 success. No approved
coverage is dropped to make current implementation pass.

| Case | Inputs and actions | Required observations |
| --- | --- | --- |
| GPW-I04; AC-011 | Public Session, selected host, 1,000 mixed items over first:25 pages. Silent baseline; repeat; add C1001, edit C0001, remove C0002, H1 to H2. Fail later comments and nested-thread pages, recover, checkpoint >32 KiB through several chunks and restore a new plugin process. | No count error, false deletion or failed-round advance. Exact recovered transitions and relations. Every wire frame <=64 KiB and object <=32 KiB. Restore has no unchanged duplicate; another old-item edit notifies. |
| GPW-I05; AC-008/AC-011/AC-012 | Actual hub/client/plugin/model action; baseline 100 items, seed P0, then 40 changes including >8 KiB and empty bodies. Interrupt acquisition, recover and freeze source; rewait through prefixes, save/restart between accepted prefixes. Separately migrate legacy state, remove/corrupt a saved chunk and obstruct saving. | Contexts <=8 KiB, contiguous represented sequences and explicit suffix, no new source activity needed. Restart preserves suffix without repeating confirmed prefix. Migration preserves issued positions. Bad newest state blocks without fallback; save failure warns while monitoring continues. |

The governed cases/suite are tagged files directly in
`cmd/github-pr-watch-verification/`; inventory is
`cmd/github-pr-watch-verification/testdata/cases.json`. Detailed new sequences
are static Pending in `github_pr_watch_integration_test.go`. Existing
`cases_integration_test.go` and `fixture_helpers_integration_test.go` provide case-local
helpers. Implementation extends their generic cursor pages, completion markers
and stream messages, not shared tooling. Existing I01/I02/I03 helpers also
become capability-aware: negotiate the profile, send null stream/end for a
new watch, collect begin/chunks/end and inspect restored schema semantically
instead of assuming legacy `state_watch`/`checkpoint_result` or v1 fields.
Their non-capacity acceptance expectations remain unchanged. Build the actual plugin using its
existing integration TestMain launcher: substitute only TLS origin/CA and clock
waiting. Keep real comparison, context, Session, hub commands, model action,
filesystem and normal-stop coordination. No production insecure API option,
new dependency, runner or CI setup is required.

Retain the existing two-minute scoped suite budget: 45 seconds shared build,
10 each for I01/I02/I04, 15 for I03, 20 for I05 and 10 cleanup. Run serially with
at most one hub/input/output/TLS service at a time, observable bounded waits,
temporary state and process exit before cleanup. Unit and protocol failure
matrices avoid repeated expensive restarts. Minimal feasibility checks exercise
existing real process wiring plus a small TLS/paging/chunk/file-commit probe;
record actual measurements and limitations in the PR. Prototype results are
mechanism evidence, not passing product acceptance. Pending proves registration
only. Genuine unresolved feasibility prevents design readiness.

Run separate `go run build/mage.go -d build -w . test` and
`go run build/mage.go -d build -w . integrationAll` on existing Ubuntu CI.
The scoped implementation gate is
`go run build/mage.go -d build -w . integration cmd/github-pr-watch-verification/testdata/cases.json <new-report-directory>`.
All five IDs must pass after implementation; Pending/skipped/filtered/missing
cases cannot establish completion. No dependency/build/CI/shared-harness
preparation accompanies this specification.

## Delivery quality check

GPW-D03 maps to FR-003–FR-012, AC-008/AC-010/AC-011/AC-012 and NFR-001–NFR-004.
On github.com and GHES 3.19.x separately, a maintainer runs the actual plugin
through public Session on a PR already above 32 items, establishes a silent
baseline, and adds, edits and removes an old item. Through the real hub/action
and receiving agent, accumulate more than 32 changes, follow bounded prefixes
with explicit rewait, and restart between accepted prefixes without new source
activity. Require all outstanding changes available, no repeat of a confirmed
prefix, explicit body omissions and unchanged destination/authority.

Record revision, host/patch, counts, times, redacted contexts/positions,
represented sequence ranges, remaining counts, committed generation and final
exhaustion. Distinguish trigger behavior, action acceptance and actual agent
assessment. Missing GHES or agent access remains unexecuted evidence; mocks
cannot substitute. Delivery updates plugin usage materials with continuation,
separate budgets, migration and recovery. Execution follows implementation
approval/merge and does not gate implementation completion.

## Risks and rollout

Implementation changes the API reader, tracker, context encoder, schema decoder
and Session adapter plus core streamed storage/protocol. It changes no product
behavior in the design PR. Retain existing review-request and legacy-profile
verification; unrelated later requirements remain separate work.

Full enumeration consumes more API calls. Long inactive waits and large bodies
can exhaust adjustable budgets; preserve progress and expose the error.
Snapshots still share the existing ten-second checkpoint window. A timeout
leaves prior committed state and a continuity warning, never partial success.
The maintainer reviews those costs and migration tradeoffs before approval.
No unresolved product-scope decision is delegated to implementation; unavailable
live host/agent access is a delivery prerequisite owned by the maintainer.

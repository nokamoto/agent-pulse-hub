---
type: Design
title: Independent CI, conflict, and readiness notifications for PR watch
description: Acquire and classify current-head CI results, base-branch conflicts, and Draft-to-Ready transitions without requiring discussion activity.
sources:
  - id: pr-watch-requirements
    resource: requirements/github-pr-watch.md
    title: GitHub pull request watch trigger
  - id: pr-watch-design
    resource: design/github-pr-watch.md
    title: GitHub pull request watch trigger
  - id: rewait-design
    resource: design/pr-watch-rewait.md
    title: Repeated waiting on a retained trigger registration
  - id: capacity-design
    resource: design/github-pr-watch-capacity.md
    title: GitHub PR watch capacity and continuation
---

# Independent CI, conflict, and readiness notifications for PR watch

## Context and authority

This specification extends [PR watch](github-pr-watch.md) for
[requirements/github-pr-watch](../requirements/github-pr-watch.md) FR-013,
AC-013, and AC-014. It owns acquisition, classification, change identity,
current-state content, and verification for CI results, conflicts with the
current base branch, and Draft/Ready for Review status. It is intended for
plugin implementers and verification authors. Its category contracts govern
these signals where the earlier PR-watch design excludes checks or lists
only discussion, head, and lifecycle fields.

The governing PR-watch design retains selection, credentials, scheduling,
discussion categories, and registration ownership. [Rewait](pr-watch-rewait.md)
retains admission generations and the terminal-outcome/checkpoint fence.
[Runtime state](trigger-runtime-state.md) retains core-owned storage. This
specification adds no public RPC, destination selection, agent-assessment
claim, or GitHub write operation.

Acquisition completeness, retained history, notification continuation, and
checkpoint capacity are separate concerns. The
[capacity specification](github-pr-watch-capacity.md) defines FR-012 and
AC-011/AC-012 acquisition budgets, v2 progress, retained positions and prefix
continuation; [streamed runtime state](runtime-state-stream.md) defines its
common checkpoint profile. This document binds signal records to that profile
in [Saved state and positions](#saved-state-and-positions) and
[Bounded notification selection](#bounded-notification-selection). The common
wire protocol, resource controls and rewait operation remain unchanged.

## Requirement coverage

All local IDs below refer to
[requirements/github-pr-watch](../requirements/github-pr-watch.md).

| Requirements | Acceptance | Contract and verification |
| --- | --- | --- |
| FR-003, FR-004, FR-013 | AC-013, AC-014 | Required acquisition, silent baseline, and independent wake rules; S-U01–S-U04, GPWS-I01 |
| FR-005, FR-007, FR-013 | AC-013 | Result/episode identity, current state, and grouped context; S-U02–S-U05, GPWS-I01 |
| FR-006, FR-007, FR-010, FR-013 | AC-014 | Inactive waits, retained episodes, restoration, and explicit positions; S-U05–S-U06, GPWS-I02 |
| FR-008, FR-009, FR-013 | AC-014 | Complete pagination, acquisition errors, unknown conflict state; S-U01, S-U03, S-U06, GPWS-I01/I02 |
| FR-001, FR-002, NFR-002 | AC-013, AC-014 with AC-001/AC-007 host and authentication constraints | Selected-host API origins, credential reuse, redacted errors; S-U01, GPWS-I01, GPWS-D01 |
| FR-011, NFR-001, NFR-003, NFR-004 | AC-013, AC-014 with AC-010 documentation/evidence constraints | External data, unchanged public interfaces, delivery instructions, separate evidence; S-U05, GPWS-I02, GPWS-D01/D02 |

The earlier design and suites retain other category coverage. This document
does not replace their acceptance IDs. It applies the FR-012 contracts to
signal continuation and persistent-position coverage; it does not claim that
the two cases below complete AC-011/AC-012.

## Required acquisition

### Hosts, credentials, and permissions

Keep github.com and GHES 3.19.x, the selected-host process, and credential
precedence from the governing design. The process option `--state-profile`
accepts `signals-v3` (default) or `discussion-v2`; reject other values at
startup. The former implements this specification. The latter retains the
capacity specification's discussion-only behavior and v1-to-v2 migration,
without acquiring or claiming signal coverage. It exists for explicit upgrade
compatibility and regression verification, not as a fallback after a failed
signal acquisition or restore. A process uses one profile for all its watches;
changing profile never implicitly converts saved state. Immutable watch
arguments and registration identity rules are unchanged.

Use only these HTTPS endpoints, with
system certificate verification and redirects disabled:

| Service | GraphQL POST endpoint | REST GET prefix |
| --- | --- | --- |
| github.com | `https://api.github.com/graphql` | `https://api.github.com/repos/` |
| Selected GHES host | `https://<host>/api/graphql` | `https://<host>/api/v3/repos/` |

The REST addition is limited to commit-status enumeration. Construct paths
from validated owner/repository segments and a validated head SHA; do not
fetch API-supplied details URLs. Set `Accept: application/vnd.github+json`
and `X-GitHub-Api-Version: 2022-11-28` for REST. Both services document that
version; an unsupported/retired version is an explicit capability error,
never a successful empty result. GraphQL has no equivalent version header.
See [github.com versions](https://docs.github.com/en/rest/about-the-rest-api/api-versions)
and [GHES versions](https://docs.github.com/en/enterprise-server@3.19/rest/about-the-rest-api/api-versions).

Use the same selected credential for both APIs. Keep the declared classic
PAT or saved `gh` OAuth profile: `public_repo` for public repositories,
`repo` for private/internal repositories, and any required organization/SSO
authorization. These scopes can permit writes; the plugin performs only
GraphQL queries and REST GETs. Fine-grained tokens and GitHub App tokens are
not added to the supported credential profile. REST documents separate
`Checks: read` and `Commit statuses: read` permissions, but those tables
alone do not establish GraphQL field coverage. Repository access alone also
does not prove CI visibility. Acceptance must verify the selected fields and
status endpoint on each supported host and repository visibility in delivery.
See [GraphQL authentication](https://docs.github.com/en/enterprise-server@3.19/graphql/guides/forming-calls-with-graphql),
[check-run permissions](https://docs.github.com/en/enterprise-server@3.19/rest/checks/runs#list-check-runs-in-a-check-suite),
and [status permissions](https://docs.github.com/en/enterprise-server@3.19/rest/commits/statuses#list-commit-statuses-for-a-reference).

Watch acceptance validates these additional capabilities without establishing
a baseline or notifying. An error or unexpected null is not evidence that
no checks/statuses exist. Do not fall back to another credential, an aggregate
result, another API, or a reduced category set after an access failure.

### PR and check-run fields

Each round reads the selected repository's stable `id` and PR `id`, `url`, `state`, `headRefOid`,
`baseRepository.id`, `baseRefName`, `baseRefOid`, `baseRef { id target { oid } }`,
`mergeable`, and `isDraft`, alongside the existing categories. Base identity
includes repository, ref name, and revision; a retarget is not merely a
timestamp change. `baseRefOid` remains available for a deleted ref, so a null
`baseRef` does not establish a currently existing base branch. Report the
base as unavailable in that case, retaining the last confirmed conflict
episode without claiming a new conflict. See the
[PR schema](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/pulls#pullrequest).

Pin CI acquisition to the observed head SHA H, using the selected PR's
repository object for that commit. Enumerate `Commit.checkSuites(first:25)`
with independent cursor pagination, then every suite's
`checkRuns(first:25,filterBy:{checkType:ALL})`. Acquire suite `id`, `app.id`,
`commit.oid`, and each run's `id`, `name`, `status`, `conclusion`, `startedAt`,
`completedAt`, `detailsUrl`, and `url`. Reject a returned commit that differs
from H as inconsistent acquisition; do not relabel an older-head result.
An empty `pull_requests` association is not a reason to discard a run: fork
pushes can lack that association.

Also enumerate `checkRuns(filterBy:{checkType:LATEST},first:25)` for each
suite to identify its currently selected runs, rather than guessing recency
from an opaque ID, response order, name, or nullable timestamps. Preserve
distinct run IDs even when their names match. The ALL and LATEST sets must
agree on shared run fields, and each LATEST ID must occur in ALL; otherwise
reject the round and retry. This is a consistency check, not an atomic
snapshot guarantee. See the [Commit schema](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/commits#commit)
and [CheckRun, CheckRunType, and CheckRunFilter](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/checks).

Do not use suite conclusion, rollup state, required-check filtering, or a
workflow-wide aggregate as the failure inventory. They cannot identify all
individual failures and reruns. REST's direct ref check-run list also has a
documented latest-1,000-suite limitation; this design uses the paginated
GraphQL suite/run connections instead. GitHub can remove old check runs;
the observed-change guarantee does not recover a result removed before any
successful observation. See [check-run API limits](https://docs.github.com/en/enterprise-server@3.19/rest/checks/runs).

### Commit-status enumeration

GET `/repos/{owner}/{repository}/commits/{H}/statuses?per_page=100&page=1`.
Follow all documented `Link` next pages. Validate that each next URL uses
the selected HTTPS authority and exact status endpoint with the same H and
page size, no credentials/fragment, and a strictly advancing page number.
Reconstruct the validated request locally; never forward credentials to an
arbitrary Link URL. Missing/malformed required pagination, repeated pages,
conflicting duplicate IDs, and partial responses fail the round.

Acquire each REST status record's `id`, `context`, `state`, `created_at`,
`updated_at`, `target_url`, and description. The requested H is its commit
provenance. Statuses are returned in reverse chronological order: for current
state, select the first record for each context across the complete list.
Do not sort solely by timestamps or combine checks with statuses sharing a
name. Retain individual status IDs for observed failure identity; `context`
is the named status channel, not a unique attempt ID. A missing target URL
is an explicit unavailable details link. The combined-status endpoint and
GraphQL rollup alone are not the record inventory. See
[status enumeration and combined-state semantics](https://docs.github.com/en/enterprise-server@3.19/rest/commits/statuses).

### Complete observations and errors

The existing round/request deadlines and decoded response limits apply to
the combined GraphQL and REST acquisition. Exceeding them is incomplete
coverage, with no snapshot or confirmation advancement. Use the capacity
specification's combined byte accounting and 300-second round deadline;
these limits cannot justify silent partial observation or a lifetime item cap.

Read PR identity/head/base again after enumerating categories. If identity,
head, base repository/ref/revision, or base availability changed, reject the
candidate round as `observation_changed_during_acquisition` and retry on the
normal schedule. Use the final PR readiness and mergeability only when that
pair is unchanged. A PR read or required CI/status acquisition failure fails
the whole round, even when another category has a qualifying change.

GraphQL errors (including HTTP 200 with partial data), unexpected null
required connections/fields, cursor failures, REST non-success responses,
invalid enum values, and malformed JSON are retrieval failures. Explicit
`mergeable=UNKNOWN` and nullable details/timestamps are valid values, not
these errors. Distinguish credential rejection, permission denial,
not-found/inaccessible resource, unsupported API, pagination/inconsistency,
rate limit, timeout, and resource-limit errors. Inspect rate-limit headers
and structured messages before treating 403 as permission denial; private
resources may be concealed by 404. Keep the last complete logical progress.
Use the governing retry schedule and server guidance. Diagnostics contain
no token, query body, private body, context, or full position. See
[GitHub error guidance](https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api).

## Classification and change identity

### CI result classes

Classification is a design decision, not an inference from an aggregate
merge gate. These are the complete supported result tables:

| Check conclusion with `status=COMPLETED` | Failure class | Independent wake eligibility |
| --- | --- | --- |
| `FAILURE`, `STARTUP_FAILURE`, `TIMED_OUT`, `ACTION_REQUIRED` | Failure | A newly observed failed result version after the baseline |
| `CANCELLED`, `STALE`, `NEUTRAL`, `SKIPPED`, `SUCCESS` | Non-failure | None from this category alone |
| null | Inconsistent completed result | Acquisition failure; no coverage advancement |

`ACTION_REQUIRED` is included because the check reports an explicit
unsatisfied action result. Cancellation and staleness are not called CI
failures; retain their exact values so the receiving agent can assess them.
For `PENDING`, `QUEUED`, `REQUESTED`, `WAITING`, and `IN_PROGRESS`, the run is
non-final and not failure-eligible, even if an earlier conclusion remains
visible. Preserve the exact status and available conclusion in current state.
Unknown statuses/conclusions fail capability coverage rather than becoming
success. These values come from the
[check enums](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/checks#checkconclusionstate).

| REST status state | Failure class | Independent wake eligibility |
| --- | --- | --- |
| `error`, `failure` | Failure | A newly observed failed status record after the baseline |
| `pending`, `success` | Non-failure | None from this category alone |
| Any other value | Unsupported result | Acquisition failure; no coverage advancement |

Do not import GraphQL-only `EXPECTED` as a REST result. An empty, successfully
enumerated CI inventory means no observable results, not successful CI.

### Failure versions, reruns, and superseded results

A run's stable source identity is `(host, repository ID, H, CheckRun, run ID)`.
Its result version includes status, conclusion, startedAt, and completedAt,
using explicit nulls. A newly observed run in a failure class, or a changed
failure result version for the same ID, is eligible. Compare the whole tuple:
a name, link, description, response ordering, or observation-time change alone
does not wake. A new status record uses `(host, repository ID, H, Status,
status ID)` and its exact state; an impossible conflicting duplicate record
within a round fails acquisition.

Use the registration's global change sequence as each observed failure
episode's identifier and retain its source identity. An identical failure
repeats no episode. An observed non-failure
followed by the same failure tuple creates another episode, so a recovery
does not permanently suppress recurrence. A failed result version with new
start/completion provenance is a visible new failed result even without an
intervening non-failure observation; retain its previous and new provenance.
Call this a result-version change, not proof that the provider reran work.
Timestamps supplement stable identity and result classification; they are
not identity or a general recency comparator.

GitHub permits an App to reset/update an existing check-run ID after a
rerequest; a new ID is not guaranteed. The rerequest itself changes the suite
but need not change the run. Consequently, suite timestamp/state changes
alone do not create an individual failure episode. An overwritten attempt
whose identity, result, and execution provenance remain identical, with no
intermediate state observed, is outside the observable guarantee. See
[rerequest behavior](https://docs.github.com/en/enterprise-server@3.19/rest/checks/runs#rerequest-a-check-run).

ALL runs and status records present at the complete initial baseline are
already known and remain silent. Repeated pagination cannot rediscover them
as new failures. A newly observed failed record/version associated with H
remains eligible even if a later attempt is already non-failure: report it
as a superseded observed failure and show the current selected result
separately. This preserves an acquired failure without presenting it as the
current selected failure. A result for a head other than the final H is not
eligible as a current-head failure. Retained earlier-head episodes can still
appear as historical changes after a supplied position, explicitly labelled
with their head and `current_head: false`.

### Conflict state and compared revisions

The conflict pair is `(PR ID, H, base repository ID, base ref name, base SHA)`.
The base SHA is the current `baseRef.target.oid`; require agreement with
`baseRefOid` when the ref exists. Treat mismatch as an inconsistent round.
Read this pair every round even when head and discussion are unchanged.

| API state | Current interpretation | Episode effect |
| --- | --- | --- |
| `CONFLICTING`, existing stable base pair | Confirmed conflict reported by GitHub | Create an episode on the first confirmation in the current pair period, or after an observed `MERGEABLE` resolution in that period |
| `MERGEABLE`, existing stable base pair | No conflict reported by GitHub | Record resolution and permit a later recurrence; no independent wake |
| `UNKNOWN` | Calculation pending | Keep a valid snapshot with explicit unknown state; no conflict wake, no inferred resolution |
| Missing base ref | Current base unavailable | Preserve available identity/provenance and prior confirmation separately; no new conflict claim |
| Retrieval error or unsupported value | Acquisition failed | Reject the round; retain prior complete progress |

A pair period is the interval of complete observations with the same comparable
head/base pair. The first comparable pair starts period 1; each observed change
from the last comparable pair increments the period, even when the pair was
seen in an earlier period. UNKNOWN retains the period and its confirmation
flag; a missing base retains the last comparable pair without confirming or
resolving it. A failed round changes neither. MERGEABLE clears the confirmation
flag; CONFLICTING sets it. A new period starts with the flag clear.

Unknown is a valid baseline value. UNKNOWN-to-CONFLICTING after that baseline
is eligible. CONFLICTING-to-UNKNOWN-to-CONFLICTING for the same pair creates
no duplicate. A complete initial CONFLICTING baseline sets the flag silently.
A later confirmed A-to-B-to-A sequence creates an episode for B and another
for the returning A, even without MERGEABLE between them: these are different
pair periods, not unchanged observations. A base-only push or retarget can
start a period. Never copy a confirmation across periods. Store the period,
pair and confirmation flag together; no unbounded set of all past pairs is
needed for suppression. Journal episodes retain their own pairs and periods.

Only `mergeable=CONFLICTING` establishes conflict. Do not infer it from
`mergeStateStatus=BLOCKED/BEHIND/UNSTABLE`, failed CI, or an unknown value.
GitHub calculates mergeability asynchronously. Re-reading an unchanged pair
prevents mixing different revisions in one observation; the API does not
expose a separate calculation revision proving when that result was computed.
Present this as GitHub's observed report for the acquired pair, with
observation time, not an independently executed merge test. See
[MergeableState](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/pulls#mergeablestate)
and [background calculation](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request).

### Readiness transitions

Use the non-null `isDraft` field, not deprecated merge-state `DRAFT`.
The complete baseline records Draft (`true`) or Ready for Review (`false`)
silently. Each observed true-to-false transition creates a new readiness
episode; false-to-true updates current state and rearms a later transition
without independently waking. Repeated false creates no episode. Include
previous/current values and PR URL. No review request, second actor, open
review, comment, or head change is required. A failed observation cannot
replace the previous value. Closed/merged lifecycle reporting remains in
the governing design; readiness alone does not imply merge permission.

## Notifications, waits, and logical progress

Commit a complete candidate observation and its new episodes together.
No-position activation establishes the first complete baseline silently,
including existing failures/conflicts and Ready status. While a notification
generation is active, any one eligible episode independently requests
destination work. Group all eligible changes available during preparation
under the governing batch/continuation contract; do not wait for discussion,
an actor change, or another category. Non-failure CI changes, resolution,
and Ready-to-Draft alone do not request work, but update current state.

A consumed generation stops emission, not acquisition. Keep qualifying
episodes observed while the agent works, including a failure later recovered,
a resolved conflict, or readiness later changed back. Rewait from P1 must
expose eligible retained episodes after P1 without new GitHub activity.
Delivery acceptance and restart suppression remain separate from the caller's
assessment position. A successful normal checkpoint/restore preserves
episodes and confirmed delivery coverage: unchanged saved failures do not
wake solely because restart grants a generation; a later fresh episode can.
Apply the existing rewait admission and checkpoint fence, not a second poller
or category-specific notification generation.

Every context remains external UTF-8 JSON under the public 8,192-byte limit,
using the governing `changes`, `current`, `omissions`, observation times,
PR identity, waiting context, and confirmation position. Logical category
fields are:

| Category | Required changed record | Required current state |
| --- | --- | --- |
| CI | Source kind, stable ID, name/context, result class and exact result/status, head SHA, episode/result version, execution provenance, available details URL, superseded/current-head labels | H; observable selected check runs and latest status per context; exact non-failure/pending values; no-results/unavailable distinctions |
| Conflict | Episode, previous/current state, H, base repository/ref/SHA, PR link and observation time | Current pair and exact confirmed/mergeable/unknown/unavailable state; prior confirmation labelled historical when useful |
| Readiness | Episode, previous/current Draft/Ready status, PR link and observation time | Current Draft/Ready status alongside lifecycle |

Unavailable optional attributes use null plus a reason. Check URLs and status
target URLs are external data, never instructions or automatic fetch targets.
No missing information is fabricated. Represent each changed identity and
provenance before advancing past it. Where full current inventories or other
content do not fit, explicit omissions and continuation must identify affected
categories/items; a summary must not claim complete CI success. Apply the
represented-prefix selection below. Do not introduce a private signal cap,
silently drop episodes, acknowledge unseen records, or use the earlier
all-or-nothing capacity errors as the FR-012 continuation design.

### Saved state and positions

Require `runtime_state_v2`, `rewait_v1` and `runtime_state_stream_v1`. Write
schema `github-pr-watch-v3`; its header has `kind: "header"`, `version: 3`
and `signal_version: 1`. This plugin-specific schema changes no wire message.
Use the capacity specification's ordered header, items and changes objects,
32,768-byte compact object bound and complete-generation validation. The
header retains scope, nonce, observation/change sequences, times, item/change
counts, retained range, issued positions, comparison position, and attempted/
confirmed progress. No second cursor or category-specific position is added.

Discussion records retain their capacity semantics. Signal items use these
category keys; IDs are stable strings and are unique within their category:

| Category and ID | Required saved comparison data |
| --- | --- |
| `ci_check`, run ID | Selected repository ID and head; suite/app identity; name; exact status/conclusion; nullable execution times and links with reasons; ALL/LATEST membership; complete result version; last eligible episode sequence, or zero for baseline-only knowledge. |
| `ci_status`, REST record ID as decimal string | Selected repository ID and head; context, state, times, nullable target and description; whether it is the current record for its context; last eligible episode sequence or zero. |
| `signal_conflict`, PR ID | Latest observed availability/state; last comparable pair; uint64 pair period; confirmation flag; last eligible episode sequence or zero. Keep the last comparable pair through UNKNOWN or missing-base observations, as defined above. |
| `signal_readiness`, PR ID | Last complete `isDraft` and last eligible episode sequence or zero. |

Store nullable values explicitly. Before the first complete baseline, omit
signal items and mark the header's baseline as false; after it, require the
two scalar signal records and all acquired CI records, including non-failures.
CI items describe only the latest complete head's inventory. On an observed
head change replace that inventory atomically; results on a returning head
are newly observed in that head period and can qualify again. An ID absent
from one complete inventory and later reappearing is likewise newly observed.
Historical journal entries preserve their original head and result; they do
not masquerade as current items. This avoids a lifetime archive of CI IDs.

Each qualifying signal appends one `changes` record with the global sequence,
category/ID, observed time, episode sequence (equal to that global sequence),
previous/new state and the required provenance from the context table. Assign
sequences for all categories together, sorting simultaneous changes by category
then stable ID. Only eligible signals enter this journal; recovery, resolution
and Ready-to-Draft update comparison items without consuming a change sequence.
Observation progress still advances on every complete round. A future recurrence
receives a new global sequence even when its result tuple equals an older one.
Do not encode signal data as an unbounded header field or split a semantic
record between objects. Exact internal field names beyond those specified here
remain implementation choices under these required semantics.

The observation byte budget counts the complete combined response acquisition
and, independently, the compact discussion-plus-signal snapshot. The history
budget counts the combined journal. Preserve the capacity specification's
64-MiB defaults, configurable byte controls, 300-second round and ten-second
request deadlines, page limit and named resource errors. Core stream accounting
covers all objects under the configured total-generation budget. An oversized
mandatory item/header fails with `item_metadata_exhausted`; never truncate IDs,
result versions or pair state to make it fit. Shorten only optional descriptions
or body excerpts with digest and explicit omission information. Their complete
values are hashed before shortening and are not failure-eligibility inputs.

Use the existing `gpw1.` nonce/sequence representation. Retain the latest four
issued positions and pin the comparison position; prune only a delivered prefix
not required by any retained position or confirmed/comparison cursor. The same
rules retain unreported discussion changes and signal episodes. A checkpoint
is one immutable cut of comparison items, journal and progress after eligible
delivery notices. Restore validates schema/version, scope, counts, category/ID
uniqueness, ordering, contiguous journal sequences, period/episode bounds and
all position references before applying any state. A false baseline cannot
contain signal comparison records; a true one cannot lack them. Reject a
selected CI item with the wrong head or conflicting current status membership.
Failed save/restore uses the common warning or watch rejection without partial
application. Counters never wrap.

### Bounded notification selection

Use the capacity specification's contiguous represented-prefix algorithm,
at most eight changes and 8,192 decoded UTF-8 bytes per notification. Reserve
identity/waiting context, times, position, continuation and a minimal current
summary first. Select the oldest eligible entries in sequence order; never
skip a large signal to send a later discussion entry. Required changed
identities, exact failed results, heads/pairs and provenance are not shortened.
If one entry and minimal context cannot fit, report
`notification_metadata_exhausted` without consuming the wait or issuing a position.

The minimal current summary contains head/lifecycle, Draft/Ready, the current
conflict state and pair (or unavailable reason), plus CI totals grouped by source
kind and exact current result/status. These finite result groups distinguish
no results from success, pending and failure. Include the latest complete
observation time and any failed refresh reason. Details for selected changes
identify whether their source is still current, superseded, absent or on an
older head; non-failure current values remain exact. Spend remaining space on
current item details in category/ID order, with represented IDs, omitted counts
and the PR link for each shortened list. Current-list omissions do not advance
or create change positions and do not create an endless current-only backlog.

Set `confirmation_position` and `continuation.through_sequence` to the last
represented change; retain the exact suffix. The other continuation fields
(`has_more`, `remaining_changes`, `observed_through_sequence`, and conditional
`operation: "rewait"`) have the capacity semantics. Each explicit rewait sends
the next prefix with no new source activity required, even after acquisition
failure. A normal restart may send the outstanding suffix from confirmed
delivery progress but never repeats its confirmed prefix solely due to restart.

## Compatibility and rollout

Signal-bearing saved data requires an explicit signal schema version and
validation of the entire logical progress. A payload without that version,
including `github-pr-watch-v1` or a discussion-only `github-pr-watch-v2`
payload, cannot prove a baseline or
position coverage for these categories. Reject it before activation with
`signal_state_incompatible`; preserve the saved state and diagnose the
affected registration. Reject incompatible explicit positions through the
existing position-rejection path. Do not fill absent signal fields with
empty/success values, silently establish a new baseline, or accept an earlier
position as if it covered the extension.

No lossless conversion from absent historical signal coverage is promised.
An operator may preserve the old state and use a compatible binary, or
explicitly follow the existing
[offline reset/recreation procedure](trigger-runtime-state.md#offline-reset-deletion-and-recreation),
accepting loss of continuity. New registrations use a new nonce and cannot
inherit old positions. Rollback to a binary that cannot understand the new
state must reject it visibly rather than overwrite it. V3 restoration preserves
its nonce, issued positions and all combined progress. V1-to-v2 migration remains
available in the discussion-only profile; it cannot synthesize missing signal
history for v3. This explicit incompatible upgrade is chosen over pretending
that old positions cover previously unobserved categories.

The existing GPW-I01–I05 expectations remain required for discussion, capacity
and common-state behavior. Run that scope with the same binary explicitly using
`--state-profile=discussion-v2`; GPWS-I01/I02 use `signals-v3`. The former's
legacy-migration branch verifies v1-to-v2 preservation; GPWS-I02 separately
verifies v3 rejection of state without signal history. Fixtures must request
the intended profile, not weaken either expectation. A v3 generation is also
incompatible with `discussion-v2`; switching profiles preserves the stopped
state and fails visibly rather than erasing the other profile's progress.
No migration or live-service decision is delegated to implementation.

## Decisions and alternatives

| Decision | Alternatives and tradeoffs |
| --- | --- |
| GraphQL ALL runs plus separate LATEST membership; REST status records | GraphQL rollup alone reduces calls but does not document complete status-attempt history. REST-only acquisition still needs suite enumeration to avoid the direct-ref limit and cannot replace existing review-thread relationships. Mixed reads add pagination and consistency costs while preserving individual identities. |
| Failure result versions include execution provenance | New-ID-only detection misses supported same-ID updates; timestamp-only recency is unreliable. This records a visible new failed result without claiming a rerun occurred. Indistinguishable overwritten attempts remain outside polling guarantees. |
| Unknown conflict is valid current state | Rejecting all UNKNOWN rounds would unnecessarily block independent CI/readiness changes. Treating unknown as conflict or resolution would invent evidence. Keep current uncertainty and confirmed episode identity separately. |
| Each observed pair period can qualify | Watching only head misses base-only conflicts. Remembering all prior pairs would suppress A-to-B-to-A changes and grow historical suppression state. Period-local confirmation detects each return while UNKNOWN within a period does not duplicate it. |
| Explicit incompatibility for absent signal history | Automatically adding a silent baseline would misrepresent existing positions. A distinct v3 schema preserves old progress and makes a deliberate reset or compatible v2 restoration visible, at the cost of an explicit upgrade step. |

## Verification strategy

Implementation supplies mocked-capability unit tests; no live service,
subprocess, server, or filesystem is needed for the broad matrices. Retain
integration only for actual Session/network serialization and assembled
rewait/checkpoint/restart boundaries. Reuse existing TLS transport injection,
public Session launch, real hub/client/model-output, and temporary-state
fixture mechanisms; do not add a runner, framework, dependency, CI job, public
test-only API origin, or insecure TLS mode.

The design-only `encoding_feasibility_integration_test.go` in the governed
suite measures a fixed 128-CI-record, nine-change snapshot: multiple bounded
JSON objects, real temporary-file readback, and predefined eight/one context
prefixes. Its `TestSignalEncodingFeasibility` entry point is separate from
acceptance registration. It establishes size/round-trip feasibility, not a
product serializer, selection algorithm, core commit, or acceptance outcome.
Combined with the existing TLS/Session fixture, this requires no dependency
or shared-harness preparation. Actual plugin selection, stream validation and
durable prefix/suffix behavior remain required in GPWS-I02.

### Unit matrix

| ID | Inputs and decisive assertions |
| --- | --- |
| S-U01 | Every GraphQL suite/run cursor and REST status page, empty inventories, duplicate/reordered records, malformed/foreign Link, ALL/LATEST mismatch, head/base race, HTTP-200 errors, nulls, 401/403/404/410/429/5xx, deadline/byte limits and selected-host token markers. Failed acquisition never mutates complete progress/coverage, never means successful CI, and emits redacted distinct errors. |
| S-U02 | Every result/status enum and unsupported value; same-name distinct IDs, same-ID unchanged result, changed failed execution provenance, link/name-only edits, new failed status records, current selections versus superseded failures, older-head inputs, observed recovery/recurrence and baseline failures. Assert exact eligibility and source/head labels. |
| S-U03 | Existing/unavailable base, all mergeable enums, unknown baseline, unknown-only repetition, CONFLICTING/UNKNOWN/CONFLICTING, MERGEABLE recurrence, base-only SHA change, retarget, pair mismatch and confirmed A-to-B-to-A. Assert period-local suppression, a fresh returning-A episode, and no invented conflict/resolution. |
| S-U04 | Draft and Ready baselines, both transition directions, repeated status, failed intermediate observation, and repeated true/false cycles with unchanged actor/discussion/head. Assert only observed Draft-to-Ready episodes qualify. |
| S-U05 | Each category alone and combined; external marker strings; changed/historical/current distinctions; complete required identities and nullable links; consumed generation followed by rewait without activity; explicit position versus action acceptance. Assert no category depends on discussion or agent assessment and no position acknowledges omitted changes. |
| S-U06 | V3 header/items/changes validation, stream/object budgets, duplicate IDs, inconsistent membership/counts, false/true baseline shape, signal version, profile selection, old/foreign positions, contiguous mixed-category sequences and retained-prefix boundaries. Restore before/after an accepted prefix and after state-only recovery; assert identical remaining suffix, comparison flags and next episode identity. Fail saving and acquisition independently. V1/v2 cannot restore in signals-v3; discussion-v2 retains its approved migration and rejects v3. No silent reset, lost inactive-wait episode or restart-only duplicate. |

### CI acceptance cases

The governed suite is `cmd/github-pr-watch-signals-verification/`; its suite
and case files are directly there with `//go:build integration`. Its inventory
is `cmd/github-pr-watch-signals-verification/testdata/cases.json` and contains
exactly GPWS-I01 and GPWS-I02 for this extension. Design supplies static
Pending cases without nonexistent production-symbol references. Product
wiring and full assertions follow in implementation; Pending registration
does not verify product behavior.

| ID and mapping | Concrete inputs and actions | Observable expected results and failure boundaries |
| --- | --- | --- |
| GPWS-I01; FR-003–FR-005, FR-007–FR-009, FR-013; AC-013/AC-014 | Launch actual plugin through public Session using test-owned verified TLS for GraphQL and REST on selected hostnames. Use acme/widget PR 17, H1/B1, Draft, one successful check/status and unchanged discussion. Fail an initial CI page, then complete and repeat the silent baseline. In independent branches with active generations, fail the H1 check and separately status, move only base to B2 with UNKNOWN then CONFLICTING, and change Draft to Ready. Repeat unchanged; supply an older-head run; independently fail a required status page and PR/conflict retrieval, then recover with the pending change. | No preactivation or baseline event, including after failed initial coverage. Each category alone emits a bounded changed record, current state, PR identity and position; unchanged, unknown-only, older-head-only and non-failure-only rounds do not emit. Failed pages/read reject complete advancement; recovery preserves and reports the outstanding eligible change. Requests use only selected origins/token; no secrets occur in context/logs. Use checkpoint inspection and Session notices to verify progress, not timing guesses. |
| GPWS-I02; FR-004–FR-010, FR-013; AC-013/AC-014 | Run the actual hub/client/plugin/model-output with signals-v3, 128 CI records carrying 256-byte metadata, and existing failed CI/conflict/Ready for a silent baseline. Recover silently, add conversation comment C1 and accept P1. While consumed, perform three failure/new-base-conflict/Draft-to-Ready cycles, each followed by recovery, retaining nine small signal entries. Freeze GitHub. Rewait P1; accept an eight-entry prefix, checkpoint >32 KiB, stop and restart before the suffix. Then rewait the fully covered P2 unchanged and introduce fresh episodes. Independently restore v1 and discussion-only v2 state. | First prefix ends at P1+8 with has_more=true, remaining_changes=1 and tail P1+9. Every context is <=8,192 bytes, current omitted lists have counts/links, and comparison state records the recovery. Restart delivers only the ninth entry, ending at P2=P1+9; no new source activity, lost suffix or repeated confirmed prefix. Stream objects are <=32,768 bytes and frames <=65,536. Rewaiting P2 unchanged is silent; fresh episodes remain eligible. Destination B is untouched, nonce/positions persist, old schemas block before activation without changing saved data or unrelated registrations, and acceptance is not agent assessment. |

For I01 the real plugin HTTP adapter, JSON parsing, comparison, context, and
public Session run; only GitHub is substituted. I02 adds real hub/client,
model-output routing, core state files, and normal process restart. Test-only
transport injection directs the canonical HTTPS authority to loopback while
retaining hostname/CA verification, using the existing fixture mechanism.
These cases test different assembled boundaries; broad enum/actor/attempt
permutations stay in cheaper unit tests.

Extend case-local launchers to pass the explicit process profile, negotiate
`runtime_state_stream_v1`, send null stream/end for a fresh watch and collect
begin/chunks/end for checkpoint inspection. Configure the local HTTPS fixture
with GraphQL cursor responses and REST status `Link` pages on the same trusted
origin. Reuse completion barriers to freeze the nine-change source. For I02,
accept the first prefix before requesting a quiescent checkpoint; inspect its
schema and retained journal, then restart the same core state directory. The
core's restored confirmed cursor, rather than a fake notification ledger, must
account for the eight/one split. Schema-rejection branches use stopped copies
of that directory and replace only the opaque saved payload with a valid
discussion-only fixture; retain the common ownership/manifest envelope.

Build binaries once per suite. The scoped budget is 60 seconds setup,
15 seconds I01, 25 seconds I02, and 10 seconds cleanup: 110 seconds total,
within 120 seconds. Run serially with temporary directories, at most one
hub, input, model output and TLS service; kill and await children and close
servers on failure. Use fixture round barriers and observable output/state
deadlines, not production polling sleeps. Existing transport/Session fixture
feasibility supports reuse; its execution timings and remaining limits belong
in the design PR and do not establish these product acceptance outcomes.

Run unit and integration results separately on the existing Ubuntu CI:

```sh
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
```

The implementation's separate scoped completion command is:

```sh
go run build/mage.go -d build -w . integration cmd/github-pr-watch-signals-verification/testdata/cases.json <new-report-directory>
```

Both approved IDs must pass; Pending, skipped, missing, filtered or deleted
cases do not satisfy completion. Existing approved PR-watch/rewait scopes
must also pass on the integrated implementation. Design checks compile and
register tagged cases and run guardrails, without claiming acceptance.

### Delivery quality checks

| ID and mapping | Actual stimulus and expected result | Evidence and evaluator |
| --- | --- | --- |
| GPWS-D01; AC-013/AC-014, FR-001/FR-002/FR-009/FR-011/FR-013, NFR-002/NFR-004 | On github.com and GHES 3.19.x, use maintainer-owned PRs and the supported selected-host credential profile. Establish silent baselines with existing CI/conflict/Ready state. Independently fail current-head checks and statuses, rerun a failed check, change only base to create conflict, and convert Draft to Ready without discussion/head changes. Repeat unchanged, recover and recur, observe calculation pending where available, and verify a failed required retrieval cannot advance progress. | Maintainer records host/patch/API version, repository visibility, credential source/scopes without secrets, actual field/endpoint capability and permission evidence, stimuli/observations, head/base SHA, source/run/status IDs and execution provenance, current versus superseded results, contexts/positions and limitations. Missing host/category access remains unexecuted; mock or pending evidence cannot replace it. |
| GPWS-D02; AC-014 with AC-010, FR-005–FR-007/FR-010/FR-011, NFR-001/NFR-004 | Through real hub, trigger, action and receiving agent, observe each signal while the agent works, rewait from P1 without further service activity, and perform successful normal save/restart followed by a fresh signal. Include non-failure current state and any continuation/omissions from the integrated contract. | Maintainer records separate standalone-trigger results, action acceptance, actual agent act/wait decision, retained-position continuity, same destination, restart behavior and explicit external-data limits. README explains classification, permissions, polling/attempt limits, unknown states, rewait and deliberate incompatible-state recovery. |

Execute these checks in delivery after approved implementation merge. They
are not design or implementation live-service execution requirements.

## Change and rollout impact

The plugin adds REST read acquisition, additional GraphQL fields and explicit
state-profile selection; its
selected arguments, executable/example paths, public Session and action
destination stay governed by the earlier design. Update plugin developer
contracts to follow this specification and the unchanged common stream/rewait
contracts. Delivery updates the lowercase plugin README and
examples where necessary. No service-neutral hub schema is allowed to learn
GitHub result enums or actor policy.

No implementation-blocking design decision remains open. This design still
requires human approval and merge before implementation. Capability
documentation is evidence for the proposed API surface,
not live host acceptance. Maintainer-owned GHES access and receiving-agent
credentials are delivery prerequisites. No supported-host reduction,
unlimited-history promise, automatic state reset, or additional OS constraint
is introduced by these cases.

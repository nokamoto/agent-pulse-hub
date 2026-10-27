---
type: Design
title: GitHub pull request watch trigger
description: Observe a selected pull request and resume an explicit wait with bounded review context and retained confirmation positions.
sources:
  - id: pr-watch
    resource: requirements/github-pr-watch.md
    title: GitHub pull request watch trigger
  - id: bundled-plugins
    resource: requirements/bundled-plugins.md
    title: Bundled trigger and action plugins
  - id: runtime-state
    resource: requirements/trigger-runtime-state.md
    title: Trigger runtime state persistence
---

# GitHub pull request watch trigger

## Context and scope

The `github-pr-watch` input plugin observes one explicitly selected pull
request. It reports changes and current review context without deciding whose
turn it is. The receiving agent decides whether to act or wait again under its
existing instructions. This design implements
[the PR-watch requirements](../requirements/github-pr-watch.md); it does not
discover PRs, fetch diffs/checks/source files, or write to GitHub.

A registration remains a continuing monitor. Its notification wait is separate:
one notification consumes a wait, while observation continues. The caller
reuses the same registration and explicitly returns a confirmation position
to wait again. Normal daemon restart starts another notification wait using
saved delivery progress. Thus a fresh change after restart can notify without
a new client request; an unchanged accepted notification does not repeat.
This restart behavior is intentional and must appear in usage instructions.

The [rewait control design](pr-watch-rewait.md) owns the shared client and
plugin control extension. This design owns GitHub arguments, observations,
positions, context, and verification. Individual cancellation and registration
replacement are not added. Existing plugins retain their current behavior.

## Architecture and responsibilities

The daemon owns registration identity, destination, notification admission,
wait generations, and state storage. The plugin owns GitHub authentication,
queries, bounded observations, comparison, context construction, and its
opaque saved payload. The action owns delivery acceptance. Neither delivery
acceptance nor a position proves that the agent assessed the content.

Place the executable at `plugins/triggers/github-pr-watch/main.go` and its
domain/application/adapters under the existing Go placement conventions.
Implementation supplies `examples/config.json`, `examples/watch-args.json`,
and `examples/destination-args.json` in that plugin; delivery supplies the
lowercase `readme.md` with author and reviewer workflows. Destination examples
identify an existing action and operator-supplied recipient rather than
embedding a Codex-specific field in this trigger. A plugin-specific skill is
not necessary: the ordinary registration and rewait commands are the workflow.

The plugin participates in runtime-state v2 and the optional rewait capability
specified by the shared design. It does not poll before activation. It uses
no private state files, background service, webhook, or external checkpoint
store. The hub remains unaware of GitHub schemas.

## Interfaces and data

### Selection and authentication

Immutable watch arguments are an object with exactly these fields:

| Field | Validation and meaning |
| --- | --- |
| `host` | Lowercase DNS hostname, without scheme, port, path, credentials, or trailing dot; `github.com` selects the public service. |
| `owner`, `repository` | Nonempty GitHub path segments, at most 100 UTF-8 bytes each; no slash, dot-only segment, control character, or percent-encoded separator. |
| `pull_number` | Positive JSON integer through 2,147,483,647. |
| `waiting_reason` | Nonempty text, at most 512 UTF-8 bytes. |
| `represented_actor` | Nonempty caller-supplied person or role description, at most 128 UTF-8 bytes. It is not an authenticated identity. |

Reject unknown fields, absent fields, invalid UTF-8, duplicate keys, and
oversized input. A confirmation position is an explicit rewait operation
input, not a mutable watch argument. The same PR can have independent
registrations for different destinations or waiting contexts. No registration
implicitly reads another registration's history.

Configure one plugin process per host with `--host <hostname>`; watch `host`
must match that process. The process selects the first nonempty `GH_TOKEN`,
then `GITHUB_TOKEN`, otherwise runs `gh auth token --hostname <host>` without
a shell or interactive login. Bound the child by the shared watch deadline,
limit its captured output to 8 KiB, and never print its output. A rejected
selected credential never falls back to another source. Environment-token
users need no `gh` installation. Different hosts use separate configured
processes and host-specific environment assignments; never reuse one global
environment token across hosts implicitly. The represented actor remains
caller data regardless of the credential's account.

Use HTTPS with system certificate verification. The only API origin is
`https://api.github.com/graphql` for github.com or
`https://<host>/api/graphql` for GHES. Disable redirects; neither credentials
nor query data follow redirects. API-provided pagination cursors are values,
never URLs. Diagnostic text must not include tokens, query bodies, waiting
context, or private GitHub bodies.

### Supported API profile

Support github.com and GHES 3.19.x with the documented GraphQL fields below.
Other GHES versions fail as unsupported until their capability profile is
designed and verified; this does not narrow support to github.com. Obtain
the GHES version from its response metadata and reject an absent or
unsupported version explicitly. Successful field queries are also required;
a version string alone is not evidence of capability or permission.

The initial authenticated query validates repository access and required
schema shape without establishing an observation baseline. Run source
observation only after activation. A capability/authentication timeout during
watch acceptance fails within the existing deadline; it does not create an
active watch. Later source failures are watch diagnostics rather than empty
observations.

The supported credential profile is a classic PAT or saved `gh` OAuth token
with `public_repo` for public repositories, or `repo` for private/internal
repositories, plus any required organization/SSO authorization. These scopes
can authorize writes, but this plugin sends queries only. Fine-grained tokens
are not a declared profile until all selected fields have separate live
permission evidence; do not infer GraphQL coverage from REST permission tables.

| Category | GraphQL fields and interpretation |
| --- | --- |
| PR | `repository(owner:,name:,followRenames:false).pullRequest(number:)`: `id`, `url`, `headRefOid`, `state`, `mergedAt`; closed-to-open is a reopen change, merged is distinct from closed. |
| Reviews | `reviews`: `id`, `state`, `body`, `author.login`, `createdAt`, `updatedAt`, `submittedAt`, `commit.oid`, `url`. Exclude `PENDING` draft reviews; `DISMISSED` is an observable state. |
| Conversation | `comments`: `id`, `body`, `author.login`, `createdAt`, `updatedAt`, `url`. |
| Requests | `reviewRequests`: `id`, `requestedReviewer.__typename` and stable identity/name for each supported union member; unknown types fail coverage rather than disappear. |
| Threads | `reviewThreads`: `id`, `isResolved`, `resolvedBy.login`, `isOutdated`, plus separately paginated comments. |
| Review comments and replies | `id`, `body`, `author.login`, `createdAt`, `updatedAt`, `commit.oid`, `originalCommit.oid`, `pullRequestReview.id`, `replyTo.id`, `state`, `url`; exclude visible pending draft comments. |

Use the official [GHES pull-request schema](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/pulls),
[IssueComment schema](https://docs.github.com/en/enterprise-server@3.19/graphql/reference/issues#issuecomment),
and [authentication guide](https://docs.github.com/en/enterprise-server@3.19/graphql/guides/forming-calls-with-graphql).
These establish documented capability, not live acceptance. The GitHub.com
schema evolves; unsupported required fields fail visibly. GraphQL has no REST
API-version header that freezes this schema.

Read category connections independently with `first: 25`, cursor pagination,
and `pageInfo {hasNextPage endCursor}`. Thread comments have their own cursors.
Reject repeated/missing cursors, unexpected null required connections, GraphQL
`errors` even with HTTP 200, and partial pages. A nullable author or timestamp
is an explicit unavailable attribute, not an acquisition failure. Use the
first visible thread comment's URL as a related link, labelled as such; never
invent a thread URL. A successful full enumeration can establish disappearance
of a previously observed ID; failed enumeration cannot.

### Observation and comparison

One observation records start/end times and all required categories. It is
not an atomic GitHub snapshot. Each round has a 30-second total deadline,
10-second request deadline, 256-KiB decoded response bound per page, and
1-MiB decoded total response bound. Exceeding a bound fails the entire round.
Only complete, capacity-valid rounds can establish or advance a baseline.

With no position, first complete observation establishes a silent baseline.
Canonical comparison includes item identities, bodies, relationships, review
states, request membership, thread state, head, and lifecycle. Creation,
edits, disappearance, and state transitions have distinct journal sequence
numbers. Retrieval order, repeated pages, and repeated identical content do
not create changes. Do not filter by authenticated user, represented actor,
or automation. Body hashes compare content; timestamps are provenance and
must not be the sole change detector.

Keep every distinct transition confirmed by successful rounds after the
retained position, including a change later reverted before rewait. Events
entirely between rounds are outside the guarantee. Missing history is never
presented as an unchanged observation.

### Context and continuity bounds

Context is a UTF-8 JSON string, at most 8,192 decoded bytes. It contains
`external_data: true`, PR identity/link, waiting context, observation times,
`changes`, `current`, `omissions`, and `confirmation_position`. Changes and
unchanged current state are separate arrays/objects. Each discussion item
includes body, author, available times, related commit, stable ID/link, and
review/reply/thread references, with null plus an unavailable reason for an
absent attribute. Current state includes review states, head, requests,
thread states, and lifecycle. External text is always JSON data, never a
registration or executable instruction. Preserve the action's common external
data notice and fixed destination.

The bounded position/history contract below is part of this design and must
fit the common 32,768-byte saved envelope. Limits are coverage boundaries,
not claims of unlimited PR support. On failure retain the last valid state
and report a specific continuity/retrieval error; never reset to now.

Use schema `github-pr-watch-v1`. Store a random 128-bit registration nonce,
monotonic uint64 observation/change sequence, latest complete fingerprints,
observed-change journal, issued positions, attempted and confirmed delivery
progress, and current aggregate metadata. Sequence exhaustion fails visibly.
A position is `gpw1.` followed by base64url without padding of one version
byte, the 16-byte nonce, and the 8-byte big-endian covered sequence. Accept it
only if it exactly matches an issued position retained by this registration.
It is not an authorization token. Its logical sequence is unrelated to
timestamps, Session event IDs, or shared wait-generation IDs.

| Saved region | Hard bound on compact encoded JSON |
| --- | --- |
| Complete observation fingerprints | 32 items total, each at most 192 bytes; 6,144 bytes total |
| Change journal | 32 records, each at most 640 bytes; 20,480 bytes total |
| Issued positions | Latest four positions, at most 1,024 bytes total |
| Scope, aggregate metadata, progress, schema and enclosing syntax | 4,096 bytes |

Measure every region and the entire envelope after encoding; the envelope
must still fit 32,768 bytes. The table leaves 1,024 bytes of reserve, not an
extra permission to exceed a region. Items count reviews, conversation
comments, review comments/replies, threads, and requests together. A record
that cannot fit its bound fails coverage even if the total count is small.
Canonical fingerprint records contain stable ID/type and SHA-256 of full
acquired body and relevant metadata. Never hash only an excerpt. Journal
records retain change kind, sequence/time, identity, provenance/relations,
links, full-body digest, and bounded body excerpt with omission metadata.
The latest complete acquisition supplies current bodies transiently; historical
body text can be shortened, and disappeared content may no longer be
retrievable. State these facts with that item's link and digest.

Positions have no wall-clock TTL. Retain the latest four issued positions
while their required history fits the journal bound. Prune only history
already covered by accepted delivery and explicitly expire positions depending
on it. Never prune unreported transitions to make a candidate round fit.
If an observation exceeds a count or encoded-byte bound, reject the candidate
round without committing its snapshot or sequence; retain prior progress and
report `coverage_capacity_exceeded`. An expired position returns
`continuity_expired`, with instructions to inspect the PR and explicitly
start a new baseline if the caller accepts lost continuity. Do not substitute
a newer position automatically. Normal restore preserves the nonce/history;
a recreated registration creates a new nonce and rejects old positions.

A notification batch covers at most eight journal records, with encoded
change data at most 5,120 bytes, current-state summary at most 1,536 bytes,
and identity/waiting context/position/omissions at most 1,024 bytes. Always
measure the complete 8,192-byte string. Keep all required changed identities,
provenance, relationships and current state summaries; shorten bodies first
and mark each affected item with reason and retrievable link. An empty body
has `body_status: empty`; shortened content has `body_status: shortened`
and its digest. The returned position covers the explicitly described
changes, including acknowledged limitations; it never asserts that omitted
body text was read or evaluated. The caller must assess the omission and use
the link before treating that information as examined. If this metadata or
the full eligible batch cannot fit, report `notification_capacity_exceeded`,
emit no ordinary context and issue no new position. Do not split a batch
silently, advance past omitted changes, or disguise a capacity error as no
change. Conservative capacity failures are deliberate, documented limits.

## Specification documents

This document is the authoritative plugin contract for implementers and
verification authors. [Rewait control](pr-watch-rewait.md) owns the generic
control, its ordering, errors, and compatibility. The
[public plugin specification](plugin-v1.md) owns wire encoding, handshake,
deadlines, and the runtime-state extension; the
[runtime-state design](trigger-runtime-state.md) owns storage and shutdown.
Implementation may choose internal packages and algorithms within these
contracts, but cannot change position validity, limits, omission semantics,
notification lifetime, or acceptance expectations as a routine refactor.

## Normal and failure scenarios

1. Register a selected PR and destination. Validate selection and access,
   accept the dormant watch, then observe only after activation. First
   complete observation is silent; a failed first round creates no baseline.
2. A later complete round records changes and prepares one grouped context.
   One daemon-admitted event consumes the current notification wait. Keep
   observing while the agent works; do not start another destination attempt
   until explicit rewait or normal restart opens a new wait.
3. The agent returns the delivered position to the same registration. Apply
   the shared control fence, then report retained changes after that position,
   even if no further GitHub activity occurs. Reject malformed, foreign,
   expired, or unsupported positions without changing the active state.
4. Apply accepted delivery notices before checkpointing. After successful
   normal save/restart, use confirmed delivery progress to suppress an
   unchanged notification; report a fresh change. An explicit older retained
   assessment position remains a separate caller choice.
5. On save failure continue monitoring with the common continuity warning.
   Invalid restored state rejects the affected watch. Failed/unknown delivery
   receives no automatic retry and does not become agent assessment.

Poll at 60 seconds by default; `--poll-interval` accepts 5 through 3,600
seconds. Schedule after completion to prevent overlapping rounds. Transient
network/5xx failures retry after 5, 10, 20, 40, then 60 seconds, with up to
20% positive jitter. Respect a later `Retry-After` or rate-reset time. Never
turn a 403 rate-limit response into a permission verdict without checking
structured rate information. Authentication rejection, access denial,
not-found/inaccessible PR, unsupported API, pagination failure, rate limit,
timeout, and coverage overflow have distinct diagnostic codes. GitHub may
deliberately conceal a private PR as 404; report that ambiguity rather than
claiming it was deleted. Permanent failures retry only on the normal interval
and remain visible. A recovered unchanged round produces no invented change.

## Decisions and alternatives

| Decision | Alternatives | Rationale and tradeoff |
| --- | --- | --- |
| Same registration with daemon-owned rewait | Replacement/cancellation, plugin-owned cross-watch supersession | Keeps one identity and history, preserves cancellation exclusion and daemon control ownership. Requires a small shared protocol extension. |
| One notification per explicit wait or daemon run | Continuous notifications while the agent works; permanent one-shot watch | Avoids overlapping wait notifications while preserving restart/fresh-change acceptance. Restart reopening a wait is visible operational behavior. |
| GraphQL category queries | REST plus GraphQL; webhook history | Includes resolution and reply relationships without cross-API ID joins; explicit pagination is necessary. Polling matches approved observed-change scope. |
| Bounded retained history | Self-contained snapshot tokens; plugin-owned disk storage | Small positions fit event context and stay registration-bound; expiration and capacity failures must be visible. |
| Standard library HTTP/JSON | New GitHub SDK | No dependency or shared build prerequisite; generated query constants and parser unit tests are sufficient. |

## Quality and operations

Use Go and OS-neutral APIs. Keep all external operations behind
consumer-owned interfaces for unit tests. Cancel HTTP requests and child
authentication processes on Session closure. Normal shutdown follows the
shared notice/checkpoint fence. No additional OS support matrix, timing SLA,
or crash-recovery guarantee is introduced.

Usage materials must explain selected host/process credentials, permissions,
silent baseline, observed-change guarantee, one-notification lifetime,
restart rearming, rewait position retention, omissions, and visible capacity
failures. Include both reviewer and author examples, including activity by
the represented person. Explain that retrieved text and waiting context do
not enlarge authority. After continuity expires, operators can explicitly
establish a fresh baseline through the
[offline reset or recreation procedure](trigger-runtime-state.md#offline-reset-deletion-and-recreation),
accepting the loss of continuity. The plugin must never do this automatically.

## Requirement coverage

All IDs in this table refer to
[requirements/github-pr-watch.md](../requirements/github-pr-watch.md).

| Requirement | Acceptance | Design coverage | Verification |
| --- | --- | --- | --- |
| FR-001, FR-002 | AC-001, AC-009 | Selection/authentication, host-scoped processes | U01, GPW-I01, D01 |
| FR-003 | AC-002, AC-009 | API profile and full comparison | U02, GPW-I01, D01 |
| FR-004 | AC-002, AC-003, AC-005, AC-009 | Silent baseline and notification wait | U02, GPW-I01, D01, D02 |
| FR-005 | AC-002–AC-004, AC-006 | Context/provenance/positions | U03, GPW-I01, GPW-I02, D02 |
| FR-006 | AC-004 | Rewait control and history | U03, GPW-I02, D02 |
| FR-007 | AC-004, AC-005 | Transition identities and one wait | U02, GPW-I01, GPW-I02 |
| FR-008 | AC-006, AC-009 | Complete observations and bounded context/history | U03, U04, GPW-I01 |
| FR-009 | AC-007, AC-009 | Failure categories and scheduling | U04, GPW-I01, D01 |
| FR-010 | AC-008 | Runtime state and distinct positions | U03, GPW-I03 |
| FR-011 | AC-009, AC-010 | Usage scope and delivery checks | D01, D02, documentation review |
| NFR-001 | AC-003, AC-010 | External JSON data and fixed route | U03, GPW-I02, D02 |
| NFR-002 | AC-001, AC-007, AC-010 | Verified TLS, host isolation, redacted diagnostics | U01, U04, GPW-I01, D01 |
| NFR-003 | AC-008, AC-010 | Component ownership and public protocol | GPW-I02, GPW-I03, source/spec review |
| NFR-004 | AC-010 | Separate unit, assembled, live-service and receiving-agent evidence | Verification strategy, D01, D02 |

## Verification strategy

Use unit tests for the broad value/decision matrices. U01 covers every
argument and credential precedence branch, no fallback, host separation, and
represented/authenticated identity separation. U02 covers each category,
actor, edit/revert/disappearance, baseline, ordering, and batching. U03 covers
position schemas, retained-history boundaries, exact byte bounds, empty versus
shortened bodies, context injection, and restored payload validation. U04
covers pagination, GraphQL partial errors, clocks, retry guidance, and secret
markers. Mock network, subprocess, storage, and clock capabilities; unit tests
must not launch servers or children.

Three assembled cases retain only boundaries unit tests cannot establish.
Do not multiply process cases by every GitHub state or authentication input.
The existing common runtime-state suites remain responsible for exhaustive
host checkpoint/activation failures; this scope verifies the plugin's actual
payload and the new rewait path. No new dependency, runner, or CI job is needed.

### CI acceptance cases

The governed suite is `cmd/github-pr-watch-verification/`; all suite/case
files are directly there with `//go:build integration`. The inventory is
`cmd/github-pr-watch-verification/testdata/cases.json`. The plugin directory
is introduced during implementation, not by adding empty bundled artifacts.

| ID | Concrete inputs and actions | Observable expected results and boundaries |
| --- | --- | --- |
| GPW-I01 | Start real plugin through public Session against verified loopback TLS GraphQL. Supply selected-host token without `gh`; activate, fail one initial page, complete baseline, repeat, add review/comment/reply and head H2, then exceed context capacity and independently fail a nested page. | No preactivation or baseline event; one bounded event with current state and relationships; empty/shortened bodies distinguished; omissions explicit; failed coverage does not move position or fabricate deletion. Credentials reach only the configured TLS origin and do not occur in context/diagnostics. |
| GPW-I02 | Start real hub/client/plugin/model-output with PR A and destination A; receive P1. Observe C2/H2 while the wait is consumed, then rewait the same ID from P1 with no additional GitHub activity. Repeat the operation ID; submit foreign/expired position and adversarial content. | C2/H2 delivered once to A, same registration and immutable arguments; P1-only changes absent; no overlapping old generation or second delivery from retry; invalid rewait changes nothing; destination B untouched. Verify terminal-outcome fence and no inferred agent assessment. |
| GPW-I03 | Accept a plugin notification, checkpoint and stop/restart the real hub with unchanged source, then add C3. Separately fail saving, restore invalid payload, and recreate a registration without old progress. | No unchanged duplicate after successful save; C3 delivered through the reopened wait; save warning with continued monitoring; invalid state blocks affected watch; new registration has silent fresh baseline and cannot use old position. |

GPW-I01 uses actual plugin networking/serialization, substituting only GitHub.
GPW-I02/I03 use actual command, routing, model-output, filesystem, and plugin;
GitHub alone is a controlled TLS service. Control server origin resolution in
the test launcher while retaining hostname validation and the test CA, as in
the existing review-request fixture. Do not add production insecure TLS or
arbitrary API-origin options for testing.

Build binaries once per suite, budget 60 seconds for setup, 15 seconds each
for I01/I02, 20 seconds for I03, and 10 seconds cleanup, within the existing
two-minute scoped suite budget. Use observable deadlines, temporary directories,
at most one hub, one input, one model output, one mock service, and serialized
cases. Kill children and close servers on failure. Feasibility checks measure
transport/setup only; static Pending cases prove registration, not product
acceptance. Actual cost measurements and limitations belong in the design PR.

Run `go run build/mage.go -d build -w . test` separately from
`go run build/mage.go -d build -w . integrationAll` on the existing Ubuntu CI.
The implementation completion gate is
`go run build/mage.go -d build -w . integration cmd/github-pr-watch-verification/testdata/cases.json <new-report-directory>`.
It must pass every approved ID. Pending, missing, skipped, or filtered cases
are not completion. Design checks compile/register tagged cases and run
guardrails without claiming acceptance.

### Delivery quality checks

| ID | Actual environment and stimulus | Required evidence and evaluator |
| --- | --- | --- |
| D01 | Independently run the real plugin through public Session on github.com and GHES 3.19.x, with maintainer-owned PRs, selected-host credentials, and author/reviewer permissions. Exercise silent baseline, every available category including edit/disappearance/dismissal/resolution, persistent versus wholly unobserved activity, token rejection, and retry diagnostics. | Maintainer records host/patch version, query capabilities, redacted auth setup, revision, stimuli/times, context/positions and limitations. Missing GHES access leaves GHES acceptance unexecuted; mocks do not substitute. |
| D02 | Use real hub, trigger, actual agent/action destination for both author and reviewer. Receive P1, let the agent decide act or wait, create C2/H2 while it works, rewait P1, and observe resumed context. Also normal restart after saved acceptance followed by a new change. | Maintainer records separate trigger evidence, action acceptance, agent's actual act/wait decision, same-registration continuity, explicit omissions, fixed destination and no granted authority. A successful action request alone is not receiving-agent success. |

Delivery authors and checks the plugin README/examples against FR-011,
including the normal-restart notification behavior. Execution of D01/D02
belongs to delivery after approved implementation merge, not to design or
implementation acceptance.

## Change and rollout impact

Adoption adds one trigger and the optional shared rewait extension. The
governing shared documents define compatibility with existing registrations
and nonparticipating plugins. Existing request-discovery behavior, URL-only
events, and service-free model tests remain intact. No product implementation
or delivery instructions are part of this design phase.

## Open questions and risks

GHES live infrastructure and receiving-agent credentials are delivery
prerequisites, owned by the maintainer. Capability documentation alone does
not close those checks. Very large PRs or long inactive waits can exceed the
declared capacity; visible continuity failure is preferable to silently
dropping changes. Position and context bounds must be reviewed together with
the shared rewait contract before implementation.

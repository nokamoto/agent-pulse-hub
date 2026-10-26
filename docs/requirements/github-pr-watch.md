---
type: Requirement
title: GitHub pull request watch trigger
description: Resume a waiting agent with pull request changes and current review context so the agent can decide whether to act or keep waiting.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: bundled-plugins
    resource: bundled-plugins.md
    title: Bundled trigger and action plugins
  - id: review-request
    resource: github-review-request.md
    title: GitHub review request trigger
  - id: runtime-state
    resource: trigger-runtime-state.md
    title: Trigger runtime state persistence
---

# GitHub pull request watch trigger

## Problem and goals

An agent acting for a pull request author or reviewer may finish its current
work and wait for another participant. Examples include a reviewer waiting for
an author's response or new revision, and an author waiting after requesting
review or posting a response. Repeated agent checks waste work, while a PR URL
alone leaves the resumed agent without the review context needed to continue.

The bundled `github-pr-watch` trigger must watch an explicitly selected PR and
deliver its observed changes together with current review state. The receiving
agent decides whether it should work, request clarification, or wait again
under its existing instructions. The trigger must not decide whose turn it is.

Success includes a safe repeated workflow: wait, receive context, assess or
perform work, and wait again without discarding observable changes that arrived
while the agent was working. Delivery acceptance and agent assessment remain
separate facts.

## Sources and decisions

The user requested author and reviewer waiting workflows, and explicitly
assigned the decision to act or keep waiting to the agent. The following
capabilities are proposed requirements for human review: explicit PR selection;
caller-supplied waiting context; review, discussion, revision, review-request,
thread-resolution, and PR-lifecycle changes; current state alongside changes;
explicit omissions; confirmation-position handoff when waiting again;
duplicate suppression and batching; no removal of the user's own activity;
visible acquisition failures; and an external-data boundary.

The [MVP requirements](mvp.md) govern service-neutral routing, fixed
destinations, one-attempt delivery, and external data. The
[bundled-plugin requirements](bundled-plugins.md) govern packaging and usage
materials. The [runtime-state requirements](trigger-runtime-state.md) govern
core-owned persistence and normal-restart continuity.

The user selected support for github.com and GitHub Enterprise Server (GHES).
Each watch selects one host; credentials, PR identity, observations, and progress
must stay within that host. Different hosts use separate watches.

Authentication reuses the established order in
[GitHub review request FR-002](github-review-request.md#functional-requirements):
`GH_TOKEN`, then `GITHUB_TOKEN`, then the selected host's saved `gh` login.
Environment-token users must not need `gh`. This trigger differs from that
integration's repository-unfiltered request discovery and URL-only events;
it watches a selected PR and provides review context.

The user selected the first complete observation as a baseline, with no initial
notification; later observed changes produce notifications. Monitoring uses
periodic observations and guarantees changes that can be confirmed through
those observations. It does not guarantee capture of every intermediate event
or of a transition that disappears entirely between observations.

## Terms and workflow

- **Waiting context:** caller-supplied explanation of why the agent is waiting
  and the person or role it is acting for. This is context, not a turn rule.
- **Observation:** acquisition of the selected PR's in-scope data. A complete
  observation covers all required categories within declared retrieval limits;
  missing pages or failed categories cannot count as complete.
- **Change:** an in-scope difference or activity confirmed through complete
  periodic observations after the supplied confirmation position. A transition
  entirely between observations need not be detected.
- **Confirmation position:** an opaque reference to the observation covered by
  delivered context. The agent can return it when waiting again to identify
  what it has had the opportunity to assess. It is neither a timestamp guessed
  by the caller nor proof that the agent performed any work.
- **Current state:** review and discussion context as observed when preparing
  the notification, with its observation time and completeness stated. It is
  not a promise of an atomic snapshot across GitHub resources.

The caller selects the PR, supplies waiting context, and uses an existing
action destination. On notification, the agent reads the changes, current
state, and any limitations, then decides what to do. If it waits again, it
supplies the last confirmation position it assessed. Changes since that
position must remain eligible even if they arrived before the new wait began.
Exact commands, argument names, and position representation belong in design.

## Scope

- In scope: a bundled GitHub trigger for an explicitly selected PR; author and
  reviewer waiting context; the data categories in FR-003; agent-owned work
  decisions; repeated waiting with progress handoff; duplicate suppression and
  grouped notifications; failures, limits, persistence, and usage materials.
- Out of scope: deciding whose turn it is; PR discovery or automatic selection;
  automatic reviews, replies, commits, review requests, thread resolution, or
  merges; CI/check results, labels, full diffs, and source-file retrieval;
  another agent delivery integration; automatic delivery retries or
  exactly-once delivery; crash/power-loss recovery guarantees.

All functional requirements below are required for this scope. The resolved
product decisions and remaining delegated design decisions are recorded below.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | A reviewer finishes reviewing and waits for the author | A response, revision, or other in-scope change resumes the agent with review context | The trigger does not infer a mandatory next actor from review status |
| UC-002 | An author requests review or posts a response, then waits | New review activity reaches the agent with the current discussion and revision state | Activity by the author remains visible as data |
| UC-003 | A resumed agent decides to wait again | Its supplied confirmation position preserves intervening observable changes | An unsupported or incompatible position fails visibly rather than resetting to now |
| UC-004 | Several changes occur before a notification is prepared | One grouped context preserves their identity, relationships, and current state | Large content is bounded with explicit omissions, not silent loss |
| UC-005 | GitHub retrieval or progress storage fails, or the daemon restarts | The operator can distinguish failure from no change and understand continuity limits | Delivery acceptance does not imply that the agent assessed the notification |

## Functional requirements

| ID | Requirement | Rationale / use case |
| --- | --- | --- |
| FR-001 | Support github.com and GHES. Let the caller select exactly one PR and its host per watch and supply waiting context identifying the reason for waiting and the represented person or role. Preserve that context in notifications. Validate the PR identity and required context without guessing a PR, identity, or destination. Multiple PRs or hosts use separate watches. Keep each host's credentials, identities, observations, confirmation positions, and runtime progress separate. | UC-001, UC-002 |
| FR-002 | Use the first nonempty credential in the order `GH_TOKEN`, `GITHUB_TOKEN`, saved `gh` authentication for the selected host. Do not require `gh` when using an environment token. Reject missing or unusable authentication clearly without interactive login, and do not fall back after rejection of the selected credential. Authentication identity may differ from the represented person in waiting context; do not substitute one for the other. | UC-001, UC-002, UC-005 |
| FR-003 | Acquire and distinguish submitted reviews and their states; PR conversation comments; review comments and their replies; current head commit and observed head changes; requested reviewers and observed request additions/removals; review-thread resolution/unresolution; and open, closed, reopened, or merged PR state. Include currently observable review dismissal, edited discussion content, and disappeared previously observed content as changes confirmed through periodic observations. Preserve relationships among reviews, comments, replies, and threads. Do not claim inaccessible draft reviews, unavailable history, or transitions entirely between observations as covered. | UC-001, UC-002, UC-004 |
| FR-004 | With no supplied confirmation position, establish a baseline from the first complete observation without notifying existing content or requesting destination work. Notify subsequent in-scope changes confirmed through periodic observations without determining whether they make it the agent's turn or whether work is required. Do not suppress changes because their actor is the authenticated user, the represented person, or an automation. Do not require another participant to act before delivering a head, request, thread, or lifecycle change. With no in-scope change, do not request destination work. | UC-001, UC-002 |
| FR-005 | Deliver the selected PR identity/link, waiting context, changes since the confirmation position, current in-scope review state, observation time, and a new confirmation position. Distinguish changed items from unchanged state and unavailable information. For discussion/review content, provide body, author, available creation/update/submission times, related commit when applicable, stable identity/link, and reply/thread relationships. State when an attribute is unavailable rather than fabricating it. Present current head, requested reviewers, review states, thread states, and PR lifecycle state so the agent can assess what remains relevant. | UC-001–UC-004 |
| FR-006 | Permit the agent to wait again from the confirmation position supplied in a notification. Include eligible changes after that position even when they occurred while the agent was working or before the repeated wait began. Do not replace a supplied position with the new watch's creation time or infer assessment from action delivery acceptance. Reject foreign, malformed, incompatible, or no-longer-supported positions with an actionable continuity error; do not silently start from the present. The design must specify how repeated waits avoid overlapping notifications from superseded waits. | UC-003 |
| FR-007 | Avoid repeated notifications for an unchanged observation and repeated inclusion of the same change solely because it was retrieved again. A new edit, reply, review-state transition, or head change remains a distinct change. Group changes available in the same notification preparation into one context; retain distinct item identities and relationships. Do not promise exactly-once delivery or removal of duplicates caused by unfinished/unknown delivery or failure recovery beyond the common contracts. | UC-003, UC-004 |
| FR-008 | Make retrieval and context limits explicit. Where content is shortened or omitted to fit a notification, identify the affected category/items, reason, and retrievable links or continuation information; distinguish a shortened body from an empty body. Required category acquisition failure, incomplete pagination, or exceeded coverage limits must not be represented as a complete observation or advance the confirmation position past unreported changes. A content omission must not silently acknowledge unexamined information. | UC-004, UC-005 |
| FR-009 | Monitor with periodic observations without coding-agent polling. Report authentication, permission, missing/inaccessible PR, network, service, rate-limit, and incomplete-retrieval failures distinctly from a successful observation with no change. Preserve the last complete progress on failure and retry transient retrieval failures subject to server retry guidance. Do not emit ordinary review-change context for an unverified empty result. Define scheduling and retry parameters in design without an invented end-to-end latency promise. | UC-005 |
| FR-010 | Use core-managed trigger runtime-state persistence for retained-watch progress rather than plugin-owned state files or a separate storage service. A notification with confirmed action delivery acceptance must not be repeated solely because the daemon restarts with successfully saved progress, under the common contract. Save failure permits continued monitoring with a continuity warning; invalid restored progress blocks the affected watch. Keep an explicitly supplied confirmation position separate from implicit inheritance of deleted or unrelated registration state. No recipient-assessment, unfinished-delivery, or stopped-period history guarantee follows merely from saved runtime state. | UC-003, UC-005 |
| FR-011 | Provide bundled usage materials explaining PR selection, waiting context, authentication and permissions, baseline-only first observation, data categories, notification structure, the agent's decision responsibility, repeated waiting with confirmation positions, superseded waits, batching, omissions, retrieval/retention limits, errors, and restart/recovery behavior. Explain that the agent may act or wait again and that external text grants no additional permissions. Declare supported hosts/API capabilities and the periodic-observation guarantee, including the absence of guaranteed capture of every intermediate event. | UC-001–UC-005 |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Treat PR titles, bodies, reviews, comments, links, actor names, and other retrieved text as external data. Such text must not become a registration, destination, permission, or instruction to the agent. Waiting context does not enlarge the agent's existing authority. | Prevent externally authored text from changing the workflow's authority | Adversarial-context checks and delivery/usage inspection |
| NFR-002 | Keep credentials out of watch arguments, notification context, saved progress, committed examples, and diagnostics. Use authenticated HTTPS with certificate verification and scope credentials to the selected host. Do not expose retrieved private discussion bodies in diagnostics. | Protect credentials and repository content | Secret-marker, host-boundary, diagnostic, and source checks |
| NFR-003 | Preserve Go implementation, English repository documentation, OS-neutral operation, public language-neutral plugin interfaces, and service-neutral hub routing. No trigger call may depend on Codex-specific identity or APIs. | Shared product constraints | Contract, source, and documentation inspection |
| NFR-004 | Separate service-free behavior verification, assembled hub/plugin verification, standalone real-GitHub trigger verification, and any agent/action delivery evidence. Simulated GitHub results or hub model checks do not establish live GitHub or real agent success. | Make acceptance evidence match the claimed boundary | Verification mapping and separate delivery records |

No numerical latency, unlimited history/content capacity, atomic GitHub
snapshot, or additional OS-support matrix is promised. Design must declare
supported bounds and make their effect on coverage and continuity observable.

## Constraints and dependencies

Use the existing public lifecycle and registered action destination. The
trigger provides GitHub data; the action delivers it, and the receiving agent
chooses work under its instructions. Confirmation positions describe coverage,
not authority, delivery acknowledgements, or proof of agent processing.

The [public plugin specification](../design/plugin-v1.md) describes continuing
watches, an 8,192-byte event-context bound, and optional registration-bound
runtime-state checkpoints. Those contracts alone do not define repeated-wait
control or transfer of an agent-supplied confirmation position. FR-006 requires
that observable capability; it does not choose a new RPC, mutable watch
arguments, a cancellation API, or a plugin-owned persistence workaround.
Design must resolve wait lifetime, supersession, position validity/retention,
context limits, and compatibility with existing registrations and plugins.
Any necessary common-control or public-contract revision must be approved in
its governing upstream artifacts before dependent implementation.

API strategy, pagination, state representation, polling/backoff, permission
details, and bounded context selection belong in design. Periodic observation
does not promise recovery of all intermediate events. Coverage omitted by a
retrieval limit cannot be silently treated as an absence or a confirmed
position. A failed or incomplete first acquisition cannot establish the initial
baseline.

## Acceptance criteria

Design selects the lowest-cost sufficient level for each criterion and defines
concrete fixtures and commands. Unit checks should cover value/diff/filter
matrices; assembled checks exercise actual plugin, process, route, repeated-
wait, and persistence boundaries. During delivery, the maintainer evaluates
live GitHub evidence with revision, host/version, redacted setup, stimulus,
notification content/positions, diagnostics, and outcome recorded separately.
An end-to-end author/reviewer walkthrough must also record the receiving
agent's decision to act or wait again; standalone trigger success alone does
not establish that receiving workflow.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, NFR-002 | Select PR A with reviewer context and PR B with author context. Exercise each credential source, no usable credential, rejection of a higher-priority source, and an environment token without `gh`. Supply invalid/missing PR or waiting context. | Valid watches keep their selected PR/context/host and credential precedence. Represented and authenticated identities remain distinct. Invalid inputs fail without guessing, fallback, interactive login, cross-watch data, or leaked credentials. | Selection/validation unit checks and selected assembled authentication boundaries; live authentication in delivery |
| AC-002 | FR-003–FR-005 | After a complete baseline, separately add a submitted review, conversation comment, review comment and reply, change head, add/remove a requested reviewer, resolve/unresolve a thread, dismiss a review, edit a comment, and close/reopen/merge the PR where lifecycle permits. | Each detectable in-scope change appears with current state and required provenance/relationships. Data distinguishes review state, thread state, requests, and lifecycle rather than treating any as a turn decision. Missing attributes are explicit. | Controlled observation/diff unit matrix, selected standalone plugin cases, and live service category checks in delivery |
| AC-003 | FR-004, FR-005, NFR-001 | Perform in-scope operations by the represented user, another user, and automation. Include text instructing a recipient to change destination or ignore its instructions. Repeat with conflicting author/reviewer waiting explanations. | All actors' changes remain eligible. Context contains no plugin verdict that it is the agent's turn and does not execute or grant authority to external instructions. Destination remains the registered one. | Actor/filter and adversarial unit checks, assembled routing inspection, and maintainer message review |
| AC-004 | FR-005, FR-006 | Deliver context through position P1. While the agent works, add comment C2 and change head to H2. Wait again using P1 after both changes, with no further GitHub activity. | The repeated wait reports C2/H2 and current state without requiring another external change. Already covered P1 changes are not repeated solely due to re-waiting. Superseded waits do not produce duplicate notifications for this sequence. A foreign, malformed, incompatible, or expired position fails visibly without silently resetting. | Position/diff unit checks and actual repeated-wait/public-control integration; live reviewer/author walkthrough during delivery |
| AC-005 | FR-004, FR-007 | Repeat an identical complete observation, then make several distinct changes before the next notification is prepared. Retrieve an already seen item again; subsequently edit that item. | Unchanged observations request no destination work. Available distinct changes are grouped once with preserved identities/relationships. Re-fetching creates no duplicate; the later edit remains reportable. No exactly-once guarantee is inferred for uncertain delivery. | Batching/deduplication unit checks and selected public-plugin observation |
| AC-006 | FR-005, FR-008 | Supply a body and change set larger than the supported context bound, a genuinely empty body, failed pagination, and an unavailable required category independently. | Bounded output explicitly identifies shortening/omission and affected content, with links or continuation information. Empty and shortened content differ. Incomplete acquisition never claims complete current state or advances coverage past unreported changes; later retrieval can still report them. | Limit/completeness unit checks, actual bounded-context integration, and maintainer inspection |
| AC-007 | FR-009, NFR-002 | After a complete observation, independently reject authentication, deny PR access, return not-found, time out, fail a page, and rate-limit retrieval. Recover without an in-scope change. | Each failure is visible with no credential/private-body disclosure; it is not a successful no-change observation. Last complete progress remains. Transient retries respect server guidance, and recovery does not invent a disappearance/change or duplicate unchanged work. | Controlled retrieval/failure/clock unit checks and selected assembled diagnostics |
| AC-008 | FR-010, NFR-003 | Confirm action acceptance, save progress, stop normally, and restart the retained watch. Present unchanged data followed by a fresh change. Separately fail saving, restore incompatible progress, and recreate a deleted registration. | Unchanged confirmed notification is not repeated solely due to successful normal restart; fresh change is reportable. Save failure warns and monitoring continues; invalid restoration blocks the affected watch. Recreated/unrelated registration does not implicitly inherit old state. No plugin-owned state storage or inferred agent assessment appears. | Common-state evidence plus plugin-specific progress unit checks and assembled restart/failure integration |
| AC-009 | FR-001, FR-003, FR-004, FR-008, FR-009, FR-011 | On github.com and a supported GHES host, start a watch without a confirmation position on a PR with existing reviews/comments. Independently fail the first acquisition, then complete it and repeat it unchanged. Add a comment that remains present at the next observation. Separately create and remove another comment entirely between two observations that show identical state. | Failed acquisition establishes no baseline. The first complete observation establishes the baseline without a notification; unchanged repetition requests no destination work. The persistent new comment produces a notification with current state. Capture of the entirely unobserved comment is not required or claimed as guaranteed. Each host uses only its selected credentials and PR progress. | Baseline/history policy and host-isolation unit checks, selected standalone plugin checks, and separate live-service verification on both host types during delivery |
| AC-010 | FR-011, NFR-001–NFR-004 | A maintainer follows finalized author and reviewer workflows through wait, context receipt, agent assessment, work or continued waiting, and a second wait with an intervening change. Inspect examples, supported bounds, failure/recovery instructions, and evidence. | The selected PR's context reaches the registered destination; the agent decides to act or wait again, and the second wait preserves the intervening change. Instructions explain limits and authority. Evidence separates service-free, assembled, live GitHub, and receiving-agent outcomes and records the actual environment. | Documentation/source review and delivery walkthroughs; independent real-GitHub trigger check through the public plugin interface |

## Open questions and assumptions

No unresolved product-scope question remains. The following resolved decisions
retain their rationale; the delegated design decisions must be settled before
design approval.

| ID | Question or assumption | Impact if unresolved or false | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Support github.com only, or github.com and GHES? | Determines host selection, API compatibility, credential isolation, and real-service acceptance environments | Responsible human | Requirements approval | Resolved by the user: include github.com and GHES. Supported GHES versions, API capabilities, and separate host-verification prerequisites must be specified before design approval. |
| Q-002 | With no supplied confirmation position, immediately deliver current review context once, or establish a baseline and notify only later changes? | Determines first-wait behavior and whether existing review context itself wakes the agent | Responsible human | Requirements approval | Resolved by the user: use current state as the baseline and notify later observed changes; no initial notification. |
| Q-003 | Guarantee changes confirmed through periodic observations, or every intermediate in-scope event? | Determines monitoring/history architecture, retention guarantees, and decisive acceptance for disappearing activity | Responsible human | Requirements approval | Resolved by the user: notify changes confirmed through periodic observations; full intermediate-event capture is not required. |
| Q-004 | Define position representation/retention, repeated-wait/supersession control, supported retrieval/context capacity, and compatible common-contract revisions required by FR-006/FR-008. | A new wait could skip changes, overlap old waits, exceed protocol bounds, or imply invalid state transfer | Design owner, with responsible human review | Design approval | Delegated design decisions |

## Change impact

This is a new requirement concept with FR-001–FR-011, NFR-001–NFR-004, and
AC-001–AC-010. No existing local ID is retired. It supplements selected-PR
monitoring alongside [request discovery](github-review-request.md); it does not
change that trigger's URL-only context or matching scope.

Repeated waiting and confirmation-position handoff require review of the
[MVP routing design](../design/mvp.md),
[public plugin specification](../design/plugin-v1.md),
[registration persistence](../design/state-persistence.md), and
[runtime-state design](../design/trigger-runtime-state.md). If satisfying the
capability requires changing the MVP's exclusion of individual cancellation or
another shared requirement, obtain approval and merge in the affected upstream
requirements before dependent design or implementation. This document does not
silently authorize a cancellation API or relax registration-state isolation.

After requirements approval and merge, create the plugin-specific design,
governed verification mapping, and any required developer/author instructions.
Implementation and delivery provide the bundled trigger, configuration
examples, usage materials, and real-service/agent evidence in their own phases.
Existing model-plugin verification remains service-free; no product code,
shared protocol change, CI change, or executed acceptance claim is part of
these requirements.

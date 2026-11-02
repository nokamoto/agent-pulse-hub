---
type: Requirement
title: GitHub pull request watch trigger
description: Continuously notify observed pull request changes with current review context without requiring the recipient to rearm the watch.
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
  - id: notification-delivery
    resource: notification-delivery.md
    title: Observable notification delivery and safe retry
---

# GitHub pull request watch trigger

## Problem and goals

An agent acting for a pull request author or reviewer may finish its current
work and wait for another participant. Examples include a reviewer waiting for
an author's response or new revision, and an author waiting after requesting
review or posting a response. Repeated agent checks waste work, while a PR URL
alone leaves the resumed agent without the review context needed to continue.

The bundled `github-pr-watch` trigger must watch an explicitly selected PR and
deliver its observed changes together with current review and PR readiness state. The receiving
agent decides whether it should work, request clarification, or wait again
under its existing instructions. The trigger must not decide whose turn it is.

Register once to receive later eligible changes, including while the agent
works, without rearming the watch. Delivery acceptance and agent assessment
remain separate facts.

A PR can accumulate more than 32 review and discussion items during ordinary
use. Its total item count must not by itself prevent a baseline, stop later
observations, or prevent notification of new changes. Small checkpoint and
notification budgets must not become a fixed lifetime item ceiling for the PR.

## Sources and decisions

The user requested author and reviewer waiting workflows, and explicitly
assigned the decision to act or keep waiting to the agent. The following
capabilities are proposed requirements for human review: explicit PR selection;
caller-supplied waiting context; review, discussion, revision, review-request,
thread-resolution, PR-lifecycle, CI-failure, base-branch conflict, and
Draft-to-Ready-for-Review changes; current state alongside changes;
explicit omissions; automatic later notifications and bounded continuation;
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
notification; later eligible changes notify automatically without `rewait`.
Monitoring uses periodic observations and guarantees changes that can be confirmed through
those observations. It does not guarantee capture of every intermediate event
or of a transition that disappears entirely between observations.

The user rejected the v1 design's failure when reviews, conversation comments,
review comments/replies, threads, and review requests together exceed 32 items.
The required outcome is continued monitoring as a PR grows, while retaining
explicit completeness and continuity guarantees. Increasing that fixed ceiling
alone does not meet this outcome. Storage representation, incremental retrieval,
and bounded notification delivery are design decisions, not a requirement to
keep the v1 capacity allocation.

## Terms and workflow

- **Waiting context:** caller-supplied explanation of why the agent is waiting
  and the person or role it is acting for. This is context, not a turn rule.
- **Observation:** acquisition of the selected PR's in-scope data. A complete
  observation covers all required categories within declared retrieval limits;
  missing pages or failed categories cannot count as complete.
- **Change:** an in-scope difference or activity confirmed through complete
  periodic observations after the initial baseline. A transition entirely
  between observations need not be detected. FR-004 and FR-013 determine
  which changes independently trigger a notification.
- **Coverage position:** a reference identifying how far represented changes
  extend. It is neither a delivery receipt nor proof of assessment, and the
  recipient does not return it to continue notifications.
- **Current state:** review and discussion context as observed when preparing
  the notification, with its observation time and completeness stated. It is
  not a promise of an atomic snapshot across GitHub resources. It also includes
  current CI results, conflict status against the selected PR's current base
  branch, and Draft/Ready for Review status.

The caller selects the PR, supplies waiting context, and registers an existing
action destination. The agent reads each notification and decides what to do;
later changes and bounded continuations arrive automatically at that destination.
Commands, progress representation, and scheduling belong in design.

## Scope

- In scope: a bundled GitHub trigger for an explicitly selected PR; author and
  reviewer waiting context; the data categories in FR-003; agent-owned work
  decisions; continuous notification without recipient rearming; duplicate
  suppression and grouped notifications; failures, limits, persistence, and
  usage materials.
- Out of scope: deciding whose turn it is; PR discovery or automatic selection;
  automatic reviews, replies, commits, review requests, thread resolution, or
  merges; CI log retrieval, automatic CI retries or conflict resolution, labels,
  full diffs, and source-file retrieval;
  another agent delivery integration; automatic delivery retries or
  exactly-once delivery; crash/power-loss recovery guarantees.

All functional requirements below are required for this scope. The resolved
product decisions and remaining delegated design decisions are recorded below.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | A reviewer finishes reviewing and waits for the author | A response, revision, or other in-scope change resumes the agent with review context | The trigger does not infer a mandatory next actor from review status |
| UC-002 | An author requests review or posts a response, then waits | New review activity reaches the agent with the current discussion and revision state | Activity by the author remains visible as data |
| UC-003 | New eligible changes are observed while the agent assesses earlier context, works, or continues waiting | The same registration automatically notifies the changes without a recipient command or returned position | Notification progress is not proof of assessment; a terminal failed or unknown delivery does not itself disable later distinct notifications |
| UC-004 | Several changes occur before a notification is prepared | One grouped context preserves their identity, relationships, and current state | Large content is bounded with explicit omissions, not silent loss |
| UC-005 | GitHub retrieval or progress storage fails, or the daemon restarts | The operator can distinguish failure from no change and understand continuity limits | Delivery acceptance does not imply that the agent assessed the notification |
| UC-006 | An author or reviewer waits while CI fails, a base-branch conflict appears, or the PR becomes Ready for Review | Each observed change can independently resume the agent with the changed details and current state | No accompanying review or comment is required; unknown conflict status is not a confirmed conflict |

## Functional requirements

| ID | Requirement | Rationale / use case |
| --- | --- | --- |
| FR-001 | Support github.com and GHES. Let the caller select exactly one PR and its host per watch and supply waiting context identifying the reason for waiting and the represented person or role. Preserve that context in notifications. Validate the PR identity and required context without guessing a PR, identity, or destination. Multiple PRs or hosts use separate watches. Keep each host's credentials, identities, observations, coverage positions, and runtime progress separate. | UC-001, UC-002 |
| FR-002 | Use the first nonempty credential in the order `GH_TOKEN`, `GITHUB_TOKEN`, saved `gh` authentication for the selected host. Do not require `gh` when using an environment token. Reject missing or unusable authentication clearly without interactive login, and do not fall back after rejection of the selected credential. Authentication identity may differ from the represented person in waiting context; do not substitute one for the other. | UC-001, UC-002, UC-005 |
| FR-003 | Acquire and distinguish submitted reviews and their states; PR conversation comments; review comments and their replies; current head commit and observed head changes; requested reviewers and observed request additions/removals; review-thread resolution/unresolution; open, closed, reopened, or merged PR state; CI results for the current head; conflict status against the current base branch; and Draft/Ready for Review status. Include currently observable review dismissal, edited discussion content, and disappeared previously observed content as changes confirmed through periodic observations. Preserve relationships among reviews, comments, replies, and threads. Do not claim inaccessible draft reviews, unavailable history, or transitions entirely between observations as covered. | UC-001, UC-002, UC-004 |
| FR-004 | For a new watch without retained progress, establish a baseline from the first complete observation without notifying existing content or requesting destination work. Notify subsequent in-scope changes confirmed through periodic observations, applying FR-013's wake conditions to CI, conflict, and readiness state, without determining whether they make it the agent's turn or whether work is required. Do not suppress changes because their actor is the authenticated user, the represented person, or an automation. Do not require activity from another participant or an accompanying review/comment before delivering a head, request, thread, lifecycle, CI-failure, base-branch conflict, or Draft-to-Ready-for-Review change. With no eligible change or outstanding continuation, do not request destination work. | UC-001, UC-002 |
| FR-005 | Deliver the selected PR identity/link, waiting context, eligible changes selected for this notification, current in-scope review state, observation time, and their coverage position. Distinguish represented changes from outstanding continuation, unchanged state, and unavailable information. For discussion/review content, provide body, author, available creation/update/submission times, related commit when applicable, stable identity/link, and reply/thread relationships. State when an attribute is unavailable rather than fabricating it. Present current head, requested reviewers, review states, thread states, PR lifecycle state, current-head CI results, base-branch conflict status, and Draft/Ready for Review status so the agent can assess what remains relevant. | UC-001–UC-004 |
| FR-006 | Automatically notify later eligible changes on the same registration and destination, including while the agent works. Remove `rewait` and the one-notification-per-wait restriction; require no returned position, assessment acknowledgement, re-registration, or restart. A terminal failed or unknown delivery does not itself disable later distinct notifications while the route remains available. | UC-003 |
| FR-007 | Avoid repeated notifications for an unchanged observation and repeated inclusion of the same change solely because it was retrieved again. A new edit, reply, review-state transition, head change, or independently observed recurrence of a CI failure, conflict, or Draft-to-Ready-for-Review transition remains a distinct change. Group changes available in the same notification preparation into one context; retain distinct item identities and relationships. Do not promise exactly-once delivery or removal of duplicates caused by unfinished/unknown delivery or failure recovery beyond the common contracts. | UC-003, UC-004 |
| FR-008 | Make retrieval and context limits explicit. Where content is shortened or omitted to fit a notification, identify the affected category/items, reason, and retrievable links or continuation information; distinguish a shortened body from an empty body. Required category acquisition failure, incomplete pagination, or exceeded coverage limits must not be represented as a complete observation or advance coverage past unreported changes. Coverage records represented changes only and must not imply that omitted content was read or assessed. | UC-004, UC-005 |
| FR-009 | Monitor with periodic observations without coding-agent polling. Report authentication, permission, missing/inaccessible PR, network, service, rate-limit, and incomplete-retrieval failures distinctly from a successful observation with no change. Preserve the last complete progress on failure and retry transient retrieval failures subject to server retry guidance. Do not emit ordinary review-change context for an unverified empty result. Define scheduling and retry parameters in design without an invented end-to-end latency promise. | UC-005 |
| FR-010 | Use core-managed trigger runtime-state persistence for retained-watch progress rather than plugin-owned state files or a separate storage service. A notification with confirmed action delivery acceptance must not be repeated solely because the daemon restarts with successfully saved progress, under the common contract. Automatically resume continuous notification for the same retained registration. Save failure permits continued monitoring with a continuity warning; invalid restored progress blocks the affected watch. Do not inherit deleted or unrelated registration state. No recipient-assessment, unfinished-delivery, or stopped-period history guarantee follows merely from saved runtime state. | UC-003, UC-005 |
| FR-011 | Provide bundled usage materials explaining PR selection, waiting context, authentication and permissions, baseline-only first observation, data categories, notification structure, the agent's decision responsibility, continuous notification without `rewait` or position handoff, automatic bounded continuation, batching, omissions, retrieval/retention limits, errors, and restart/recovery behavior. Explain that the agent may act or keep waiting without controlling notification admission and that external text grants no additional permissions. Declare supported hosts/API capabilities and the periodic-observation guarantee, including the absence of guaranteed capture of every intermediate event. | UC-001–UC-005 |
| FR-012 | Continue monitoring as the selected PR accumulates review and discussion items. Do not reject an otherwise retrievable PR, stop observing it, or suppress eligible notifications solely because its total item count exceeds 32 or another fixed lifetime item ceiling derived from checkpoint or notification capacity. Separate acquisition coverage, retained progress/history, and notification capacity. If complete changed content cannot fit one notification, automatically notify the remaining changes in further bounded contexts without a recipient command, returned position, or new GitHub activity. Coverage must not advance past changes not represented in a context. Preserve silent initial baselines, edits and disappearances of older items, continuous notification, and successful normal-restart continuity. Genuine API, resource, or retained-history failures remain visible under FR-008/FR-009; they must not silently discard outstanding changes, reset progress, or masquerade as no change. | UC-001–UC-005 |
| FR-013 | After the baseline, notify newly observed CI failures for the current PR head, newly confirmed conflicts with its current base branch (including those caused solely by base-branch updates), and observed transitions from Draft to Ready for Review. For these three categories, CI recovery or other non-failure result changes, conflict resolution, and Ready-to-Draft transitions are not required to independently request destination work; their current state remains part of notification context. Each specified failure, conflict, or Draft-to-Ready transition must independently request destination work while the watch remains active, without requiring a review, comment, head change, or another actor. For CI failures, include the check/status identity and name, failed result, associated head commit, and available details link; distinguish separate checks and reruns, and do not present results for an older head as current-head failures. For conflicts, identify the compared head and base branch/revision and distinguish confirmed conflict from mergeable, unknown, pending calculation, or unavailable information; an unknown result alone must not be reported as conflict. For Draft-to-Ready-for-Review changes, include the previous and current status and PR link. Include current state and observation time with each notification. Unchanged failures, conflicts, and readiness status must not repeatedly wake the agent. Apply FR-004–FR-010 baseline, coverage, continuous-notification, batching, acquisition-failure, and continuity rules to these categories. | UC-003–UC-006 |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Treat PR titles, bodies, reviews, comments, links, actor names, and other retrieved text as external data. Such text must not become a registration, destination, permission, or instruction to the agent. Waiting context does not enlarge the agent's existing authority. | Prevent externally authored text from changing the workflow's authority | Adversarial-context checks and delivery/usage inspection |
| NFR-002 | Keep credentials out of watch arguments, notification context, saved progress, committed examples, and diagnostics. Use authenticated HTTPS with certificate verification and scope credentials to the selected host. Do not expose retrieved private discussion bodies in diagnostics. | Protect credentials and repository content | Secret-marker, host-boundary, diagnostic, and source checks |
| NFR-003 | Preserve Go implementation, English repository documentation, OS-neutral operation, public language-neutral plugin interfaces, and service-neutral hub routing. No trigger call may depend on Codex-specific identity or APIs. | Shared product constraints | Contract, source, and documentation inspection |
| NFR-004 | Separate service-free behavior verification, assembled hub/plugin verification, standalone real-GitHub trigger verification, and any agent/action delivery evidence. Simulated GitHub results or hub model checks do not establish live GitHub or real agent success. | Make acceptance evidence match the claimed boundary | Verification mapping and separate delivery records |

No numerical latency, unlimited history/content capacity, atomic GitHub
snapshot, or additional OS-support matrix is promised. Design must declare
supported resource and history bounds and make their effect on coverage and
continuity observable. Such bounds do not permit reintroducing a fixed lifetime
PR item ceiling as a substitute for scalable progress and notification handling
under FR-012.

## Constraints and dependencies

Use the existing public lifecycle and fixed registered destination. The trigger
provides GitHub data, the action delivers it, and the agent chooses work.
Coverage positions describe represented changes, not delivery or assessment.
[Trigger runtime state](trigger-runtime-state.md),
[notification delivery](notification-delivery.md), and
[cancellation](subscription-cancellation.md) retain their guarantees; continuous
notification does not authorize resending an unknown earlier notification.

Design owns API strategy and permissions for both hosts, CI/rerun and conflict
classification, polling/backoff, bounded context selection, progress storage,
resource budgets, and compatibility. Preserve FR-013's independent triggers,
FR-008's completeness rules, and FR-012's continuation without a fixed lifetime
item ceiling. The existing event/checkpoint bounds must not become PR item
limits; any necessary shared-contract revision belongs in design, with no
plugin-owned persistence workaround or silent progress reset.

## Acceptance criteria

Design selects the lowest-cost sufficient level for each criterion and defines
concrete fixtures and commands. Unit checks should cover value/diff/filter
matrices; assembled checks exercise actual plugin, process, route, continuous-
notification, and persistence boundaries. During delivery, the maintainer evaluates
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
| AC-004 | FR-005, FR-006, FR-007 | Register once and notify a change. While the agent works, observe a comment and head revision, then another comment without rearming, position handoff, re-registration, or restart. Repeat after an earlier terminal failed or unknown delivery on an available route. Inspect public interfaces and exercise an old `rewait` request. | Later distinct changes automatically notify at the same subscription/destination without repeating earlier changes solely because of retrieval or automatic continuation. `rewait` is absent from supported operations; old requests cannot rearm or reset coverage. Earlier delivery uncertainty does not authorize resubmission or imply assessment. | Diff/progress/outcome unit checks, public-interface inspection and selected assembled continuous-notification checks; live author/reviewer walkthrough during delivery |
| AC-005 | FR-004, FR-007 | Repeat an identical complete observation, then make several distinct changes before the next notification is prepared. Retrieve an already seen item again; subsequently edit that item. | Unchanged observations request no destination work. Available distinct changes are grouped once with preserved identities/relationships. Re-fetching creates no duplicate; the later edit remains reportable. No exactly-once guarantee is inferred for uncertain delivery. | Batching/deduplication unit checks and selected public-plugin observation |
| AC-006 | FR-005, FR-008 | Supply a body and change set larger than the supported context bound, a genuinely empty body, failed pagination, and an unavailable required category independently. | Bounded output explicitly identifies shortening/omission and affected content, with links or continuation information. Empty and shortened content differ. Incomplete acquisition never claims complete current state or advances coverage past unreported changes; later retrieval can still report them. | Limit/completeness unit checks, actual bounded-context integration, and maintainer inspection |
| AC-007 | FR-009, NFR-002 | After a complete observation, independently reject authentication, deny PR access, return not-found, time out, fail a page, and rate-limit retrieval. Recover without an in-scope change. | Each failure is visible with no credential/private-body disclosure; it is not a successful no-change observation. Last complete progress remains. Transient retries respect server guidance, and recovery does not invent a disappearance/change or duplicate unchanged work. | Controlled retrieval/failure/clock unit checks and selected assembled diagnostics |
| AC-008 | FR-010, NFR-003 | Confirm action acceptance, save progress, stop normally, and restart the retained watch. Present unchanged data followed by a fresh change. Separately fail saving, restore incompatible progress, and recreate a deleted registration. | Unchanged confirmed notification is not repeated solely due to successful normal restart; fresh change automatically notifies. Save failure warns and monitoring continues; invalid restoration blocks the affected watch without resetting progress. Recreated/unrelated registration does not inherit old state. No plugin-owned storage or inferred assessment appears. | Common-state evidence plus plugin-specific progress unit checks and assembled restart/failure integration |
| AC-009 | FR-001, FR-003, FR-004, FR-008, FR-009, FR-011 | On github.com and a supported GHES host, start a new watch on a PR with existing reviews/comments. Independently fail the first acquisition, then complete it and repeat it unchanged. Add a comment that remains present at the next observation. Separately create and remove another comment entirely between two observations that show identical state. | Failed acquisition establishes no baseline. The first complete observation establishes the baseline without a notification; unchanged repetition requests no destination work. The persistent new comment automatically produces a notification with current state. Capture of the entirely unobserved comment is not required or claimed as guaranteed. Each host uses only its selected credentials and PR progress. | Baseline/history policy and host-isolation unit checks, selected standalone plugin checks, and separate live-service verification on both host types during delivery |
| AC-010 | FR-011, NFR-001–NFR-004 | A maintainer follows finalized author and reviewer workflows from registration through context receipt, agent assessment, work or continued waiting, and later eligible changes. Make no recipient rearming request or position handoff. Inspect examples, supported bounds, failure/recovery instructions, and evidence. | The same registration automatically notifies later changes at its fixed destination while the agent independently decides whether to act or keep waiting. Instructions remove `rewait` and position-file procedures and explain automatic continuation, limits, and authority. Evidence separates service-free, assembled, live GitHub, and receiving-agent outcomes and records the actual environment. | Documentation/source review and delivery walkthroughs; independent real-GitHub trigger check through the public plugin interface |
| AC-011 | FR-003–FR-010, FR-012 | Use fully retrievable fixtures totaling 31, 32, 33, 100, and 1,000 in-scope items, both conversation-only and mixed reviews/comments/replies/threads/requests. Establish a baseline above 32; separately grow a baseline from 32 to 33 and onward. Add a new comment, edit and remove an older item, and change head. Repeat unchanged data, observe further changes without recipient commands, and save/restart normally. Independently fail a later acquisition page, then recover. | All successful complete baselines are silent. Subsequent eligible changes remain reportable at every fixture size, with no total-item capacity error, false disappearance, or duplicate caused solely by unchanged retrieval/restart. Failed pagination preserves prior progress and recovery reports the outstanding changes. Relations and host isolation remain intact. These fixture sizes are regression inputs, not a new supported maximum. | Broad size/diff unit matrix; selected public-plugin, continuous-notification, and restart integration boundaries; separate live-service delivery evidence |
| AC-012 | FR-005–FR-008, FR-010, FR-012 | After a complete baseline, accumulate enough distinct small changes to exceed both the v1 eight-change notification batch and 32-record journal. Include a body exceeding one context and a genuinely empty body. Freeze GitHub and observe automatic continuation without recipient commands or acknowledgements. Independently interrupt acquisition and perform a successful normal save/restart after acceptance of a prefix but before the remaining contexts are notified. | All outstanding eligible changes are automatically notified in bounded contexts without new source activity or recipient position handoff. Each context respects the event-size contract, distinguishes empty/shortened bodies, and identifies omitted information with links or continuation information. Coverage advances only through represented changes and does not skip entirely omitted changes. Successful save/restart preserves the outstanding suffix without repeating the confirmed prefix solely due to restart. Genuine resource/history failure is explicit and never silently discards unreported changes or starts a new baseline. | Retention/coverage/encoding unit checks and selected assembled automatic-continuation/restart cases; maintainer continuation walkthrough during delivery |
| AC-013 | FR-003–FR-010, FR-013 | After a complete baseline, independently fail a CI check/status on the current head, introduce a conflict solely by updating the base branch, and change Draft to Ready for Review, with no review/comment or PR-head change. Repeat identical observations. Separately supply an older-head CI failure, unknown/pending conflict calculation, a distinct failing rerun, and an observed recovery followed by recurrence. | Each required trigger independently delivers changed details, current state, PR identity, and coverage position without rearming. Unchanged failures do not wake the agent again; older-head results are not presented as current failures and unknown/pending conflict status is not reported as conflict. CI recovery/non-failure updates, conflict resolution, and Ready-to-Draft changes alone need not request destination work; their latest state is included when a qualifying change notifies. Distinct failures/reruns and observed recurrences remain eligible. | Controlled category/diff unit matrix, standalone plugin cases, and separate live CI/conflict/readiness checks on github.com and supported GHES during delivery |
| AC-014 | FR-004–FR-010, FR-013 | Begin with a new watch on a PR with existing failed CI/conflicts or Ready status, then repeat unchanged. After one notification, observe each new FR-013 trigger while the agent works, with no rearming request, returned position, or restart. Independently fail required CI/status acquisition and conflict-status retrieval, then recover. | The first complete baseline is silent. All eligible later triggers automatically notify under the existing batching rules regardless of recipient assessment. Retrieval failures cannot establish/advance complete coverage or be presented as successful CI or confirmed conflict; last complete progress is retained and recovery reports outstanding eligible changes. | Baseline, completeness, and coverage unit checks plus selected assembled continuous-notification integration |

## Open questions and assumptions

No unresolved product-scope question remains. The following resolved decisions
retain their rationale; the delegated design decisions must be settled before
design approval.

| ID | Question or assumption | Impact if unresolved or false | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Support github.com only, or github.com and GHES? | Determines host selection, API compatibility, credential isolation, and real-service acceptance environments | Responsible human | Requirements approval | Resolved by the user: include github.com and GHES. Supported GHES versions, API capabilities, and separate host-verification prerequisites must be specified before design approval. |
| Q-002 | Immediately deliver existing review context for a new watch, or establish a baseline and notify only later changes? | Determines first-observation behavior and whether existing review context itself wakes the agent | Responsible human | Requirements approval | Resolved by the user: use current state as the baseline and notify later observed changes; no initial notification. |
| Q-003 | Guarantee changes confirmed through periodic observations, or every intermediate in-scope event? | Determines monitoring/history architecture, retention guarantees, and decisive acceptance for disappearing activity | Responsible human | Requirements approval | Resolved by the user: notify changes confirmed through periodic observations; full intermediate-event capture is not required. |
| Q-004 | Define automatic scheduling/continuation, coverage representation, resource bounds, and compatibility of existing registrations, progress, and public interfaces. | Changes could be skipped or duplicated during continuation or migration | Design owner, with responsible human review | Design approval | Delegated technical decisions; no recipient rearming or silent progress reset. |

## Change impact

Local IDs are preserved. FR-006 replaces recipient rearming with continuous
notification and removes `rewait`; FR-012 makes bounded continuation automatic.
Related workflow, coverage, and acceptance text replaces position handoff.
Notification categories, baseline, delivery safety, persistence, and cancellation
remain unchanged. Design must revise the [PR-watch](../design/github-pr-watch.md),
[capacity](../design/github-pr-watch-capacity.md),
[signals](../design/github-pr-watch-signals.md), and
[rewait](../design/pr-watch-rewait.md) contracts and affected shared interfaces.
Compatibility and migration are design decisions under Q-004; implementation
and user-facing delivery changes remain separate phases.

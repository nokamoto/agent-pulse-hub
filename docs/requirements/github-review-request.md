---
type: Requirement
title: GitHub review request trigger
description: Notify an action when an open pull request requests review from the authenticated GitHub user.
sources:
  - id: initial-requirements
    resource: https://github.com/nokamoto/agent-pulse-hub/pull/72
    title: Initial GitHub review request trigger requirements
  - id: bundled-plugins
    resource: bundled-plugins.md
    title: Bundled trigger and action plugins
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: runtime-state
    resource: trigger-runtime-state.md
    title: Trigger runtime state persistence
  - id: gh-environment
    resource: https://cli.github.com/manual/gh_help_environment
    title: GitHub CLI environment variables
  - id: github-search
    resource: https://docs.github.com/en/rest/search/search
    title: GitHub REST search API
  - id: ghes-search
    resource: https://docs.github.com/en/enterprise-server@3.19/rest/search/search
    title: GitHub Enterprise Server REST search API
---

# GitHub review request trigger

## Problem and goals

Developers need to start an action when a pull request asks them for review,
without asking a coding agent to poll GitHub. A bundled trigger must monitor
GitHub, report the matching pull request URL, and retain its progress through
the daemon's runtime-state facility. Operators should be able to use an
existing token or GitHub CLI login without maintaining a separate state store.

Success means detecting newly observed direct review requests for the
authenticated user after the watch establishes its baseline. The operator can
start a watch and wait for new work without receiving notifications for
requests present in the first complete observation. An unchanged request
should stay quiet during normal operation and after a successful normal restart.

## Sources and decisions

The [initial requirements PR](https://github.com/nokamoto/agent-pulse-hub/pull/72)
records the earlier decisions. This revision adopts the requested silent
baseline for new watches.

- Reuse established authentication inputs to reduce adoption effort: prefer
  `GH_TOKEN`, then `GITHUB_TOKEN`, then the selected host's saved `gh` login.
  A token supplied through an environment variable must not require `gh`.
- Monitor direct requests for the authenticated user on open pull requests,
  across repositories visible to that identity and credential. Provide no
  repository or organization filter.
- Support github.com and GitHub Enterprise Server (GHES). Each registration
  selects one host, defaulting to github.com; different hosts use separate
  registrations.
- Detect changes in periodically observed review-request state, not every
  historical request event. Use a configurable interval, defaulting to 60 seconds.
- Use the first complete observation as a silent baseline. Preserve progress
  using core-owned persistence; do not promise recovery of every stopped-period
  event.
- Send only the PR URL as event context. PR metadata, bodies, and diffs are
  not part of that context.
- An authentication failure must not silently select a lower-priority
  credential. Closed and merged PRs are excluded.

The [MVP](mvp.md) governs routing and one-attempt delivery.
[Bundled-plugin requirements](bundled-plugins.md) govern packaging and usage
materials. [Trigger runtime state](trigger-runtime-state.md) governs persistence,
normal-restart continuity, and state-failure behavior.

## Terms and operator workflow

A **matching request** is an open PR on the selected host that currently names
the authenticated user as a requested reviewer. A request for a team alone
does not match. A personal request created by team auto-assignment does match;
the identity of the actor or automation assigning it is not a filter.

A **complete observation** is a successful collection of the matching set
within the retrieval coverage supported by the design. Partial pages,
incomplete search results, or known exceeded retrieval limits are not complete
observations. This definition does not promise unlimited GitHub API coverage.

A **baseline** is the first complete observation for a watch without prior
progress, including an observation with no matches. The baseline is the start
of change detection. Any request present in that observation is treated as an
existing request, including one made after registration but before the baseline.

The operator configures credentials for a host, selects this trigger and an
action, and registers a watch. The trigger identifies the authenticated user
and periodically checks for matching requests. A new watch emits a separate
URL event for each match newly observed after the baseline. A normal restart
resumes the retained registration without re-registration and restores any
compatible saved progress, subject to the upgrade rules below.

## Scope

- In scope: a bundled trigger; github.com and GHES; host-specific authentication;
  direct requests on open PRs; repository-unfiltered polling; a silent baseline;
  observed disappearance and reappearance; PR URL context; core-managed progress;
  actionable failure reporting and usage instructions.
- Out of scope: team-only requests; repository/organization filtering; PR body
  or diff retrieval as a feature; review submission; webhook receivers or GitHub
  App installation management; complete request-event history; automatic delivery
  retries; exactly-once delivery; crash or power-loss recovery guarantees.

All functional requirements below are required for this scope.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator registers a host with a token or existing CLI login | Monitoring uses the selected host and authenticated identity | Invalid credentials do not select a different identity |
| UC-002 | The trigger completes an observation after the baseline and finds a direct request absent from the preceding complete observation | Its URL is emitted once for the observed request occurrence | Team-only, closed, and merged PRs do not match |
| UC-003 | A matching request disappears and later returns | A complete observation of absence makes the reappearance eligible for notification | Transitions entirely between observations need not be detected |
| UC-004 | Operator starts a new watch or normally restarts a retained watch | New watches establish a silent baseline. Retained watches continue from saved progress when available. | In-flight delivery, unsaved state, and crash recovery have limited guarantees |
| UC-005 | GitHub access or state storage fails | The operator sees the cause; incomplete results do not erase progress | Save and restore failures follow the common runtime-state contract |

## Functional requirements

| ID | Requirement | Rationale / use case |
| --- | --- | --- |
| FR-001 | Support github.com and GHES. Select one host per registration, defaulting to github.com. Keep each host's requests, identity, credentials, and progress separate. Multiple hosts require separate registrations. | UC-001 |
| FR-002 | Use the first nonempty credential source in this order: `GH_TOKEN`, `GITHUB_TOKEN`, saved `gh` authentication for the selected host. Determine the user from that credential on that host. Environment-token operation must not require the `gh` executable. If a selected credential is rejected, report the failure and do not fall back. If neither environment token nor usable saved authentication is available, report the missing prerequisite without interactive login. | UC-001 |
| FR-003 | Monitor open PRs requesting review directly from the authenticated user across repositories available through the selected host and credential, without a user-configurable repository or organization filter. Exclude team-only requests and closed or merged PRs. Do not assume visibility beyond the credential's permissions. | UC-002 |
| FR-004 | Poll without involving the coding agent. Default the configured interval to 60 seconds and allow a positive operator-specified interval. Reject zero, negative, or malformed intervals with an actionable configuration error. API response time, rate limits, and outages may delay observations; the interval is not an end-to-end delivery deadline. | UC-002, UC-005 |
| FR-005 | For a watch without prior progress, record the first complete observation as its baseline and emit no events for those matches. A failed or incomplete observation leaves the watch waiting for its baseline. After the baseline, emit one event for each match newly present compared with the preceding complete observation. An unchanged match stays quiet. If a complete observation establishes absence, the next reappearance is eligible even if the request was first recorded in the baseline. Transitions entirely between complete observations may go undetected. | UC-002–UC-004 |
| FR-006 | Each event's context must be exactly the PR URL, with no title, repository field, PR number field, body, diff, or explanatory wrapper. Emit distinct events for distinct matching PRs. Preserve the existing hub routing and action-selection contract. | UC-002 |
| FR-007 | Participate in core-managed trigger runtime-state persistence. The trigger owns compatible progress data but must not own state files or a separate persistence service. When saving progress under the silent-baseline behavior, preserve the distinction between an established baseline with zero matches and a watch still waiting for its baseline across a successful normal restart. A retained registration must resume without repeating a request whose notification had confirmed delivery acceptance solely because of restart. Follow the common registration-isolation, explicit-reset, save-failure continuation, and restore-failure blocking behavior. | UC-004, UC-005 |
| FR-008 | On resumption from saved progress, compare the next complete observation with that progress. Keep unchanged baseline matches quiet and notify newly observed matches that are still present. A watch still waiting for its baseline establishes it under FR-005. Apply the upgrade rules below to compatible checkpoints from the version that notified initial matches. A previously matching PR that disappears and returns only while stopped need not produce a new event. Queued, failed, unknown, or in-progress delivery outcomes receive no additional recovery guarantee or automatic retry. | UC-003, UC-004 |
| FR-009 | Report authentication, permission, network, service, rate-limit, and incomplete-retrieval failures without treating them as an empty matching set. Preserve prior progress on incomplete observations. Retry transient retrieval failures and rate limits, respecting server retry guidance; neither repeated failures nor recovery may fabricate a disappearance. Known retrieval limits must be visible, not silently truncate the claimed monitoring coverage. | UC-005 |
| FR-010 | Provide bundled instructions and examples covering host selection, separate host registrations and credentials, authentication precedence, permissions, interval, when the baseline is established, restart and reset behavior, URL-only context, state recovery, and retrieval limits. State supported GHES versions and required API capabilities. Explain how to configure tokens independently for different hosts. | UC-001–UC-005 |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Keep credentials out of watch arguments, event context, runtime checkpoints, committed examples, and diagnostics. Scope authentication to the selected host and use authenticated HTTPS with certificate verification. | Avoid leaking credentials through saved registrations or output | Configuration/source review and controlled secret-marker and host-isolation checks |
| NFR-002 | Preserve Go implementation, English documentation, OS-neutral usage, service-neutral hub behavior, and the public trigger interface. The plugin must not require a particular developer OS or a coding agent for polling. | Existing project constraints | Contract/source and documentation inspection |
| NFR-003 | Separate service-free unit/assembled evidence from real github.com and GHES verification. A simulated response or successful hub model test must not be reported as proof of real-service compatibility. | Independent bundled-plugin verification | Review verification mappings and separately retained delivery evidence |

No numerical end-to-end latency, unlimited repository/request capacity, or
additional platform matrix is promised. Supported retrieval and checkpoint
capacity must be made explicit in design, with visible failure rather than
silent loss of coverage or progress.

## Constraints and dependencies

Use the public lifecycle and the optional runtime-state contract defined by
[the plugin specification](../design/plugin-v1.md) and
[runtime-state design](../design/trigger-runtime-state.md). Successful
notification means confirmed action delivery acceptance, not recipient
processing. The normal-restart guarantee requires successful persistence and
compatible saved state; save failures continue monitoring with a continuity
warning, while invalid restored state prevents the affected watch from starting.

The authentication precedence in FR-002 applies to both platforms. It is the
plugin's contract: GitHub CLI itself normally uses `GH_ENTERPRISE_TOKEN` and
`GITHUB_ENTERPRISE_TOKEN` for GHES. Those variables must not implicitly override
the plugin's selected environment token or its saved-login fallback. Selection
of the API client and CLI invocation mechanics belongs in design.

GitHub search has pagination, result-count, repository-coverage, and incomplete-
result limits. A repository-unfiltered watch does not remove these external
limits. Design must choose a retrieval strategy and declare supported coverage,
including how limitations are detected and reported. It must also fit necessary
progress into the existing bounded checkpoint contract without silently dropping
state required for restart continuity. If these contracts cannot support the
selected behavior, report the required upstream change rather than weakening
the requirement during implementation.

Exact argument names, API queries, token permissions by authentication type,
GHES version compatibility, polling/backoff scheduling, checkpoint encoding,
and notification/progress coordination belong in design. Do not introduce a
GitHub-specific hub API or change common retry and persistence guarantees here.

### Upgrade from notification of initial matches

Keep compatible checkpoints from the version that notified initial matches as
prior progress. Its empty checkpoints do not distinguish an unobserved watch
from a watch that observed zero matches. Resume both as a watch with an empty
matching set, so new matches on the next complete observation are eligible for
notification. For nonempty checkpoints, use the saved delivery state to
determine first-attempt eligibility. A still-present match remains eligible if
the checkpoint records no prior attempt. Previously attempted matches stay
quiet while they remain present. A registration with no saved progress
establishes a silent baseline under FR-005.

## Acceptance criteria

Design must choose the lowest-cost sufficient test level for each criterion.
Use controlled GitHub and credential boundaries for service-free checks;
exercise actual public plugin/hub/process boundaries for assembled behavior.
Real-service verification is performed during delivery and evaluated by the
delivery maintainer, retaining host/version, revision, redacted configuration,
stimulus, observed URL events, and relevant delivery outcomes. The design
specifies the evidence that confirms the required matches were included in a
complete observation during these checks.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, NFR-001 | Independently configure github.com and a GHES host. Supply both environment tokens, only `GITHUB_TOKEN`, neither token with saved host login, and no usable credential. Repeat with a rejected higher-priority token and a valid lower-priority source. | Precedence and authenticated identity match FR-002. Environment-token cases work without `gh`; saved login is host-specific. Missing or rejected authentication is actionable, with no silent fallback or credential transfer to another host. | Credential-selection unit checks, assembled environment/CLI boundary checks, and delivery authentication on both platforms |
| AC-002 | FR-003, FR-005, FR-006 | Start a fresh watch with an existing direct request A. First return an incomplete result missing A, then a complete observation containing A. After that, introduce direct requests B and C in distinct repositories, a team-only request, another user's request, and closed/merged PRs. Repeat the resulting complete observation. | The incomplete result leaves the baseline unset. A stays quiet through the baseline and subsequent observations. Exactly one event is emitted for each of B and C, and none for excluded PRs. No repository filter is required or offered. Each context equals its PR URL. | Matching/diff unit checks and standalone public-plugin observation. Direct/team/excluded checks on the real service during delivery. |
| AC-003 | FR-005, FR-008 | Include A in a complete baseline, then observe a complete set without A, then a set containing A. Separately cancel and re-request A entirely between two observations that both contain A. | A is quiet in the baseline. The observed reappearance produces one URL event. A stays quiet when it is present in both complete observations. | Controlled observation-sequence tests |
| AC-004 | FR-004 | Omit the interval, configure a different positive interval, and independently supply zero, negative, and malformed values. | Poll scheduling uses the 60-second default or supplied interval under a controlled clock; invalid values are rejected clearly. No coding-agent polling occurs. | Clock/configuration unit checks and source inspection |
| AC-005 | FR-005, FR-007, FR-008, NFR-002 | Establish a baseline with A and confirm action acceptance for a later match B. Persist progress, stop normally, and restart the retained registration with A, B, and a fresh C present. Also exercise two distinct registrations and explicit reset/recreation under the common contract. | A and B stay quiet across restart. Only C is notified. State stays with its registration and resumption needs no re-registration or plugin-owned state storage. After explicit reset/recreation, the first complete observation of the current matches is silent. | Assembled normal-restart/isolation verification using the actual core runtime-state facility and controlled GitHub boundary, plus source review |
| AC-006 | FR-005, FR-009 | After a complete observation containing A, independently fail a page, return an incomplete result, signal a known retrieval limit, deny access, time out, or rate-limit the request. Recover with a complete observation containing unchanged A and a new match B. | Failures identify their category without secrets. No failed or partial observation establishes A's absence. A stays quiet and B produces one URL event on recovery. Transient and rate-limit retries respect server guidance. Unsupported coverage is reported explicitly. | Retrieval and failure unit checks and selected assembled diagnostics checks |
| AC-007 | FR-007, FR-008, NFR-001 | Fail checkpoint saving while polling continues and separately restore incompatible progress. Keep a matching request present in subsequent complete observations while its delivery is queued or in progress, or after it has failed or its acceptance is unknown. | Save failure warns about continuity and permits fresh notifications. Incompatible restoration prevents the affected watch from starting and does not silently reset its state. The trigger does not emit a retry for the same occurrence. Recovery of unfinished deliveries has no additional guarantee. Checkpoints and diagnostics contain no credential values. | Reuse common state-failure evidence plus plugin-specific state validation and coordination tests |
| AC-008 | FR-005, FR-010, NFR-001–NFR-003 | On github.com and a declared supported GHES version, start a new watch with an existing direct request A. Verify the complete baseline contains A, then create direct request B. Confirm action acceptance for B and successful progress persistence. Restart normally with both requests still present and verify a complete observation containing both. Inspect instructions, examples, and verification records. | On both hosts, A stays quiet and B yields its URL once. Both stay quiet after the restart. OS-neutral instructions state baseline timing, permissions, host credential isolation, supported versions and capacity, interval, recovery, and limits. Evidence separates unit, assembled, and real-service results. | Maintainer documentation review, source inspection, and separate delivery walkthroughs |
| AC-009 | FR-005, FR-007, FR-008 | Under the silent-baseline behavior, normally stop and restart one retained registration before its first complete observation. For another, establish and save a baseline with zero matches, then normally stop and restart it. Observe a complete set containing A for each restored watch. | A stays quiet for the watch still waiting for a baseline and produces one URL event for the watch with a saved empty baseline. | Controlled checkpoint and observation-sequence tests |
| AC-010 | FR-007, FR-008 | Restore two compatible checkpoints from the version that notified initial matches. One has an empty matching set. The other contains three still-present matches with distinct outcomes: never attempted, attempted without confirmed acceptance, and confirmed acceptance. In both cases, include an additional match in the next complete observation. | Both watches notify the additional match once. The second watch also emits one event for the match never attempted. The two previously attempted matches stay quiet. | Controlled upgrade and observation-sequence tests |

## Open questions and assumptions

No unresolved product-scope question remains. The following bounded design
decisions must be resolved before design approval; they are not approval to
claim compatibility or capacity without evidence.

| ID | Decision | Impact if unresolved | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Supported GHES versions, API capabilities, token permissions, and real-service verification environment | Compatibility and delivery prerequisites cannot be assessed | Design owner, with responsible human review | Design approval | Open design decision |
| Q-002 | Complete retrieval strategy, supported capacity, and bounded progress representation | Search limits or checkpoint overflow could defeat coverage or restart continuity | Design owner, with responsible human review | Design approval | Open design decision |

## Change impact

FR-005 replaces notification of initial matches with a silent baseline.
FR-007, FR-008, and FR-010 cover retaining, restoring, and documenting the
baseline. AC-002, AC-003, AC-005, and AC-008 verify the revised behavior.
AC-007 clarifies the existing delivery-failure check. AC-009 and AC-010
cover baseline state across restart and upgrade. No existing ID is retired.

Update the [plugin design](../design/github-review-request.md) and its
verification mapping for initialization, compatible checkpoint restoration, and
restart. The shared routing, registration, runtime-state, and bundled-artifact
contracts remain authoritative. Update the
[usage guide](../../plugins/triggers/github-review-request/readme.md) and
real-service verification during delivery.

<!-- Apply phase:requirements explicitly; this template does not set labels. -->
<!-- Requirements phase only: changes belong in docs/requirements/. -->

## Purpose and scope

<!-- Explain the problem, intended outcome, scope, and exclusions. -->

## Requirements to review

<!-- Link requirement documents and related approved requirements or PRs. -->

## Decisions requiring human review

<!-- Summarize scope, acceptance criteria, constraints, recommendations and rationale. -->

## Verification and requirements review

<!-- Record reviewed base/head commits (or local diff), overall verdict, and
     all R01-R12 results from the review-requirements checklist with evidence.
     Include checks not run and their impact. Do not pre-check results. -->

## Open questions and risks

<!-- Identify design blockers and deferred decisions with owners and resolution stages.
     State None if there are none. Keep a blocked PR in draft. -->

## Changes since previous review

<!-- State Initial review, or summarize changed decisions/IDs and downstream impact. -->

## Phase handoff

- [ ] The complete diff changes only docs/requirements/.
- [ ] The phase:requirements label is applied.
- [ ] All applicable review checklist items pass; evidence is recorded above.
- [ ] Questions affecting design have been resolved.

## Human approval and merge

The responsible human can approve by reviewing the final revision and merging
this PR personally, including their own PR. No separate Approve review is
needed for that route. Alternatively, record approval of the current head with
an Approve review or an explicit PR comment, then merge or authorize an agent
to merge that revision. Repository protections and required checks still apply.

Design starts after approval and merge are verified under the
[AIDD handoff rules](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#agent-verification-and-handoff-evidence).
The downstream agent records the route, human actor, final head SHA, merge time,
merge commit SHA, and any explicit approval reference. These are post-merge
handoff evidence, not boxes the PR author must pre-check.

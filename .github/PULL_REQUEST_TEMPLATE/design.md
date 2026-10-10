<!-- Apply phase:design explicitly; this template does not set labels. -->
<!-- Design phase only: docs/design/ and separate specifications governed by it. -->

## Purpose and scope

<!-- Explain the intended outcome, scope, and exclusions. -->

## Requirements and design to review

<!-- Link the merged requirements PR, approved source revision, requirement
     documents, and design documents. Record its normal approval route, human
     actor, final head SHA, merge time, merge commit SHA, and any explicit approval
     reference under the AIDD handoff rules. Human merge needs no separate
     Approve review, including for the human's own PR.
     Summarize coverage and related designs. Link the design sections governing
     specification documents: paths, purpose and readers, authority and
     derivation, contract decisions, and constraints on routine implementation
     choices. Link each specification included here or already approved and
     merged. If contracts stay in the design, identify that decision.
     When this design session follows implementation feedback, link the stop
     report and explain how the specification and design resolve the gap. -->

## Decisions requiring human review

<!-- Summarize alternatives, recommendations, rationale, and tradeoffs. -->

## Verification and design review

<!-- Record reviewed base/head commits (or local diff), overall verdict, and
     all D01-D13 results from .agents/skills/review-design/references/checklist.md
     with evidence. Include guardrail results, manual structure checks for separate specifications outside docs/design/, and checks not run and their impact.
     Distinguish planned verification from executed checks. -->

## Open questions and risks

<!-- Identify implementation blockers and deferred decisions with owners and
     resolution stages. State None if absent. Keep blocked PRs in draft. -->

## Changes since previous review

<!-- State Initial review or summarize changed decisions and downstream impact. -->

## Phase handoff

- [ ] The complete diff stays within docs/design/ and its governed specifications, including deletions and both sides of renames.
- [ ] The phase:design label is applied.
- [ ] Source requirements have human approval and are merged.
- [ ] All applicable review checklist items pass with evidence above.
- [ ] Questions affecting implementation have been resolved.

## Human approval and merge

The responsible human can approve by reviewing the final revision and merging
this PR personally, including their own PR. No separate Approve review is
needed for that route. Alternatively, record approval of the current head with
an Approve review or an explicit PR comment, then merge or authorize an agent
to merge that revision. Repository protections and required checks still apply.

Implementation starts or resumes after design approval and merge are verified under the
[AIDD handoff rules](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#agent-verification-and-handoff-evidence).
The downstream agent records the route, human actor, final head SHA, merge time,
merge commit SHA, and any explicit approval reference. These are post-merge
handoff evidence, not boxes the PR author must pre-check.

<!-- Apply phase:design explicitly; this template does not set labels. -->
<!-- Design phase: docs/design/ and the narrow verification artifact exception
     defined by the AIDD design verification deliverables. -->

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
     with evidence. Include design and docs-layout guardrail results, and checks
     not run and their impact.
     Distinguish planned verification from executed checks. -->

<!-- Link selection rationale comparing defect-detection value with implementation
     complexity and maintenance and execution costs, including sufficient cheaper
     verification, consolidation, and removed candidates with retained coverage.
     Removal of a candidate does not remove a product requirement. Link upstream
     approval and merge for changes to approved required behavior or cases.
     Link the selected concrete command-level Ginkgo integration acceptance cases
     initially static Pending, their stable
     IDs, requirement/acceptance mappings, inputs, actions, expected observable
     results and failure boundaries, and governed paths directly in
     cmd/<command>/*_integration_test.go with //go:build integration for all
     case and suite files, and fixtures in that command's testdata/.
     Record each case's concrete implementation method and resolved CI feasibility;
     Pending defers test code completion and product wiring only.
     Explain why integration is needed, real product components and substituted
     external boundaries, and setup, runtime, resource, and cleanup budgets.
     Record compile and registration commands and results with the integration
     tag enabled, separate service-free unit and integration CI commands,
     environment, and evidence
     for minimal harness feasibility after simplifying or replacing uncertain
     verification. Distinguish executed feasibility checks from Pending cases;
     listing unproven cases and a blocker report does not complete selection.
     Report any necessary case still lacking a feasible method and its coverage
     impact as a blocker. Link separately approved and merged
     dependency, build, CI, tooling, or shared harness prerequisites; they
     cannot be added here. Identify delivery quality checks using actual Codex
     or external services, their expected results, and planned delivery evidence.
     Report approved acceptance-phase conflicts and the required upstream PR. -->

[Go test levels and cost](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/go-development.md#test-levels-and-cost),
[design verification deliverables](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#design-verification-deliverables)
and [implementation completion and delivery boundary](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#implementation-completion-and-delivery-boundary).

## Open questions and risks

<!-- Identify implementation blockers and deferred decisions with owners and
     resolution stages. State None if absent. Keep blocked PRs in draft. -->

## Changes since previous review

<!-- State Initial review or summarize changed decisions and downstream impact. -->

## Phase handoff

- [ ] The complete diff stays within docs/design/ and the narrow verification artifact exception, including governed specifications, deletions and both sides of renames.
- [ ] Concrete integration cases and suites have design-governed paths and the integration build tag directly in `cmd/<command>/`, with fixtures in that command's `testdata/`; the PR contains no production code or dependency, build, CI, tooling, or shared harness changes.
- [ ] Selected cases justify their cost and have concrete feasible implementation methods; Pending defers test code completion and product wiring, with retained coverage explained for removed candidates.
- [ ] Service-free CI feasibility and its runtime-cost budget have evidence; required prerequisites are separately approved and merged.
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

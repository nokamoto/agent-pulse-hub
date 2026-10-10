---
type: Playbook
title: AI-Driven Development
description: Development phases, responsibilities, and PR approval boundaries.
sources:
  - id: okf
    resource: https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md
    title: Open Knowledge Format v0.2
  - id: github-review
    resource: https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/reviewing-proposed-changes-in-a-pull-request
    title: Reviewing proposed changes in a pull request
---

# AI-Driven Development

## Purpose and scope

This document defines the principles, phases, deliverables, roles, and pull request (PR) approval boundaries for AI-Driven Development (AIDD). Requirements, design, implementation, and verification remain traceable through versioned deliverables and their PRs.

## Core principles

- **Humans provide direction and accountability; agents work autonomously.** Humans determine goals, priorities, and acceptable risks, and approve work at appropriate milestones. Agents handle investigation, deliverable creation, verification, and corrections without requesting approval for every action within the approved scope.
- **Start development from requirements.** Keep the rationale for design and implementation in the repository rather than relying solely on conversations as the specification. Make ambiguities and assumptions explicit, and do not expand scope based on agent guesses.
- **Support autonomous execution with skills and guardrails.** Skills describe reusable ways of working. Guardrails define execution scope, prohibited actions, and approval boundaries. Enforce mechanically verifiable constraints through automated checks.
- **Turn verification into automation.** Maintain reproducible verification as tests and CI to reduce repeated manual checks. For checks that cannot be automated, explain why and identify what humans must verify.
- **Minimize the cost of human approval.** Divide changes into reviewable units and present decisions, diffs, requirement mappings, and verification results together. Let humans focus on suitability and risk without repeating checks already covered by automation.

## Development flow

Use the following flow. Even for small changes, check the affected
requirements and design. When an upstream phase needs no update, explain
why in the PR for the first downstream phase with deliverables.

```text
Requirements definition → Requirements PR → Human review → Approval and merge
Design → Design PR → Human review → Approval and merge
Implementation and verification → Implementation PR → Human review → Approval and merge
```

Each phase produces a separate PR containing its deliverables. Agents prepare the PR, complete self-review and applicable checks, and request human review. Humans evaluate the phase's approval criteria and either merge the PR themselves as approval or record explicit approval before a permitted merge. Both are normal approval routes, as described in [Human approval routes](#human-approval-routes). Agents address review comments in that PR. The next phase starts only after approval and merge are verified. While awaiting approval or merge, agents may continue investigation or verification that does not depend on unapproved decisions.

If an upstream assumption needs to change, return to the earliest affected phase. Explain the reason and impact in a PR for that phase, obtain human approval and merge the revision, then restore consistency across downstream deliverables. Corrections that do not affect approved decisions do not require renewed upstream approval.

## Deliverables and roles by phase

### 1. Requirements definition

Define what to achieve, why it matters, and the conditions for determining completion.

| Item | Description |
| --- | --- |
| Deliverables | Requirements in `docs/requirements/`, including goals, use cases, scope and exclusions, functional and nonfunctional requirements, constraints, acceptance criteria, and open questions |
| PR | A requirements PR containing the requirement changes and decisions requiring human approval |
| Human role | Communicate user intent and priorities, and decide scope, acceptance criteria, and important constraints |
| Agent role | Organize information, identify gaps, contradictions, and assumptions, and draft requirements with verifiable acceptance criteria |
| Approval criteria | Goals and scope are clear, acceptance criteria can be evaluated, and open questions that affect design have been resolved |

For decisions that can be deferred, record their impact and the stage at which they must be resolved.

### 2. Design

Define how to fulfill the approved requirements and how to verify the result.

| Item | Description |
| --- | --- |
| Deliverables | Design documents in `docs/design/` and any separate specification documents governed by them, including related requirements, architecture and responsibilities, interfaces and data, normal and failure scenarios, extensibility and safety considerations, verification strategy, and rationale for important decisions |
| PR | A design PR linking to the merged requirements PR and presenting the design changes and significant tradeoffs |
| Human role | Evaluate significant tradeoffs, including maintainability, compatibility, and operational risks |
| Agent role | Investigate the existing architecture and constraints, compare relevant alternatives, draft the design, and check requirement coverage and verifiability |
| Approval criteria | The design provides a credible path to satisfying the requirements and a verification strategy; important technical decisions and risks are agreed upon; open questions that affect implementation have been resolved |

Match the level of design detail to the size and risk of the change. Delegate implementation details that do not require human judgment to agents within the approved design and constraints.

### Specification documents and approved design

A specification document defines a contract used to implement or integrate a
feature, such as a protocol or API. Creating a separate specification is design
work, including when it extracts a contract already described elsewhere.
Changes to its contract, placement, authority, or relationship to other design
documents are also design work.

The governing design in `docs/design/` must record:

- Whether the specification stays within the design or has a separate document,
  its path, and its purpose and readers.
- Which document is authoritative for each contract, and how other documents
  derive from or reference it. Avoid competing sources of truth.
- The contract decisions and the constraints on routine implementation choices.
  Delegating code details does not delegate creation of a specification document.

A design PR that creates or changes a separate specification includes that
specification itself and the governing design
changes needed to establish that relationship. A plan to create the document
later does not satisfy this requirement. Separate specifications may live under
`docs/` outside `docs/design/`, for example in `docs/protocol/`. Design PRs may
change `docs/design/` and these specification documents when their relationship
is recorded in the governing design; code, tooling, and unrelated documents
remain outside the design PR. Apply this boundary to deletions and both sides
of renames, using the base revision for removed paths.

Each separate specification uses `type: Design`, links to its governing design,
identifies the authoritative source for its contracts, and records derivation
in OKF `sources`. The type classifies content; it does not prove approval or
grant independent authority. Review the specification's content and its
relationship to the governing design together before approval and merge.
If no separate specification is needed, state that the design contains the
contract. A requirement to document a protocol does not by itself settle its
placement or authority.

If implementation discovers the need for a separate specification or a change
to those design decisions, stop the current implementation session. Preserve
existing work and report the reason, affected contracts and documents, unfinished
implementation, and that a separate design session is required. After that
report, end work in the session. Do not revise the design or specification in
that session or automatically start another session. A human initiates the
separate design session, which prepares the design PR with the specification
and affected design changes. If requirement scope or constraints must change,
report that a requirements session is needed before design instead.

Implementation may resume only after human approval and merge of the upstream
PRs are verified under the [handoff rules](#agent-verification-and-handoff-evidence).
This feedback suspends work; it does not require discarding code or reverting
commits. Existing documents do not need wholesale reorganization when this
rule is adopted; apply it when specification work is needed.

Routine code choices, wording corrections, and explanatory examples that
preserve the approved contract, placement, authority, and document relationships
may remain in implementation. They cannot be used to create a new separate
specification in an implementation PR. For such documentation updates, link
the governing design and explain in the implementation PR why no design decision
changes. Reviewers assess that explanation and the content; structural
guardrails alone cannot establish approval or semantic consistency.

### Developer documentation and upstream feedback

Create operating instructions and specifications that developers will continue
referencing after implementation as requirements or design deliverables before
implementation begins. Include the actual documentation in the upstream phase
PR; a requirement or plan to write it later is insufficient. Use the existing
phase scope and document placement rules. For example, include the actual
operating instructions in the relevant design document under `docs/design/`
to keep them within the design PR's scope. Adding new operating instructions
or contracts to an existing file also belongs upstream; an existing README or
other document does not make that new content implementation work. Separate
specifications follow the
[specification rules above](#specification-documents-and-approved-design).

If implementation discovers that such documentation is missing, stop the
current implementation session, including when the approved requirements or
design already require the document. Preserve existing work and report the gap,
affected documents and contracts, unfinished implementation and acceptance
criteria, and the upstream phase needed to resolve the gap. Use requirements
when scope or constraints need to change; otherwise use design. End work in the
session without creating or changing the affected documentation or revising
requirements or design, and
do not automatically start another session. A human initiates the separate
upstream session. Resume implementation only after human approval and merge of
the required upstream PRs are verified under the
[handoff rules](#agent-verification-and-handoff-evidence).

Verification results and instructions for reviewers to reproduce checks may
remain in the implementation PR. They do not replace documentation needed for
continued developer use. Routine wording corrections and explanatory examples
in existing documentation may remain in implementation when they preserve the
approved procedures and contracts, placement, authority, and document relationships under the
specification rules. This allowance does not permit creating a new document.

### 3. Implementation

Translate the approved requirements and design into working code and reproducible verification.

| Item | Description |
| --- | --- |
| Deliverables | A repository revision containing code, necessary tests and CI changes, and permitted updates to existing documentation under the developer documentation rule; PR evidence mapping those changes to acceptance criteria and recording verification results |
| PR | An implementation PR linking to the merged requirements and design PRs and presenting the implementation changes and verification evidence |
| Human role | Confirm acceptance criteria, remaining risks, and operational impact, and decide whether to merge |
| Agent role | Implement, self-review, verify, and fix defects; automate necessary verification and present reviewable diffs and evidence |
| Approval criteria | Applicable acceptance criteria are met, required automated checks pass, necessary manual checks are complete, and known limitations and remaining risks are explicit |

Agents investigate and correct verification failures. Checks that could not be run must not be reported as passing; report the reason, impact, and actions needed to resolve the issue. Do not present work as ready to merge while required verification remains incomplete.

#### Implementation completion and delivery boundary

Implementation produces a reviewable repository revision and its verification
evidence. The agent is responsible for making that revision satisfy the approved
requirements and design, correcting defects, and providing enough instructions
and evidence for reviewers to reproduce the applicable checks. Use the
[implementation PR template](../../.github/PULL_REQUEST_TEMPLATE/implementation.md)
to identify the delivered changes, their requirement and design mappings,
verification results, and any work left for delivery.

An implementation PR is ready for human review only when the implementation
approval criteria above are met. The phase ends when human approval and merge
of that revision are verified under the
[approval routes](#human-approval-routes). A local build, passing CI, or opening
a PR alone does not complete the phase. Its completed result is the merged
repository revision together with the PR's verification and approval evidence.

Here, **developer delivery** means subsequent work that makes the completed
implementation available to developers for acquisition, setup, and continued
use. Publishing or distributing release artifacts, arranging installation in
a recipient's environment, and providing onboarding or operational handover
beyond the approved implementation acceptance criteria belong to delivery.
Implementation completion does not claim that these activities are complete
or authorize the agent to perform them. Delivery's detailed process,
deliverables, and approval criteria require separate definition; this rule
establishes only the implementation boundary.

Classify work by its purpose and the approved acceptance criteria, not by its
file extension or location. Build and startup checks and their reproduction
instructions in the PR remain implementation verification. Documentation for
continued developer use follows the
[upstream documentation rule](#developer-documentation-and-upstream-feedback).
For example, the [MVP requirements](../requirements/mvp.md) NFR-004 and AC-009
require Windows setup and operation instructions and a clean-build
demonstration. Those obligations cannot be deferred to delivery. If the
instructions are missing during implementation, stop and return upstream to
create them; do not claim the acceptance criterion is met. Building an artifact
for the demonstration is verification; publishing it for downstream developers
is delivery.

In the implementation PR, record identified delivery work separately from
implementation defects or incomplete checks, or state that none has been
identified. Do not present that list as a complete delivery plan. An unmet
implementation acceptance criterion remains a blocker; labeling it delivery
does not defer it. If the approved requirements or design leave the boundary
unclear, report the unresolved decision and use the existing upstream feedback
and approval rules before claiming implementation completion. This boundary
does not define the final end-user offering or change approved product scope.

## Approval and traceability

Write documentation under `docs/` as an Open Knowledge Format (OKF) v0.2 bundle rooted at `docs/`. Each concept document uses UTF-8 Markdown with YAML frontmatter containing `type`. Its concept ID is its path relative to `docs/` without `.md`; this document's ID is `aidd/README`. Use standard relative Markdown links between documents, with surrounding text explaining the relationship.[^okf]

Use `Requirement`, `Design`, and `Playbook` as project-defined concept types for the corresponding documents. Keep concept paths stable and update incoming links when moving files.

Human approval must identify the phase PR and approved revision through one of the routes below. PR creation, silence, successful automated checks, and an agent's readiness verdict do not substitute for human approval. Reflect decisions agreed upon in conversation in the relevant deliverables and PR.

Link design documents to the requirement concepts they fulfill, and link implementation and verification evidence from the PR to the corresponding requirement and design documents. Use OKF `sources` entries to record documents from which a concept derives.[^okf] If an approved deliverable changes, do not assume the existing approval covers the new version; obtain renewed approval as appropriate to the impact on decisions.

Design documents should link to source requirement concepts and use their
requirement-local IDs (for example, `FR-001`) in coverage mappings where
needed. Keep the source requirements PR URL and approval/revision evidence
in the design PR description, including the source requirements PR's final head SHA and merge
commit SHA. Do not require each design document to repeat that evidence.

[^okf]: Open Knowledge Format v0.2, sections 2–6.

When requesting approval, briefly present the following in the PR description:

- The purpose, scope, and major changes since the previous review.
- Links to the deliverables to review and their relationship to requirements and design.
- Automated check results and links to evidence, manual check results, and checks not performed.
- Decisions requiring human judgment, recommendations and rationale, and remaining risks.

Reference the authoritative deliverable instead of duplicating content across documents. Provide a summary of results with access to details as needed, rather than requiring reviewers to read large volumes of logs.

### Human approval routes

The approver is the human responsible for the phase's scope, decisions, and risks. An account's permission to merge is not permission for an agent to make those decisions. Use either normal route; neither requires an exception request.

| Route | What the human does | Approval evidence |
| --- | --- | --- |
| Human merge | Review the final diff, decisions, and verification against the phase criteria, then merge the PR personally. The merge approves that final revision. | The merged PR's final head SHA, human merge actor, merge time, and merge commit SHA |
| Explicit approval, then merge | Approve the current revision with a GitHub Approve review, or a PR comment such as `I approve the requirements in this PR at head <full SHA>.` Then merge personally or explicitly authorize an agent to merge that revision. | Review/comment URL, human approver, approved head SHA, and subsequent merge metadata |

GitHub does not allow authors to approve their own PRs with an Approve review.[^github-review] When the human is also the PR author, they can use Human merge directly or write the explicit approval comment. A separate reviewer, comment, or Approve review is not required for Human merge. Repository branch protections and required checks still apply to both routes; this policy does not authorize bypassing them.

For the usual Human merge route:

1. The agent prepares a reviewable PR with the materials listed above and states that human merge is a normal approval route.
2. The human reviews the final revision and either requests changes or merges it when satisfied. If the revision changes during review, review the updated diff before merging.
3. The next phase's agent verifies the merge and records the evidence below in its PR's source or handoff section. The human does not need to add a retrospective approval comment.

For Explicit approval, then merge, approval and permission to merge are separate. An agent may merge only with explicit human authorization for that PR and revision, after checking approval, current head, and repository requirements. A conversational approval can be used when it names the PR and revision; preserve the human's statement and an accessible source reference in the PR before handoff. If the statement is ambiguous, ask only for the missing PR, revision, or decision. Do not ask for a second approval merely because the human used a supported route.

Auto-merge, a merge queue, a bot merge, or a known agent-executed merge uses Explicit approval, then merge. An agent using a human's GitHub credentials does not turn its own merge into Human merge. Enabling automation or observing an automated merge alone does not prove approval of the final revision. Verify the explicit approval and its revision coverage before handoff.

[^github-review]: GitHub Docs, Reviewing proposed changes in a pull request, on authors reviewing their own PRs.

### Agent verification and handoff evidence

Before dependent design or implementation, check the source PR on GitHub and record a compact evidence summary in the downstream PR. Link the PR and any explicit approval record instead of copying it into every deliverable.

- **Scope and revision:** source phase, PR URL, final head SHA, merge commit SHA, and source concept paths. Confirm the documents used correspond to the merged revision, including any later changes that need their own approval. Verify PR evidence even if a document's status text still says approval is pending; that text alone does not establish or invalidate approval.
- **Merge:** merged state, merge time, and merge actor. A closed but unmerged PR does not pass.
- **Approval:** the normal route and human actor. For Human merge, use GitHub's merge metadata together with available execution context. A human merge with no Approve review passes; a known automated or agent-executed merge needs explicit human approval. If the actor or execution context leaves the route uncertain, report the specific uncertainty rather than assuming either approval or an exception.
- **Explicit approval coverage, when applicable:** review/comment or preserved statement reference, approver, and approved head SHA. Approval must cover the merged final head; a dismissed or superseded approval, or approval of an earlier head, does not cover later changes. Obtain renewed approval using either normal route before dependent work.

Use the PR's final head SHA to identify its reviewed diff and the merge commit SHA to locate the integrated documents. They can differ after squash or rebase merges. Do not require their equality.

For example, a requirements PR authored and personally merged by the responsible human, with no Approve review, passes via Human merge once its merged revision and evidence are verified. Record `Human merge; <PR URL>; head <SHA>; merged by <login> at <time>; merge <SHA>`. This also applies to already merged PRs; missing a separate review does not make them exceptions.

If required metadata is inaccessible or approval coverage is uncertain, report the prerequisite as UNVERIFIED and continue only independent work. Ask for the missing evidence, not a blanket exception. Check phase approval criteria and required verification separately: a valid approval record does not make incomplete checks pass. Local document guardrails validate structure; they do not fetch or establish GitHub approval.

## Responsibilities of skills, guardrails, and CI

| Mechanism | Responsibility |
| --- | --- |
| This document | Define development principles, phases, deliverables, roles, and approval boundaries |
| Skills | Define detailed procedures for each phase and methods for creating and checking deliverables |
| Guardrails | Express execution scope and approval boundaries through repository instructions, tool permissions, and automated checks |
| CI | Continuously run the reproducible verification needed for changes, such as builds, tests, and static analysis, and make results available for review |

Automation supports human judgment about the suitability of requirements and design. Add necessary verification alongside implementation, and treat recurring checks as opportunities to improve skills, guardrails, and CI.

## Rule-change consistency review

For a PR that changes a development rule or how another repository artifact
uses it, the PR author completes the following checks as part of self-review
and records evidence for human review. Review meaning and responsibilities;
matching wording is not required.

- Identify and link the authoritative document for each changed rule, including
  any rationale and exceptions that affect its application.
- Identify affected uses in skills, assets, review checklists, and PR templates.
  Check them against the authoritative rule for contradictions, independent
  redefinitions, and missed updates. Explain why any inspected use needs no change.
- Check that each use serves its role: skills describe procedures; assets provide
  document structure and input hints; review checklists express pass/fail criteria;
  PR templates collect evidence. Repetition needed to perform those tasks is
  acceptable when its meaning remains consistent with the authoritative rule.
- Record the authoritative source, inspected use paths, findings, corrections,
  and any unresolved uncertainty in the PR description. If no rule or use changes,
  record this review as not applicable with a reason.

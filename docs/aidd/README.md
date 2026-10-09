---
type: Playbook
title: AI-Driven Development
description: Development phases, responsibilities, and PR approval boundaries.
sources:
  - id: okf
    resource: https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md
    title: Open Knowledge Format v0.2
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
Requirements definition → Requirements PR → Human review and approval → Merge
Design → Design PR → Human review and approval → Merge
Implementation and verification → Implementation PR → Human review and approval → Merge
```

Each phase produces a separate PR containing its deliverables. Agents prepare the PR, complete self-review and applicable checks, and request human review. Humans evaluate the phase's approval criteria and approve the PR for merge. Agents address review comments in that PR. The next phase starts from the merged deliverables of the preceding phase. While awaiting approval or merge, agents may continue investigation or verification that does not depend on unapproved decisions.

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
| Deliverables | Design documents in `docs/design/`, including related requirements, architecture and responsibilities, interfaces and data, normal and failure scenarios, extensibility and safety considerations, verification strategy, and rationale for important decisions |
| PR | A design PR linking to the merged requirements PR and presenting the design changes and significant tradeoffs |
| Human role | Evaluate significant tradeoffs, including maintainability, compatibility, and operational risks |
| Agent role | Investigate the existing architecture and constraints, compare relevant alternatives, draft the design, and check requirement coverage and verifiability |
| Approval criteria | The design provides a credible path to satisfying the requirements and a verification strategy; important technical decisions and risks are agreed upon; open questions that affect implementation have been resolved |

Match the level of design detail to the size and risk of the change. Delegate implementation details that do not require human judgment to agents within the approved design and constraints.

### 3. Implementation

Translate the approved requirements and design into working code and reproducible verification.

| Item | Description |
| --- | --- |
| Deliverables | Code, necessary tests and CI changes, related documentation updates, and review materials mapping the work to requirements and design and presenting verification results |
| PR | An implementation PR linking to the merged requirements and design PRs and presenting the implementation changes and verification evidence |
| Human role | Confirm acceptance criteria, remaining risks, and operational impact, and decide whether to merge |
| Agent role | Implement, self-review, verify, and fix defects; automate necessary verification and present reviewable diffs and evidence |
| Approval criteria | Applicable acceptance criteria are met, required automated checks pass, necessary manual checks are complete, and known limitations and remaining risks are explicit |

Agents investigate and correct verification failures. Checks that could not be run must not be reported as passing; report the reason, impact, and actions needed to resolve the issue. Do not present work as ready to merge while required verification remains incomplete.

## Approval and traceability

Write documentation under `docs/` as an Open Knowledge Format (OKF) v0.2 bundle rooted at `docs/`. Each concept document uses UTF-8 Markdown with YAML frontmatter containing `type`. Its concept ID is its path relative to `docs/` without `.md`; this document's ID is `aidd/README`. Use standard relative Markdown links between documents, with surrounding text explaining the relationship.[^okf]

Use `Requirement`, `Design`, and `Playbook` as project-defined concept types for the corresponding documents. Keep concept paths stable and update incoming links when moving files.

Record human approval as a review on each phase's PR, identifying the version or diff being approved. PR creation, silence, and successful automated checks do not substitute for human approval. Reflect decisions agreed upon in conversation in the relevant deliverables and PR, and obtain approval through the PR review.

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

## Responsibilities of skills, guardrails, and CI

| Mechanism | Responsibility |
| --- | --- |
| This document | Define development principles, phases, deliverables, roles, and approval boundaries |
| Skills | Define detailed procedures for each phase and methods for creating and checking deliverables |
| Guardrails | Express execution scope and approval boundaries through repository instructions, tool permissions, and automated checks |
| CI | Continuously run the reproducible verification needed for changes, such as builds, tests, and static analysis, and make results available for review |

Automation supports human judgment about the suitability of requirements and design. Add necessary verification alongside implementation, and treat recurring checks as opportunities to improve skills, guardrails, and CI.

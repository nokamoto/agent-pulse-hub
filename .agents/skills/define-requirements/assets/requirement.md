---
type: Requirement
title: "<Requirement title>"
description: "<Intended outcome in one sentence>"
---

# <Requirement title>

## Problem and goals

<Describe the problem, affected users, why it matters, and observable success.>

## Sources and decisions

<Link authoritative inputs and related concepts using relative Markdown links.
Add OKF sources entries to frontmatter for source documents when applicable.
Record user decisions and their rationale; distinguish proposals from decisions.>

## Scope

- In scope: <Included outcomes.>
- Out of scope: <Explicit exclusions.>

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | <Actor and trigger> | <Outcome> | <Failure or boundary behavior> |

## Functional requirements

| ID | Requirement | Rationale / use case | Priority |
| --- | --- | --- | --- |
| FR-001 | <Observable required behavior> | <UC-001 and reason> | <Agreed priority or unresolved> |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | <Measurable target under stated conditions> | <Reason> | <Measurement or inspection> |

<Consider relevant reliability, security, privacy, performance, compatibility,
and operational needs. Explain non-applicability instead of inventing targets.>

## Constraints and dependencies

<List mandatory constraints with their sources, external dependencies, and
assumptions. Distinguish required constraints from design options.>

## Acceptance criteria

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001 | <Given / when> | <Then, with a decisive pass/fail condition> | <Test, measurement, or human inspection> |

<Cover every functional and nonfunctional requirement, including relevant
failures. For manual evaluation, specify evidence and who evaluates it.>

## Open questions and assumptions

| ID | Question or assumption | Impact if unresolved or false | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | <Unknown> | <Effect on scope or design> | <Owner or unassigned> | <Stage> | <Open / resolved, with decision> |

<Write None if no questions remain. Identify design-blocking questions explicitly.>

## Change impact

<For new requirements, state that they are new and identify related concepts.
For revisions, list changed or retired IDs, affected design and implementation,
compatibility impacts, and decisions needing renewed human approval.>

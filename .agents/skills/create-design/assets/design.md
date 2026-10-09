---
type: Design
title: "<Design title>"
description: "<How this design fulfills the requirements>"
sources:
  - id: requirements
    resource: requirements/<stable-topic>.md
    title: "<Source requirement title>"
---

# <Design title>

## Context and scope

<Explain the outcome, scope, exclusions, and existing system context. Link
source requirement concepts with relative Markdown links and include local
IDs where needed to explain scope and coverage. Keep source PR and
approval or revision provenance in the design PR description; do not
repeat source PR links or approval, head, or merge SHA values in this
design document solely to record provenance.>

## Requirement coverage

| Requirement concept and local ID | Acceptance criterion | Design section or decision | Verification |
| --- | --- | --- | --- |
| <requirements/topic + FR-001 or NFR-001> | <AC-001> | <Section link> | <Verification strategy entry> |

<Cover every in-scope functional and nonfunctional requirement and acceptance
criterion. Explain partial coverage and link other responsible designs.>

## Architecture and responsibilities

<Describe components, boundaries, dependencies, and responsibilities. Add a
diagram when useful. Distinguish existing and new behavior.>

## Interfaces and data

<Specify relevant contracts, inputs, outputs, validation, errors, ownership,
and data lifecycle. Address persistence, retention, and compatibility where
applicable; explain non-applicability.>

## Normal and failure scenarios

<Describe triggers, interactions, outcomes, and relevant failures or boundaries.
Address recovery, concurrency, retries, and idempotency when needed.>

## Decisions and alternatives

| Decision | Relevant alternatives | Rationale and tradeoffs |
| --- | --- | --- |
| <Proposed decision> | <Viable alternatives> | <Requirement evidence, costs, and risks> |

## Quality and operations

<Address relevant security, privacy, performance, reliability, observability,
extensibility, and operational risks. Explain how approved constraints are met
without inventing targets. Explain non-applicable concerns.>

## Verification strategy

| Requirement / acceptance IDs | Method and level | Conditions and expected result | Evidence and evaluator |
| --- | --- | --- | --- |
| <Concept path + IDs> | <Test, measurement, or inspection> | <Decisive pass/fail conditions> | <Planned evidence; evaluator for manual checks> |

<Include relevant failures and nonfunctional evaluation. Distinguish planned
verification from checks actually executed.>

## Change and rollout impact

<For a new design, identify affected components and related designs. For
revisions, identify changed decisions and downstream work. Explain applicable
migration, compatibility, rollout, and rollback needs or why none apply.>

## Open questions and risks

| Question or risk | Impact | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- |
| <Unknown or risk> | <Effect if unresolved> | <Owner or unassigned> | <Stage> | <Open or resolved with decision> |

<Write None if none remain. Identify implementation blockers and decisions
requiring human judgment explicitly.>
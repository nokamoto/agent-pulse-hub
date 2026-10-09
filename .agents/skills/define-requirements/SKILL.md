---
name: define-requirements
description: Draft or revise agent-pulse-hub requirements with verifiable acceptance criteria and prepare a requirements pull request. Use for requirements definition before design or implementation.
---

# Define requirements

Inspect relevant existing requirements and downstream design before editing.

1. Establish the problem, users, intended outcome, and scope from the request and
   available evidence. Ask about missing decisions that materially affect scope
   or acceptance, while continuing independent drafting. Do not invent consent,
   priorities, performance targets, or product behavior.
2. Create `docs/requirements/<stable-topic>.md` from
   [the asset](assets/requirement.md), or revise the existing concept. Replace
   template prompts with evidence-backed content. Keep stable concept paths and
   local requirement IDs; identify requirements by concept path plus local ID.
   When a requirement derives from a source document, record that document in
   the YAML frontmatter's `sources` list, using Open Knowledge Format (OKF) as
   specified by the [AIDD playbook](../../../docs/aidd/README.md#approval-and-traceability).
   Capture user-provided rationale in the document when no linkable source exists.
   Describe intended behavior and lasting constraints. Keep authoring-time
   implementation progress and temporary repository state out of requirements;
   put work status in the PR or issue. When existing behavior is needed to
   define a constraint or transition, identify the affected version or
   migration condition.
3. Cover normal and relevant failure scenarios, functional and nonfunctional
   requirements, constraints, and observable acceptance criteria. Separate
   confirmed decisions, assumptions, and open questions. Record each deferred
   question's impact, decision owner, and deadline stage. Questions affecting
   design prevent readiness; do not turn guesses into approved decisions.
4. For revisions, identify changed or retired IDs and affected downstream
   documents. Describe migration or compatibility implications when relevant.
   Keep design and implementation changes in their own phase PRs.
5. Apply the [Markdown readability review skill](../review-markdown-readability/SKILL.md)
   to the requirements document. Then delegate requirements self-review to a
   new subagent with a clean context (no inherited
   conversation; use `fork_turns="none"` when available). Have it apply the
   [review-requirements skill](../review-requirements/SKILL.md) to the complete
   diff. Supply the repository location, review target and base revision, and
   original requirement inputs needed to evaluate intent; omit the author's
   reasoning and expected verdict. Leave review criteria and reporting to that
   skill. Address findings and request re-review of the updated diff. After
   Markdown corrections, repeat the readability review and re-evaluate affected
   requirements checks. If a
   clean-context subagent is unavailable, report self-review as incomplete.
6. Prepare the body using
   [the PR template](../../../.github/PULL_REQUEST_TEMPLATE/requirements.md).
   Include the checklist results and decisions requiring human review. Explain
   the playbook's [human approval routes](../../../docs/aidd/README.md#human-approval-routes):
   the responsible human may approve by personally merging the final revision,
   including their own PR, or explicitly approve that revision before merge.
   Do not require a separate Approve review for Human merge. If PR
   creation is requested, use the `phase:requirements` label and verify it was
   applied. If remote access is unavailable, provide the prepared body and
   explicitly report the missing remote step. Do not claim a PR or label exists
   without checking. Follow existing authorization for remote operations.

Requirements PRs change only `docs/requirements/`. If supporting workflow or
tooling changes are needed, prepare them separately. Do not erase unrelated
working-tree changes. Keep unresolved requirements as drafts. Design begins
after human approval and merge, as defined in the
[AIDD playbook](../../../docs/aidd/README.md#development-flow).

---
name: define-requirements
description: Draft or revise agent-pulse-hub requirements with verifiable acceptance criteria and prepare a requirements pull request. Use for requirements definition before design or implementation.
---

# Define requirements

Read repository `AGENTS.md`, `docs/aidd/README.md`, and
[the requirements workflow](../../../docs/aidd/requirements-workflow.md).
Inspect relevant existing requirements and downstream design before editing.
Write repository artifacts in English; use the user's language in conversation.

1. Establish the problem, users, intended outcome, and scope from the request and
   available evidence. Ask about missing decisions that materially affect scope
   or acceptance, while continuing independent drafting. Do not invent consent,
   priorities, performance targets, or product behavior.
2. Create `docs/requirements/<stable-topic>.md` from
   [the asset](assets/requirement.md), or revise the existing concept. Replace
   template prompts with evidence-backed content. Keep stable concept paths and
   local requirement IDs; identify requirements by concept path plus local ID.
   Record actual source documents in OKF `sources` when applicable, and capture
   user-provided rationale in the document when no linkable source exists.
3. Cover normal and relevant failure scenarios, functional and nonfunctional
   requirements, constraints, and observable acceptance criteria. Separate
   confirmed decisions, assumptions, and open questions. Record each deferred
   question's impact, decision owner, and deadline stage. Questions affecting
   design prevent readiness; do not turn guesses into approved decisions.
4. For revisions, identify changed or retired IDs and affected downstream
   documents. Describe migration or compatibility implications when relevant.
   Keep design and implementation changes in their own phase PRs.
5. Apply [the review checklist](../review-requirements/references/checklist.md)
   to the complete diff. Correct findings within the agreed scope and report
   unresolved items with evidence. Passing a self-review is not human approval.
6. Prepare the body using
   [the PR template](../../../.github/PULL_REQUEST_TEMPLATE/requirements.md).
   Include the checklist results and decisions requiring human review. If PR
   creation is requested, use the `phase:requirements` label and verify it was
   applied. If remote access is unavailable, provide the prepared body and
   explicitly report the missing remote step. Do not claim a PR or label exists
   without checking. Follow existing authorization for remote operations.

Requirements PRs change only `docs/requirements/`. If supporting workflow or
tooling changes are needed, prepare them separately. Do not erase unrelated
working-tree changes. Keep unresolved requirements as drafts. Design begins
after human approval and merge, as defined in the AIDD playbook.

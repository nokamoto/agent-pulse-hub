---
name: edit-repository-rules
description: Create or revise agent-pulse-hub development rules and their uses in repository instructions, skills, templates, guardrails, or CI, and prepare a dedicated repository rules pull request.
---

# Edit repository rules

Use this skill for changes to `AGENTS.md`, `docs/aidd/`, `.agents/skills/`,
PR templates, or development rules enforced by guardrails and CI. Product
requirements and design deliverables use their respective phase skills.

Follow the [AIDD playbook](../../../docs/aidd/README.md). Keep principles and
approval boundaries in their authoritative source; this skill describes how
to prepare a rule change for review.

## Establish scope and affected uses

Identify the requested behavior, the changed rule's authoritative source, and
the intended users of each affected artifact. Read applicable repository
instructions and the playbook's
[rule-change consistency criteria](../../../docs/aidd/README.md#rule-change-consistency-review).
Search for references and other uses of the changed rule, including wording
that expresses the same meaning differently. Inspect affected procedures,
assets, review checklists, PR templates, and automated enforcement.

Check affected requirements and design. Explain in the PR when they need no
update. If approved product decisions must change, use separate PRs for the
applicable upstream phases and verify approval and merge before dependent work,
as required by the [development flow](../../../docs/aidd/README.md#development-flow).
Keep unrelated working changes intact.

## Edit and verify

Update the authoritative rule and the uses that need changes. Apply the
playbook's consistency criteria to meaning and each artifact's responsibility;
link to the source when a use needs context. Record inspected paths that need
no change and explain why. Identify unresolved decisions rather than silently
choosing new policy. When authoring a skill, apply `skill-creator` if available.

Run the repository checks required by [AGENTS.md](../../../AGENTS.md), including
formatting and guardrails. Run Go tests and lint when Go source, modules, or
executable checks change. Report actual results and any unavailable required
checks; incomplete required verification prevents readiness.

Apply [Markdown readability review](../review-markdown-readability/SKILL.md)
to every written or edited Markdown document. Then use
[review-repository-rules](../review-repository-rules/SKILL.md) for independent
consistency self-review of the complete diff, following its clean-context
reviewer setup. Address findings and re-review the updated diff. Repeat
readability review after Markdown edits and re-run affected checks. Report
unavailable independent review as incomplete.

## Prepare the repository rules PR

Use [the dedicated PR template](../../../.github/PULL_REQUEST_TEMPLATE/repository-rules.md).
Fill it with the purpose and scope, authoritative sources, inspected uses,
findings and corrections, verification and review evidence, and remaining
decisions or risks. State whether requirements or design need updates and why.
Keep product phase deliverables in their separate phase PRs. Keep a PR with
unresolved blocking decisions or incomplete required verification in draft.

When PR creation or update is authorized, publish the prepared revision and
verify the remote PR's branch, head, and body. If remote access is unavailable,
provide the prepared body and identify the incomplete remote step. Request
human review under the playbook's
[approval routes](../../../docs/aidd/README.md#human-approval-routes).
PR preparation does not authorize an agent to merge.

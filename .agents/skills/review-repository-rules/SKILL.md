---
name: review-repository-rules
description: Review changes to agent-pulse-hub development rules and their uses for semantic consistency, responsibility boundaries, impact coverage, and verification evidence, and report readiness for human review.
---

# Review repository rules

Use this skill for a PR or local diff that changes repository development
rules or their uses in `docs/aidd/`, `AGENTS.md`, skills, assets, review
checklists, PR templates, guardrails, or CI. Product requirements and design
deliverables use their respective review skills.

Use the [AIDD playbook's rule-change consistency criteria](../../../docs/aidd/README.md#rule-change-consistency-review)
as the authoritative review criteria, and its
[responsibility definitions](../../../docs/aidd/README.md#responsibilities-of-skills-guardrails-and-ci)
and [approval routes](../../../docs/aidd/README.md#human-approval-routes)
for their respective boundaries. This skill describes the review procedure;
it does not define a separate rule policy.

## Prepare the review

Identify the original request, repository, target PR and base/head commits, or
local diff and base. Read the complete changed-file list, including deletions
and both sides of renames, and the full affected artifacts. Determine which
rules or uses changed and locate each authoritative source. Read applicable
repository instructions and relevant existing rules before evaluating the diff.

For the author's independent self-review, delegate this skill to a fresh
clean-context subagent with `fork_turns="none"`, model `gpt-6.1-sol`, and
reasoning effort `high`. Supply the original request, repository, target/base
revision or exact local snapshot, and authoritative sources. Omit author
reasoning, prior findings, and expected verdicts. The delegated reviewer
performs the review directly without further delegation. If this independent
review is unavailable, report it as incomplete.

## Examine consistency and evidence

Apply every playbook consistency criterion to the changed rules and their
uses. Search both explicit references and wording that expresses the same
meaning differently. Inspect relevant procedures, assets, checklists,
templates, guardrails, and CI, including unchanged uses that may need updates.
Evaluate meaning and each artifact's responsibility rather than requiring
identical wording. Cite inspected paths, findings, and reasons why affected
uses need no change.

Check the impact on requirements and design against the playbook's
[development flow](../../../docs/aidd/README.md#development-flow).
Evaluate the PR's explanation when no upstream update is needed. When changed
product decisions require separate upstream PRs, verify the prerequisite
approval and merge evidence before dependent work. Do not infer new policy
from ambiguous wording; identify the missing decision and its impact.

Inspect verification results against [AGENTS.md](../../../AGENTS.md) and the
changed artifacts. Distinguish executed checks from planned checks and confirm
that results cover the reviewed revision. Do not treat checked boxes or
successful guardrails as evidence of semantic consistency. If evidence or
remote artifacts are unavailable, review what is accessible and mark the
missing evidence UNVERIFIED.

[Markdown readability review](../review-markdown-readability/SKILL.md) remains
a separate required review for written or edited Markdown. Check its reported
scope and final version; consistency review does not substitute for its
restricted first-read review or meaning comparison.

## Report the result

Report findings first, with exact file/section/line or PR metadata references,
impact, and the smallest useful correction. Then report PASS, FAIL,
UNVERIFIED, or N/A with evidence and rationale for each applicable playbook
consistency criterion, upstream impact, and required verification and reviews.
N/A needs an applicability explanation and cannot excuse missing evidence.

Provide one overall verdict:

- NEEDS CHANGES if any applicable check fails or a blocking decision is unresolved.
- INCOMPLETE if none fail but required evidence or review is unverified.
- READY FOR HUMAN REVIEW only if all applicable checks pass.

State the reviewed revision or exact local snapshot, scope, authoritative
sources, inspected uses, corrections, unresolved decisions, and limitations.
Re-evaluate affected checks after changes; do not carry a verdict to a new
diff without examining it. Independent re-review uses a fresh reviewer with
the same inputs and protocol, without earlier findings or author explanations.

Review without editing unless fixes are requested. An agent's readiness
verdict does not grant human approval, merge permission, or permission to
begin a dependent phase. Record evidence in
[the repository rules PR template](../../../.github/PULL_REQUEST_TEMPLATE/repository-rules.md)
when preparing an authorized PR update.

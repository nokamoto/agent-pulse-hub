---
name: review-requirements
description: Evaluate requirements documents or requirements pull requests against the repository checklist and report evidence-based readiness for human review.
---

# Review requirements

Use [the checklist](references/checklist.md) as the review criteria.
Use the [AIDD playbook](../../../docs/aidd/README.md#human-approval-routes)
for human approval routes. Human merge is a normal route and does not require
a separate Approve review, including for the human's own PR.

Identify the review target: PR URL and base/head commits, or local diff and its
base. Read the complete changed-file list, including deletions and both sides
of renames, then affected requirements and relevant existing requirements and
design. Do not rely on the author's checked boxes as evidence. If remote access
is unavailable, review local artifacts and mark missing evidence UNVERIFIED.

For every checklist ID, report PASS, FAIL, UNVERIFIED, or N/A with a concrete
file/section/line or PR metadata reference and rationale. N/A needs an
applicability explanation; it cannot excuse missing evidence. Identify
contradictions, missing acceptance coverage, unsupported assumptions, and
unresolved decisions that affect design. A draft may contain unknowns, but is
not ready until blocking decisions are resolved.

Report findings first, with impact and the smallest useful correction. Then
provide the full checklist table and an overall verdict:

- NEEDS CHANGES if any applicable item fails.
- INCOMPLETE if none fail but any applicable item is unverified.
- READY FOR HUMAN REVIEW only if all applicable items pass.

Before the main session decides how to act on findings, apply
[Validate review findings](../validate-review-findings/SKILL.md). Return the
original review and independent assessment to the main session for its final
disposition. Validation does not waive checklist failures or human approval.
State the reviewed revision and scope, deferred decisions, and limitations.
Re-evaluate affected checks after updates; do not reuse an earlier verdict for
a new diff without examining changes. Review without editing unless fixes are
requested. Do not issue human approval, merge, or authorize the next phase
on the strength of this verdict.

---
name: review-requirements
description: Evaluate requirements documents or requirements pull requests against the repository checklist and report evidence-based readiness for human review.
---

# Review requirements

Read `AGENTS.md`, `docs/aidd/README.md`, and
[the requirements workflow](../../../docs/aidd/requirements-workflow.md).
Use [the checklist](references/checklist.md) as the review criteria.
Write repository artifacts and PR review bodies in English; converse in the
user's language.

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

State the reviewed revision and scope, deferred decisions, and limitations.
Re-evaluate affected checks after updates; do not reuse an earlier verdict for
a new diff without examining changes. Review without editing unless fixes are
requested. Return the review in chat unless posting to GitHub is explicitly
authorized. Do not issue human approval, merge, or authorize the next phase
on the strength of this verdict.

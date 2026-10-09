---
name: review-design
description: Evaluate design documents or design pull requests against requirement coverage, technical decisions, verification, and phase boundaries, and report readiness for human review.
---

# Review design

Use [the checklist](references/checklist.md) as review criteria and the
[AIDD playbook](../../../docs/aidd/README.md) for approval boundaries.

Identify the target PR and base/head commits, or local diff and base. Read the
complete changed-file list, including deletions and both sides of renames.
Read affected design, source requirements and acceptance criteria, relevant
existing design and code. Verify source approval and merge evidence from the
requirements PR. The design document needs source concept links and local
IDs for traceability. It does not need to repeat requirements PR metadata,
approval evidence, or head and merge SHAs solely to record provenance. Do
not treat the author's checked boxes or guardrail success as content-review
evidence.
If remote evidence is inaccessible, review available artifacts and report
missing evidence as UNVERIFIED.

For every checklist ID report PASS, FAIL, UNVERIFIED, or N/A with concrete
file/section/line or PR metadata evidence and rationale. N/A requires an
applicability explanation and cannot excuse unavailable evidence. Check the
credibility of the design and verification approach, not only headings.

Report findings first with impact and the smallest useful correction. Then
provide every checklist result and one overall verdict:

- NEEDS CHANGES if any applicable item fails.
- INCOMPLETE if none fail but any applicable item is unverified.
- READY FOR HUMAN REVIEW only if all applicable items pass.

State the reviewed revision and scope, unresolved decisions, and limitations.
Distinguish planned verification from executed checks. Re-evaluate affected
checks after changes; do not carry a verdict to a new diff without examination.
Review without editing unless fixes are requested. This verdict does not grant
human approval, merge permission, or permission to begin implementation.
---
name: review-design
description: Evaluate design documents or design pull requests against requirement coverage, technical decisions, verification, and phase boundaries, and report readiness for human review.
---

# Review design

Use [the checklist](references/checklist.md) as review criteria and the
[AIDD playbook](../../../docs/aidd/README.md) for approval boundaries.

Identify the target PR and base/head commits, or local diff and base. Read the
complete changed-file list, including deletions and both sides of renames.
Read affected design and separate specification documents, concrete acceptance
cases and fixtures, source requirements and acceptance criteria, and relevant
existing design and code. Apply the
[design verification deliverables](../../../docs/aidd/README.md#design-verification-deliverables)
to case coverage, static `Pending` status, compile and registration evidence,
governed paths, service-free CI feasibility, and separately resolved
prerequisites. Check command-level Ginkgo placement, build tags, test-level
choices, real product components and substituted boundaries, verification
cost, and separate unit and integration evidence under the
[Go test conventions](../../../docs/aidd/go-development.md#test-levels-and-cost).
Verify that actual Codex or external
service quality checks are designed for delivery under the
[completion boundary](../../../docs/aidd/README.md#implementation-completion-and-delivery-boundary).
If approved acceptance phases conflict, require the applicable upstream
revision before dependent implementation. Verify source approval and merge
evidence from the requirements PR. The design document needs source concept links and local
IDs for traceability. It does not need to repeat requirements PR metadata,
approval evidence, or head and merge SHAs solely to record provenance. Do
not treat the author's checked boxes or guardrail success as content-review
evidence.
If remote evidence is inaccessible, review available artifacts and report
missing evidence as UNVERIFIED.

For source approval, use the playbook's
[handoff evidence](../../../docs/aidd/README.md#agent-verification-and-handoff-evidence).
A verified Human merge passes without an Approve review, even if the human
authored the source PR. Do not classify that route as an exception. For an
automated or known agent-executed merge, verify explicit human approval of the
merged revision. Record the route, human actor, revision and merge metadata,
and any explicit approval reference; report only missing or uncertain evidence
as UNVERIFIED. This review evaluates upstream approval evidence; approval of
the design PR itself remains a subsequent human milestone.

For every checklist ID report PASS, FAIL, UNVERIFIED, or N/A with concrete
file/section/line or PR metadata evidence and rationale. N/A requires an
applicability explanation and cannot excuse unavailable evidence. Check the
credibility of the design and verification approach, not only headings.

Report findings first with impact and the smallest useful correction. Then
provide every checklist result and one overall verdict:

- NEEDS CHANGES if any applicable item fails.
- INCOMPLETE if none fail but any applicable item is unverified.
- READY FOR HUMAN REVIEW only if all applicable items pass.

Before the main session decides how to act on findings, apply
[Validate review findings](../validate-review-findings/SKILL.md). Return the
original review and independent assessment to the main session for its final
disposition. Validation does not waive checklist failures or human approval.
State the reviewed revision and scope, unresolved decisions, and limitations.
Distinguish planned verification from executed checks. Re-evaluate affected
checks after changes; do not carry a verdict to a new diff without examination.
Review without editing unless fixes are requested. This verdict does not grant
human approval, merge permission, or permission to begin implementation.

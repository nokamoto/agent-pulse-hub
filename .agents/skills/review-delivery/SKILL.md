---
name: review-delivery
description: Review agent-pulse-hub delivery instructions, real-service verification evidence, scope boundaries, and delivery pull requests for readiness for human review.
---

# Review delivery

Use the [delivery phase](../../../docs/aidd/README.md#4-delivery) as the
authoritative rule and [the checklist](references/checklist.md) as review
criteria. Review without editing unless corrections are requested.

Identify the original requested scope, target PR and base/head commits, or
exact local snapshot and base. Read the full changed-file list and diff,
including untracked deliverables, deletions, and both sides of renames.
Inspect the complete user-facing instructions, governing requirements and
design, implementation handoff, and delivery evidence. Compare every scoped
approved delivery case with its actual result and the product revision used.

For the author's independent self-review, use a fresh clean-context subagent
with `fork_turns="none"`, model `gpt-6.1-sol`, and reasoning effort `high`.
Provide the original request, repository, exact review target and base, and
evidence locations without author reasoning or expected verdicts. That
reviewer performs the review directly without further delegation. Report
unavailable independent review as incomplete.

Verify source approval using the
[handoff rules](../../../docs/aidd/README.md#agent-verification-and-handoff-evidence).
Human merge does not require a separate Approve review. Automated or known
agent-executed merges require explicit human approval covering the merged
revision. This review evaluates upstream approval; approval of the delivery
PR remains a subsequent human milestone.

Report findings first with exact file/section/line or evidence references,
impact, and the smallest useful correction. For every checklist ID report
PASS, FAIL, UNVERIFIED, or N/A with evidence and rationale. N/A requires a
scope-based applicability explanation; it cannot excuse missing evidence.
Do not count checked boxes, service-free CI, or planned execution as proof of
real-service success. Review accessible material and mark inaccessible
required evidence UNVERIFIED.

Return one overall verdict:

- NEEDS CHANGES if any applicable item fails or a blocking decision remains.
- INCOMPLETE if none fail but required evidence or review is unverified.
- READY FOR HUMAN REVIEW only when all applicable items pass.

Before the main session acts on findings, have it apply
[Validate review findings](../validate-review-findings/SKILL.md) independently.
Validation does not waive failures. State the reviewed revision and scope,
unresolved work, and limitations. Re-evaluate the final diff and affected
evidence after corrections; independent re-review uses a fresh reviewer
without previous findings. Markdown edits also require renewed
[readability review](../review-markdown-readability/SKILL.md).
Readiness is neither human approval, merge permission, authorization for
external actions, nor delivery completion.

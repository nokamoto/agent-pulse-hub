---
name: prepare-delivery
description: Prepare agent-pulse-hub delivery after implementation approval and merge, update user-facing instructions, execute approved delivery checks, and prepare a delivery pull request.
---

# Prepare delivery

Follow the [delivery phase](../../../docs/aidd/README.md#4-delivery) for scope,
deliverables, feedback, and completion. This skill prepares a delivery PR;
it does not authorize publication or change approved product behavior.

## Establish the delivery scope

Identify the requested product revision and scope. Read the implementation
PR's delivery handoff, governing requirements and design, and approved delivery
cases. Verify upstream approval and merge using the
[handoff rules](../../../docs/aidd/README.md#agent-verification-and-handoff-evidence).
Record source PRs, approval routes, reviewed heads, merge commits, and the
product revision to test. Explain upstream phases needing no update.
Report inaccessible or uncertain prerequisites as UNVERIFIED and continue
only independent work until they are verified.

Determine which acquisition method, environments, real-service checks, and
distribution or handover outputs apply to that approved scope. Identify
required access and authorization before external actions. Do not assume a
release, deployment, new distribution mechanism, or recipient installation
is part of every delivery.

## Prepare instructions and evidence

Apply the [README rule](../../../docs/aidd/README.md#user-facing-readme) before
drafting: decide what belongs in the root overview, the relevant user guide,
an authoritative specification, or PR evidence. Keep the root concept, small
input/output example, and guide links easy to scan. Put complete setup and
operation in the guide, with limitations at the affected step. Extend an
existing guide where it serves the same task; add a focused guide only when
needed, and update incoming links when moving content. A feature does not
automatically need a new root section or walkthrough. Review the whole root
page after editing, not just the added paragraph, for accumulating detail.
Keep contract inventories in specifications and verification records in the PR.
Use the README rule's primary shell convention;
check prerequisites against the governing requirements and design rather than
copying the local verification environment. Record shell adaptations and
unexecuted published commands under
[delivery inputs and outputs](../../../docs/aidd/README.md#delivery-inputs-and-outputs).
If no README change is necessary,
explain why and still verify the existing instructions for this scope.

Follow the root entry link and the guide's complete acquisition and setup
steps, then execute every scoped
delivery case against the identified product revision. Use the approved
expected results and execution roles; a case requiring a top-level Codex
conversation must not be executed through a subagent instead.
Record the evidence required by
[delivery inputs and outputs](../../../docs/aidd/README.md#delivery-inputs-and-outputs)
in the PR, including separate product and documentation revisions, environment,
case mappings, actual outcomes, and evidence links. Keep credentials and
private content out of retained evidence. Record required distribution and
handover outcomes, or explain their non-applicability.

If a required case cannot run, record the reason and impact and keep the PR
in draft. If a defect, missing upstream document, or changed acceptance
decision requires another phase, follow
[delivery feedback](../../../docs/aidd/README.md#delivery-scope-and-feedback):
preserve work, report the affected cases and prerequisite phase, and end the
session. Do not fix product code or infrastructure in the delivery diff or
start another session automatically. After verified prerequisite approval
and merge, rerun affected checks and update the tested revision.

## Review and prepare the PR

Run the applicable checks from [AGENTS.md](../../../AGENTS.md), including
formatting and guardrails, and record actual results. Apply
[Markdown readability review](../review-markdown-readability/SKILL.md) to all
written or edited Markdown. For the root README, specify first-time human
users as the primary audience and understanding the concept, seeing a simple
example, and choosing a guide as the purpose. Review each detailed guide for
its complete working path separately; include external plugin authors for
the plugin section.
Do not frame the audience solely as agents, contract implementers, or delivery
reviewers. Delegate delivery self-review to a fresh
clean-context subagent with `fork_turns="none"`, model `gpt-6.1-sol`, and
reasoning effort `high`. Supply the original requested scope, repository,
exact target/base revision or local snapshot, and evidence locations; have it
apply [review-delivery](../review-delivery/SKILL.md). Omit author reasoning,
prior findings, and expected verdicts. Address validated findings, repeat
readability review after edits, and re-review the final diff and evidence.
Unavailable checks or independent review leave readiness incomplete.

Prepare the body using the
[delivery PR template](../../../.github/PULL_REQUEST_TEMPLATE/delivery.md).
Keep incomplete required actions or evidence explicit and the PR in draft.
When PR creation or update is authorized, publish the prepared revision and
verify its remote branch, head, and body. Otherwise provide the prepared body
and identify any unfinished remote step. Request human review through the
[approval routes](../../../docs/aidd/README.md#human-approval-routes).
Human merge is a normal approval route. Report phase completion only after
the [delivery completion criteria](../../../docs/aidd/README.md#delivery-completion)
and approval and merge are verified; preparation does not authorize merging.

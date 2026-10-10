---
name: create-design
description: Draft or revise agent-pulse-hub design from approved requirements, map verification coverage, and prepare a design pull request before implementation.
---

# Create design

Follow the [AIDD playbook](../../../docs/aidd/README.md). Design explains how
approved requirements will be fulfilled and verified.

1. Identify source requirement concepts and local IDs and verify their merged
   requirements PR using the playbook's
   [handoff evidence](../../../docs/aidd/README.md#agent-verification-and-handoff-evidence).
   Record the approval route, human actor, final head SHA, merge time, merge
   commit SHA, and any explicit approval reference for the downstream PR.
   Human merge is normal approval, including when the human authored the PR;
   do not require an additional Approve review or describe it as an exception.
   Confirm the source documents match the approved merged revision. Inspect
   relevant design and code. If required evidence is missing or uncertain,
   report the specific prerequisite as UNVERIFIED and continue only independent
   investigation until it is verified.
2. Create `docs/design/<stable-topic>.md` from
   [the asset](assets/design.md), or revise the existing concept. Preserve stable
   paths. Replace prompts and explain non-applicable sections. Identify each
   requirement by concept path plus local ID. Record source documents in the
   Open Knowledge Format (OKF) frontmatter `sources` field; resources are
   relative to the `docs/` bundle root, for example `requirements/topic.md`.
   Use relative Markdown links in the body.
   Keep upstream PR and approval/revision evidence in the design PR
   description rather than copying provenance-only PR links or SHAs into
   the design document.
   Describe the intended architecture and decisions without authoring-time
   implementation progress or temporary repository state; put work status in
   the PR or issue. Retain existing behavior when it affects a design decision,
   and identify the applicable version or migration condition.
3. Describe responsibilities, interfaces, data lifecycle, and normal and
   relevant failure scenarios. Compare viable alternatives and explain important
   tradeoffs. Address applicable compatibility, operations, extensibility,
   security, and privacy concerns. Match detail to risk and leave routine
   implementation choices to implementation. Apply the playbook's
   [specification document rules](../../../docs/aidd/README.md#specification-documents-and-approved-design):
   record whether contracts stay in the design or use separate specifications,
   their paths, purpose and readers, authority and derivation, and the contract
   decisions and constraints on routine implementation choices. Create or revise
   the separate specifications as part of the design PR, with Design frontmatter
   and links to the governing design. Do not defer document creation to
   implementation. When a human starts this design session from implementation
   feedback, use the reported gap as input and keep implementation stopped until
   the upstream approval and merge prerequisites are verified.
4. Map every in-scope functional and nonfunctional requirement and acceptance
   criterion to design and feasible verification. Explain partial coverage and
   related designs. Record open decisions, impact, owner, and resolution stage.
   Implementation blockers prevent readiness. If approved requirements must
   change, return to a separate requirements PR and obtain approval and merge
   before dependent design work.
5. Run `go run build/mage.go -d build -w . guardrails` at the repository root.
   The design guardrail checks recursive `docs/design/**/*.md` files and this
   asset: ordered `type`, `title`, `description`, `sources` fields;
   `type: Design`; nonempty string title/description; a nonempty sources list
   with nonempty string `id` and `resource` and optional string `title`;
   and a nonempty body. Other fields, duplicate keys, malformed YAML, and
   symlinks are rejected. Separate specifications outside `docs/design/` are
   outside this guardrail's scan: review the same document structure manually
   and record the files and results in the PR. Coverage, document relationships,
   and authority require content review; human approval follows the playbook's
   approval routes.
6. Apply [Markdown readability review](../review-markdown-readability/SKILL.md).
   Delegate self-review to a fresh clean-context subagent using
   `fork_turns="none"`, model `gpt-6.1-sol`, reasoning effort `high`.
   Supply the repository, target/base revision, and original requirement inputs;
   have it apply [review-design](../review-design/SKILL.md) to the complete diff.
   Omit author reasoning and expected verdict. Correct findings, repeat
   readability review after Markdown edits, and re-evaluate affected design
   checks. Report unavailable review or checks as incomplete.
7. Prepare the body with [the PR template](../../../.github/PULL_REQUEST_TEMPLATE/design.md).
   Include source handoff evidence and explain the two normal
   [human approval routes](../../../docs/aidd/README.md#human-approval-routes).
   If PR creation is requested, explicitly apply and verify `phase:design`;
   Markdown templates do not apply labels. If missing, create the label when
   authorized or report the missing setup. Report unavailable remote steps
   without claiming success.

Keep the complete design PR within the playbook's
[design and specification scope](../../../docs/aidd/README.md#specification-documents-and-approved-design),
including deletions and both sides of renames. Prepare supporting workflow or
tooling separately. Preserve unrelated working changes. Keep blocked PRs in
draft. Implementation starts or resumes only after human approval and merge
of the upstream PRs are verified.

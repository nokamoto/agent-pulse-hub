---
name: create-design
description: Draft or revise agent-pulse-hub design from approved requirements, map verification coverage, and prepare a design pull request before implementation.
---

# Create design

Follow the [AIDD playbook](../../../docs/aidd/README.md). Design explains how
approved requirements will be fulfilled and verified.

1. Identify source requirement concepts and local IDs, the merged requirements
   PR, and human approval for its revision. Inspect relevant design and code.
   If approval or merge evidence is missing, report the prerequisite and
   continue only independent investigation until it is verified.
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
3. Describe responsibilities, interfaces, data lifecycle, and normal and
   relevant failure scenarios. Compare viable alternatives and explain important
   tradeoffs. Address applicable compatibility, operations, extensibility,
   security, and privacy concerns. Match detail to risk and leave routine
   implementation choices to implementation.
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
   symlinks are rejected. Coverage and approval require review.
6. Apply [Markdown readability review](../review-markdown-readability/SKILL.md).
   Delegate self-review to a fresh clean-context subagent using
   `fork_turns="none"`, model `gpt-6.1-sol`, reasoning effort `high`.
   Supply the repository, target/base revision, and original requirement inputs;
   have it apply [review-design](../review-design/SKILL.md) to the complete diff.
   Omit author reasoning and expected verdict. Correct findings, repeat
   readability review after Markdown edits, and re-evaluate affected design
   checks. Report unavailable review or checks as incomplete.
7. Prepare the body with [the PR template](../../../.github/PULL_REQUEST_TEMPLATE/design.md).
   If PR creation is requested, explicitly apply and verify `phase:design`;
   Markdown templates do not apply labels. If missing, create the label when
   authorized or report the missing setup. Report unavailable remote steps
   without claiming success.

Design phase PRs change only `docs/design/`, including deletions and both sides
of renames. Prepare supporting workflow or tooling separately. Preserve
unrelated working changes. Keep blocked PRs in draft. Implementation starts
only after human approval and merge of the design PR.
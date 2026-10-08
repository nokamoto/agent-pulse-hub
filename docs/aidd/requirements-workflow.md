---
type: Playbook
title: Requirements workflow
description: Skills, pull request convention, and review gates for requirements definition.
sources:
  - id: aidd
    resource: README.md
    title: AI-Driven Development
---

# Requirements workflow

This workflow applies the phase boundaries in [AI-Driven Development](README.md).
Requirements live in `docs/requirements/`; workflow tooling changes use a
separate maintenance PR, without the requirements phase label.

## Skills

Repository skills live in `.agents/skills/`, following the
[Codex skill convention](https://developers.openai.com/codex/skills/).

- Invoke `$define-requirements` with the problem or requested change. The
  [creation skill](../../.agents/skills/define-requirements/SKILL.md) uses its
  [requirement asset](../../.agents/skills/define-requirements/assets/requirement.md)
  to create or revise a stable requirement concept.
- Invoke `$review-requirements` with a PR URL or local diff/base. The
  [review skill](../../.agents/skills/review-requirements/SKILL.md) evaluates all
  [checklist criteria](../../.agents/skills/review-requirements/references/checklist.md)
  and provides an evidence-based verdict.

Example: `$define-requirements Define requirements for external event delivery.`
The agent clarifies material unknowns before presenting the draft as ready.
If a running session does not discover a newly added skill, start a new session
in this repository, or explicitly ask the agent to read the linked SKILL.md.

## Pull requests and label setup

Use the label `phase:requirements` (color `0052CC`, description
`Requirements definition and review`). A maintainer creates it once in the
repository's GitHub label settings. With authenticated GitHub CLI:

```sh
gh label create "phase:requirements" --color "0052CC" --description "Requirements definition and review"
```

Check for an existing label first; do not overwrite unrelated label settings.
Adding these files does not create a label remotely.

Use [the requirements PR template](../../.github/PULL_REQUEST_TEMPLATE/requirements.md).
For browser PR creation, use the `template=requirements.md` query parameter in
the compare URL after the template is on the default branch, as described in
[GitHub's PR template guide](https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/creating-a-pull-request-template-for-your-repository).
Apply `phase:requirements` explicitly; Markdown PR templates do not apply it.
With the CLI, fill a local copy and pass that completed file to
`gh pr create --body-file <completed-file> --label "phase:requirements"`.
Use `--draft` while design-blocking decisions remain unresolved.

The PR contains only changes under `docs/requirements/`. Review the complete
diff, including deleted paths and both rename endpoints. Skills, templates,
CI configuration, and design belong in separate PRs. Report checklist results
in the PR body or an authorized review comment; do not add review reports as
requirement concepts.

READY FOR HUMAN REVIEW means all applicable checks passed, not human approval.
Human review of the current revision and merge are required before dependent
design work starts. Update review evidence when the diff changes.

## Guardrail decision

Do not add a GitHub Action in this initial setup. Use R11 for scope review and
R12 for label/PR evidence. This is a review convention, not automated enforcement.
A path check catches accidental phase mixing, but cannot assess requirement
quality or establish approval; label-only selection also allows unlabeled or
relabeled PRs to avoid the check. With no established CI or phase-label
enforcement here, defer that operational complexity until needed.

Revisit automation if mixed-phase changes recur or automated enforcement
becomes a repository requirement. A future check should examine the complete
PR diff against its base, handle additions/deletions/renames, fail on incomplete
file enumeration, and rerun on updates and label changes. Decide separately
how to detect missing phase labels and configure required checks. Run with
read-only permissions without executing untrusted PR code in a privileged
context. Keep semantic review and human approval even when a path check passes.

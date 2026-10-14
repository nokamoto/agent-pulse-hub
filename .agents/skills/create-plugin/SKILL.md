---
name: create-plugin
description: Create or extend a bundled Agent Pulse Hub trigger or action plugin through the repository's requirements, design, implementation, and delivery phases. Use for plugin authoring, artifact preparation, and independent integration verification; not for installing plugins or registering a conversation.
---

# Create a bundled plugin

Use this skill to author plugins shipped in this repository. A trigger watches
an event source; an action receives routed context and performs a destination
operation. These correspond to the public input and output roles respectively.
This skill coordinates the existing phase workflows; it does not approve a
product contract or authorize crossing a phase boundary.

## Establish the task and current phase

1. Read [AGENTS.md](../../../AGENTS.md), the
   [AIDD playbook](../../../docs/aidd/README.md), the
   [bundled-plugin requirements](../../../docs/requirements/bundled-plugins.md),
   and the plugin-specific requirements and governing design, if present.
   Consult the [MVP design](../../../docs/design/mvp.md) and
   [public plugin specification](../../../docs/design/plugin-v1.md) for shared
   routing and lifecycle contracts. Read their contents rather than inferring
   protocol versions or compatibility from filenames.
2. Identify the plugin name, trigger/action role, external source or destination,
   requested phase, and intended observable result. Establish configuration,
   credentials or executable prerequisites, watch or destination arguments,
   relevant failures, and how the integration can be verified independently.
   Ask for missing product decisions while continuing independent investigation.
3. Check which requirements, design, and repository-rule prerequisites are
   approved and merged using the playbook's
   [handoff evidence](../../../docs/aidd/README.md#agent-verification-and-handoff-evidence).
   Start at the earliest phase needing work within the user's authorized scope.
   If a prerequisite is unverified, report exactly what is missing; do not
   treat requirements approval as design approval or start dependent work.

## Requirements and design

Use [define-requirements](../define-requirements/SKILL.md) and
[create-design](../create-design/SKILL.md) for their respective phases, including
their reviews, separate PRs, and approval boundaries. Keep common packaging
obligations in the bundled-plugin requirements and integration-specific behavior
in its own requirement concept.

During design, resolve the following against the authoritative contracts:

- Use `plugins/triggers/<plugin-name>/` or `plugins/actions/<plugin-name>/`.
  Follow the [artifact locations and presence check](../../../docs/aidd/go-development.md#bundled-plugin-artifacts);
  specify source and example filenames within those locations and the lowercase
  `readme.md`. For design-only suites before introducing a plugin, use the
  verification command placement defined there. Check the
  [Go placement rules](../../../docs/aidd/go-development.md#package-placement)
  and actual guardrails before relying on a layout. New permitted source or
  test placement, build tooling, and artifact-presence enforcement require the
  separate [repository rules workflow](../edit-repository-rules/SKILL.md).
  Verify its approval and merge before dependent work; a requirement for a
  directory does not itself make existing validators accept it.
- Define configuration and argument validation, lifecycle and shutdown,
  observable success, failure and uncertain outcomes, and service compatibility.
  Keep service-specific behavior in the plugin and shared routing in the hub.
  Trigger/action terminology does not by itself rename public wire roles,
  configuration keys, or CLI flags. Resolve compatibility and migration in
  design before changing them.
- Identify affected specifications and developer operating instructions and
  create them during the upstream phase required by the playbook. Define
  user-facing usage materials for delivery. If relocating the existing model
  plugins, account for build callers, examples, and existing service-free
  verification.
- Select verification under the playbook's
  [design verification rules](../../../docs/aidd/README.md#design-verification-deliverables).
  Separate unit tests with mocked external capabilities, command-level
  integration acceptance, standalone plugin verification through the public
  interface, and real-service delivery checks. Preserve the service-free
  [model-plugin hub suite](../../../docs/design/mvp.md#verification-strategy).
  Record concrete cases, prerequisites, expected
  results, evidence, cost, and the responsible phase; simulated service results
  do not prove the real integration works.

## Implement the approved scope

After verifying upstream approval and merge, implement in Go using the approved
public lifecycle and role interface. Follow [Go development conventions](../../../docs/aidd/go-development.md)
and the approved source layout. Keep external context as data and credentials
out of committed examples and diagnostics.

Run the formatting, guardrail, test, and lint commands in
[AGENTS.md](../../../AGENTS.md), plus the scoped acceptance checks selected in
design. Report unit and integration results separately. Apply the
[implementation completion boundary](../../../docs/aidd/README.md#implementation-completion-and-delivery-boundary):
every scoped approved CI case must pass; Pending, skipped, missing, filtered,
or deleted cases do not establish completion. Record evidence in the
[implementation PR template](../../../.github/PULL_REQUEST_TEMPLATE/implementation.md).

If implementation reveals missing developer documentation, a contract change,
or a need to change CI or shared verification infrastructure, follow the
feedback rules in AGENTS.md: preserve work, report the affected upstream
artifact and unfinished checks, and end the implementation session. Do not
silently repair upstream decisions in the implementation PR.

## Deliver usage materials and evidence

After implementation approval and merge, use
[prepare-delivery](../prepare-delivery/SKILL.md) and
[review-delivery](../review-delivery/SKILL.md). Check each plugin's materials
against bundled-plugins FR-002:

- The lowercase `readme.md` explains purpose, prerequisites, build/run steps,
  configuration and argument meanings, observable results, and relevant failures.
- Configuration examples show daemon setup and watch or destination arguments.
  Identify values the operator must supply rather than guessing credentials,
  paths, or recipient identities. Use English documentation and the playbook's
  OS-neutral procedures.
- Plugin-specific skills are discoverable from its README when required by
  the approved plugin design. Update the root README's appropriate usage and
  plugin-author entry points under its existing content rules.

Run the approved standalone real-service checks separately from hub model
checks. Retain actual environment, commands, results, and limitations in the
delivery evidence. Missing prerequisites or unexecuted checks remain explicit
gaps, not successful verification. The artifact-presence guardrail establishes
presence only; review content and runtime behavior separately.

Apply [Markdown readability review](../review-markdown-readability/SKILL.md)
after writing or editing Markdown in any phase. Finish with the current phase's
deliverables, checks and evidence, unresolved decisions or prerequisites, and
its reviewable PR or prepared PR body. Continue to the next phase only under
the playbook's verified approval and merge rules and the user's task scope.

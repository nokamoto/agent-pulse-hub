---
type: Requirement
title: Bundled trigger and action plugins
description: Organize bundled integrations by role and enforce consistent implementation and usage materials.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
---

# Bundled trigger and action plugins

## Problem and goals

Operators and plugin authors need a predictable place to find the integrations
shipped with Agent Pulse Hub, their implementation, and instructions for using
them. Role names should describe what plugins do rather than depend on the
reader's interpretation of input and output direction.

These requirements define the common organization for a growing collection of
bundled plugins. Plugin-specific behavior and acceptance criteria belong in
separate requirement documents, such as the
[Codex Queue action requirements](codex-queue.md).

## Sources and decisions

The selected role names are **trigger / action**. Each plugin directory must
contain implementation, `readme.md`, and configuration examples, with a
guardrail enforcing their presence. Existing manual and model-output plugins
must migrate into the same organization.

The [MVP requirements](mvp.md) remain the service-neutral routing baseline.
Individual plugin requirements supplement this document and that baseline;
they do not replace common packaging or verification obligations.

## Terms and workflow

- **Trigger plugin:** watches or subscribes to an event source and reports
  matching events. This is the role called an input plugin in the MVP.
- **Action plugin:** receives routed event context and performs a
  destination-specific operation. This is the MVP output-plugin role. Naming
  does not add arbitrary new action behavior; individual integrations define
  their own scope.
- **Bundled plugin:** a plugin whose implementation and usage materials ship
  in this repository, as distinct from a separately maintained executable.

The operator selects a trigger and action, follows their bundled instructions
to build/configure them, and registers a watch and destination through the
local client. The daemon routes matching trigger events to the selected action.
Maintainers use artifact checks and plugin-specific verification to assess
whether each bundled integration is complete.

## Scope

Included are role naming, bundled-plugin organization, implementation and usage
materials, migration of the existing model plugins, and mechanical presence
checks. All requirements below are required; there is no optional feature tier.

Excluded are plugin installation management, automatic discovery, and changes
to the MVP's common routing/lifecycle behavior. Individual external-service
integration behavior belongs in separate plugin requirements. The service-free
model-plugin walkthrough and end-to-end suite remain required.

## Use cases

| ID | Actor and trigger | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator selects a bundled plugin | Its role, implementation, usage, and configuration examples are discoverable together | Missing required artifacts fail the repository guardrail |

## Functional requirements

| ID | Requirement |
| --- | --- |
| FR-001 | Name the two plugin roles trigger and action in the bundled-plugin organization and user-facing descriptions. Store bundled triggers in `plugins/triggers/<plugin-name>/` and actions in `plugins/actions/<plugin-name>/`. Document their correspondence to the MVP input/output roles; naming does not reverse routing direction. |
| FR-002 | Each bundled plugin directory must contain its implementation, a lowercase `readme.md`, and configuration examples for using that plugin. The README must explain its purpose, prerequisites, build/run procedure, configuration and argument meanings, observable result, and relevant failures. Examples must show how to configure the plugin with the daemon and supply its watch or destination arguments without guessing required user-specific values. Move the existing manual trigger and model-output action implementations into their respective role directories and apply the same artifact requirements to both. Shared framework code may remain outside plugin directories. |
| FR-003 | Provide an automated repository guardrail that checks every bundled plugin directory for the required implementation, `readme.md`, and configuration-example artifacts. Missing artifacts must fail the check with the plugin path and missing artifact identified. A complete plugin must pass. Define mechanically checkable artifact locations in the repository rules workflow; presence checks do not establish documentation correctness or runtime success. |

## Nonfunctional requirements

The MVP's NFR-001–005 remain applicable: no coding-agent polling, public
language-neutral interfaces, external-data boundaries, reproducible operation,
and service-free hub verification distinct from independent service-plugin
checks. Go implementation, English documentation, and OS-neutral product
contracts remain mandatory. Performance, platform, and delivery guarantees
remain governed by the MVP requirements and their acceptance checks.

## Constraints and dependencies

Use the MVP's `hashicorp/go-plugin` lifecycle and public language-neutral role
interfaces. The daemon owns integration-independent control; plugins own
service-specific operations. Model plugins remain usable without live services.

Exact source layout within each plugin and configuration-example filenames are
design decisions constrained by FR-002. Mechanically checkable artifact
locations and enforcement belong to a separate repository rules change under
the [AIDD workflow](../aidd/README.md). This document specifies its required
outcome, not the guardrail implementation.

## Acceptance criteria

The maintainer evaluates documentation and migration evidence. Existing MVP
acceptance criteria continue to verify the model plugins after relocation;
changing build paths must not remove their coverage or service-free operation.

| ID | Requirements | Stimulus and decisive expected result | Verification |
| --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, FR-003 | Inspect each bundled plugin's role/path and artifacts. A complete fixture passes the guardrail. Independently remove implementation, README, and examples: each fails and names the affected plugin and missing artifact. Inspect examples and README for the required usage information. | Automated presence checks and maintainer documentation review; fixture locations and checks must be documented in repository rules |

## Open questions and assumptions

No unresolved scope decisions. The design owner must specify
compatibility for existing public `input`/`output` spellings, build paths, and
transition instructions before implementation. This determines operator
migration steps; it does not defer trigger/action terminology or require
backwards compatibility.

## Change impact

These requirements supplement the MVP's service-neutral routing contract.
Update [the routing design](../design/mvp.md),
[the plugin specification](../design/plugin-v1.md), and
[the operations guide](../design/windows-operations.md) where role names,
plugin locations, or examples are affected. Build-path migration must update
callers and instructions without dropping model-plugin coverage. Delivery must
update the root README's entry points.

Repository rules and guardrail implementation require their own change,
covering artifact locations, discovery, missing-artifact fixtures, and
integration with the standard guardrail command. Repository enforcement,
plugin implementation, and skill changes belong in their respective phase
changes, separate from requirements.

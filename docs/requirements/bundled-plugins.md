---
type: Requirement
title: Bundled trigger and action plugins
description: Organize bundled integrations by role and deliver events to existing Codex conversations through a Codex Queue action.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: prior-codex-requirements
    resource: https://github.com/nokamoto/agent-pulse-hub/blob/7fe3fb4/docs/requirements/mvp.md
    title: Previous Codex delivery requirements
  - id: prior-codex-adapter
    resource: https://github.com/nokamoto/agent-pulse-hub/blob/7fe3fb4/internal/adapters/codex/adapter.go
    title: Previous Codex Queue adapter
  - id: prior-registration-skill
    resource: https://github.com/nokamoto/agent-pulse-hub/blob/7fe3fb4/.agents/skills/register-pulse/SKILL.md
    title: Previous current-conversation registration skill
---

# Bundled trigger and action plugins

## Problem and goals

Operators and plugin authors need a predictable place to find the integrations
shipped with Agent Pulse Hub, their implementation, and instructions for using
them. Role names should describe what plugins do rather than depend on the
reader's interpretation of input and output direction.

This extension introduces trigger and action terminology and a bundled Codex
Queue action. An external event must be able to reach a selected existing
Codex conversation and let it continue under its existing instructions,
without embedding Codex integration in the daemon.

## Sources and decisions

The selected role names are **trigger / action**. Each plugin directory must
contain implementation, `readme.md`, and configuration examples, with a
guardrail enforcing their presence. Codex Queue is the first external-agent
integration added under this organization. The linked historical Codex
integration informs its delivery behavior and registration workflow.

The [MVP requirements](mvp.md) remain the service-neutral routing baseline.
The historical sources above establish the prior Codex behavior; their
daemon-owned adapter, old registration flags, and platform-specific examples
are not requirements for the new plugin.

## Terms and workflow

- **Trigger plugin:** watches or subscribes to an event source and reports
  matching events. This is the role called an input plugin in the MVP.
- **Action plugin:** receives routed event context and performs delivery or
  another destination-specific operation. This is the MVP output-plugin role;
  this extension adds only Codex delivery, not arbitrary new action behavior.
- **Bundled plugin:** a plugin whose implementation and usage materials ship
  in this repository, as distinct from a separately maintained executable.
- **Queue acceptance:** Codex confirms that a message was queued for the
  requested conversation. It does not prove that the agent has processed it.

The operator builds and configures the desired trigger and action. In the
target Codex conversation, the user invokes the registration skill to connect
the trigger watch to that conversation. A matching event is routed by the
daemon to the Codex Queue action, which submits it to Codex and reports the
delivery outcome. The conversation processes accepted work when available.

## Scope

Included are role naming, bundled-plugin organization and usage materials,
mechanical presence checks, the Codex Queue action, its current-conversation
registration skill, and independent verification of the Codex integration.
All requirements below are required within this extension; there is no
separate optional feature tier.

Excluded are new external-service triggers, other agent integrations, plugin
installation management, automatic discovery, new conversations, persistence,
automatic retries, and changes to the MVP's common routing/lifecycle behavior.
The service-free model-plugin walkthrough and end-to-end suite remain required.

## Use cases

| ID | Actor and trigger | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator selects a bundled plugin | Its role, implementation, usage, and configuration examples are discoverable together | Missing required artifacts fail the repository guardrail |
| UC-002 | User invokes registration in an existing Codex conversation | The requested trigger watch is connected to that conversation through the Codex Queue action | Missing or inconsistent conversation identity fails without guessing a recipient |
| UC-003 | Trigger reports an event for a registered Codex destination | The action queues the context for that destination and reports acceptance | Busy conversations are not interrupted; uncertain acceptance is not reported as success |
| UC-004 | Maintainer verifies the Codex integration | Standalone action evidence establishes real Codex behavior separately from hub routing | Model-only or simulated checks do not establish real Codex success |

## Functional requirements

| ID | Requirement |
| --- | --- |
| FR-001 | Name the two plugin roles trigger and action in the bundled-plugin organization and user-facing descriptions. Store bundled triggers in `plugins/triggers/<plugin-name>/` and actions in `plugins/actions/<plugin-name>/`. Document their correspondence to the MVP input/output roles; naming does not reverse routing direction. |
| FR-002 | Each bundled plugin directory must contain its implementation, a lowercase `readme.md`, and configuration examples for using that plugin. The README must explain its purpose, prerequisites, build/run procedure, configuration and argument meanings, observable result, and relevant failures. Examples must show how to configure the plugin with the daemon and supply its watch or destination arguments without guessing required user-specific values. Move the existing manual trigger and model-output action implementations into their respective role directories and apply the same artifact requirements to both. Shared framework code may remain outside plugin directories. |
| FR-003 | Provide an automated repository guardrail that checks every bundled plugin directory for the required implementation, `readme.md`, and configuration-example artifacts. Missing artifacts must fail the check with the plugin path and missing artifact identified. A complete plugin must pass. Define mechanically checkable artifact locations in the repository rules workflow; presence checks do not establish documentation correctness or runtime success. |
| FR-004 | Bundle `plugins/actions/codex-queue/` as an action using the public output-plugin interface and the same daemon-managed plugin lifecycle as other actions. Codex executable selection, destination identity, CLI interaction, and queue-acceptance interpretation belong to this plugin, not to the daemon or triggers. |
| FR-005 | Deliver event source, subscription identifier, and context to the registered existing Codex conversation using `codex queue`. Do not create a conversation or allow event content to change the recipient. While the conversation is working, enqueue without interrupting it or report that acceptance was not established. Acceptance must be confirmed for the requested conversation, separately from subsequent agent processing. |
| FR-006 | Preserve one delivery attempt and no automatic retry. Report an attempt that cannot start as failed; when submission may have happened but target acceptance cannot be confirmed, report unknown rather than success. This includes interrupted or timed-out commands and missing, malformed, or mismatched acknowledgements. Preserve relevant plugin/subscription diagnostics without claiming that a queued message has been processed. |
| FR-007 | Bundle a skill for registering the current top-level Codex conversation through the local client, selecting a configured trigger and its watch arguments and the Codex Queue action. Obtain conversation identity from the invoking conversation's environment; fail on missing, malformed, or inconsistent identity rather than selecting another conversation. Report registration success only after the client confirms it and returns a subscription identifier. Preserve uncertain registration outcomes and do not automatically retry. |
| FR-008 | Supply Codex-specific usage instructions and configuration examples, including how to identify a compatible Codex executable, build/configure the action, register, trigger an event, observe the existing conversation's response, and stop. Explain queue acceptance versus processing, busy-conversation behavior, registration loss on shutdown, and no automatic retries. Make the registration skill discoverable from the plugin README. |

## Nonfunctional requirements

| ID | Requirement and evaluation |
| --- | --- |
| NFR-001 | Keep Codex dependencies out of the service-free model-plugin hub suite. Verify the action against Codex independently of the daemon and any trigger's live service, using the public action interface. Keep simulated CI results, real Codex results, and hub routing results distinct. |
| NFR-002 | Identify forwarded context as untrusted external data. Neither delivery nor the registration skill grants new authority beyond the recipient's existing instructions and permissions. Preserve context as data and the registered recipient as routing authority. Inspect messages and skill instructions and test adversarial event context. |
| NFR-003 | Preserve OS-neutral product contracts and procedures, Go implementation, and English repository documentation. Do not hard-code a development host's Codex installation path or shell. Identify actual Codex compatibility prerequisites and record the tested environment; one successful run does not imply universal platform support. |
| NFR-004 | While no event occurs, do not request Codex work. Delivery must not depend on periodic coding-agent checks. Observe an idle period and a triggered delivery independently; no numerical latency target is introduced. |

## Constraints and dependencies

Use the MVP's `hashicorp/go-plugin` lifecycle and public language-neutral role
interfaces. The daemon owns integration-independent control; the Codex action
owns service-specific operations. The existing model output remains useful
and is not replaced by Codex.

Actual Codex verification requires a Codex installation supporting `queue` and
an existing accessible conversation. Design must establish the supported CLI
invocation and acknowledgement contract, compatible conversation-identity
source, supported skill invocation contexts (including how delegated/subagent
invocations are handled), and verification method. Historical acknowledgement text and
environment-variable names are evidence to investigate, not an unchecked
promise of current compatibility. Unsupported prerequisites must produce an
actionable diagnostic rather than false success.

Exact source layout within each plugin, configuration-example filenames,
skill packaging/discovery, and protocol/CLI compatibility treatment are design
decisions constrained by these requirements. Enforcement changes belong to a
separate repository rules change under the [AIDD workflow](../aidd/README.md);
this requirements change specifies the required guardrail outcome.

## Acceptance criteria

The maintainer evaluates manual evidence. Real Codex evidence records OS,
shell, Codex version and executable selection, commands, selected conversation,
unique event context, action results, and observed conversation behavior.

| ID | Requirements | Stimulus and decisive expected result | Verification |
| --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, FR-003 | Inspect each bundled plugin's role/path and artifacts. A complete fixture passes the guardrail. Independently remove implementation, README, and examples: each fails and names the affected plugin and missing artifact. Inspect examples and README for the required usage information. | Automated presence checks and maintainer documentation review; fixture locations and rules are defined in the repository rules change |
| AC-002 | FR-004, NFR-001 | Launch the Codex action through the public plugin lifecycle and exercise destination validation/delivery with a standalone caller. Separately run hub model-plugin checks without Codex or live services. Both sets report independent results; source/interface inspection finds no daemon-owned Codex integration. | Automated isolated action checks and model-plugin hub verification; real-service evidence belongs to AC-003/004 |
| AC-003 | FR-005, FR-008, NFR-001, NFR-003 | Following the plugin instructions, submit recognizable context to existing conversation A through a standalone action caller. A, instructed in advance to acknowledge it, responds without another user message. Unregistered conversation B receives nothing and no new conversation is created. Record target-specific queue acceptance separately from A's response. | Maintainer-run real Codex check with the evidence above; no live trigger service or daemon required |
| AC-004 | FR-005, FR-006 | Submit while A is working: observe queued work without interruption, or a reported non-acceptance outcome, with no automatic retry. Independently simulate launch failure, timeout/interruption, missing/malformed acknowledgement, and acknowledgement for another target. Launch failure is failed; possibly submitted but unconfirmed work is unknown; none produces a false success or second attempt. | Real Codex busy-conversation evidence plus automated controlled CLI failure checks |
| AC-005 | FR-007, FR-008 | Invoke the skill in A using configured plugins and valid watch arguments: confirmed registration returns its identifier and targets A. Missing, malformed, or inconsistent identity does not register another recipient. Rejected or uncertain registration is not reported as active and is not retried automatically. | Automated registration boundary checks and maintainer skill walkthrough with the actual daemon, bundled manual trigger, and Codex action |
| AC-006 | NFR-002, FR-005, FR-007 | Deliver context asking to change recipient or grant additional permissions. The submission still targets A; context remains identified as external data. Inspect the skill and forwarded message for the existing-authority boundary. | Controlled submission inspection and maintainer skill review; no claim that arbitrary downstream model behavior can be guaranteed |
| AC-007 | NFR-003, FR-002, FR-008 | Inspect implementation and instructions for Go, English, portable paths/process invocation, explicit executable selection, and documented compatibility prerequisites. Follow build/configuration examples and observe their stated results, including clear unsupported-prerequisite failures. | Source/document review, controlled prerequisite-failure checks, and the recorded AC-003/005 walkthroughs |
| AC-008 | NFR-004 | Observe a declared idle period with zero Codex work submissions, then deliver an event and observe a submission without an agent polling turn. | Automated submission observation with declared timing evidence; no numerical timing gate |

## Open questions and assumptions

None blocking requirements approval. Existing manual and model-output plugins
must migrate into the trigger and action directories and satisfy the same
artifact checks as Codex Queue.

The design owner must specify compatibility for existing public `input`/`output`
spellings, build paths, and transition instructions before implementation.
This determines operator migration steps; it does not defer the selected
trigger/action terminology or require backwards compatibility.

## Change impact

This is a new requirement concept; IDs are local to this document. It extends
the MVP rather than restoring retired MVP FR-005. The MVP's exclusion of live
Codex integration still describes its service-free baseline; this extension
adds Codex-specific obligations without weakening MVP NFR-005 or its model
acceptance criteria. All MVP IDs remain unchanged.

Design must update [the routing design](../design/mvp.md),
[the plugin specification](../design/plugin-v1.md), and
[the operations guide](../design/windows-operations.md) where role names,
plugin locations, registration, or examples are affected. It must define the
Codex action/skill contracts and allocate automated CI versus real-service
delivery verification. Any build-path migration must update affected callers
and instructions without dropping model-plugin coverage. Delivery must add
the new plugin's usage material and update the root README's entry points.

Repository rules and guardrail implementation require their own change,
covering artifact locations, discovery, missing-artifact fixtures, and
integration with the standard guardrail command. Repository enforcement,
plugin implementation, and skill changes belong in their respective phase
changes, separate from requirements.

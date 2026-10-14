---
type: Requirement
title: Codex Queue action plugin
description: Deliver events to existing Codex conversations through a bundled queue action and current-conversation registration skill.
sources:
  - id: bundled-plugins
    resource: bundled-plugins.md
    title: Bundled trigger and action plugins
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

# Codex Queue action plugin

## Problem and goals

An external event must be able to reach a selected existing Codex conversation
and let it continue under its existing instructions without another user
message. Codex-specific delivery belongs in an action plugin, leaving the
daemon and trigger plugins independent of Codex.

## Sources and decisions

Codex Queue is an action plugin governed by the
[bundled-plugin requirements](bundled-plugins.md). That document governs
trigger/action terminology, directory layout, mandatory artifacts, model-plugin
migration, and guardrail enforcement. Those common obligations apply to this
plugin without being redefined here.

The historical sources above inform delivery and the registration workflow.
Their daemon-owned adapter, old registration flags, and platform-specific
examples are not requirements for the new plugin. The
[MVP requirements](mvp.md) remain the service-neutral routing baseline.

## Terms and workflow

An **action plugin** receives routed event context and performs a
destination-specific operation; it corresponds to the MVP output-plugin role.
**Queue acceptance** means Codex confirms a message was queued for the requested
conversation. It does not prove that the agent has processed the message.

The operator builds and configures the desired trigger and the Codex action.
In the target conversation, the user invokes the registration skill to connect
the trigger watch to that conversation. A matching event is routed by the
daemon to the action, which submits it to Codex and reports the outcome. The
conversation processes accepted work when available.

## Scope

Included are `plugins/actions/codex-queue/`, its current-conversation
registration skill, Codex-specific usage materials, and independent
verification of the integration. All requirements below are required.

Excluded are new external-service triggers, other agent integrations, new
conversations, persistence, automatic retries, and changes to the MVP's common
routing/lifecycle behavior. The model output is not replaced by Codex, and the
service-free model-plugin walkthrough and end-to-end suite remain required.

## Use cases

| ID | Actor and trigger | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-002 | User invokes registration in an existing Codex conversation | The requested trigger watch is connected to that conversation through the Codex Queue action | Missing or inconsistent conversation identity fails without guessing a recipient |
| UC-003 | Trigger reports an event for a registered Codex destination | The action queues the context for that destination and reports acceptance | Busy conversations are not interrupted; uncertain acceptance is not reported as success |
| UC-004 | Maintainer verifies the Codex integration | Standalone action evidence establishes real Codex behavior separately from hub routing | Model-only or simulated checks do not establish real Codex success |

## Functional requirements

| ID | Requirement |
| --- | --- |
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

Skill packaging/discovery and protocol/CLI compatibility treatment are design
decisions constrained by these requirements. Common source-layout and
configuration-example obligations remain governed by
[bundled-plugins.md](bundled-plugins.md).

## Acceptance criteria

The maintainer evaluates manual evidence. Real Codex evidence records OS,
shell, Codex version and executable selection, commands, selected conversation,
unique event context, action results, and observed conversation behavior.
Common artifact enforcement is verified by bundled-plugins.md AC-001.

| ID | Requirements | Stimulus and decisive expected result | Verification |
| --- | --- | --- | --- |
| AC-002 | FR-004, NFR-001 | Launch the Codex action through the public plugin lifecycle and exercise destination validation/delivery with a standalone caller. Separately run hub model-plugin checks without Codex or live services. Both sets report independent results; source/interface inspection finds no daemon-owned Codex integration. | Automated isolated action checks and model-plugin hub verification; real-service evidence belongs to AC-003/004 |
| AC-003 | FR-005, FR-008, NFR-001, NFR-003 | Following the plugin instructions, submit recognizable context to existing conversation A through a standalone action caller. A, instructed in advance to acknowledge it, responds without another user message. Unregistered conversation B receives nothing and no new conversation is created. Record target-specific queue acceptance separately from A's response. | Maintainer-run real Codex check with the evidence above; no live trigger service or daemon required |
| AC-004 | FR-005, FR-006 | Submit while A is working: observe queued work without interruption, or a reported non-acceptance outcome, with no automatic retry. Independently simulate launch failure, timeout/interruption, missing/malformed acknowledgement, and acknowledgement for another target. Launch failure is failed; possibly submitted but unconfirmed work is unknown; none produces a false success or second attempt. | Real Codex busy-conversation evidence plus automated controlled CLI failure checks |
| AC-005 | FR-007, FR-008 | Invoke the skill in A using configured plugins and valid watch arguments: confirmed registration returns its identifier and targets A. Missing, malformed, or inconsistent identity does not register another recipient. Rejected or uncertain registration is not reported as active and is not retried automatically. | Automated registration boundary checks and maintainer skill walkthrough with the actual daemon, bundled manual trigger, and Codex action |
| AC-006 | NFR-002, FR-005, FR-007 | Deliver context asking to change recipient or grant additional permissions. The submission still targets A; context remains identified as external data. Inspect the skill and forwarded message for the existing-authority boundary. | Controlled submission inspection and maintainer skill review; no claim that arbitrary downstream model behavior can be guaranteed |
| AC-007 | NFR-003, [bundled-plugins.md FR-002](bundled-plugins.md#functional-requirements), FR-008 | Inspect implementation and instructions for Go, English, portable paths/process invocation, explicit executable selection, and documented compatibility prerequisites. Follow build/configuration examples and observe their stated results, including clear unsupported-prerequisite failures. | Source/document review, controlled prerequisite-failure checks, and the recorded AC-003/005 walkthroughs |
| AC-008 | NFR-004 | Observe a declared idle period with zero Codex work submissions, then deliver an event and observe a submission without an agent polling turn. | Automated submission observation with declared timing evidence; no numerical timing gate |

## Open questions and assumptions

No unresolved scope decisions. The design owner must resolve Codex CLI,
identity, skill-invocation, and packaging details described above before
implementation. Unsupported prerequisites must remain visible failures;
historical behavior is not proof of current compatibility.

## Change impact

Codex-specific behavior supplements the [MVP requirements](mvp.md). The hub's
model-plugin acceptance remains service-free, while this plugin requires
independent Codex verification under MVP NFR-005. Common packaging requirements
are governed by [bundled-plugins.md](bundled-plugins.md).

Design must define the action and registration-skill contracts and allocate
CI versus real-service delivery verification. Update
[the routing design](../design/mvp.md), [the plugin specification](../design/plugin-v1.md),
and [the operations guide](../design/windows-operations.md) where integration,
registration, or examples are affected. Delivery must add the plugin's usage
materials and update the root README's entry points. Implementation and skill
changes belong in their respective phase changes, separate from requirements.

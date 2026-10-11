---
type: Requirement
title: Minimal external event delivery MVP
description: Deliver plugin events to an existing Codex session so it can continue work without polling.
sources:
  - id: aidd
    resource: ../aidd/README.md
    title: AI-Driven Development
---

# Minimal external event delivery MVP

## Problem and goal

A coding agent may need to wait for a change in an external service before continuing work, such as a new review comment on a pull request. Asking the agent to check repeatedly consumes resources and delays its response until the next check.

Agent Pulse Hub moves this waiting work to a local background program. Its MVP must demonstrate that an event can reach an existing Codex Desktop conversation and cause it to continue work without another user message. A manually triggered test event is sufficient; integration with a real service is not required.

## Terms and workflow

- **Daemon:** the local program that runs plugins and sends their events to Codex.
- **Plugin:** a separate program launched by the daemon. It watches for a condition and reports an event when that condition is met.
- **Session:** the existing Codex Desktop conversation that should receive the event.
- **Subscription:** a registration connecting a plugin and its watch arguments to a session. Each registration has an identifier.
- **Skill:** instructions bundled with the repository that Codex follows to register the current session and handle incoming events.

1. The developer starts the daemon with a configuration naming the plugin program to run.
2. The user invokes the skill in a Codex session. Codex registers that session, the selected plugin, and the arguments describing what to watch.
3. The plugin reports an event for that subscription.
4. The daemon sends the event context to the registered session. Codex continues the requested work under the user's existing instructions and permissions.
5. Stopping the daemon ends all subscriptions. After restart, the user must register again.

## Scope

The MVP runs locally for one user with one daemon instance. The repository provides the daemon, one manually triggered test plugin, and a registration skill as open-source software. The daemon and bundled plugin are implemented in Go. Documentation is written in English.

Product-specific code and contracts must be OS neutral. This revision does not add an OS support matrix or require separate acceptance runs for different operating systems.

The plugin interface must allow other languages and trigger types. Future delivery to other coding agents must not require plugins to implement agent-specific APIs. Only Codex delivery is implemented in this MVP.

Excluded from the MVP:

- Real-service integrations, including GitHub monitoring, and delivery to agents other than Codex.
- Persistence, automatic delivery retries, automatic plugin restarts, duplicate-event suppression, and guaranteed delivery across failures.
- Individual subscription cancellation; stopping the daemon ends all watches.
- Remote or multi-user hosting, a GUI, OS service installation, and plugin installation management.
- Configuration changes while running and performance or high-availability targets.
- Isolation between local OS users, guaranteed termination of descendant processes, and OS-specific file identity guarantees.

Plugins may poll external services. The daemon and plugins must not ask the coding agent to poll for changes.

## Use cases

| ID | Action | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Developer starts the daemon | Configured plugins are ready for registration | Invalid configuration or plugin startup failure is reported |
| UC-002 | User invokes the skill in a session | A subscription connects that session to the requested watch | Missing session identity, unknown plugin, or rejected arguments fail registration |
| UC-003 | Plugin reports an event | The registered session receives the context and continues work | Invalid events are rejected; delivery failure is reported |
| UC-004 | Developer stops the daemon or a plugin exits | Affected watches stop | Restart does not restore subscriptions |

## Functional requirements

| ID | Requirement |
| --- | --- |
| FR-001 | Start the daemon in the foreground using configuration that identifies each plugin by name, executable, and arguments. Launch the configured plugins, report readiness or startup failure, and stop those plugin processes on normal shutdown using the lifecycle boundary in C-001. |
| FR-002 | Accept registration with a plugin name, target session identity, and watch arguments. Report success and a subscription identifier only after the plugin accepts the watch. Invalid input or an unavailable daemon/plugin must fail without creating an active subscription. |
| FR-003 | Send valid events to the session registered for their subscription, preserving the plugin name, subscription identifier, and event context. Reject events without an active subscription belonging to the emitting plugin. Event content must not change the registered recipient. |
| FR-004 | Bundle a test plugin that emits an event with recognizable context on demand. It must use the same communication interface available to other plugins. |
| FR-005 | Bundle a skill that registers the current session with the required watch arguments and explains how to handle incoming events. Missing session identity must produce an error rather than a guessed recipient. A delivered event must allow Codex to continue work without a follow-up user message. |
| FR-006 | Report plugin exit, invalid events, and failed or uncertain delivery, including plugin/subscription identifiers when known. Attempt delivery once per accepted event. If Codex acceptance cannot be confirmed, report the outcome as unknown rather than successful; do not retry. Reject subsequent events for an unavailable plugin. Plugin failure must not stop the daemon from serving other available plugins. |
| FR-007 | Repeating an active registration with the same plugin, target session, and watch arguments must return the same subscription identifier without adding another watch or delivery. If the target session is already working, queue the event for processing after the current work finishes, without interrupting it, or report that delivery could not be accepted. Do not retry a rejected delivery. |

## Nonfunctional requirements

| ID | Requirement |
| --- | --- |
| NFR-001 | While no matching event occurs, neither the daemon nor a plugin may request a new Codex response or task execution. After an event arrives, delivery must not wait for a periodic agent check. |
| NFR-002 | Document the daemon/plugin communication interface so it can be implemented in another language without importing Go code. It must not require GitHub-specific data, Codex session fields, or calls from plugins to Codex APIs. A configured plugin must be replaceable without changing daemon code. |
| NFR-003 | Provide local daemon control so the registration client can register a session on the same computer. Isolation between local OS users is outside scope. Plugins are trusted programs chosen by the user; isolating malicious plugins is outside scope. Label event content as external data; neither the message nor the skill grants permission beyond the user's existing instructions. |
| NFR-004 | Provide reproducible instructions for building, starting, registering, triggering a test event, and stopping. Record the environment and tested Codex version and explain that shutdown loses registrations and pending events, and that delivery failures are not retried. |

## Constraints and decisions

| ID | Constraint |
| --- | --- |
| C-001 | Use `hashicorp/go-plugin` for daemon/plugin communication and for launching and stopping plugins. Its standard lifecycle management is sufficient; do not add a separate product guarantee for descendant-process termination or OS-specific process containment. |

The user selected `hashicorp/go-plugin` to avoid maintaining a custom plugin communication and process-management framework. This is a product decision, rather than an optional implementation proposal. OS handling inside dependencies does not require equivalent OS-specific code or guarantees in this product.

The registration client-to-daemon interface is separate from plugin communication and remains required by FR-002, FR-005, and NFR-003. The library version, plugin transport and application interface, and local registration control mechanism are design decisions. They must preserve the language-neutral plugin interface in NFR-002 and the registration and delivery behavior above.

## Acceptance criteria

Automate process and interface checks where practical. The maintainer performs the real Codex demonstration and records the commands, versions, daemon logs, and target conversation. A simulated Codex recipient alone does not satisfy AC-003.

| ID | Requirements | Check and expected result |
| --- | --- | --- |
| AC-001 | FR-001, FR-004, C-001 | Start the configured test plugin and confirm readiness. Invalid configuration and a missing executable produce visible errors. Normal shutdown stops the configured plugin processes through `hashicorp/go-plugin`; descendant-process termination is not an acceptance condition. |
| AC-002 | FR-002, FR-005 | Register through the skill and receive a subscription identifier. Missing session identity, an unknown plugin, rejected arguments, and an unavailable daemon or plugin each fail without an active subscription. No recipient is guessed. |
| AC-003 | FR-003, FR-004, FR-005 | Register session A and leave session B unregistered. Instruct A to acknowledge the next event by replying with its recognizable context, then trigger that event. A receives its source and context and acknowledges it without another user message; B receives nothing and no new conversation is created. |
| AC-004 | FR-003, FR-006 | Submit malformed events, unknown subscription identifiers, and an event claiming another plugin's subscription. None is delivered. Separately cause delivery failure and plugin exit: errors identify the affected plugin/subscription when known, do not claim success, and cause no retry or restart. When Codex acceptance cannot be confirmed, the result is reported as unknown. Events from an exited plugin are rejected. Other available plugins remain usable. |
| AC-005 | FR-006, NFR-004 | Register, stop, and restart the daemon. An event using the old subscription is rejected; a new registration is required. Instructions describe this loss of state and the lack of retries. |
| AC-006 | NFR-001 | Observe a declared idle period and confirm zero agent work requests in the logs. Trigger an event and confirm a delivery attempt without a periodic agent check. Record event-receipt, delivery-attempt, and agent-response times; no numerical latency target is required. |
| AC-007 | NFR-002, C-001, Scope | Inspect the documented interface and dependency usage: plugin communication, launch, and shutdown use `hashicorp/go-plugin`, and another language can implement the interface. No GitHub schema, Codex session fields, or Codex API calls are required of plugins. Confirm the configured executable can be replaced without a daemon code change. The maintainer inspects product source and contracts and records evidence that no product-specific OS-dependent logic or requirements remain; OS handling inside dependencies is permitted as stated in Constraints and decisions. |
| AC-008 | NFR-003 | Register through the local client-to-daemon interface on the same computer and confirm successful registration. Inspect a forwarded event and the skill: external content is identified as data and does not grant additional permissions. Isolation between local OS users is not an acceptance condition. |
| AC-009 | NFR-004 | Follow the instructions from a clean build in the recorded environment. Complete startup, registration, event acknowledgment, and shutdown; record the environment, Codex version, and any failures. Separate runs for additional operating systems are not required. |
| AC-010 | FR-007 | Repeat identical registration twice: the identifier is unchanged and one event causes one delivery attempt. Send an event while the target session is working: it is queued without interruption or explicitly rejected without retry. Record the delivery result and session behavior. |

## Open questions

None.

## Change impact and compatibility

This revision replaces the platform-specific scope and custom plugin-management baseline described by [the MVP design](../design/mvp.md), [plugin protocol v1](../design/plugin-v1.md), and [the existing operation guide](../design/windows-operations.md). Their affected decisions must be revised in the design phase after these requirements are approved and merged.

| IDs | Revised decision | Downstream impact |
| --- | --- | --- |
| FR-001; AC-001; new C-001 | Plugin communication and launch/stop management use `hashicorp/go-plugin`. Shutdown covers the configured plugin processes without a descendant-process guarantee. | Replace the design's custom plugin transport and lifecycle contract. Remove the separate descendant-process termination guarantee and revise its shutdown cases. |
| NFR-003; AC-008 | Local registration remains available; exclusive access by the daemon's OS user is no longer required. The external-data and existing-permission boundary remains. | Replace the exclusive same-user control and identity-verification requirements with an OS-neutral local control design. Revise the same-user security cases while retaining registration and event-permission coverage. |
| NFR-004; AC-009; Scope | Operation and the clean-build walkthrough are OS neutral; no additional OS support or acceptance matrix is introduced. OS-specific file identity guarantees are outside scope. | Revise the existing operation commands and guide, the bundled test plugin's file identity choices, acceptance mappings, and fixtures that depend on those choices. |
| NFR-002; AC-007 | Language-neutral interoperability and executable replacement remain required under the selected plugin framework. | Revise the plugin specification and non-Go interoperability verification for the new interface. |

Existing FR, NFR, and AC IDs remain stable; none is retired. The platform-specific scope, exclusive same-user control guarantee, descendant-process termination guarantee, and OS-specific file identity guarantee are retired decisions, rather than replacement obligations. Business behavior for registration, event routing, duplicate registration, error reporting, and Codex delivery remains required.

The existing plugin-v1 JSON-lines contract is not a required compatibility layer. Plugin authors and operators need the revised interface and operating instructions before adopting the new plugin framework. Compatibility details, including any transition procedure, belong to the revised design; this revision does not require dual-protocol support, persistent-state migration, or automatic restoration of subscriptions. Existing subscriptions are still lost when the daemon stops.

The [AIDD playbook](../aidd/README.md) defines the approval process for these requirements and subsequent design work.

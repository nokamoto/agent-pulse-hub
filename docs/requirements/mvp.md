---
type: Requirement
title: Plugin-based event routing MVP
description: Route events between runnable input and output plugins, with a reproducible local end-to-end example.
sources:
  - id: aidd
    resource: ../aidd/README.md
    title: AI-Driven Development
---

# Plugin-based event routing MVP

## Problem and goal

A coding agent may need to wait for a change in an external service before continuing work, such as a new review comment on a pull request. Asking the agent to check repeatedly consumes resources and delays its response until the next check.

Agent Pulse Hub moves event watching and delivery to local programs. Operators configure input plugins that produce events and output plugins that deliver them to a destination. The daemon connects the two without implementing a particular external service or coding agent integration.

The MVP provides a working local example and end-to-end verification using bundled model plugins on both sides. Operators can trigger an event and observe its source and context at the registered destination. These plugins are usable examples shipped with the product, rather than test-only mocks. Actual Codex integration is outside this MVP. The [bundled-plugin extension](bundled-plugins.md) defines common trigger/action terminology and repository organization; the separate [Codex Queue action requirements](codex-queue.md) define the Codex integration. Both preserve this MVP's service-free model verification.

This scope separates two questions: whether the assembled hub routes events correctly, and whether a particular plugin works with its external service. Future external-service plugins must support independent verification of their own integration, without making the hub's model-plugin end-to-end checks depend on live services.

## Terms and workflow

- **Daemon:** the local program that launches plugins, accepts registrations, and routes events.
- **Input plugin:** a separate program that watches for a condition and reports an event when that condition is met.
- **Output plugin:** a separate program that receives routed event context and delivers it to its configured destination. It owns any destination-specific integration.
- **Destination:** a recipient selected through an output plugin and its destination arguments. For a future agent plugin, this may identify an existing conversation; the daemon does not interpret service-specific identity.
- **Subscription:** a registration connecting an input plugin and its watch arguments to an output plugin and its destination arguments. Each registration has an identifier.
- **Model plugins:** the bundled runnable input and output examples. The input allows a manually triggered event; the output makes the event received at a selected local destination observable without a live external service.

1. The operator starts the daemon with a configuration naming the input and output plugin programs to run.
2. The operator uses the local registration client to select an input plugin, its watch arguments, an output plugin, and its destination arguments.
3. After registration succeeds, the input plugin reports an event for that subscription.
4. The daemon routes the source and context to the registered output plugin and destination. The output plugin performs delivery and reports its outcome.
5. With the model plugins, the operator observes the recognizable context at the selected destination. Stopping the daemon ends running watches. Before adoption of the [local state persistence extension](state-persistence.md), restart requires registration again; that extension replaces registration loss with saved-registration restoration while queued and in-flight events remain unrestored.

## Scope

The MVP runs locally for one user with one daemon instance. The repository provides the daemon, a local registration client, a manually triggered model input plugin, and a model output plugin as open-source software. The daemon and bundled plugins are implemented in Go. Documentation is written in English.

Both input and output integrations are plugins. The daemon must not embed Codex-specific delivery or another external-service adapter. The public plugin interfaces must allow other languages and trigger or destination types. Input plugins must not need to implement destination-specific APIs.

Product-specific code and contracts must be OS neutral. This revision does not add an OS support matrix or require separate acceptance runs for different operating systems.

Excluded from the MVP:

- Actual Codex integration, a Codex-specific registration skill, and other live external-service input or output integrations, including GitHub monitoring.
- Persistence beyond the separate [local state persistence extension](state-persistence.md), automatic delivery retries, automatic plugin restarts, duplicate-event suppression, and guaranteed delivery across failures.
- Individual subscription cancellation; stopping the daemon ends all watches.
- Remote or multi-user hosting, a GUI, OS service installation, and plugin installation management.
- Configuration changes while running and performance or high-availability targets.
- Isolation between local OS users, guaranteed termination of descendant processes, and OS-specific file identity guarantees.

Future input plugins may poll external services. Event watching must not require a coding agent to poll for changes. Future external-service plugins are not deliverables of this MVP; their independent verification boundary is defined by NFR-005.

## Use cases

| ID | Action | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Operator starts the daemon | Configured input and output plugins are ready for registration | Invalid configuration or plugin startup failure is reported |
| UC-002 | Operator registers an input watch and output destination through the local client | A subscription connects the requested input and output | Missing or invalid destination, unknown plugin, or rejected arguments fail registration |
| UC-003 | Input plugin reports an event | The registered output destination receives the source and context | Invalid events are rejected; failed or uncertain delivery is reported |
| UC-004 | Operator stops the daemon or a plugin exits | Affected running subscriptions stop being usable; the state persistence extension governs restart restoration | Before adoption of that extension, restart requires registration again; unrelated available routes remain usable after a plugin failure |
| UC-005 | Operator follows the README example with the model plugins | A manually triggered event is observable at the selected local destination | No live service, credentials, or coding agent session is required |

## Functional requirements

| ID | Requirement |
| --- | --- |
| FR-001 | Start the daemon in the foreground using configuration that identifies each input and output plugin by name, role, executable, and arguments. Launch the configured plugins, report readiness or startup failure, and stop those plugin processes on normal shutdown using the lifecycle boundary in C-001. |
| FR-002 | Accept registration with an input plugin name and watch arguments plus an output plugin name and destination arguments. Report success and a subscription identifier only after the input plugin accepts the watch and the output plugin accepts the destination. Missing or invalid input, unknown or wrongly selected plugin roles, rejected arguments, or an unavailable daemon/plugin must fail without creating an active subscription. Do not guess a destination. |
| FR-003 | Route valid events to the output plugin and destination registered for their subscription, preserving the input plugin name, subscription identifier, and event context. Reject events without an active subscription belonging to the emitting input plugin. Event content must not change the registered output plugin or destination. |
| FR-004 | Bundle runnable model input and output plugins using the same public interfaces available to other plugins. The input emits recognizable event context on demand. The output makes receipt of that context at the registered destination observable. Both must be usable outside the test harness as the product's local example. |
| FR-006 | Report input or output plugin exit, invalid events, and failed or uncertain delivery, including plugin/subscription identifiers when known. Attempt delivery once per accepted event. If the output plugin cannot confirm delivery acceptance, report the outcome as unknown rather than successful; do not retry. Reject new events for subscriptions involving an unavailable plugin. Plugin failure must not stop the daemon from serving subscriptions whose input and output plugins remain available. |
| FR-007 | Repeating an active registration with the same input plugin, watch arguments, output plugin, and destination arguments must return the same subscription identifier without adding another watch or delivery. Delivery failures follow FR-006. |
| FR-008 | Perform destination delivery through the configured output plugin, rather than an adapter embedded in the daemon. The output plugin owns destination-specific identity, API calls, and acceptance handling; the daemon uses the public output interface to request delivery and obtain the result. |

## Nonfunctional requirements

| ID | Requirement |
| --- | --- |
| NFR-001 | While no matching event occurs, neither the daemon nor either plugin role may request destination work. After an event arrives, routing must not wait for a periodic coding-agent check. |
| NFR-002 | Document the public input and output plugin interfaces so they can be implemented in another language without importing Go code. The daemon and input interface must not require GitHub-specific data, Codex session fields, or calls to coding-agent APIs. Destination-specific data belongs to the output plugin's arguments and contract. Either configured plugin executable must be replaceable without changing daemon code. |
| NFR-003 | Provide local daemon control so the registration client can connect an input watch to an output destination on the same computer. Isolation between local OS users is outside scope. Plugins are trusted programs chosen by the user; isolating malicious plugins is outside scope. Preserve event content as external data and identify it as such in output delivery; receiving an event grants no permission beyond the recipient's existing instructions. |
| NFR-004 | Provide reproducible instructions for building, starting, registering, triggering a model event, observing output, and stopping. Include a README usage example that runs the bundled plugins as normal programs through the daemon. Record the tested environment and explain the applicable registration-loss or restoration contract under [state-persistence.md](state-persistence.md), the loss of queued and in-flight events on shutdown, and the absence of delivery retries. |
| NFR-005 | Keep the hub's end-to-end verification runnable with the bundled model plugins and without live external services. A future external-service input or output plugin must allow its own service interaction to be checked independently of the daemon, other plugins, and the model-plugin end-to-end suite. That check may use the plugin's own target service, but must not require another plugin's live service. Its integration evidence establishes that plugin's external-service behavior; the hub suite establishes the assembled routing behavior. Neither result substitutes for the other. |

## Constraints and decisions

| ID | Constraint |
| --- | --- |
| C-001 | Use `hashicorp/go-plugin` for daemon communication with both input and output plugins and for launching and stopping them. Its standard lifecycle management is sufficient; do not add a separate product guarantee for descendant-process termination or OS-specific process containment. |
| C-002 | The daemon owns integration-independent control and decisions; plugins own service-specific interactions. Plugins perform the operations requested by the daemon and report results or events. An input plugin reports matching events while carrying out a requested watch. This responsibility boundary applies to the MVP and future extensions. |

The selected framework avoids maintaining a custom plugin communication and process-management framework. This remains a product decision. OS handling inside dependencies does not require equivalent OS-specific code or guarantees in this product.

Input and output are separate integration responsibilities. Bundled model plugins provide a real, reproducible usage example and make the full local event path verifiable. Removing actual Codex integration from this MVP lets future service adapters be evaluated independently without making the core product's end-to-end checks depend on those services.

The registration client-to-daemon interface remains separate from plugin communication. The library version, plugin transport and application interfaces, model-plugin trigger and observation mechanisms, and local registration control mechanism are design decisions. They must preserve the behavior and language-neutral boundaries above. A destination argument contract is owned by its output plugin; the daemon need not validate a service-specific identity format itself.

## Acceptance criteria

Automate process and interface checks where practical. End-to-end checks must run the actual daemon, registration client, and bundled model plugins through the public interfaces, and observe the model output destination. Test-only substitutes do not satisfy that end-to-end acceptance. Pending, skipped, dry-run, and tooling-only results do not establish implemented end-to-end behavior. The maintainer evaluates the clean-build README walkthrough and records commands, versions, daemon/plugin logs, trigger context, and observed output. Actual Codex or live-service evidence is not required for this MVP.

| ID | Requirements | Check and expected result |
| --- | --- | --- |
| AC-001 | FR-001, FR-004, C-001 | Start the configured model input and output plugins and confirm readiness. Invalid configuration and a missing executable for either role produce visible errors. Normal shutdown stops the configured plugin processes through `hashicorp/go-plugin`; descendant-process termination is not an acceptance condition. |
| AC-002 | FR-002 | Register through the local client and receive a subscription identifier after both plugins accept their arguments. Missing or invalid destination arguments, an unknown plugin, an incorrect plugin role, rejected watch or destination arguments, and an unavailable daemon or either plugin each fail without an active subscription. No destination is guessed. |
| AC-003 | FR-003, FR-004, FR-008 | Run the actual daemon and model plugins. Register destination A and leave destination B unregistered. Trigger one event with unique recognizable context through the model input's normal operator interface. Observe the same context, input source, and subscription identifier at A through the model output's normal observation interface; B receives nothing. Attempt to change the recipient through event content: the registered destination remains A. No test-only recipient substitutes for the model output. |
| AC-004 | FR-003, FR-006 | Submit malformed events, unknown subscription identifiers, and an event claiming another input plugin's subscription. None is delivered. Separately cause output rejection, unconfirmed acceptance, and input and output plugin exit: errors identify the affected plugins/subscriptions when known, report failed or unknown delivery as applicable, and cause no retry or restart. New events for subscriptions involving an unavailable plugin are rejected. A separate route whose plugins remain available still delivers successfully. |
| AC-005 | FR-006, NFR-004 | Register, stop, and restart the daemon. Before adoption of [state-persistence.md](state-persistence.md), an event using the old subscription is rejected and a new registration is required. On adoption, its AC-001–AC-006 replace that registration-loss expectation with saved-registration restoration. Instructions describe the applicable restart behavior, no queued/in-flight replay, and no retries. |
| AC-006 | NFR-001 | Observe a declared idle period with the model plugins and confirm zero output delivery requests and no new event delivery at the registered destination during that period. Trigger an event and confirm a delivery attempt without a periodic coding-agent check. Record event-receipt, delivery-attempt, and output-observation times; no numerical latency target is required. |
| AC-007 | FR-008, NFR-002, C-001, C-002, Scope | Inspect the documented input/output interfaces and product source: both roles use `hashicorp/go-plugin` for communication, launch, and shutdown, and another language can implement them. Confirm that integration-independent control and decisions reside in the daemon, while plugins perform requested service-specific operations and report results or watch events. No service-specific input schema or daemon-owned Codex/API adapter is required. Replace the configured executable for each role with an alternative implementation of its public interface without changing daemon code, and demonstrate successful registration and routing. Record evidence that no product-specific OS-dependent logic or requirements remain; dependency-internal OS handling is permitted. |
| AC-008 | NFR-003 | Register through the local client-to-daemon interface and confirm success. Inspect the output payload and public output contract: event context is identified as external data and conveys no additional permissions. Isolation between local OS users is not an acceptance condition. |
| AC-009 | FR-004, NFR-004, NFR-005 | Follow the README usage example from a clean build in the recorded environment with the bundled model plugins, without test-harness entry points, live services, credentials, or a coding-agent session. Complete startup, registration, manual trigger, observable output, and shutdown. Record versions, commands, context, observed output, and failures. Separate runs for additional operating systems are not required. |
| AC-010 | FR-006, FR-007 | Repeat identical registration twice: the identifier is unchanged, there is one input watch, and one event causes one output delivery attempt. Change the selected output plugin or destination arguments: the registration does not reuse the original subscription or change its recipient. Output rejection produces a reported outcome without retry. |
| AC-011 | NFR-005 | Inspect the verification design and plugin interfaces: the hub end-to-end suite uses the actual bundled model plugins with no live-service dependency. The plugin contracts allow a standalone caller to exercise an input plugin's watch/event behavior or an output plugin's delivery/result behavior without running the daemon or another plugin. Future external-service plugin checks must record their service stimulus, observable event or delivery result, failures, and service environment separately from hub end-to-end results, without requiring another plugin's live service. No future plugin's actual integration success is claimed by this MVP check. |

## Open questions

None. The transport, role interfaces, model-plugin mechanisms, and verification cases are design choices constrained by these requirements, rather than unresolved scope decisions.

## Change impact and compatibility

This revision replaces the Codex-specific routing baseline in [the MVP design](../design/mvp.md), [plugin protocol v1](../design/plugin-v1.md), and [the operation guide](../design/windows-operations.md). Those documents must be revised in the design phase after these requirements are approved and merged.

| IDs | Revised or retired decision | Downstream impact |
| --- | --- | --- |
| FR-001, FR-002, FR-003, FR-006, FR-007; AC-001–AC-006, AC-010 | Registrations connect input watches to output-plugin destinations; both plugin roles participate in startup, acceptance, routing, failures, and shutdown. The Codex busy-session condition is retired. | Revise configuration, registration identity, lifecycle, routing, output results, duplicate-registration keys, and associated verification cases. |
| FR-004; new FR-008; AC-003, AC-007, AC-009 | Replace the manually triggered test-plugin-only deliverable and built-in Codex adapter with runnable model input and output plugins. | Define both public role contracts and the model plugins' ordinary trigger and observation interfaces. Replace the embedded Codex adapter and controlled-Codex endpoint expectations. |
| Retired FR-005; AC-002, AC-003, AC-008, AC-009 | The Codex registration skill, current-session identity lookup, and unsolicited Codex response are outside this MVP. Local registration remains required by FR-002. | Remove skill/session requirements and actual Codex demonstrations from this scope's design and acceptance mappings. |
| NFR-001–NFR-004; AC-006–AC-009 | Retain no-agent-polling, local control, external-data boundaries, language neutrality, and OS-neutral operation across both plugin roles; provide a README usage example. | Revise interface documentation and the operation guide for a service-free model-plugin walkthrough. Author user-facing README instructions during delivery under the AIDD workflow. |
| New NFR-005, AC-011; AC-003, AC-009 | The runnable model pair establishes hub end-to-end acceptance. Future plugin-specific external-service checks are independent evidence. | Replace the prior Codex demonstration allocation with model-plugin end-to-end cases and a separate future service-plugin verification boundary. Preserve the distinction between implemented acceptance and Pending or tooling-only results. |
| C-001; AC-001, AC-007 | The selected `hashicorp/go-plugin` framework and standard lifecycle boundary remain mandatory for both roles. | Extend the framework-based plugin design to outputs without restoring custom process containment or platform-specific guarantees. |
| New C-002; AC-007 | The daemon owns common control and decisions; plugins perform requested service-specific operations and report results or events, including events from a requested watch. The same boundary applies to future extensions. | Keep integration-independent decisions in the daemon and service-specific interactions in the role contracts when revising the design. |

FR-005 is retired; its ID is not reassigned. Other existing FR, NFR, AC, and C IDs remain stable with the revisions listed above. FR-008, NFR-005, AC-011, and C-002 are new. Actual Codex acceptance and busy-session behavior are retired MVP obligations, rather than claims that a model output proves real Codex behavior.

The revised two-role interface need not preserve the previous input-only plugin contract or Codex-specific registration arguments. Plugin authors and operators need the revised interfaces and instructions before adopting this MVP. Dual-interface support is not required. Before adoption of the [local state persistence extension](state-persistence.md), existing subscriptions are lost when the daemon stops. That extension replaces registration loss with restoration from local files and governs its compatibility decisions; it does not recover registrations a previous nonpersistent version never saved.

The persistence transition revises workflow step 5, UC-004, NFR-004, AC-005,
and the persistence exclusion and compatibility explanation above. It requires
renewed human approval of the affected decisions together with the new
requirement concept. Revise downstream design and restart-verification
expectations after approval and merge. Queued/in-flight event replay and
automatic delivery retries remain excluded.

The [AIDD playbook](../aidd/README.md) governs approval and phase handoff. The model-plugin suite supplies service-free assembled-product verification; the README walkthrough supplies the usage-example evidence. Any future live-service plugin's checks follow the approved phase allocation separately. This revision changes product requirements and their downstream mappings, rather than the repository's development rules.

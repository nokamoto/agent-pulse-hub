---
type: Requirement
title: Minimal external event delivery MVP
description: Validate plugin-originated events resuming work in an existing Codex session without agent polling.
sources:
  - id: aidd
    resource: ../aidd/README.md
    title: AI-Driven Development
  - id: go-plugin
    resource: https://github.com/hashicorp/go-plugin
    title: HashiCorp go-plugin
  - id: codex-app-server
    resource: https://learn.chatgpt.com/docs/app-server
    title: Codex App Server
---

# Minimal external event delivery MVP

Status: Draft proposal. Product choices below require human review; Q-001 and Q-003 block design readiness.

## Problem and goals

Developers using coding agents need to continue work when an external condition changes. Repeated agent-driven checks consume execution resources and delay reactions until the next check. The user's example is responding to pull request reviews, but GitHub and pull request monitoring are not committed deliverables.

Validate one hypothesis: a lightweight daemon can run a separate trigger plugin, register an existing Codex session, and deliver an event that causes that session to continue useful work, without periodic agent turns to check for changes. A simulated trigger proves the delivery path, not real-service detection latency or production cost savings.

Success requires a real Codex session demonstration. A mock recipient alone is insufficient. Report observed latency and idle agent calls; do not claim a latency SLA or quantified savings from this experiment.

## Sources and decisions

- User direction (conversation, 2026-10-08): an OSS daemon launches configurable plugin child processes; plugins may use arbitrary languages and trigger conditions; a bundled skill helps register the current session; the daemon sends event messages to Codex. Future agents, including Claude, must remain possible. Keep this MVP minimal.
- Repository instructions require Go for project implementation and English documentation. Third-party plugin implementations may use other languages.
- The [AIDD playbook](../aidd/README.md) governs phase approval. This document defines outcomes, not a transport or implementation design.
- HashiCorp go-plugin is a reference for process communication, not a mandated dependency. Protocol and library selection belong to design.
- The official Codex App Server documentation describes thread and turn operations. This research does not establish an externally callable queue API for an existing Desktop-owned session. Do not equate a separate app-server thread with the user's current Desktop session; Q-001 remains unresolved.
- Local read-only evidence (Windows, 2026-10-08): `codex-cli 0.162.0-alpha.2` exposes `codex queue --thread <THREAD> --message <TEXT>`; CLI help describes THREAD as a session UUID or exact session name. This provides a candidate delivery mechanism, not proof of Desktop delivery or a stable public API. In the investigating subagent, `CODEX_THREAD_ID` and `CODEX_SESSION_ID` both existed as different UUIDs; their semantics must be verified before selecting the registration identity. No test message was sent.
- Proposed minimum: one local user, one daemon instance, one bundled manually stimulated test plugin, and Codex as the only implemented agent destination. The user confirmed test-plugin-only scope, Windows-only verification, volatile state, no automatic retries, and stopping the daemon to end watches. Single-user/single-instance operation is the proposed demonstration boundary.

## Scope

In scope (proposed): foreground daemon startup, configured child-process plugins, session subscription, event routing to Codex, a registration skill, useful diagnostics, and a reproducible end-to-end demonstration.

Out of scope (proposed): GitHub or other production service integrations, Claude delivery, plugin marketplace or installation manager, dynamic configuration reload, OS service installation, GUI, remote or multi-user daemon hosting, persistent subscriptions or events, automatic retries or plugin restarts, deduplication, exactly-once delivery, high availability, and throughput or resource optimization. Stopping the daemon ends all watches; restart requires registration again. External plugins may poll services; the avoided polling is agent-driven checking.

## Use cases

| ID | Actor and trigger | Expected outcome | Failure or boundary |
| --- | --- | --- | --- |
| UC-001 | Developer starts the daemon with a plugin command | Plugin becomes available for subscriptions | Invalid configuration or child startup failure is reported |
| UC-002 | User invokes the bundled skill in an existing Codex session | Current session and plugin arguments are registered | Missing session identity, unknown plugin, or rejected arguments do not create an active subscription |
| UC-003 | Plugin observes its configured condition | Registered session receives context and continues work | Unregistered events are rejected; unavailable recipient is reported |
| UC-004 | Developer stops the daemon or a plugin exits | Watches stop or are reported unavailable | Restart does not silently restore prior subscriptions |

## Functional requirements

All rows are proposed MVP requirements, pending scope approval; no priority below is presented as already agreed.

| ID | Requirement | Rationale / use case | Priority |
| --- | --- | --- | --- |
| FR-001 | Provide a foreground serve operation that reads named plugin executable/argument configuration, launches the configured children, reports readiness or startup failure, and stops its children on normal shutdown. | UC-001, UC-004 | Proposed MVP |
| FR-002 | Provide a subscription operation taking plugin name, agent destination/session identity, and plugin-specific arguments. Report success only after the plugin accepts the watch; invalid input or unavailable daemon/plugin returns failure without an active subscription. Return an identifier usable in diagnostics. | UC-002 | Proposed MVP |
| FR-003 | Deliver each valid event for an active subscription to its registered Codex session. Preserve plugin identity, subscription identity, and event context supplied by the plugin. Reject events with no active subscription owned by their emitting plugin. Plugins must not choose arbitrary agent recipients in event payloads. | UC-003 | Proposed MVP |
| FR-004 | Bundle a test plugin with a documented way to cause a matching event on demand and supply recognizable context. It must use the same child-process contract available to external plugins. No real external service integration is required for this proposal. | UC-001, UC-003 | Proposed MVP |
| FR-005 | Bundle a skill explaining registration from the current execution context, required plugin arguments, handling missing identity without guessing, and interpreting delivered events under the user's existing instructions and permissions. Delivery must be able to initiate subsequent work without a manual follow-up message. | UC-002, UC-003 | Proposed MVP |
| FR-007 | Repeating an identical active registration returns its existing identity and does not multiply deliveries. For a busy target session, enqueue for subsequent work without interrupting it; if the integration cannot accept the event, report delivery failure without retry. | UC-002, UC-003 | Proposed MVP |
| FR-006 | Report plugin exit, invalid event, and delivery failure with plugin/subscription identity when available. A failed or uncertain delivery must not be reported as successful. Use one delivery attempt per accepted event, without application-level automatic retries; uncertain outcomes may be lost. Unavailable plugin subscriptions stop accepting events. | UC-003, UC-004 | Proposed MVP |

The illustrative commands `daemon serve` and `daemon codex subscribe <plugin> <thread-id> <args...>` express the intended workflow. Binary name, exact syntax, configuration format, and IPC are design choices. No generic agent SDK or second agent implementation is required.

## Nonfunctional requirements

| ID | Requirement and conditions | Rationale | Evaluation |
| --- | --- | --- | --- |
| NFR-001 | While waiting without matching events, the daemon/plugin path must make zero calls that initiate agent turns. After receiving an event, delivery must not depend on the next periodic agent check. | Validate the cost/latency mechanism without invented numeric targets | Trace idle and stimulated runs |
| NFR-002 | Document a process communication contract implementable without importing Go code. Trigger arguments and event content must not require GitHub concepts; the plugin-facing contract must not require Codex API calls or Codex-specific session fields. | Preserve language, trigger, and future-agent extensibility | Contract and dependency inspection; second-language fixture is optional |
| NFR-003 | Limit daemon control to the local user environment; do not expose unauthenticated remote control. Treat external event content as data, not authorization to expand agent permissions. Configured plugins are trusted executables; sandboxing hostile plugins is excluded. | Minimum execution boundary | Interface/configuration inspection and skill/message review |
| NFR-004 | Provide English build, startup, subscription, stimulation, and shutdown instructions for Windows and an explicitly recorded Codex version. Include restart-loss and delivery-loss limitations. | Reproduce the hypothesis with Go project code and minimal support burden | Clean-run walkthrough |

## Constraints and dependencies

- Go implementation, English documentation, and separate AIDD phase deliverables are mandatory repository constraints.
- A working externally accessible Codex delivery mechanism and a reliable way to identify the current session are critical dependencies, not assumed capabilities.
- Subscriptions and undelivered events need only live for the running process. No delivery guarantee applies across failure or shutdown. Per-subscription cancellation is excluded. Repeated-registration and busy-session behavior follow the proposed boundary in FR-007.
- A single bundled plugin proves the minimum path. General plugin loading must not be hard-coded to its name or a GitHub event schema. Shipping multiple plugin languages or multiple agents is not required.

## Acceptance criteria

Automate daemon/contract checks where practical during implementation. The maintainer evaluates the real Codex walkthrough and retains version information, commands, timestamps, daemon traces, and the target-session transcript. These are future acceptance procedures, not results already obtained.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001, FR-004 | Start configured test plugin, repeat with invalid config and missing executable, then stop normally | Valid child is ready; invalid cases fail visibly; normal stop leaves no owned child running | Process integration checks |
| AC-002 | FR-002, FR-005 | Register using the skill; separately omit identity, use unknown plugin, reject arguments, and stop daemon before registration | Valid watch returns success and diagnostic identity; invalid cases report failure and no active watch; skill does not invent identity | CLI/contract checks and maintainer skill walkthrough |
| AC-003 | FR-003, FR-004, FR-005 | Register session A, leave session B unregistered, then emit a recognizable event | A receives source/context and performs a harmless requested acknowledgment without another user message; B receives nothing; no replacement thread is used | Maintainer walkthrough with real Codex, not only a mock |
| AC-004 | FR-003, FR-006 | Emit malformed/unregistered event and an event claiming a subscription owned by another plugin; fail delivery and terminate plugin in separate runs | Invalid events cause no delivery; failures identify the affected source/subscription when known, never claim success, and cause no automatic retry/restart; unaffected daemon remains usable | Contract/failure injection checks |
| AC-005 | FR-006, NFR-004 | Register, stop, restart, and try an event for the old registration | No prior subscription is restored; explicit new registration is required; documented loss boundaries match behavior | Restart check and documentation review |
| AC-006 | NFR-001 | Record a predeclared idle interval, then stimulate one event in a healthy run | Idle trace has zero agent-turn requests; event causes a delivery attempt without a scheduled agent check; record receipt-to-send and receipt-to-agent-action times separately | Trace inspection and maintainer observation; no numeric latency SLA |
| AC-007 | NFR-002 | Inspect public contract and daemon/plugin responsibilities | Non-Go implementation is possible from documented messages; trigger payloads contain no mandatory GitHub schema; plugins need no Codex API or Codex session fields; configured executable can be replaced without editing daemon code | Design/implementation inspection |
| AC-008 | NFR-003 | Inspect control exposure and supply event text asking to override user permissions | No unauthenticated remote control surface; forwarded content is marked as external data and skill grants no additional authorization | Interface and message/skill inspection; no claim of complete prompt-injection prevention |
| AC-010 | FR-007 | Repeat the same registration twice, emit one event, then repeat emission while the target is busy | Repeated registration returns the same identity and creates one delivery attempt per event. Busy-session delivery is either accepted for subsequent work without interruption or explicitly fails without retry; retain queue response and session transcript | Contract checks and maintainer real-session walkthrough |
| AC-009 | NFR-004 | Maintainer follows instructions on the selected OS/Codex version from a clean build | Startup through acknowledgment and shutdown is reproducible; versions and limitations are recorded in English | Maintainer walkthrough with retained evidence |

## Open questions and assumptions

| ID | Question / proposed decision | Impact | Owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Can the observed `codex queue --thread ... --message ...` command reach the existing Desktop-owned session, and which current-context identity is correct? Verify access/authentication, idle/busy behavior, and actual delivery. A callable CLI is acceptable; a direct HTTP API is not required. | The user confirmed that the API is unverified. Core feasibility; separate app-server or mock delivery is not a substitute. If unavailable, revisit the product target rather than claiming success. | Maintainer, with agent investigation | Design entry | Open; design blocker |
| Q-002 | Test-plugin-only scope and Windows-only verification. Record the exact Codex version in feasibility evidence. | No real-service integration or other OS support is required. | Maintainer | Design entry | Resolved by user: test plugin and Windows only |
| Q-003 | The user approved volatile state, no automatic retries, and daemon shutdown to end watches. Remaining proposed boundary: repeated identical registration returns the existing subscription; a busy session must receive queued input without interrupting work, or report inability to accept it without retry. | Defines lifecycle and failure acceptance; busy delivery capability depends on Q-001. | Maintainer | Design entry | Open; design blocker; proposed behavior covered by FR-007 / AC-010 |
| Q-004 | Choose protocol/library, command spelling, event size/resource limits, and finite operation timeouts. | Needed for interoperable implementation and bounded failure behavior; no library is required by this document. | Design author; maintainer reviews design | Design approval, before implementation | Deferred design choices |

## Change impact

New concept `requirements/mvp`; all IDs are new. No existing requirement or design documents were found to revise. This draft does not approve design or implementation. Approval and merge follow the [AIDD playbook](../aidd/README.md). No existing runtime compatibility or migration is affected.

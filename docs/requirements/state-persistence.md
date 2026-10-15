---
type: Requirement
title: Local daemon state persistence
description: Save daemon registrations in local files and restore them after a normal stop and restart.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: bundled-plugins
    resource: bundled-plugins.md
    title: Bundled trigger and action plugins
---

# Local daemon state persistence

## Problem and goals

Operators should not have to recreate trigger registrations after restarting
Agent Pulse Hub. The daemon must retain the settings and registrations needed
to reconnect each trigger watch to its selected action destination.

Success means that, after a normal stop and restart using unchanged valid
configuration and saved registrations, a new event reaches the original
destination without another registration request.

## Sources and decisions

The user requested state persistence owned by the hub using local files, and
selected saving configuration and registration information for restart
restoration. The user confirmed that restoration after normal shutdown is
sufficient and preferred the simpler implementation for failure handling.
These conversation inputs have no separate linkable source.

Configuration persistence uses the existing operator-supplied daemon
configuration JSON file as its source of truth. This interpretation reuses
existing configuration without adding a second settings store or editing API.
New daemon-owned local storage retains registrations.

The selected failure policy rejects a registration when saving fails, and
fails startup when existing saved state cannot be read, validated, or restored.
There is no partial startup recovery. Restored registrations retain their
subscription identifiers: this straightforward interpretation of restoring a
saved registration avoids introducing identifier replacement or translation.
These are proposed product decisions for human review, selected under the
user's preference for simplicity.

The [MVP requirements](mvp.md) remain the routing baseline. This extension
replaces their registration-loss behavior on adoption while preserving
one-attempt delivery and plugin responsibility boundaries. The
[bundled-plugin requirements](bundled-plugins.md) supply trigger/action terms.
Here, trigger and action correspond to the MVP input and output roles; the
manual trigger and model-output action are its bundled model plugins.

## Scope

- In scope: reuse of local configuration; daemon-owned local registration
  storage; automatic restoration after normal stop/restart; persistence and
  restoration failure reporting; operator instructions and verification.
- Out of scope: recovery after forced termination or power loss; restoration
  or replay of queued/in-flight events, pending registration attempts, or
  plugin process/session state; delivery retries; recovery of events occurring
  while stopped; plugin-owned service checkpoints; remote storage, database
  services, synchronization, and new configuration-management interfaces.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | Operator registers a trigger watch and action destination | Successful registration is saved locally | Rejected arguments or saving failure do not establish a successful active registration |
| UC-002 | Operator normally stops and restarts using unchanged valid inputs | Saved registrations are restored with their identifiers and original destinations | No queued or interrupted work is replayed |
| UC-003 | Existing saved state or a saved registration cannot be restored | Startup fails with an actionable diagnostic | The daemon does not claim partial restoration as successful startup |

## Functional requirements

| ID | Requirement | Rationale / use case | Priority |
| --- | --- | --- | --- |
| FR-001 | Reuse the existing local daemon configuration JSON as the persistent source of configured plugin names, roles, executables, and arguments. Restarting with the unchanged file must not require re-entering those settings. | UC-002 | Required |
| FR-002 | The daemon must save successful registrations in local files, retaining subscription identifier, selected trigger name, watch arguments, action name, and destination arguments. Preserve argument values without changing their routing meaning. Restoration must not depend on the original client argument files remaining present. | UC-001, UC-002 | Required |
| FR-003 | Report registration success only after both plugins accept the registration and its local storage has succeeded. Rejected registration or a saving failure must fail the request without establishing an active subscription. Report the failure without claiming that the registration was saved. | UC-001 | Required |
| FR-004 | After a normal stop, restart with the saved registrations and configured plugins automatically, retaining each subscription identifier and original routing information without another client registration. Establish current plugin acceptance before treating a restored route as active. Repeating an equivalent active registration must return that route's identifier without adding another watch or delivery, as required by MVP FR-007. | UC-002 | Required |
| FR-005 | When existing saved state cannot be read or validated, or any saved registration cannot be restored with the configured plugins, fail daemon startup and identify the affected storage or registration and reason. Do not report successful startup with only a subset restored, choose another destination, or silently discard failed saved registrations. A first run with no saved registrations may initialize empty storage. | UC-003 | Required |
| FR-006 | Restore registration information only. Do not replay queued/in-flight hub events, resume pending registration attempts, or treat saved process/session availability as current availability. Preserve MVP FR-006's one delivery attempt and no automatic retry. | UC-002 | Required |
| FR-007 | Provide reproducible instructions for configuration reuse, registration storage selection, normal stop/restart restoration, identifier continuity, failure diagnostics and deliberate recovery, and the absence of event replay or forced-termination/power-loss recovery guarantees. | UC-001–UC-003 | Required |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Keep persistence integration independent and OS neutral. Treat plugin argument values as opaque routing data and retain service-specific operations in plugins. Hub persistence verification must remain runnable without Codex, credentials, or another live service. | MVP C-002, NFR-002, NFR-005 | Source/contract inspection and restart verification with the actual bundled model plugins |

Normal stop/restart is the required reliability boundary. No numerical
performance target, additional OS support matrix, or stronger durability
guarantee is introduced. Existing trust and external-data boundaries remain
applicable to locally stored arguments.

## Constraints and dependencies

Use local files for daemon-owned registration storage. Retain Go implementation,
English documentation, the `hashicorp/go-plugin` lifecycle, and language-neutral
plugin interfaces. Do not move hub registration ownership into plugins.

Successful restoration depends on accessible valid state, configured plugins,
and acceptance of saved arguments. A restoration failure follows FR-005 rather
than bypassing a plugin's validation. The manual trigger's existing requirement
that its trigger file be absent at registration affects restoration design;
this extension adds no guarantee for events arriving while stopped.

Storage format, location-selection syntax, save mechanics, concurrent access
prevention, and restoration sequencing are design decisions. Design must
reconcile durable registration with plugin acceptance before client success;
it must not weaken FR-003 or add partial-startup behavior.

## Acceptance criteria

These are required observations, not execution results. Use unit tests for
value and failure matrices and integration tests for assembled restart/file
boundaries. The hub checks run the actual daemon, local client, manual trigger,
and model-output action. Real Codex evidence remains separate under
[codex-queue.md](codex-queue.md).

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001, FR-002, FR-004, NFR-001 | Start with valid configuration and writable state storage. Register trigger T to destination A, leaving B unregistered. Remove the client's argument files after success, stop normally, and restart with the same configuration and state. Publish a unique event after restoration. | No new registration request is needed; the saved identifier, trigger, arguments, and destination are restored. The event reaches A once with its original source/context and saved identifier; B receives nothing. No live service or agent session is used. | Automated assembled-product restart/file verification and saved-value inspection |
| AC-002 | FR-004 | After AC-001, repeat the equivalent registration using newly materialized argument files and publish another unique event. | The saved identifier is returned, no second watch is added, and the event causes one delivery attempt to A. | Registration unit checks and assembled duplicate-registration observation |
| AC-003 | FR-002, FR-003 | Separately reject plugin arguments and prevent state saving during a new registration. | Each request fails, no active subscription or successful saved registration is established, and saving failure has a visible diagnostic. | Controlled plugin/storage failure unit checks and appropriate local-file integration |
| AC-004 | FR-005 | Separately start with unreadable or malformed existing saved state, a saved route naming a missing plugin, and a plugin rejecting saved arguments. Include another otherwise restorable route. | Every scenario fails startup with the affected storage/registration and reason identified. No successful partial startup or substitute destination is reported; failed saved registrations are not silently discarded. A fresh first run without saved registrations can start with empty storage. | State-validation unit checks and file/startup integration with controlled plugin failures |
| AC-005 | FR-006 | Separately stop with a queued event, a possibly sent in-flight event, and an incomplete registration, then restart with completed saved registrations. | None of that work is replayed or resumed and no delivery retry occurs. A new event can use a successfully restored route. | Controlled lifecycle/state unit checks and restart integration where process boundaries are material |
| AC-006 | FR-007 | Follow finalized instructions from a clean build through registration, normal stop, restart, post-restoration event delivery, and deliberate recovery from an approved failure scenario. | Recorded commands, configuration/state locations, diagnostics, unique event context, and observed output establish restoration without re-registration. Instructions accurately describe the required limits and failure policy. | Maintainer walkthrough and documentation inspection |

## Open questions and assumptions

None requiring a further product-scope decision. Configuration reuse and the
failure/identifier choices are recorded above for human review. Storage and
restoration mechanisms must be resolved during design before implementation.

## Change impact

FR-001–FR-007, NFR-001, and AC-001–AC-006 are new local IDs in this concept.
The transition revises MVP workflow step 5, UC-004, NFR-004, AC-005, and its
persistence exclusion and compatibility explanation. It revises Codex Queue
FR-008's shutdown-registration-loss instructions. Existing IDs remain stable;
queued/in-flight event loss, no retry, and independent service verification
remain applicable.

Before adoption, the previous daemon-lifetime registration contract applies.
This extension does not recover registrations a previous nonpersistent version
never saved. Existing saved-state compatibility and format evolution must be
defined in design within the visible startup-failure policy above.

After approval and merge, revise [the routing design](../design/mvp.md),
[the plugin specification](../design/plugin-v1.md),
[the operations guide](../design/windows-operations.md), and
[the bundled-plugin design](../design/bundled-plugins.md) for persistence,
activation, restart, and verification. Review
[the Codex action design](../design/codex-queue.md) for derivative instructions;
no daemon-owned Codex integration is introduced. Delivery updates the root
README and affected plugin instructions. Design, implementation,
test-inventory changes, and delivery materials remain separate phase changes.

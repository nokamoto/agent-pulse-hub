---
type: Requirement
title: Trigger runtime state persistence
description: Let triggers resume from core-managed runtime state after normal shutdown and restart without implementing their own persistence.
sources:
  - id: mvp
    resource: mvp.md
    title: Plugin-based event routing MVP
  - id: registrations
    resource: state-persistence.md
    title: Local daemon state persistence
  - id: bundled-plugins
    resource: bundled-plugins.md
    title: Bundled trigger and action plugins
---

# Trigger runtime state persistence

## Problem and goals

Restoring a registration recreates its watch arguments and destination, but
does not restore progress that a trigger accumulated while watching. A trigger
may otherwise notify the same completed update again after a restart, or need
to implement its own state files and restoration procedure.

The daemon core must persist and return runtime state supplied by a participating
trigger. The trigger owns the meaning of that state and uses it to resume its
watch. Success means that normal shutdown and restart preserve this progress
without requiring the trigger to implement storage, and an update successfully
notified before shutdown is not notified again solely because of the restart.

## Sources and decisions

The user selected trigger-only support, core-owned persistence and readback,
minimal trigger effort to hand off and restore state, and a normal-shutdown
reliability boundary. Existing triggers must be able to opt out without changes.
The user also selected continued monitoring and notifications after save
failure, with a visible notice that continuity at the next restart cannot be
guaranteed; no automatic reset of corrupt or incompatible state; and no
inheritance of deleted registration state by a new registration with identical
arguments. These conversation decisions have no separate linkable source.

[Registration persistence](state-persistence.md) remains responsible for saved
routes and their identifiers. This extension adds evolving trigger progress;
it does not turn watch arguments into mutable state. The
[MVP requirements](mvp.md) retain routing and delivery authority. In particular,
MVP FR-006 defines successful notification by confirmed delivery acceptance;
it does not establish subsequent recipient processing. A notification still
queued, in progress, failed, or of unknown acceptance is outside this
extension's completed-notification guarantee.

For this document, **runtime state** means trigger-supplied data representing
logical watch progress, such as a cursor or the set of already notified updates.
It does not mean a process memory image, open connection, or resumable stack.
**Initial state** means the trigger's documented behavior for a new watch with
no prior runtime state. The service-specific meaning of progress and initial
state belongs to the trigger.

## Scope

- In scope: optional trigger participation; core-owned local persistence and
  readback; restoration before the affected watch starts; completed-update
  continuity after normal restart; registration isolation; failure reporting;
  operator and trigger-author instructions; service-free verification.
- Out of scope: action runtime state; process/session restoration; forced
  termination or power-loss recovery; queued/in-flight event recovery;
  in-progress notification loss or duplicate guarantees; automatic delivery
  retries; general duplicate-event suppression; a common guarantee to retrieve
  updates that occur while stopped; a new live subscription-deletion API;
  external-service trigger implementations, including GitHub review triggers.

## Use cases

| ID | Actor and trigger | Expected outcome | Relevant failure or boundary case |
| --- | --- | --- | --- |
| UC-001 | Trigger supplies evolving watch progress | The core owns saving it and returning it for restoration | Save failure is visible and does not count as successful persistence |
| UC-002 | Operator normally stops and restarts a participating watch | The trigger resumes with its saved progress and does not repeat a successfully notified update solely because of restart | In-progress notification outcomes and stopped-period updates have separate boundaries |
| UC-003 | Saved runtime state is unreadable, corrupt, or incompatible | The operator sees the affected watch and reason; that watch does not start | Returning to initial state requires an explicit operator choice |
| UC-004 | Operator uses multiple registrations or recreates a deleted registration | State returns only to its owning registration | Identical arguments do not transfer the deleted registration's progress |
| UC-005 | Operator uses an existing trigger that does not participate | Its existing registration and event behavior continues unchanged | Runtime-state support is not mandatory for existing triggers |

## Functional requirements

| ID | Requirement | Rationale / use case | Priority |
| --- | --- | --- | --- |
| FR-001 | Provide an optional way for a trigger to hand off and recover its runtime state. The core must own local saving, loading, and return of that state; a participating trigger must not need to implement state-file access, storage-location management, or a separate persistence service. | UC-001, UC-002 | Required |
| FR-002 | Treat runtime state as trigger-owned data. Preserve its meaning without service-specific interpretation in the core. The trigger owns extracting progress, producing restorable data, validating compatibility, and applying the returned state before its affected watch starts. Keep runtime state distinct from registration arguments and destination selection. | UC-001, UC-002, UC-003 | Required |
| FR-003 | After successful persistence and normal shutdown, restart with the same valid configuration, retained registration, and compatible trigger must return the saved progress automatically before that watch starts. No client re-registration or operator reconstruction of the progress is required. Normal shutdown must preserve the progress needed by FR-004 unless a visible save failure prevents successful persistence. | UC-002 | Required |
| FR-004 | For a retained participating registration, an update whose notification had confirmed delivery acceptance before normal shutdown must not be notified again solely because the daemon restarts with successfully persisted runtime state. Preserve the MVP's one-attempt delivery and no automatic retry. Do not claim delivery or recipient processing for queued, in-progress, failed, or unknown notifications. | UC-002 | Required |
| FR-005 | Bind saved progress to its owning registration and trigger. Do not apply one registration's state to another. Repeating an equivalent active registration retains its existing identity and progress under MVP FR-007. Once a registration is deleted through the supported recovery procedure, a newly created registration must not inherit its state even when its trigger and arguments are identical. | UC-004 | Required |
| FR-006 | When existing runtime state cannot be read, validated, or applied, identify the affected trigger/registration and reason to the operator and do not start the affected watch. Do not silently discard state, substitute another registration's state, or restart from initial state. Resetting to initial state requires an explicit operator choice through a documented recovery procedure. | UC-003 | Required |
| FR-007 | Continue the affected watch's monitoring and notifications after runtime-state save failure. Make the failure visible with the affected trigger/registration and reason, and notify the operator that continuity at the next restart cannot be guaranteed. Do not report the failed operation as successfully persisted or claim the normal-restart continuity guarantee for unsaved progress. | UC-001 | Required |
| FR-008 | Existing triggers that do not participate must continue to register, restore registrations, and report events without implementing runtime-state support or changing their existing artifacts. They receive no new runtime-progress continuity guarantee. | UC-005 | Required |
| FR-009 | Keep retrieval of updates occurring while the daemon is stopped a trigger responsibility. State restoration must not imply a common guarantee to collect those updates or replay hub work. A participating trigger must document what its restored progress means for stopped-period updates. | UC-002 | Required |
| FR-010 | Provide operator and trigger-author instructions covering participation, state handoff/restoration responsibilities, normal shutdown/restart, completed versus in-progress notification limits, save/restore failures, deliberate reset, registration deletion/recreation, and compatibility with nonparticipating triggers. | UC-001–UC-005 | Required |

## Nonfunctional requirements

| ID | Quality requirement and conditions | Rationale | Evaluation method |
| --- | --- | --- | --- |
| NFR-001 | Preserve OS-neutral, language-neutral public trigger support and the existing separation of core control from service-specific plugin behavior. Hub continuity checks must run without Codex, credentials, GitHub, or another live service. | MVP C-002, NFR-002, NFR-005 | Public-contract/source inspection and service-free automated restart verification |
| NFR-002 | Keep trigger participation limited to supplying, validating, and applying its state plus the coordination needed for the agreed notification boundary. Do not require each trigger to build a persistence subsystem. | User's minimal-effort goal; UC-001 | Trigger-author contract review and inspection of the participating verification trigger for absence of storage ownership |
| NFR-003 | Keep state failures actionable without dumping runtime-state payloads into diagnostics. Runtime state is trusted local data under the existing plugin/operator trust boundary; it must not become authority to change the registered recipient. | UC-003, MVP NFR-003 | Diagnostic review and controlled malformed-state/isolation checks |

No numerical performance target, additional platform support matrix,
encryption requirement, or crash-durability guarantee is introduced.

## Constraints and dependencies

Retain Go product implementation, English documentation, the
`hashicorp/go-plugin` lifecycle, and local core-managed storage. The trigger
owns the state schema and service behavior; the core owns persistence and
readback. Actions do not gain runtime-state obligations through this extension.

Storage grouping, physical keys, encoding, limits, API shape, protocol
negotiation, notification/state coordination, and save timing are design
decisions. Select them to reduce trigger-author effort while satisfying the
observable restart guarantee and unchanged operation of nonparticipating
triggers. No per-watch/per-instance storage API is selected here.

Design must distinguish runtime-state restoration failure from registration
record failure. Registration corruption still follows state-persistence FR-005;
runtime-state failure follows this document's FR-006 and must not be described
as successful restoration of the affected watch. No automatic initial-state
fallback or new live deletion interface is authorized.

## Acceptance criteria

These criteria describe required observations, not execution results. Design
must allocate inexpensive value/failure checks to unit tests and actual
command, file, process, and restart behavior to assembled verification. Use a
service-free participating trigger and an observable action; retain the
existing real bundled-model routing checks. A verification trigger does not
establish any future external-service trigger's correctness.

| ID | Requirement IDs | Preconditions / stimulus | Observable expected result | Verification method |
| --- | --- | --- | --- | --- |
| AC-001 | FR-001–FR-004, NFR-001, NFR-002 | Register a participating trigger with distinguishable logical progress. Supply update U1, observe confirmed action acceptance, allow successful persistence, stop normally, and restart the same saved registration. Present U1 again and then a fresh U2. | The same registration resumes without re-registration using its restored progress; U1 causes no second notification solely due to restart; U2 is notified once. The trigger performs no state-file/storage-service operations. No live service is required. | Automated assembled restart verification plus contract/source inspection; separate unit state-value checks |
| AC-002 | FR-002, FR-005, NFR-003 | Give two registrations different progress. Restart both and check the returned state. While stopped, delete one registration through the documented recovery procedure and recreate identical registration arguments. | Each retained registration receives only its own progress. The recreated registration receives no state from the deleted one. Neither state changes the destination. Equivalent active registration retains its existing identity/progress. | State-association unit checks and assembled deletion/recreation observation where storage boundaries matter |
| AC-003 | FR-002, FR-006, NFR-003 | Separately restore unreadable, malformed, and trigger-incompatible runtime state for a retained registration. Follow the documented initial-state recovery procedure only after an explicit operator choice. | Every failure identifies the affected trigger/registration and reason without exposing payload contents. Its watch does not start before recovery and no silent initial-state reset occurs. The chosen reset permits the documented initial-state behavior. | Controlled read/validation/application failure unit checks, startup observation, and maintainer recovery walkthrough |
| AC-004 | FR-007 | Prevent runtime-state saving for a participating registration after progress changes, then publish a fresh update while its trigger and action remain otherwise available. | The failure identifies the affected trigger/registration and reason and visibly informs the operator that continuity at the next restart cannot be guaranteed. Monitoring continues and the fresh update is notified through the existing delivery contract. The failed save is not reported as successful persistence or successful continuity for unsaved progress. | Controlled storage-failure checks, continued-watch/notification observation, and diagnostic inspection |
| AC-005 | FR-008, NFR-001 | Use an existing nonparticipating trigger with its unchanged artifacts to register, deliver a fresh event, stop normally, and restore its saved registration. | Existing registration, routing, and restart behavior succeeds without runtime-state implementation or artifact changes. No runtime-progress guarantee is claimed for that trigger. | Existing bundled-model assembled verification and compatibility inspection |
| AC-006 | FR-004, FR-009 | Separately stop with queued/in-progress/unknown notifications, and create updates while stopped. Inspect the restored-state behavior and the trigger's documented stopped-period policy. | Results do not assert loss/duplicate protection for unfinished notifications or a common stopped-update collection guarantee. No hub replay or automatic retry is introduced. The participating trigger's actual stopped-period policy is documented and verified separately from the common core guarantee. | Controlled lifecycle checks, documentation inspection, and trigger-specific verification where applicable |
| AC-007 | FR-010, NFR-001–NFR-003 | A maintainer follows the finalized author/operator instructions for participation, normal restart, failure diagnostics, explicit reset, and registration recreation; separately follows existing nonparticipating-trigger instructions. | Recorded commands, revisions, unique updates, delivery outcomes, saved/restored progress observations, and recovery choices establish the stated results and limits. Author instructions require no trigger-owned persistence subsystem and use OS-neutral procedures. | Maintainer walkthrough and public-contract/source review; retain separate unit, assembled, and any real-service results |

## Open questions and assumptions

| ID | Question or assumption | Impact if unresolved or false | Decision owner | Resolve before | Status |
| --- | --- | --- | --- | --- | --- |
| Q-001 | Behavior after runtime-state save failure | Determines post-failure monitoring, notification, and operator diagnostics. | Responsible human | Requirements approval and design handoff | Resolved by the user: continue monitoring and notifications, and notify the operator that continuity at the next restart cannot be guaranteed. Implemented in FR-007 and AC-004. |

No unresolved product-scope questions remain.

Storage grouping and API selection are delegated design decisions, not open
product-scope choices. Notification success follows MVP FR-006's confirmed
delivery acceptance; no additional recipient-processing guarantee is assumed.

## Change impact

FR-001–FR-010, NFR-001–NFR-003, and AC-001–AC-007 are new local IDs in this
concept. No existing ID is retired. This extension qualifies the runtime-state
exclusions and registration-only wording in state-persistence.md and the
persistence/duplicate-suppression exclusion in mvp.md. Registration-storage
failures, no hub event replay, and one-attempt delivery remain applicable.

After requirements approval and merge, revise the
[persistence design](../design/state-persistence.md),
[public plugin specification](../design/plugin-v1.md),
[routing design](../design/mvp.md), and
[operation guide](../design/windows-operations.md) for state ownership,
optional participation, notification progress, startup gating, compatibility,
failure recovery, and verification. In particular, the existing design's
prohibition on a persistence interface and registration-only saved format
must be reconciled in the design phase; this document selects no replacement
interface. Review [bundled-plugin design](../design/bundled-plugins.md) and
affected trigger-author instructions for derivative changes. Actions and
their existing service acceptance contracts remain outside runtime-state scope.

Design must define compatibility for existing saved registrations and triggers,
state-schema incompatibility detection, and supported offline deletion/reset
procedures. Implementation, governed acceptance inventories, delivery
instructions, and future external-service trigger work belong to their
respective later phase changes. Requirements approval does not establish any
of those implementation or service-verification results.

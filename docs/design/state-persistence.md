---
type: Design
title: Local registration persistence
description: Commit accepted routes to local files and restore their identifiers and destinations before startup admission.
sources:
  - id: persistence-requirements
    resource: requirements/state-persistence.md
  - id: mvp-requirements
    resource: requirements/mvp.md
  - id: routing-design
    resource: design/mvp.md
  - id: runtime-state-design
    resource: design/trigger-runtime-state.md
---

# Local registration persistence

## Context and scope

This design fulfills [local state persistence](../requirements/state-persistence.md).
It extends the [MVP routing design](mvp.md) after adoption of persistent
registrations. Configuration stays in the existing operator-owned JSON; the
hub stores accepted routes separately. A normal stop and restart restores the
same subscription IDs and opaque argument values without the original client
argument files. Trigger/action refer to the public input/output roles.

Completed registrations survive. Participating triggers additionally preserve
opaque progress under [trigger runtime state](trigger-runtime-state.md).
Event queues, in-flight deliveries, pending registrations, plugin sessions
and process availability remain volatile.
There is no forced-termination or power-loss recovery guarantee, stopped-event
collection, delivery retry, automatic plugin restart, or service-specific hub
logic. An older nonpersistent daemon has no registrations to migrate.

## Requirement coverage

All IDs below are local to `requirements/state-persistence` unless qualified.

| Requirements | Acceptance | Design | Verification |
| --- | --- | --- | --- |
| FR-001, FR-002, FR-004, NFR-001 | AC-001 | Storage contract; startup; unchanged configuration and plugin boundaries | SP-V01-RESTORE; value-preservation unit tests |
| FR-004 | AC-002 | Existing canonical duplicate key and restored ID | SP-V01-RESTORE; canonical-key unit tests |
| FR-002, FR-003 | AC-003 | Commit and activation; failure tombstones | SP-V02-SAVE-FAILURE; storage/plugin failure unit tests |
| FR-005 | AC-004 | Complete validation; closed startup admission; deliberate recovery | SP-V03-STARTUP-FAILURE; validation/read-error unit tests |
| FR-006 | AC-005 | Volatile work and normal stop | MVP-V05-RESTART; lifecycle unit tests |
| FR-007 | AC-006 | Operation guide and delivery walkthrough | SP-D01, extending MVP D09 and BUNDLE-D01 |
| NFR-001 | AC-001, AC-003, AC-004 | Go filesystem APIs; opaque data; unchanged nonparticipating protocol | All SP cases and boundary inspection |
| `requirements/mvp` FR-007, NFR-004 | AC-005 | Stable restored routes, volatile events | SP-V01-RESTORE; MVP-V05-RESTART; SP-D01 |
| `requirements/codex-queue` FR-008 | Existing delivery criteria | Derivative restart instructions only | Existing Codex Queue delivery checks plus instruction inspection |

Other routing, plugin-language and actual Codex acceptance remains governed by
[mvp.md](mvp.md), [bundled-plugins.md](bundled-plugins.md), and
[codex-queue.md](codex-queue.md); this extension does not replace those checks.

## Architecture and responsibilities

The command selects configuration and storage, reserves the listener, owns
storage exclusion and coordinates startup/shutdown. A local filesystem adapter
loads and commits records; the hub owns validation, registration identity,
plugin acceptance and activation. Persisted records are independent of the
live registry: plugin exit, inactive tombstones and live-entry cleanup never
delete an already committed registration. Plugins retain their existing
service operations and argument validation.

Use an application-facing store operation to commit one complete registration
and load all records, with mocked filesystem behavior in unit tests. Internal
package names and helper signatures are implementation choices under the
[Go conventions](../aidd/go-development.md). Do not import plugin-private code
into the hub. The optional public checkpoint messages in
[plugin v2](plugin-v1.md#optional-trigger-runtime-state) hand off opaque progress;
they never expose filesystem locations or storage operations to plugins.

## Storage contract

### Location and ownership

`daemon --config <absolute-file> [--state-dir <absolute-directory>]
[--address <address>]` adds one optional flag, at most once. Its default is the
cleaned absolute configuration pathname with `.state` appended, for example
`/srv/pulse/daemon.json.state`. Configuration contents and the register/stop
CLI remain unchanged. The default distinguishes configuration files in the
same directory; copying configuration does not implicitly copy registrations.
The operator must reuse the same configuration and state directory on restart.

The root is a dedicated local directory. Its parent must already exist. Create
a missing root and its subdirectories with owner-only requested permissions
(`0700` directories, `0600` files), subject to host filesystem semantics. An
existing root must be a directory. Do not resolve physical aliases or promise
an ACL security boundary. Operators use one canonical spelling and do not edit,
move, share, or back up a live state tree. Filesystem path aliases and hostile
local modification are outside the existing trust boundary.

| Entry | Meaning |
| --- | --- |
| `.lock/` | Exclusive owner marker, created with a single `os.Mkdir` before reading state; existing marker fails startup even at a different control address. |
| `registrations/` | Committed registration files only. |
| `staging/` | Temporary, uncommitted record files; never loaded as registrations. |
| `runtime/` | Trigger checkpoint generations governed by [runtime state](trigger-runtime-state.md#storage-and-compatibility), independent of registration records. |

Create missing subdirectories only for a fresh empty root. An existing initialized
root must have `registrations/` and `staging/`; the runtime extension may create
its missing `runtime/` directory in a valid older tree after exclusion.
Unknown root entries, non-directory reserved
entries, symlinks, non-regular registration entries and unexpected registration
filenames fail startup. Staging files are uncommitted attempts, not successful
registrations; cleanup may remove them only after acquiring the lock. A leftover
lock requires deliberate operator recovery, never timeout/PID-based lock stealing.
Keep exclusion until all registration writes and configured plugin cleanup finish.
Remove only the marker this process acquired, on normal shutdown and failed-startup cleanup; a failed acquisition never removes another owner's marker. A cleanup
failure is diagnosed with its path; do not claim the marker was released.

### Record format and compatibility

Each committed file is named `<subscription_id>.json`; generated subscription
IDs use lowercase UUID syntax and are unique across retained records. Restoring
an ID is intentional reuse; new request and delivery IDs retain existing rules.
The record is one strict UTF-8 JSON object with exactly these fields:

```json
{"version":1,"subscription_id":"8fc5db2a-77a0-4f60-bf92-bbe214a83a71","input_plugin":"manual","watch_args":{"trigger_file":"/srv/pulse/trigger.txt"},"output_plugin":"model","destination_args":{"output_file":"/srv/pulse/a.jsonl"}}
```

`version` is the integer token `1`. Names are nonempty strings; both argument
values are objects. Use existing strict JSON rules, including rejection of BOM,
invalid Unicode, duplicate keys, unknown/missing fields and trailing data.
Preserve exact decimal numbers and string values without float conversion;
whitespace and object-key order need not survive. Validate filename/ID agreement,
unique IDs and canonical route keys, configured names/roles, existing registry
capacity of 1,024 and encoded outgoing-message limits before launching plugins.
A record read is bounded to 128 KiB; this accommodates every route admitted by
the existing 65,536-byte control and plugin bounds, including generated IDs.
Oversized, truncated, unreadable and unsupported-version files fail startup.
No best-effort salvage or automatic format migration is supplied.

Never store events, delivery/request IDs, process IDs, readiness, active status,
credentials from the environment, or references to client argument files.
These exclusions describe registration records and hub work; participating
triggers may store opaque logical progress in the separate runtime area.
Argument values themselves may contain sensitive data; diagnostics identify
path, subscription ID, operation and reason without dumping arguments.

## Commit and activation

Preserve existing duplicate matching: both configured names and both canonical
argument objects, including exact decimal comparison. An equivalent active
route returns its saved ID without another write, validation or watch. Concurrent
equivalent pending requests share the same outcome.

For a new route:

1. Reserve capacity and a fresh ID, validate both outgoing message bounds, and
   request destination acceptance followed by watch acceptance under existing
   deadlines. Rejection produces no committed file and no active route.
2. Process positive watch acceptance before the next input event. If either
   plugin is already unavailable or stop has begun, do not commit or activate.
3. Serialize commit and activation against shutdown and plugin-unavailability
   state changes. Encode the full record to a unique staging file using exclusive
   creation, check the complete write and close, then rename it to its unique,
   previously absent final path in `registrations/`. All paths stay on the same
   filesystem. Never replace an existing committed record. The successful rename
   is the commit point; do no further fallible storage work before activation.
4. Activate under the same lifecycle serialization and return the normal
   success response. For `runtime_state_v2`, install routing/runtime correlations
   and admission before the one-way activation grant under the
   [runtime startup contract](trigger-runtime-state.md#registration-and-startup).
   Failed commit sends no grant; post-commit grant failure keeps the record and
   follows plugin unavailability rather than storage rollback. A lost client response after commit is uncertain;
   an explicit equivalent registration resolves it while the route is active.

The input stream cannot admit its next event until this decision finishes.
Filesystem operations introduce no crash durability promise or new fsync
requirement; successful write, close and rename suffice for normal stop/restart.
Do not add an unbounded application queue of persistence work. Local storage
latency can exceed the existing client deadline, producing the already-defined
uncertain response; do not extend plugin/control deadlines or retry automatically.

Write, close or rename failure returns `storage_error` with bounded actionable
message, never success. Remove the uncommitted temporary file when possible and
log cleanup failure. Preserve all previously committed files. An accepted or
uncertain input watch becomes the existing inactive tombstone, consuming its
slot until that input stops; discard its events. Do not kill a healthy shared
input solely because saving failed. In particular, the ordered watch-result
callback must report protocol processing success separately from the hub's
registration failure, rather than treating a storage error as transport failure.
There is no cancellation/rollback RPC or active unsaved route.

All successfully saved registrations remain retained even if their plugins
later fail. Count retained records as well as live pending/inactive entries
without double counting the same ID, so restart cannot exceed capacity.
Administrative removal is offline deliberate recovery, not a new removal API.

## Startup and normal stop

1. Parse the existing configuration and state flag, acquire state exclusion,
   validate the entire committed set, then reserve the control listener before
   launching any plugin. No registration/event admission is open yet.
2. Launch configured plugins through the unchanged `hashicorp/go-plugin`
   lifecycle. Existing failure handling for an unused plugin is unchanged;
   an unavailable plugin referenced by a saved route makes restoration fail.
3. Restore records in filename order using the saved ID and argument objects.
   Validate the destination and obtain current watch acceptance for each route,
   applying any participating trigger state under the
   [runtime startup contract](trigger-runtime-state.md#registration-and-startup).
   Do not rewrite records or assign replacement IDs. Keep restored routes staged
   until every saved route succeeds. Before opening admission, recheck all
   referenced instances and that stop has not begun under lifecycle serialization.
4. Install every restored active route/runtime correlation, then open
   registration and event admission together under lifecycle serialization.
   This is startup commit. Report
   `daemon_ready` with `restored_registrations` count. Empty storage reports zero.
   After that record dispatch runtime-state v2 grants under the
   [runtime startup contract](trigger-runtime-state.md#registration-and-startup).
   Readiness does not promise grant receipt or completed source polling;
   post-commit grant failure follows normal plugin unavailability. V2 watches
   stay dormant until granted; v1 and ordinary watches keep their early-emission
   behavior. Events received while restoration is incomplete are discarded with a reason;
   no buffered event is replayed when the gate opens. Operators publish new
   events only after readiness. A plugin's positive watch result is still sent
   before its first event, but does not itself promise hub-wide startup readiness.

Any load, validation or restoration failure reports the affected storage path
or registration ID and reason, exits one, closes the listener, stops every
started plugin, and releases exclusion after cleanup. Do not expose successful
partial startup, deliver from staged routes, switch destinations, or delete a
failed record. Even a route accepted before another fails remains saved for
deliberate repair and a later full restart.

Normal stop closes admission, prevents new commits, waits for any serialized
commit decision already in progress, discards queued events and marks possibly
sent in-flight work unknown under the MVP policy. Cancel pending registrations,
finish participating-input outcome notices and final checkpoint/save before
stopping those plugins, then release state exclusion. A commit that won the
serialization race remains saved; one cancelled before commit does not resume.
Nonparticipating inputs need no final snapshot. Restored routes use fresh
sessions and current acceptance, not prior process state. Normal stop acknowledgement still precedes
process exit; operators wait for exit before restarting.

## Specification documents

This document is authoritative for local record format, storage selection,
commit ordering, exclusion, restoration and persistence verification. These
contracts stay here; no additional specification is required.
[MVP design](mvp.md) retains routing/configuration/control authority and derives
its persistence additions from this document. [Plugin v2](plugin-v1.md) retains
wire-schema authority and documents the activation gate and optional runtime
messages without changing version 2. [Runtime state](trigger-runtime-state.md)
owns mutable progress storage and coordination. [Operations](windows-operations.md) supplies the complete
reusable procedure, including deliberate recovery. [Bundled design](bundled-plugins.md)
and [Codex Queue](codex-queue.md) derive restart instructions; service checks
remain owned by their respective designs. Package layout, internal errors and
temporary filename suffixes are routine choices within these contracts.

## Decisions and alternatives

| Decision | Alternative | Rationale and tradeoff |
| --- | --- | --- |
| Immutable file per accepted route | Rewrite a single snapshot | Avoid replacing existing files and preserving a prior snapshot on failure; bounded 1,024-file enumeration is sufficient. |
| Temporary write/close then unique rename | Append directly to the final file | A failed write must not publish a successful registration; staging separates incomplete attempts. No crash guarantee is implied. |
| Directory exclusion and explicit stale-lock recovery | OS-specific file locks or PID expiry | Standard-library mechanism is portable and prevents concurrent owners using the same path; abnormal termination may require manual cleanup. |
| Fail whole startup | Skip invalid routes | Matches FR-005 and prevents misleading partial success. |
| Commit in ordered acceptance handling | Save at shutdown or activate first | Satisfies success-after-save and prevents unsaved deliveries; local storage latency delays that stream. |
| Keep runtime-inactive saved routes | Delete on plugin failure | Failure does not revoke accepted registration ownership; restart revalidates it. |

## Quality and operations

Use Go filesystem/path/process APIs and argument arrays; no shell invocation or
OS-specific permissions trick is part of the product. State is trusted local
routing input, not authority to change plugin responsibilities. Standard owner
permissions are best effort, not authentication, encryption or local-user
isolation. Keep state and backups protected like configuration and argument
files. Logs retain bounded reasons, not sensitive argument contents.

The [operation guide](windows-operations.md) defines storage reuse, readiness,
normal stop and failure recovery. Stop all owners and preserve a copy before
repairing state. A manual trigger file present at restoration is still rejected;
inspect/archive it deliberately while stopped rather than claiming it will be
collected. Restore valid configuration/destination conditions before retrying.
Never silently repair a record or automatically drop a failed registration.

## Verification strategy

### Selection and unit coverage

Use unit tests for strict format/value matrices, filename/version/duplicate and
capacity rejection, unreadable-file errors, every write/close/rename failure,
canonical equality, lifecycle races, output/input rejection and inactive-watch
cleanup. Mock filesystem and plugin boundaries; permission changes are neither
portable nor reliable under privileged CI. Check pre-commit cancellation and
post-commit stop separately; assert only committed route data enters storage.

Three new assembled cases cover actual file/restart behavior. Combine duplicate
registration with restoration instead of launching a second workflow. Reuse and
revise MVP-V05-RESTART for volatile queued/in-flight/pending work instead of adding
a duplicate shutdown suite. Broad corrupt-format matrices, every plugin error,
and crash/power-loss experiments add cost without useful required integration
coverage; their applicable decisions remain in unit tests, and crash guarantees
remain out of scope. No existing stable acceptance ID is removed.

### CI acceptance cases

Govern new static Pending cases in
`cmd/agent-pulse-hub/state_persistence_integration_test.go` and inventory
`cmd/agent-pulse-hub/testdata/state-persistence-cases.json`. The inventory also
includes revised `MVP-V05-RESTART` in `mvp_integration_test.go`. All files use the
existing `integration` tag and `TestIntegrationMVP` suite. Its existing fixtures
remain under `cmd/agent-pulse-hub/testdata/`. Implementation must make all four
scoped IDs pass, as well as preserve applicable existing acceptance coverage.

| Stable case ID | Inputs and action | Observable results and failure boundary |
| --- | --- | --- |
| SP-V01-RESTORE | Real daemon/client/manual/model-output; unchanged config; fresh default state; absent trigger T; destination A, unused B. Register, capture ID and inspect stored values, delete client argument files, stop and await exit, restart, wait ready, publish unique E1; rematerialize equivalent arguments, register and publish E2. | Initial ready count zero; one committed record. Restart count one; no re-registration before E1; A has E1/E2 once each with exact source/context/saved ID and external-data fields; B absent; equivalent request returns original ID and does not add a record/watch/delivery. |
| SP-V02-SAVE-FAILURE | Real daemon/client/manual/model-output and explicit state directory; first reject invalid destination and invalid watch separately. After readiness, replace the empty registrations directory with a regular file while retaining the lock; register valid route and publish its trigger. Restore the directory while stopped, restart and inspect state. | The registration client exits one for each rejection and the forced save failure; storage failure has storage_error and affected path/reason; no committed route, active subscription or output delivery. Shared input stays alive after storage failure; restart ready count zero. Deliberate fixture mutation is fault injection, not supported operator use. |
| SP-V03-STARTUP-FAILURE | Create two valid stored model routes in isolated storage; stop. In separate copies corrupt a record, name an unconfigured plugin, or leave the saved manual trigger present so current watch validation rejects it. Include a valid earlier route. Separately start a second daemon at another address against a live locked directory. | Each process exits one without daemon_ready or delivery; diagnostics identify failed file/ID/reason; committed files unchanged; started children exit and lock released on failed startup. Second owner fails without touching first owner. Unit read-error injection covers unreadable files. |
| MVP-V05-RESTART | Existing controlled Python input/output fixture: accepted saved route, held in-flight delivery, queued event and separately gated incomplete registration. Stop after journal barriers, await exits, remove consumed fixture command files, release gates, restart same config/state, then emit one fresh event on the saved ID. | No old queued/in-flight event or pending registration is replayed/resumed; no automatic retry. Saved ID restores and fresh event delivers once. The incomplete registration has no committed record. Existing shutdown/unknown-result and configured-process exit observations remain required. |

SP-V01–03 need integration to establish actual command, filesystem, process and
plugin composition; mocks alone cannot prove storage reuse or startup gating.
SP-V01–03 use the actual bundled manual/model-output pair and no substituted service. V05 uses
the real hub/control/plugin framework with existing Python plugins replacing
external timing boundaries; it does not substitute hub state logic. The Python
fixture's command files are a test stimulus, not a service checkpoint: remove
consumed `command-N.json` files after stopping, restart its sequence at 1, and
use fresh unique contexts so fixture re-emission cannot mimic hub replay.

Use deterministic normal stop and bounded waits, not forced kill as acceptance
stimulus. Cleanup may terminate failed test children after reporting failure.
The helper startup barrier must wait for daemon_ready, not merely per-plugin
ready diagnostics. Each daemon test gets explicit isolated storage or a unique
configuration path; restart reuses the intended path. Existing helper changes
needed to pass state selection/readiness belong to the command-local integration
files during implementation, not shared tooling or CI changes.

### Commands, feasibility and budget

Use repository-pinned Go and Python fixture dependencies, existing CI Ubuntu
jobs and `APH_PLUGIN_PYTHON`; no new dependency, setup step, runner or shared
harness is required. Run unit and integration separately:

```sh
go run build/mage.go -d build -w . guardrails
go run build/mage.go -d build -w . test
go test -tags=integration ./cmd/agent-pulse-hub -run='^$' -count=1
go test -tags=integration ./cmd/agent-pulse-hub -run='^TestIntegration' -v -ginkgo.dry-run -ginkgo.v
go run build/mage.go -d build -w . integrationAll
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/state-persistence-cases.json <new-report-directory>
```

The scoped four-case suite keeps the existing two-minute ceiling: warmed build
setup at most 20 seconds, V01 10 seconds, V02 10 seconds, V03 20 seconds, V05
10 seconds, cleanup five seconds, leaving 45 seconds reserve. Peak six child
processes follows the existing helper limit. Use temporary directories, bounded
file polling and awaited process exits. Cache warm-up is reported separately.
The new cases do not add a real-time 30-second deadline test; existing MVP
coverage retains it. Full MVP and persistence scoped runs use separate reports.

Before design readiness, execute the existing model/process harness and a
minimal standard-library file probe for lock exclusion, temp-close/rename,
regular-file obstruction and cleanup. Record commands, OS/shell, timings and
results in the PR. These demonstrate feasibility only. Compilation and dry-run
registration of Pending cases do not establish product acceptance. No production
persistence symbols are referenced by Pending tests. Implementation must execute
the completed cases and meet the unchanged runner ceiling; missing, Pending,
skipped or filtered cases fail the scoped acceptance gate.

### Delivery quality checks

| Check | Scope and procedure | Required evidence and evaluator |
| --- | --- | --- |
| SP-D01 | Extend MVP D09/BUNDLE-D01 clean-build walkthrough using finalized root/plugin instructions and the operation guide. Capture state location and ID; remove client argument files; normal stop, wait exits, restart; publish new unique event before re-registering; verify same ID/A once/B absent. Then while stopped preserve state, deliberately corrupt a copied record, observe startup failure, restore the preserved valid copy and repeat restoration. Include the separate foreground interrupt observation. | Delivery maintainer records revision, environment, exact commands, redacted config/state paths, saved ID, diagnostics, output records, exclusion release and recovery. Instructions must state no event replay, stopped-event collection or crash guarantee. |

SP-D01 is local-model delivery, not real Codex evidence. Existing Codex Queue
real-service checks remain separately executed during delivery, including
current destination acceptance; storage verification does not establish Codex
availability or queue semantics. No new service integration is introduced.

## Change and rollout impact

Adoption adds the state flag/default and storage_error control result without
changing local-control version or nonparticipating plugin v2 schemas. Existing clients continue
to parse failure code/message; existing configuration remains valid. Select a
stable writable location before first persistent use. Previously memory-only
registrations must be registered once; no unavailable history is reconstructed.

Stop before moving storage, changing configuration or repairing records. Keep
a copy of the entire stopped state directory; preserve file names and values.
Unsupported format versions fail visibly until an explicitly designed migration
exists. Rolling back to a nonpersistent binary loses automatic restoration and
requires explicit registration; keep the newer state aside and do not claim
that the old binary maintains it. Appended destination output cannot be undone.

## Open questions and risks

No unresolved implementation decision remains. Human design review covers the
per-route files, conservative directory lock, startup gate and unchanged
normal-stop-only guarantee. Slow local filesystems can delay registration;
stale locks and invalid plugin arguments require deliberate operator recovery.
These are visible limits rather than automatic repair or retry features.

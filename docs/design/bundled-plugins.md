---
type: Design
title: Bundled model plugin migration
description: Co-locate the manual trigger and model-output action with their usage materials while preserving protocol v2 and service-free verification.
sources:
  - id: bundled-requirements
    resource: requirements/bundled-plugins.md
  - id: mvp-requirements
    resource: requirements/mvp.md
  - id: mvp-design
    resource: design/mvp.md
  - id: runtime-state-design
    resource: design/trigger-runtime-state.md
---

# Bundled model plugin migration

## Context and scope

Move the manual file trigger and JSON-lines model-output action into the
role-based layout required by [bundled-plugins](../requirements/bundled-plugins.md).
Operators find each executable's source, instructions, and configuration examples
together. A trigger produces events; an action receives routed events. These
are the public protocol's input and output roles respectively.

This design covers common FR-001–003 and AC-001 for both model plugins. It
preserves the [MVP](mvp.md) behavior and all eleven service-free acceptance
cases. Codex Queue and its registration skill remain a separate design under
[their requirements](../requirements/codex-queue.md). No installation manager,
automatic runtime discovery, new wire version, or new routing behavior is added.

## Architecture and source layout

Use the root Go module. Each plugin root is a `main` package; move its existing
entry point and plugin-specific implementation rather than adding wrappers
around implementations left in the hub. Preserve package names to keep the
move mechanical.

| Existing source | Destination |
| --- | --- |
| `cmd/manual-plugin/main.go` | `plugins/triggers/manual/main.go` |
| `internal/adapters/manualplugin/` including unit tests | `plugins/triggers/manual/internal/adapters/manualplugin/` |
| `cmd/manual-plugin/*_integration_test.go` | `plugins/triggers/manual/*_integration_test.go` |
| `cmd/model-output-plugin/main.go` | `plugins/actions/model-output/main.go` |
| `internal/adapters/modeloutput/` including unit tests | `plugins/actions/model-output/internal/adapters/modeloutput/` |

Remove the old command and implementation packages after relocation; do not
leave duplicate executable entry points or forwarding packages. Shared
`internal/adapters/pluginprocess`, `pluginprotocol`, `protocol`, and root domain
packages remain shared. The plugin-local adapters may import these shared
packages. The daemon and shared framework must not import either plugin's
private implementation. No new domain/application layers are required for
these two adapters. Future plugin-local layers follow the existing
[Go placement and dependency rules](../aidd/go-development.md#package-placement).

Keep hub acceptance and its Python fixtures in `cmd/agent-pulse-hub/`.
Move the manual standalone suite with the manual command. It still launches
only that plugin through the public Session; it does not depend on the daemon.
The model-output action remains exercised through the actual hub route and
its isolated adapter unit tests; do not introduce a duplicate output suite.

## Public compatibility and data

The names trigger/action apply to directory roles and user-facing descriptions.
Retain the following exact public spellings and meanings:

- Plugin protocol version 2, handshake, gRPC service, `input`/`output` dispense
  names, ready roles, and all message fields and deadlines.
- Daemon JSON `role: "input"` and `role: "output"`, and configuration fields
  `name`, `executable`, and `args`.
- CLI `--input-plugin`, `--output-plugin`, `--watch-args-file`, and
  `--destination-args-file`; local-control request fields remain unchanged.
- Manual `trigger_file` and model-output `output_file` schemas, validation,
  event context bounds, observation records, and diagnostic attribution.

There are no new `trigger`/`action` aliases in configuration or the wire
protocol. Descriptions explain the mapping beside examples, so users do not
substitute directory names into literal role fields. Executable output names
may stay `manual-plugin` and `model-output-plugin`; configured instance names
`manual` and `model` remain valid. Source build paths change, executable paths
need change only when the operator chooses different build outputs.

The [local persistence design](state-persistence.md) governs committed
subscriptions, storage selection and restart restoration. Queued/in-flight
events and pending registrations remain memory-only. The separate
[runtime-state design](trigger-runtime-state.md) adds optional trigger checkpoints;
manual and model-output remain nonparticipating with unchanged artifacts and
no logical-progress guarantee. Source relocation itself
adds no storage format or plugin-owned persistence. Destination records and
unconsumed trigger files
remain operator-owned data. The daemon still validates destinations before
accepting watches, binds recipients from registration, and attempts delivery
once. Neither plugin gains authority to select another destination.

## Usage artifacts and examples

Each plugin has a nonempty lowercase `readme.md` and these JSON examples:

| Plugin | Files directly under `examples/` | Contents |
| --- | --- | --- |
| `plugins/triggers/manual/` | `daemon.json`, `watch.json` | Complete two-plugin daemon configuration; `{"trigger_file":"<ABSOLUTE_TRIGGER_FILE>"}`. |
| `plugins/actions/model-output/` | `daemon.json`, `destination.json` | The same complete two-plugin daemon configuration; `{"output_file":"<ABSOLUTE_OUTPUT_FILE>"}`. |

Both `daemon.json` files contain exactly this configuration shape:

```json
{"plugins":[
  {"name":"manual","role":"input","executable":"<ABSOLUTE_MANUAL_EXECUTABLE>","args":[]},
  {"name":"model","role":"output","executable":"<ABSOLUTE_MODEL_OUTPUT_EXECUTABLE>","args":[]}
]}
```

These are JSON templates, not directly runnable configurations. Instructions
require replacing each entire placeholder string with an actual absolute path
and writing JSON through a serializer so escaping is correct on any host.
The daemon does not expand placeholders. Use an existing writable directory,
an absent trigger, and initially absent output file; preserve existing-file
validation behavior described in the MVP profile. Do not supply credentials,
agent identity, a hard-coded home directory, or assumed executable suffix.

Both READMEs explain purpose, trigger/input or action/output mapping,
Go prerequisites and repository-root build invocation, daemon-managed launch
(no standalone shell execution of plugin binaries), configuration and argument
fields, readiness, registration, publication, expected JSONL output, shutdown,
and recovery. Link the counterpart's examples and the
[operation guide](windows-operations.md) for the complete paired workflow.
Explain that registration does not create the output, `accepted` is not an
fsync guarantee, `unknown` may already have written data, and no automatic
retry or plugin restart occurs. Explain state selection/reuse, saved-ID
restoration, storage/startup failure and deliberate offline recovery under
[local persistence](state-persistence.md), including no event replay, stopped-event
collection or forced-termination/power-loss guarantee. Explain invalid
paths/content, duplicate trigger
paths, failed builds, unavailable plugins, and partial output recovery.

Implementation introduces both plugin directories with complete, accurate
README and example artifacts in the same change, satisfying the existing
presence guardrail immediately. Delivery verifies and refines those materials
and updates the root README's build paths, role descriptions, and plugin-author
entry points within its prescribed headings. The build procedures and artifact
contracts are defined here and in the operation guide, not deferred to a new
implementation-time specification.

## Normal and failure scenarios

1. Build the hub and both plugins from the same revision with the new source
   paths; use explicit output paths and the compiler's executable suffix.
2. Materialize the example JSON with chosen paths, start the daemon, wait for
   both plugins and registration readiness, and register the pair. An equivalent
   active registration still returns the same subscription identifier.
3. Publish a closed sibling temporary file by rename onto the absent trigger.
   The manual trigger consumes it; the action appends one JSONL record with
   unchanged source, subscription identifier, context, and external-data notice.
4. Stop through the public client or foreground interrupt. Wait for configured
   plugin exits. Restart with unchanged configuration/state restores accepted
   registrations and their IDs after current plugin acceptance; queued events
   do not return. Output records remain separate operator-owned files.

A stale source build path fails to build: update the caller, do not retain an
old binary as evidence of success. Unreplaced placeholders or missing executables
fail configuration before plugin launch. Missing or invalid arguments retain
existing rejections. Launch failure, stream loss, and unconfirmed writes retain
MVP failed/unknown handling and no retry. Inspect residual trigger/claim files
and partial output before deliberate recovery; relocation grants no new file
identity, concurrent-writer, or crash-recovery guarantees.

## Decisions and alternatives

| Decision | Alternative and tradeoff |
| --- | --- |
| Move entry points and private adapters together | Moving only `main.go` leaves implementation dispersed and fails the intended ownership boundary. Shared framework stays central to avoid protocol duplication. |
| Preserve v2 and public input/output spellings | Renaming wire roles or adding aliases increases compatibility and test scope without improving directory discoverability. Explain the mapping instead. |
| Remove old source entry points; preserve chosen binary names | Compatibility wrappers retain two source interfaces indefinitely. This source-layout break requires updating build callers but not runtime configuration schemas. |
| Reuse all eleven MVP cases | Duplicate migration-specific process suites would repeat the same behavior. Source/artifact inspection and existing guardrail fixtures cover placement; delivery checks exercise actual examples. |
| Complete paired daemon templates in both plugins | Fragments are shorter but require operators to invent the missing configuration. Review both copies for consistency. |

## Specification documents

This document owns source layout, artifact contents, and source-path migration.
[MVP design](mvp.md) remains authoritative for runtime routing, configuration,
CLI, and model-plugin behavior. [Plugin protocol v2](plugin-v1.md) remains the
language-neutral interoperability specification; its terminology clarification
derives from this design without changing schemas. The
[operation guide](windows-operations.md) derives its build paths and migration
procedure from this design and its runtime steps from the MVP.

No additional specification is needed. Implementers may choose internal helper
names within the defined packages, but cannot change role spellings, observable
behavior, accepted argument schemas, or acceptance-case coverage as part of
relocation.

## Requirement coverage

| Requirement concept and IDs | Acceptance | Design and verification |
| --- | --- | --- |
| `requirements/bundled-plugins` FR-001 | AC-001 | Role directories, public mapping, maintainer inspection of paths and descriptions; delivery check BUNDLE-D01. |
| `requirements/bundled-plugins` FR-002 | AC-001 | Source relocation, README and JSON contracts; all eleven MVP cases and BUNDLE-D01. |
| `requirements/bundled-plugins` FR-003 | AC-001 | Existing `plugins` guardrail and its complete/missing-artifact fixtures; run guardrails and tooling unit tests. No enforcement change. |
| `requirements/mvp` NFR-001 | AC-006 | Retain MVP-V03-ROUTE idle/no-delivery observation and source inspection. |
| `requirements/mvp` NFR-002 | AC-007 | Retain MVP-V02-REGISTER non-Go replacement of both roles and unchanged protocol inspection. |
| `requirements/mvp` NFR-003 | AC-008 | Retain MVP-V03-ROUTE recipient isolation/external-data fields and MVP-V08-LOCAL-CONTROL. |
| `requirements/mvp` NFR-004 | AC-005, AC-009 | Retain MVP-V05-RESTART and [D09, the clean-build README walkthrough](mvp.md#delivery-verification); BUNDLE-D01 verifies migrated usage materials. |
| `requirements/mvp` NFR-005 | AC-009, AC-011 | Retain actual model route, standalone manual cases, shared/service-specific boundary inspection and D09 without external services. |

MVP functional requirements and all existing acceptance criteria remain covered
by the [MVP case mapping](mvp.md#requirement-coverage). Codex-specific FR-004–008
and AC-002–008 belong to the separate Codex Queue design, outside this scope.

## Verification strategy

### Existing cases and relocation

Source relocation itself retains all eleven stable IDs and the behavior in
[the MVP case table](mvp.md#selected-command-workflows); it introduces no new
runtime case and does not turn executable cases into Pending. The separate
[persistence design](state-persistence.md#ci-acceptance-cases) revises
MVP-V05-RESTART's stimuli, expectations and budget, initially as static Pending,
and adds three cases. Apply that revision when adopting persistence; keep all
other cases enabled and unchanged by relocation. Relocation defects are caught
by building the actual new packages and running the same public workflow;
artifact presence is checked more cheaply by the existing validator fixtures.

The governed inventory remains `cmd/agent-pulse-hub/testdata/mvp-cases.json`.
During implementation, change only its manual suite package from
`./cmd/manual-plugin` to `./plugins/triggers/manual`; retain all nine hub IDs and
both manual IDs. Move the manual suite/cases/helpers together, preserving the
`integration` tags and `TestIntegration` entry point. No plugin directories or
production placeholders are introduced by the design itself.

Update actual build callers in the hub's `mvp_helpers_integration_test.go` and
`budget_feasibility_integration_test.go` to map binary names to explicit source
packages, instead of deriving all sources from `cmd/`. Update the moved manual
helper's repository-root calculation for its three-level directory depth and
its new build target. Preserve fixtures, public Session use, deadlines, cleanup,
case labels, and machine-report checks. Do not copy acceptance helpers into
production or import private plugin adapters from the hub tests.

Retain unit tests beside the relocated adapters, including mocked filesystem
failures. Inspect imports to ensure only private code moved, both commands use
the common framework, and no daemon package acquires plugin-private imports.
Search callers and documentation for old source paths; historical migration
explanations are allowed, executable build commands must use new paths.

### Commands, feasibility, and cost

Use Go 1.26.0 or later and the repository-pinned Python 3.12 environment/packages
from `build/testdata/plugin-tooling/requirements.txt`, selected through
`APH_PLUGIN_PYTHON`. No service, credentials, or Codex installation is needed.
Warm dependency/build caches separately and record actual OS, shell, versions,
commands, elapsed times, reports, and scope. Existing actual binaries and
executable cases establish the unchanged process/Session/file method before
relocation; implementation must rerun them against the new paths.

Run these checks from the repository root after relocation:

```text
go run build/mage.go -d build -w . guardrails
go run build/mage.go -d build -w . test
go test -tags=integration ./cmd/agent-pulse-hub ./plugins/triggers/manual -run=^$ -count=1
go test -tags=integration ./cmd/agent-pulse-hub ./plugins/triggers/manual -run=^TestIntegration -v -ginkgo.dry-run -ginkgo.v
go test -tags=integration ./cmd/agent-pulse-hub -run=^TestDesignBudgetFeasibility$ -count=1 -timeout=2m -v
go run build/mage.go -d build -w . integrationAll
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/mvp-cases.json <new-report-directory>
```

For design-time compilation/registration and baseline feasibility only, use
`./cmd/manual-plugin` in place of the future manual suite path and the unchanged
inventory. This verifies the existing method, not completed relocation.

Keep the MVP budgets: hub cases 94 seconds plus 20 seconds warmed compilation
and builds and five seconds cleanup (119 seconds);
manual cases ten seconds plus 35 seconds setup/cleanup reserve (45 seconds).
Peak case children remain six. The hub builds all three real binaries; the
standalone suite builds only manual. Both fit the unchanged two-minute scoped
per-suite limit. Use unique temporary directories, bounded waits, supervised
children, and awaited shutdown. Retain the real 30-second delivery-timeout case;
mock exceptional filesystem and parsing matrices in unit tests.

Required implementation evidence is a passing report for every inventory ID
from the migrated revision, separately from unit/tooling checks. Missing,
Pending, skipped, or filtered cases cannot satisfy it. Design compilation,
dry-run registration, baseline success, and artifact presence do not establish
migration acceptance. No dependency, CI, runner, or validator changes are needed.

### Delivery quality

BUNDLE-D01 extends existing D09 rather than duplicating its runtime walkthrough.
The delivery maintainer starts from a clean checkout, builds using the updated
root README, and follows each plugin README. Parse both daemon templates and
verify they match; replace their placeholders through JSON serialization and
use the resulting watch/destination files in D09's actual registration and
publication. All required fields must be explained without invented paths.

Require the D09 idle observation, duplicate registration result, exact unique
context at A, absence at B, shutdown, same-ID restart restoration without client
argument files, [SP-D01](state-persistence.md#delivery-quality-checks) saved-state failure/recovery, and separate foreground
interrupt run. Record revision, environment, commands, substituted non-secret
configuration, source template paths, plugin diagnostics, resulting records,
and cleanup. Review README purposes, prerequisites, fields, results, relevant
failures, and cross-links against FR-002. Missing artifacts, stale build paths,
unexplained placeholders, wrong roles, or absent observations fail this check.
No actual Codex or external-service verification applies to these local models.

## Rollout and risks

Operators stop the old daemon and wait for plugin exits, inspect residual
trigger/output files, build from the new package paths, and update configured
executable paths only if their chosen output locations changed. Start and
restore saved registrations using the same configuration/state. An older
nonpersistent daemon requires one initial registration; it has no saved history
to migrate. Old `./cmd/manual-plugin`
and `./cmd/model-output-plugin` source builds are intentionally unsupported.
Rollback uses the prior revision's matching source paths and binaries, then
registers anew when returning to a nonpersistent daemon; preserve newer state
separately under the persistence rollback contract. It cannot retract already
appended output.

The main risks are stale caller paths, losing manual cases during relocation,
and confusing directory terminology with literal wire roles. Explicit package
mapping, the unchanged case-ID inventory, and adjacent role explanations address
these risks. Keep Go process invocation and file paths OS neutral; a successful
run on one host is evidence for that host, not universal platform support.
External context remains untrusted data and grants no new permissions. There
are no unresolved implementation decisions; human review covers the source-path
break, preserved public spellings, source ownership, and artifact contract.

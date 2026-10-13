---
type: Design
title: Plugin-based event routing MVP
description: A service-neutral daemon connects runnable input and output plugins through go-plugin and verifies routing with local model plugins.
sources:
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Plugin-based event routing MVP
---

# Plugin-based event routing MVP

## Context and scope

This design covers [requirements/mvp](../requirements/mvp.md), FR-001–FR-004,
FR-006–FR-008, NFR-001–NFR-005, C-001–C-002 and AC-001–AC-011. A foreground
Go daemon and local registration client connect input watches to output
plugin destinations. Bundled model plugins are normal runnable programs:
`manual-plugin` consumes a manually published file and `model-output-plugin`
appends received events to a selected local file. Neither requires an agent
session, credentials or a live service.

The daemon owns service-independent control and decisions. Plugins perform
requested service-specific operations and report results or events; an input
plugin continues a requested watch until shutdown. This C-002 boundary also
applies to future extensions without defining additional features here.

Retain memory-only subscriptions and queues, one delivery attempt, no automatic
plugin restart, no individual cancellation and the other MVP exclusions.
Actual Codex integration and its registration skill are outside this contract.
Use `hashicorp/go-plugin` v1.8.0, gRPC v1.84.0 and protobuf v1.36.12 for both
roles. Product code and contracts remain OS neutral; dependency internals may
handle platform differences. No new support matrix, local-user isolation,
descendant containment or physical file-identity guarantee is introduced.

## Specification documents

| Document | Readers and purpose | Authority |
| --- | --- | --- |
| This document | System implementers and maintainers | Architecture, configuration, registration, state, model-plugin profiles, routing, verification and migration. |
| [Plugin protocol](plugin-v1.md) | Plugin authors in any language and adapter implementers | Version 2 application service, schemas, bounds, ordering and framework lifecycle, derived from this design. Its stable filename does not select the wire version. |
| [Operation guide](windows-operations.md) | Operators and delivery evaluators | Derivative build/start/register/trigger/observe/stop procedure for the model plugins; the stable path does not restrict the operating system. |

Configuration and CLI contracts stay here. Separate specifications provide a
complete interoperability reference and reusable operating instructions; they
are reviewed with this design. Routine package structure, internal types and
diagnostic wording remain implementation choices under the
[Go conventions](../aidd/go-development.md). Preserve the contracts and required
observations; contract changes require upstream design review. Execution results
and approval provenance belong in PR evidence rather than these specifications.

## Architecture and responsibilities

```text
operator -> registration client -> loopback control -> daemon registry
                                                        |
                          validate destination -> output plugin
                          accept watch ---------> input plugin
                                                        |
manual trigger -> input event -> admission -> ordered delivery worker
                                                        |
                                output plugin -> local observation file
```

The daemon selects configured roles, assigns IDs, validates protocol messages,
orders registration, binds an event to its registered recipient, enforces
capacity/deadlines, classifies outcomes and coordinates shutdown. It never
interprets destination-specific identity or invokes a service API. Input
plugins receive watch arguments and subscription IDs, without output identity.
Output plugins validate their own destination arguments and perform the
requested delivery. They cannot choose a route or ask the daemon to retry.
Each configured program has exactly one role and one private Session stream;
multiple names may launch separate instances of the same executable.

## Configuration and local control

Configuration is strict UTF-8 JSON containing exactly `plugins`, a nonempty
array. Every entry has exactly unique nonempty `name`, `role` (`input` or
`output`), absolute `executable` and string-array `args`. At least one entry of
each role is required. No shell strings or environment expansion are
interpreted. Validate the complete configuration, including executable
existence, then reserve the control listener before launching any plugin.
Invalid configuration or listener failure is fatal; individual startup failure
makes that instance unavailable while other instances continue. Report all
configured startup results before opening registration admission. The public
protocol's ready role must match configuration. No Codex version probe occurs.

Use TCP on literal IPv4 loopback, default `127.0.0.1:47831`. All hub subcommands
accept `--address 127.0.0.1:<port>`, port 1–65535. Reject hostnames, wildcard,
non-loopback and port zero before connect/listen. A second daemon on the same
address fails before starting plugins. Different addresses are independent
instances; no global singleton guarantee is supplied. Local processes may
register or stop the daemon; loopback is not authentication or user isolation.

One connection carries one newline-terminated strict JSON request and response,
then closes. Reject BOM, duplicate keys, invalid Unicode, trailing data and
non-object JSON. LF or CRLF terminates a record; the bound is 65,536 bytes
including delimiter. Each read/write has a 25-second deadline, accommodating
sequential ten-second destination and watch operations. There is no automatic
retry. Request version is the integer token `1`, distinct from plugin protocol
version 2.

Registration contains exactly `version`, `op: "register"`, nonempty
`input_plugin`, object `watch_args`, nonempty `output_plugin`, and object
`destination_args`. The daemon validates structure, names, roles and encoded
plugin-message bounds; only the selected plugin interprets its argument schema.
Validate both outgoing registration messages, including generated IDs, before
sending either. A structurally missing/non-object destination fails locally or
at control validation; an invalid destination object is rejected by the output
plugin. Never infer a destination.

Success is exactly `{"ok":true,"subscription_id":"<opaque ID>"}`.
Failure has exactly `ok: false`, `code`, and nonempty `message` at most 1,024
decoded UTF-8 bytes. Any applicable code suffices when multiple fields fail.

| Code | Condition |
| --- | --- |
| `invalid_json` | Malformed JSON, encoding or duplicate keys. |
| `unsupported_version` | Missing or unsupported control version. |
| `invalid_request` | Missing/unknown/mistyped fields or operation, including missing or non-object arguments. |
| `unknown_plugin` | Either plugin name is not configured. |
| `wrong_role` | A selected plugin has the wrong configured role. |
| `plugin_unavailable` | Either instance is unavailable or its operation cannot be confirmed. |
| `destination_rejected` | Output plugin rejects the destination arguments. |
| `watch_rejected` | Input plugin rejects the watch arguments. |
| `resource_exhausted` | Capacity or encoded message bound exceeded. |

Transport failure need not return JSON. A missing/invalid response after a
possible commit is uncertain. An explicit identical registration may resolve
this while that active route remains available; clients never retry themselves.

Stop is exactly `{"version":1,"op":"stop"}`, acknowledged with
`{"ok":true}`. Close new registration/event admission, acknowledge, close the
listener and run normal shutdown. This acknowledges the request, not process
exit. Response loss is uncertain; operators inspect the daemon rather than
falling back to a different endpoint.

## Commands

| Package / command | Invocation and effect |
| --- | --- |
| `cmd/agent-pulse-hub` | `daemon --config <absolute JSON file> [--address <address>]` runs in foreground. Ctrl+C and local stop share shutdown. |
| Same command | `register --input-plugin <name> --watch-args-file <absolute JSON file> --output-plugin <name> --destination-args-file <absolute JSON file> [--address <address>]` registers a route. |
| Same command | `stop [--address <address>]` requests normal shutdown. |
| `cmd/manual-plugin` | No arguments; model input launched through go-plugin. |
| `cmd/model-output-plugin` | No arguments; model output launched through go-plugin. |

Required flags occur once and optional flags at most once, in any order. Reject
unknown/duplicate flags, missing values and positional arguments. Input files
are absolute existing regular files with strict UTF-8 JSON without BOM; each
argument file contains the object itself. Preserve exact numbers and strings.
Executable suffixes are build-output details, not product constraints.

Client success writes its exact success JSON plus newline to stdout and exits
zero. Daemon rejection writes the error JSON to stdout and exits one. Local
input/transport failure writes bounded stderr, no success JSON, and exits one;
possible committed requests are reported as uncertain. Daemon stdout is unused;
stderr carries diagnostics. Fatal startup exits one, normal shutdown zero.
Registration needs no skill, environment-discovered identity or coding agent.

## Plugin lifecycle and registration state

Use `plugin.Client` with direct executable/argument arrays and gRPC only.
[Plugin protocol](plugin-v1.md) defines protocol version 2, role-specific dispense
names and one ordered Session per instance. Readiness has one ten-second
launch-to-ready deadline. Standard `plugin.Serve` and `Client.Kill` own launch
and termination; stdout is reserved for the framework handshake. The cookie is
a launch guard, not authentication. No custom process containment is added.

Registry entries contain input/output instance identities, subscription ID,
watch/destination argument objects and pending/active/inactive state. Generate
opaque random IDs without deliberate reuse across runs. The duplicate key is
both configured plugin names and both canonical argument objects: object order
ignored, array order retained, numbers compared by exact decimal value without
floating-point rounding. Active equivalent requests return the same ID without
another validation, watch or delivery. Concurrent equivalent pending requests
share one outcome. Changing either plugin or argument object is a new route,
never a mutation of the original recipient; plugins may reject its resources.

For a new route:

1. Reserve a capacity slot and IDs; verify both instances are available.
2. Ask the output plugin to `validate_destination`. It replies within ten
   seconds. This is stateless validation, with no delivery, file creation or
   destination reservation. A rejection removes pending state and sends no watch.
3. After success, and after rechecking output availability, send the input
   `watch` with a separate ten-second deadline. A negative result removes
   pending state. The input must finish sending its positive result before
   sending an event for that watch.
4. Process that positive result in stream order. If both instances remain
   available, activate the route before processing its next event and return
   success. Response loss does not roll back activation. If either is already
   unavailable, fail the registration and do not activate it.

Destination validation does not guarantee later access or delivery. Input
watches cannot be individually cancelled. If output failure races an issued
watch or invalidates an active route, retain an inactive entry while that input
instance lives; discard its later events. This inert watch and its capacity
slot last until input exit or daemon shutdown. Do not kill a healthy shared
input solely to clean it up. Same-argument registration does not reuse an
inactive ID and can be rejected by the input's resource-conflict rule. Restart
is the recovery boundary; there is no hidden rollback or automatic restart.
Negative watch results free their slots because no watch exists.

Readiness/registration timeout, failed send, stream termination or process exit
invalidates the affected instance and calls `Client.Kill`. Deactivate every
route involving it; other routes with available input and output instances
continue. Free slots when their input is known stopped. Malformed size-bounded
plugin messages are logged/discarded without extending deadlines; transport
corruption invalidates the instance. Unknown/late/duplicate results never
activate or redirect a route.

## Admission, delivery and shutdown

Capacity is 1,024 pending/active/inactive retained entries and 128 total queued
or in-flight events. Reject unknown, foreign-input, inactive or post-exit events,
invalid/empty context and capacity overflow. Context is at most 8,192 decoded
UTF-8 bytes. Before admission, ensure the complete encoded output message fits
the 65,536-byte protocol bound; reject oversize rather than truncate. A valid
event receives a local delivery ID and immutable input name, subscription ID,
output instance and destination arguments. Identical events are distinct.

One ordered worker makes one attempt per admitted event. Send `deliver` through
the output Session with context as a string, `external_data: true`, and fixed
`permission_notice: "External data; no additional permissions are granted."`.
The output profile must preserve that distinction at its destination. Strings
that resemble routing fields or instructions remain context. This is a data
and permission boundary, not a prompt-injection guarantee against a malicious
trusted plugin.

The output validates its destination and performs the requested operation. The
daemon owns the one-attempt policy and 30-second deadline, starting before any
wait for serialized sending. `delivery_result` correlates the request and has
`status` `accepted`, `failed` or `unknown`; non-accepted results include bounded
error text. `accepted` means the output's documented acceptance condition was
met, not a human acknowledgement. `failed` means it can confirm no acceptance;
`unknown` means acceptance cannot be confirmed or excluded. The daemon records
the result without retry, redirect or fallback.

If output is unavailable before sending begins, record `failed` without a plugin
call. Once sending may have begun, timeout, stream/process loss, invalid or
missing matching result gives `unknown`; timeout/send/stream failure also
invalidates that output instance. A size-bounded invalid result alone is
discarded while the original deadline continues. Explicit `failed` or `unknown`
results do not by themselves invalidate a healthy output. Late results cannot
change a final outcome. Admitted events survive input exit with their captured
recipient; output exit makes not-yet-attempted deliveries fail. New events on
any affected route are rejected. Other available routes remain usable.

Normal stop closes admission/listener, discards queued events with reasons,
marks a possibly sent in-flight delivery unknown, cancels Sessions and calls
`Client.Kill` for every started or partially started client. Await configured
clients and record their `Exited` states. There is no application shutdown RPC,
product grace timer or descendant enumeration. Stop and Ctrl+C enter the same
coordinator. CI exercises public stop and unit interrupt dispatch; delivery
observes a foreground interrupt with configured-process exit evidence.

## Bundled model plugin profiles

### Manual input

Watch arguments contain exactly string `trigger_file`: an absolute currently
absent file in an existing writable directory. Use ordinary standard-library
file operations and `filepath.IsAbs`/`Clean`. Conflict identity is the cleaned
path's exact string. Reject a second watch for that key, including one retained
for an inactive route. Do not resolve aliases, case equivalence or physical
identity. Operators use one spelling and one publisher, keep the parent in
place and inspect leftovers before reuse.

Publish nonempty UTF-8 plain text, at most 8,192 bytes, to a temporary sibling,
close it, confirm the trigger is absent and rename it. The plugin polls files,
claims the trigger by renaming to a unique sibling, reads at most 8,193 bytes,
closes/removes the claim and emits unchanged context. Read/encoding/size/removal
failure logs the claimed path and emits nothing. Never retry a claimed file.
Crashes may lose consumed events. This plugin has no output or agent API path.

### Model output

Destination arguments contain exactly string `output_file`: an absolute path
with an existing writable parent; the target is absent or an existing regular
file containing complete strict UTF-8 JSON objects, one per LF-terminated line.
Reject malformed arguments, a missing parent, non-regular target or malformed
existing JSONL. Use ordinary file/path APIs; no physical-identity guarantee is
added. Operators use one spelling, one output instance per file, and never
modify/move/truncate a file while in use. Multiple routes in that instance may
share a file. An empty existing file is valid.

Destination validation is read-only and creates nothing. It checks the schema
and observable path conditions; permissions can still change and actual write
capability is established only by opening for delivery. Serialize deliveries,
open with create/append, write one complete JSON object plus LF, then close.
The record contains exactly `delivery_id`, `input_plugin`, `subscription_id`,
`context`, `external_data`, `permission_notice` from the request and
`observed_at`, a UTC RFC 3339 timestamp assigned when constructing the record.
No destination work occurs before `deliver`. IDs and context are unmodified.
The normal observation interface is reading the UTF-8 JSONL file with ordinary
file tools; no harness endpoint or acknowledgement simulator is involved.

Successful full write and close produce `accepted`. Failure before opening or
writing that confirms no record was appended produces `failed`; write or close
failure after a write may have begun produces `unknown` and a diagnostic. Do
not retry, repair a partial record or truncate output. Operators preserve and
inspect a damaged file before deliberate recovery. No fsync, crash durability,
exactly-once across failures or external-writer safety is promised. The file
is user-visible output rather than persisted hub registration/queue state.
Its retention and removal are operator responsibilities.

## Decisions and alternatives

| Decision | Alternative and tradeoff |
| --- | --- |
| go-plugin gRPC for both roles | Embedded delivery violates FR-008/C-001. Custom launch/transport duplicates the selected framework. Go net/rpc impairs language neutrality. |
| One BytesValue/strict-JSON Session per role instance | Generated product messages add tooling; separate watch/event channels add ordering work. Preserve exact JSON numbers with one stream. |
| Protocol version 2 at the existing document path | Reusing v1 would silently change ready and application schemas. Reject old plugins visibly; no compatibility adapter. |
| Stateless destination validation before watch | Input-first can start watches for rejected destinations; transactional reservation/cancellation would add excluded lifecycle complexity. A later output failure can leave bounded inert watches. |
| Destination arguments captured by daemon and sent per delivery | Output-owned routing tables duplicate core control and need cleanup transactions. Larger messages cost repeated encoding but leave service identity opaque. |
| File input and append-only local output | Live agents/services prevent independent core E2E; stdout is reserved by framework negotiation and is less useful as a selected destination. File output needs single-writer discipline. |
| Loopback TCP, bounded memory and one delivery worker | Authentication/persistence/parallelism add excluded guarantees. Local callers are trusted; slow delivery delays later events and failures may lose work. |

## Requirement coverage

All IDs are from `requirements/mvp`. V01–V10 refer to the stable case IDs below;
D09 is the final operator walkthrough. Retired FR-005 has no remaining skill
obligation. AC-011 is interface/verification-boundary inspection, not a claim
that any future service plugin works.

| Requirements | Acceptance | Design and verification |
| --- | --- | --- |
| FR-001, C-001 | AC-001, AC-007 | Both-role config/lifecycle; V01 invalid-config/shutdown, V02 alternate-language roles, structural inspection. |
| FR-002 | AC-002 | Two-stage registration and role checks; V02 register, V08 local-control, unit validation matrix. |
| FR-003, FR-004, FR-008 | AC-003 | Actual model input/output route to file A; V03 context/source/ID and recipient-in-context isolation. |
| FR-006 | AC-004, AC-005 | V04 malformed/foreign events, rejection/unknown, exits of each role and unaffected route; V05 restart; unit failure matrices. |
| FR-007 | AC-010 | V10 equivalent registration, changed output/destination and distinct repeated events; V04 rejection/no retry. |
| NFR-001 | AC-006 | V03 idle zero requests/output, receipt/attempt/observation chronology and source inspection. |
| NFR-002, C-002 | AC-007 | Both-role language-neutral protocol, V02 replacement and source responsibility inspection. |
| NFR-003 | AC-008 | V08 local client, V03 external-data/permission payload, protocol and source inspection. |
| NFR-004 | AC-005, AC-009 | Operating guide, V05 state loss, D09 clean-build README walkthrough. |
| NFR-005 | AC-009, AC-011 | Actual model E2E and independent plugin-host boundary below; D09 normal usage without services. |

## Verification strategy

### Selection and retained coverage

Use unit tests with mocked external capabilities for argument/encoding/state
matrices and exceptional file operations. Integration checks assembled command,
process, stream and real-file boundaries. Preserve eleven stable workflow IDs:
nine hub cases and two manual-input cases. Extend the real file route to cover
model output, avoiding a redundant output command suite. Broader output path,
write and close failures belong in unit tests; one real append/observation path
is sufficient to catch output wiring defects.

Replace former Codex-child assertions with output Session failure cases; remove
Codex acknowledgement, skill discovery and busy-conversation checks because the
approved requirements retired them. They are not deferred acceptance conditions.
The full model route uses actual bundled commands, not a fake recipient. Python
protocol fixtures are restricted to role replacement and failure injection.
Retain one real 30-second output timeout; mock ten-second registration/readiness
deadline matrices. Replace prior implemented acceptance bodies with static
Pending for the new contract, without claiming the old product implements it.

Governed files are `cmd/agent-pulse-hub/*_integration_test.go` for the selected
cases, suite and case-owned feasibility/harness code, corresponding
`cmd/manual-plugin/*_integration_test.go` for its two cases, and fixtures under
those commands' `testdata/`. All Go files use the `integration` build tag.
The complete inventory is `cmd/agent-pulse-hub/testdata/mvp-cases.json`.
Obsolete Codex-only case fixtures may be removed. No production, shared Mage,
build, dependency-version or CI change is part of this design. Reuse the
existing approved Python setup and pins already present in product integration.

Build hub and both model plugins once per hub suite. Use unique temporary
folders and an ephemeral loopback port per case. Release the port-selection
listener before daemon bind; only an observed setup bind collision permits
reselection, at most three times and before registration. All other errors
fail. Use bounded files and supervised direct children. Always stop/kill and
await fixture processes and remove temporary paths in cleanup. Logs identify
commands, source revision, plugin instances, subscriptions, deliveries, UTC
receipt/attempt/observation times and outcomes without logging context by default.

### Selected command workflows

A/B are distinct local output files, not service identities. P/Q are distinct
input instances; O/R are distinct output instances. Controlled plugins implement
the public protocol through real go-plugin processes. The following caps include
case cleanup; setup/compilation and suite cleanup have separate reserves below.

| Case ID | Concrete inputs, actions and observable results | Why integration / cap / peak children |
| --- | --- | --- |
| `MVP-V01-INVALID-CONFIG` | Truncated config, missing input executable, then missing output executable: exit one, diagnostic, no plugin launch or delivery. | File/CLI startup wiring; remaining matrix unit. 4 s / 2. |
| `MVP-V02-REGISTER` | Replace both roles with independent Python implementations. Hold destination result: no watch or success; reject once: no watch. On fresh request accept destination, hold watch result: no active route. Release then emit immediately: same subscription/context delivered to chosen destination args. Reject a later watch: no active subscription. | Both executable replacement, two-stream registration ordering, non-Go interoperability. 10 s / 4. |
| `MVP-V03-ROUTE` | Actual hub/client/manual/model-output. Register A twice: same ID. Declare one-second idle: zero delivery requests and no A/B record. Publish context containing `PULSE-ROUTE` and text naming B/output R through normal sibling rename. Exactly one A JSONL record preserves source, ID, full context and external-data/permission fields; B absent. Trigger/claim removed. Retain receipt/attempt/observed times and source evidence of no idle work. | Complete normal model E2E, real observation interface. 6 s / 4. |
| `MVP-V04-INVALID-EVENT` | P->O and Q->R routes. P sends malformed JSON, unknown ID, Q's ID then valid event: only valid delivers. Exit P: new P registrations/events fail, Q->R still works. Exit O with an additional Q->O route active: new events on it rejected, Q->R still works. Logs name affected role/instance/subscription; no restart. | Stream recovery, source binding, both-role failure isolation. 8 s / 6. |
| `MVP-V04-DELIVERY-OUTCOME` | Output returns failed for one event, unknown for another, then holds a third beyond the actual 30-second deadline. Exactly one request per event; failures and unknowns recorded distinctly, timeout makes output unavailable and is unknown. No retry; standard cleanup observes configured output exit. | Result/timeout wire behavior; all outcome parsing matrices unit. 35 s / 4. |
| `MVP-V05-RESTART` | Register, stop, restart same config; controlled input emits old ID then newly registered ID to actual model output. Old rejected; new delivered once with different ID. | Daemon lifetime and state loss. 5 s / 4. |
| `MVP-V10-DUPLICATE` | Lose first client response while result gated; two concurrent equivalent registrations share validation/watch and ID. Explicit repeat returns same ID. Change destination args, then output name: distinct IDs and original recipient retained (fixture accepts multiple watches). Two identical events on original route yield two delivery IDs in admission order, one request each. | Concurrency/response loss and routing key wiring. 8 s / 6. |
| `MVP-V01-SHUTDOWN` | Start both real model plugins. Public stop acknowledges; listener closes; daemon exits zero and configured-client exit records confirm each process stopped. Host awaits children. | Standard lifecycle wiring; interrupt dispatch unit, foreground interrupt D09. 8 s / 4. |
| `MVP-V08-LOCAL-CONTROL` | Register over loopback; second daemon same address fails before plugin startup. First still accepts fresh registration/event. Non-loopback address rejected before connect/listen. | Bind exclusivity/client wiring; address matrix unit. 5 s / 5. |
| `MVP-V04-MANUAL-INVALID-CONTENT` | Standalone host launches real manual plugin. Publish 8,192 valid bytes, 8,193 bytes, invalid UTF-8 then valid marker. Only valid contexts emit unchanged; errors visible, no truncation/retry. | Real filesystem ingestion and plugin stream; permission faults unit. 5 s / 1. |
| `MVP-V02-MANUAL-PATHS` | Standalone host watches absent absolute path; reject cleaned-equivalent path and existing file. Accept distinct absent path and consume marker. | Real file/watch wiring; alias guarantees excluded. 5 s / 1. |

### Unit and structural coverage

Implementation supplies unit tests beside each responsibility:

- CLI/config/control: strict JSON, encoding, flags, both roles, absent/invalid
  arguments, bounds, addresses, unavailable daemon and uncertain responses.
- Registry: canonicalization of both argument objects, pending deduplication,
  changed output/destination, each rejection/exit race, inert watch retention,
  active/inactive transitions and 1,024/1,025 capacity. Output rejection sends
  no watch; no response loss changes the recipient.
- Plugin adapter: both-role schemas/order, wrong ready role, request correlation,
  ten-second deadlines including blocked send, malformed/late/duplicate results,
  transport corruption, standard Kill on partial start and failure.
- Admission/worker: 128/129 capacity, ownership, unavailable input/output,
  pre-send failed versus possibly sent unknown, late results, exactly one
  attempt, queue discard and admitted-event behavior after input exit.
- Model output: argument/path validation, strict existing JSONL, external-data
  metadata, append/close success, open failure, partial write and close error
  unknown, no implicit retry/truncation. Mock filesystem failure matrices.
- Manual input: path cleaning, missing directory, empty context, claim/read/close/
  remove failures; shutdown stop/interrupt dispatch and awaited configured exits.

Inspect product source and contracts for AC-007/AC-011: both roles use the
framework lifecycle, daemon control remains service neutral, input receives no
destination, and no embedded Codex/API adapter or Codex registration skill is
shipped. Inspect absence of product-specific OS imports, branches, suffix-based
implementations, identity/security APIs and containment; ordinary standard-library
operations and dependency internals are permitted. Check idle daemon/model
paths cannot independently request output work. Record paths, revision and
findings separately from executed tests. Inspection adds no platform matrix.

### Independent plugin verification boundary

A standalone caller may launch exactly one plugin through go-plugin, open its
role Session and use the published protocol without a daemon or another plugin.
For input it sends watch arguments and observes result/events after a stimulus;
for output it validates destination arguments, sends a synthetic external-data
delivery and observes the result and target effect. This caller is test code,
not another product command or plugin. Protocol feasibility hosts and the manual
suite demonstrate that seam; no product-only registration API is needed.

A future service plugin's own check records its revision, service environment,
credentials setup without secrets, stimulus, expected/actual event or delivery,
failures and cleanup. It may contact its own target service but must not depend
on another plugin's service, the daemon or the hub E2E suite. Such evidence
establishes that plugin's integration only. Actual bundled-model E2E establishes
core routing only. Neither substitutes for the other, and this MVP claims no
future service integration success or additional feature plan.

### Feasibility, commands and budget

Reuse module-pinned Go tooling and Python 3.12 with the exact packages in
`build/testdata/plugin-tooling/requirements.txt`, already provisioned by the
product integration CI job under
[Plugin tooling qualification](../aidd/go-development.md#plugin-tooling-qualification).
Use writable temporary storage, no credentials, elevation or external service.
Record actual versions/OS/shell as execution context, not a support promise.

Case-owned feasibility probes launch both independent Python roles through the
real framework and Session, exercise ready/validation/watch/immediate-event/
delivery/result, standard shutdown and one real 30-second unconfirmed output
request. They also qualify loopback bind conflict and ordinary file publication,
append/read observation. These probes qualify fixture mechanics and costs,
not the product's new wiring. Existing commands can be built to bound startup
cost; the slower existing command build is reserved again as a proxy for the
new output command. Implementation measures all three actual binaries within
that reserve. Pending declarations use no nonexistent production symbols.

The hub case caps total 89 seconds. Reserve 20 seconds for warmed-cache tagged
compilation and three common builds, five seconds for final cleanup and five
seconds contingency: 119 seconds inside the unchanged two-minute scoped budget.
The manual suite reserves 35 seconds around ten seconds of cases: 45 seconds.
Peak case children are six, excluding compiler/runner infrastructure. No queue
flood or unbounded trace is needed. Measure feasibility separately; a missed
budget requires investigation or reviewed design/infrastructure prerequisite,
never shortening the product deadline or dropping required cases.

Run normal unit/general integration first to populate module/build caches, then
compile, register and execute the design probes and scoped inventory. Record
cache preparation separately; actual scoped compilation/builds still count.

```text
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
go test -tags=integration ./cmd/agent-pulse-hub ./cmd/manual-plugin -run=^$ -count=1
go test -tags=integration ./cmd/agent-pulse-hub ./cmd/manual-plugin -run=^TestIntegration -v "-ginkgo.dry-run" "-ginkgo.v"
go test -tags=integration ./cmd/agent-pulse-hub -run="^TestPluginSessionFeasibility$|^TestLoopbackControlFeasibility$" -count=1 -v
go test -tags=integration ./cmd/agent-pulse-hub -run=^TestPluginDeliveryDeadlineFeasibility$ -count=1 -timeout=1m -v
go test -tags=integration ./cmd/agent-pulse-hub -run=^TestDesignBudget -count=1 -timeout=2m -v
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/mvp-cases.json <new-report-directory>
```

Unit and integration results remain separate. Static Pending, skipped, missing,
filtered, dry-run and tooling/feasibility results cannot establish product
acceptance. Implementation must pass every scoped ID on the same revision and
retain machine reports/logs. Design readiness requires executed probe,
compilation and registration evidence rather than planned commands.

### Delivery verification

D09 covers the clean-build README walkthrough under AC-009 and NFR-004/005,
using the [operation guide](windows-operations.md) and actual model plugins.
After implementation approval and merge, the maintainer updates the README's
normal usage example and plugin-author links within its established structure.
Record acquisition/revision, environment, versions, build commands, config,
registration result, unique context, A's exact record, B's absence, logs and
shutdown evidence. No test harness entry point, live service, credentials or
coding-agent session may be required. No release artifact is required by this
scope; a source-build acquisition path is sufficient.

Observe a declared idle interval and correlate event receipt, delivery attempt
and file observation without a latency target. Repeat the registration to show
same ID/one watch; stop and restart with new registration, preserving the state
loss/no-retry explanation. Perform public stop and foreground Ctrl+C on separate
live runs and retain configured-plugin exit evidence. Missing or failed steps
remain incomplete; CI alone does not establish README usability. Actual Codex
and future live-service checks are not MVP delivery obligations.

## Migration, operations and risks

Stop the prior daemon, inspect remaining triggers and output files, replace
hub/client and configured plugins together, select the address and register
again. Plugin protocol v1, old `codex_executable` config, session-based CLI and
registration skill are not compatibility layers. Reject old input explicitly.
Implementation removes the daemon's Codex adapter and the bundled
`.agents/skills/register-pulse/` skill; delivery replaces README usage. Keep
specification paths stable. Rollback restores matching binaries/config/instructions
and registers anew; it cannot retract already observed output.

Log bounded sanitized UTC lifecycle/receipt/attempt/outcome records with known
input/output instance, request/subscription/delivery IDs. Do not log raw context
or full arguments by default. Model output deliberately persists context at the
operator-selected file; operators own its privacy, retention and partial-file
recovery. Trusted plugins can access local resources. Unknown outcomes may
already have produced output; publishing another trigger is a distinct event,
not an automatic recovery mechanism.

No unresolved product decision is deferred to implementation. Human review
covers two-stage stateless validation, bounded inert watches, protocol migration,
local-caller trust, single-writer file limitations and once-only unknown outcomes.
Implementation verifies all scoped cases and source boundaries; delivery verifies
the normal README path. Begin each only after its prerequisite approval and
merge under the [AIDD playbook](../aidd/README.md).

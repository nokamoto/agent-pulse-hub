---
type: Design
title: Minimal external event delivery MVP
description: A local daemon uses go-plugin to route language-neutral events to existing Codex conversations without agent polling.
sources:
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Minimal external event delivery MVP
---

# Minimal external event delivery MVP

## Context and scope

This design covers [requirements/mvp](../requirements/mvp.md), FR-001–FR-007,
NFR-001–NFR-004, C-001 and AC-001–AC-010. A foreground Go daemon/client,
manual plugin and registration skill connect local watches to existing Codex
conversations. Plugins are trusted programs selected by the user. Subscriptions
and queued events exist only in memory. No persistence, retries, automatic plugin
restart, individual cancellation or additional service integration is introduced.

Product code and contracts are OS neutral. Delegate plugin communication, launch
and termination to `hashicorp/go-plugin` v1.8.0, using gRPC v1.84.0 and protobuf
v1.36.12. Dependency-internal platform handling is permitted. Local-user isolation,
descendant termination and physical file identity guarantees are outside scope.
No additional support or test matrix is required.

## Specification documents

| Document | Readers and purpose | Authority |
| --- | --- | --- |
| This document | System implementers and maintainers | Architecture, configuration, local control, state, manual-plugin behavior, delivery, verification and migration. |
| [Plugin protocol v1](plugin-v1.md) | Plugin authors in any language and adapter implementers | Application gRPC service, encoding, bounds, ordering, handshake and lifecycle integration. It derives from this design; requirements govern scope. |
| [Operation guide](windows-operations.md) | Operators and delivery evaluators | Derivative build/start/register/trigger/stop instructions. The existing path stays stable; the guide adds no independent product contract. |

Configuration, CLI and skill contracts stay here. The separate plugin specification
gives independent authors a complete interoperability contract. Its contract and
relationship are reviewed with this design, not deferred to implementation.
Routine types, package internals, buffers and diagnostic wording may vary while
preserving these contracts and the [Go conventions](../aidd/go-development.md).
Conflicts require design correction. Provenance and executed results belong in
the PR, not in the enduring specification.

## Architecture and responsibilities

```text
Current Codex conversation -> registration CLI -> loopback control listener
                                                daemon registry
                                                      |
                                             go-plugin client
                                                      |
                                      gRPC bidirectional Session
                                                      |
                                           configured plugin
                                                      |
                                      event(subscription, context)
                                                      |
                                  validation -> bounded delivery queue
                                                      |
                                       Codex CLI queue adapter
                                                      |
                                      registered existing conversation
```

The daemon owns subscription IDs and recipients. Plugins receive watch arguments
and subscription IDs, never session identity or agent credentials. Each private
go-plugin client/stream binds events to the configured source name. The delivery
adapter owns Codex-specific invocation. Replacing a configured executable requires
no daemon code change.

## Configuration and local control

Configuration is strict UTF-8 JSON with exactly `codex_executable` and `plugins`.
Each plugin has exactly a unique nonempty `name`, absolute `executable` and
string-array `args`. No shell strings or environment expansion are interpreted.
Validate the entire file, including executable existence, before starting plugins.
Probe Codex with `--version`, reserve the listener, then start configured plugins.
Probe/listener failure is fatal. Individual handshake/readiness failure leaves
that plugin unavailable while other plugins remain usable. Report every startup
result before registration availability.

Use the standard TCP listener on literal IPv4 loopback, default
`127.0.0.1:47831`. All hub subcommands accept optional `--address
127.0.0.1:<port>`, with port 1–65535. Reject hostnames, wildcard/non-loopback
addresses and port zero before connecting or listening. Operators may choose a
different port but must pass it to every client. A second daemon on the same
address fails before starting plugins. Different addresses are independent
instances; no global singleton guarantee is supplied.

This is local control, not authentication. Local processes may register or stop
the daemon. No secret, user identity verification or user-isolation guarantee is
supplied. The daemon never listens on an external interface.

One connection carries one newline-terminated JSON request/response, then closes.
Use strict UTF-8 without BOM; reject duplicate keys, invalid Unicode, trailing
data and non-object JSON. LF or CRLF terminates a record, at most 65,536 bytes
including delimiter. Reads/writes have a 15-second deadline, allowing the
ten-second watch deadline. No automatic retry is allowed.

Registration has exactly `version: 1`, `op: "register"`, nonempty `plugin`, UUID
`session_id` and object `watch_args`. Success has exactly `ok: true` and nonempty
opaque `subscription_id`. Failure has exactly `ok: false`, stable `code` and a
nonempty diagnostic `message` of at most 1,024 decoded UTF-8 bytes. Version is
integer token `1`; session IDs are UUIDs, never names to look up. Validate the
encoded outgoing plugin watch bound before sending any bytes, so oversized input
cannot stop a healthy plugin.

| Code | Condition |
| --- | --- |
| `invalid_json` | Malformed JSON, encoding or duplicate keys. |
| `unsupported_version` | Missing or unsupported version. |
| `invalid_request` | Unknown/missing/mistyped fields except identity, invalid operation, non-object arguments. |
| `missing_session` | Absent, empty or invalid UUID identity. |
| `unknown_plugin` | Name not configured. |
| `plugin_unavailable` | Plugin unavailable, exited, timed out or unable to receive the watch. |
| `watch_rejected` | Plugin rejects arguments or cannot serve the watch. |
| `resource_exhausted` | Registration capacity or encoded request bound exceeded. |

Any applicable validation code is sufficient when several fields are invalid.
Transport failure need not return an error object. Once a request may have
committed, a missing/invalid response is uncertain. Explicit identical registration
can resolve this while the same daemon/plugin remains available.

Stop is exactly `{"version":1,"op":"stop"}` with response `{"ok":true}`.
Close new registration/event admission, acknowledge, close the listener and run
normal shutdown. The response confirms a request, not completed process exit;
operators wait for daemon exit and lifecycle records. Response loss is uncertain,
with no automatic retry or fallback to a different endpoint.

## Commands and registration skill

| Package / command | Invocation and effect |
| --- | --- |
| `cmd/agent-pulse-hub` | `daemon --config <absolute JSON file> [--address <address>]` runs in foreground. Ctrl+C and local stop enter the same shutdown coordinator. |
| Same command | `register --plugin <name> --session-id <UUID> --watch-args-file <absolute JSON file> [--address <address>]` performs one registration. |
| Same command | `stop [--address <address>]` requests normal shutdown. |
| `cmd/manual-plugin` | No arguments; launched by go-plugin using the public plugin interface. |

Required flags occur once, optional flags at most once, in any order. Reject
unknown, duplicate, missing or positional arguments. Input files are absolute
existing regular files containing strict UTF-8 JSON without BOM. The watch file
contains the arguments object itself; preserve exact numbers and string values.
Executable names are build outputs, not platform-dependent product contracts.

Client success writes its exact success JSON plus newline to stdout and exits
zero. Daemon rejection writes its error JSON and exits one. Local input/transport
failure writes bounded stderr, no success object, and exits one. Report possible
committed requests as uncertain. The daemon writes stderr diagnostics, leaves
stdout unused, exits one on fatal startup and zero after normal shutdown.

The runnable skill is `.agents/skills/register-pulse/SKILL.md`. Inputs are absolute
hub executable, plugin name, absolute watch file and optional address; ask only
for missing required inputs. In the current top-level conversation:

1. Read `CODEX_THREAD_ID` and `CODEX_SESSION_ID`; require matching valid UUIDs.
   Missing, malformed or conflicting identity fails without guessing. Never
   delegate identity lookup or registration to a subagent.
2. Invoke register with that identity and supplied inputs. Do not start a daemon
   or plugin.
3. Claim an active watch only on exit zero and a valid success object; otherwise
   report rejection, failure or uncertainty without retry.
4. Explain state loss, no retries and that context is external data under existing
   user instructions/permissions. Link the operation guide.

## Plugin lifecycle and subscription state

Pass each executable/argument array directly to `plugin.Client`, allowing only
gRPC. Use the handshake/key and Session in [plugin-v1](plugin-v1.md). Go servers
use `plugin.Serve`; other languages implement the documented standard handshake,
health/controller services and application stream. Stdout belongs to the library
handshake; stderr is bounded diagnostics. The cookie is a launch guard, not
authentication. Do not add custom stdio framing or containment.

Readiness has one ten-second deadline from launch through negotiation and first ready. Use one Session
per instance and one ordered admission path. Process a positive watch result and
activate its registration before reading the next event. Negative results remove
pending state. Request IDs correlate concurrent watches. Watch timeout, send
failure, stream termination or process exit invalidates that instance and calls
`Client.Kill`; other plugins continue. Never reconnect or restart automatically.
Malformed application messages follow the specification's recoverable rejection
rules; transport corruption terminates the stream and its instance.

Registry entries contain `(plugin instance, subscription_id, session_id,
watch_args, state)`. Generate random IDs without deliberate reuse across runs.
Equivalent registration means same plugin, session UUID and canonical JSON:
object order ignored, array order retained, numbers compared by exact decimal
value without floating-point rounding. Concurrent identical requests share one
pending watch/result. Rejected watches leave no active subscription. Losing the
client response does not roll back an accepted watch.

## Admission, delivery and shutdown

Global capacity is 1,024 pending/active subscriptions and 128 queued/in-flight
events. Plugin message/context bounds are in the specification. Validate source,
active ownership, context and queue capacity before admission. Reject unknown,
foreign, inactive and post-exit events. Each admitted event gets a local delivery
ID and fixed recipient, with one attempt through a single ordered worker.
Identical events remain distinct. Already admitted events retain their recipient
after plugin exit; shutdown discards queued events.

Invoke directly: `codex queue --thread <session_id> --message <envelope>`.
No shell, new conversation, interruption or fallback API. Reject envelopes above
32,768 UTF-8 bytes before launch without truncation. This is a portable input
bound, not a host command-limit guarantee; actual launch failure is still failed
delivery. Deadline is 30 seconds; independently bound stdout/stderr to 65,536
bytes each. Overflow after launch is unknown and terminates that invocation.

The envelope names plugin/subscription and JSON-encodes context as external data.
The fixed wrapper says it may be untrusted and grants no permission beyond the
user's existing instructions. Context cannot choose a recipient; this is not a
prompt-injection guarantee.

`accepted` requires exit zero and exactly `Queued message <message UUID> for
thread <target UUID>.`, with at most one final line ending, valid UUIDs and the
registered target. Queue acceptance is not conversation acknowledgement.
Prelaunch failure is `failed`. Launched nonzero exit, timeout, lost/unrecognized
output or mismatched target is `unknown`. No outcome retries. Record the actual
Codex version without an allowlist. Controlled executable fixtures verify adapter
recognition; actual compatibility and busy behavior remain delivery checks.

Normal stop closes admission/listener, discards queued events with reasons,
cancels in-flight delivery (unknown if launched), cancels Sessions and calls
`Client.Kill` for each started/partially started client. Record configured-process
exit observations using the library's `Exited` state. Standard graceful shutdown
and fallback suffice; no product shutdown message, grace timer, descendant
enumeration or containment guarantee is added. Await configured clients and any
directly launched delivery process. Ctrl+C and stop share this coordinator;
unit tests cover both dispatch paths, CI drives stop, delivery observes interruption.

## Manual test plugin and file lifecycle

Arguments contain exactly string `trigger_file`: an absolute, currently absent
file in an existing writable directory. Use standard `filepath.IsAbs`, `Clean`
and ordinary file operations. Conflict key is the cleaned path's exact string;
a second watch with that key is rejected. Do not resolve physical identities,
aliases or case equivalence. Operators use one spelling per trigger and must not
move its parent, use alternate aliases or publish concurrently. Directory/access
failure rejects registration; subsequent file faults remain visible.

The operator writes nonempty UTF-8 plain text, at most 8,192 bytes, to a temporary
sibling file, closes it, confirms the trigger is absent and renames it. This
assumes one publisher; no race-proof no-replace or alias guarantee is supplied.
The plugin polls only files, claims a trigger by renaming it to a unique sibling,
reads at most 8,193 bytes, closes/removes the claim and emits unchanged context.
Read, encoding/size or removal failure logs the claimed path and emits nothing.
Never retry a claimed file; inspect leftovers before another publication. A crash
can lose consumed events. No plugin path polls or invokes Codex.

## Decisions and alternatives

| Decision | Alternative and tradeoff |
| --- | --- |
| Required go-plugin with gRPC | Custom communication/process management violates C-001. Go net/rpc impairs language neutrality; gRPC supplies standard language runtimes. |
| One Session with BytesValue/strict JSON | Generated product messages add tooling prerequisites. Wrappers preserve exact JSON numbers. Unary Watch plus Events needs a separate ordering protocol; one stream preserves result/event order. |
| Loopback TCP and stop operation | User-specific transport/identity and signal harnesses add unnecessary code. TCP costs port selection and local-caller trust. Stop exposes the existing shutdown action without service management. |
| Exact cleaned trigger paths | Physical identity adds excluded guarantees. Operators own alias discipline; lexical duplicate prevention stays deterministic. |
| In-memory state and one worker | Persistence/parallelism is unnecessary for scope. Slow commands delay later events; crashes lose work. |
| Strict queue acknowledgement | Exit-code-only success could misreport delivery; unknown outcomes and no retry preserve the failure boundary. |

## Requirement coverage

All IDs below belong to `requirements/mvp`. V01–V10 are coverage groups; `MVP-*`
IDs identify integration cases. V07 covers NFR-002/AC-007 interoperability: `MVP-V02-REGISTER`, `MVP-V04-INVALID-EVENT`, and the plugin unit conformance and structural inspection below. V06 is idle behavior and V09 the operating walkthrough; their evidence is V03/D06 and D09 respectively. D02–D10 are separate delivery checks.

| Requirement | Acceptance | Design and verification |
| --- | --- | --- |
| FR-001, C-001 | AC-001, AC-007 | Configuration/lifecycle; V01 invalid-config/shutdown, V02 interoperability, source inspection. |
| FR-002 | AC-002 | Control/registry; V02 register/manual-paths, V08 local-control; unit rejection matrix. |
| FR-003 | AC-003, AC-004 | Admission/envelope; V03 route, V04 invalid-event, D03 acknowledgement. |
| FR-004 | AC-001, AC-003 | Public manual plugin; V03 and both manual cases. |
| FR-005 | AC-002, AC-003 | Skill contract; unit identity cases, D02/D03. |
| FR-006 | AC-004, AC-005 | Outcomes/invalidation; V04 outcomes/invalid-event, V05 restart. |
| FR-007 | AC-010 | Canonical identity/queue; V10 duplicate, D10 busy target. |
| NFR-001 | AC-006 | File-only idle; V03 and source inspection, D06 real timing. |
| NFR-002 | AC-007 | Plugin specification; V02 Python replacement and source inspection. |
| NFR-003 | AC-008 | Local control/data wrapper; V08, V03, D02/D03. |
| NFR-004 | AC-005, AC-009 | Operation guide; V05, D09 walkthrough. |

## Verification strategy

### Selection and retained coverage

Choose the least costly sufficient level. Unit tests mock external capabilities
for input/decision matrices; integration verifies assembled commands, streams,
exits and files. Retain eleven workflow slots. Retire `MVP-V08-SAME-USER` because
the source requirement retired user isolation; replace it with
`MVP-V08-LOCAL-CONTROL`. Preserve other IDs while revising transport expectations.
Earlier containment/file-identity/protocol guarantees are superseded by the
requirements, not silently dropped to make implementation pass.

Replace old acceptance bodies and obsolete case-owned fixtures with static
Pending for the revised contract. Pending never means passed behavior. Governed
paths are `cmd/agent-pulse-hub/{mvp,suite,plugin_feasibility}_integration_test.go`,
`cmd/manual-plugin/{mvp,suite}_integration_test.go` and hub `testdata/` fixtures.
Retire obsolete `harness_integration_test.go` and its case-owned console/script
fixtures, not the shared Mage runner. Consolidate all eleven cases into
`cmd/agent-pulse-hub/testdata/mvp-cases.json`, retiring the additional old inventory.
No dependency, build, CI or shared-runner change belongs in this design.

Implementation builds hub/manual once per suite. Use the compiled suite test
binary as a controlled Codex child: record arguments/time, return exact success
or nonzero, or block until cancelled. Independent Python fixtures use published
Session contracts and pinned dependencies only. Registry, commands, local/plugin
adapters, manual plugin and delivery adapter remain real. No test-only product
injection API is added.

### Selected command workflows

Each Pending case refers to this concrete contract. A/B are distinct fixed UUIDs,
only A registered unless stated. Use unique temporary directories and a selected
free loopback port. Release the selection listener before daemon bind; only an
observed setup bind collision permits reselection, before any registration, at
most three times. All other errors fail the case.

| Case ID | Inputs, actions and observable results | Integration necessity / runtime cap including cleanup / peak children |
| --- | --- | --- |
| `MVP-V01-INVALID-CONFIG` | Invoke truncated config, then missing executable. Both exit one with diagnostics, no plugin start or work request. | File/CLI startup wiring; matrix stays unit. 4 s / 2. |
| `MVP-V02-REGISTER` | Configure independent Python executable/args. Hold positive result for A: client pending, no delivery. Release result then immediate event: one ID and exact A delivery. Rejected arguments produce error/no active subscription. | Non-Go replacement, gRPC ordering/activation. 8 s / 4. |
| `MVP-V03-ROUTE` | Real manual plugin, register A twice: same ID. Declare 1 s idle/zero work. Publish `PULSE-ROUTE` by sibling rename. One A attempt with exact source/ID/context/data wrapper, trigger/claim removed, receipt/attempt times, no B call. Inspect plugin idle path for independent work capability. | Complete file-to-adapter path. 5 s / 4. |
| `MVP-V04-INVALID-EVENT` | Two fixture instances. A emits malformed JSON, unknown ID, B's ID, then valid event: only valid delivers. End A stream/process; log A/IDs unavailable, reject new A registration and unread post-exit events. B still registers/emits. | Message recovery, source binding and process isolation. Transport corruption matrix stays unit. 6 s / 5. |
| `MVP-V04-DELIVERY-OUTCOME` | One event to nonzero child, another to child held past actual 30 s deadline. Both unknown, one attempt each; held child exits on cancellation, no retry. | Real timeout/adapter classification. 35 s / 4. |
| `MVP-V05-RESTART` | Register, stop, restart same config. Emit old ID then fresh ID: old rejected, fresh delivered once, IDs differ. | Process/state lifetime. 5 s / 4. |
| `MVP-V10-DUPLICATE` | Close first control response before gated result. Two concurrent equivalent requests, release one result, repeat publicly: one watch, same returned IDs. Emit identical event twice: two delivery IDs, admission order, one attempt each. | Connection loss/concurrency. Canonicalization matrix stays unit. 6 s / 5. |
| `MVP-V01-SHUTDOWN` | Ready fixture and real manual plugin; public stop acknowledges, listener closes, daemon exits zero, configured-client exit records confirm standard cleanup. Fixture host awaits child termination. Unit interrupt dispatch and delivery interruption supplement this path. | Standard lifecycle/public shutdown without descendant harness. 8 s / 4. |
| `MVP-V08-LOCAL-CONTROL` | Register through loopback; second daemon on same address fails before plugin startup. First remains usable for fresh registration/event. Non-loopback address rejected before connect/listen. | Bind exclusivity/client wiring. Address matrix stays unit. 5 s / 4. |
| `MVP-V04-MANUAL-INVALID-CONTENT` | Host real manual plugin through go-plugin. Publish 8,192 valid bytes, 8,193 bytes, invalid UTF-8, then valid marker. Only valid contexts emit unchanged; invalid inputs log failures, no truncation/retry. | Real file ingestion/stream; permission faults mocked. 5 s / 1. |
| `MVP-V02-MANUAL-PATHS` | Host real manual plugin; watch absent absolute path. Reject second spelling cleaned to same exact path and already-existing file. Accept distinct absent path, consume one marker. | Filesystem/watch wiring; alias guarantees excluded. 5 s / 1. |

### Unit responsibilities and structural inspection

Implementation supplies ordinary Go tests beside each responsibility:

- CLI/config/control: strict JSON/Unicode, unknown/duplicate fields, flags,
  addresses, missing identity, connection failure and uncertain responses.
- Registry: exact canonicalization, pending/rejected/concurrent transitions,
  1,024/1,025 capacity and oversized watch rejection before sending.
- Plugin adapter: specified schemas/order/bounds, mocked readiness/watch deadlines,
  unknown/late results, stream/send failure, invalidation and standard Kill on
  partial startup/cancellation.
- Admission/delivery: ownership, 128/129 capacity, post-exit versus admitted events,
  ordered once-only worker, shutdown loss, outcome recognition, 32,768/32,769-byte
  envelope and 65,536/65,537-byte output boundaries.
- Manual plugin: cleaned exact paths, missing directory, empty context and
  claim/read/close/remove failure without event or retry.
- Shutdown: stop/interrupt dispatch to one coordinator, admission closure,
  cancellation and awaited direct-child exit. Trust dependency lifecycle rather
  than retesting its internal containment.

For AC-007 inspect all product source, build constraints, dependencies and
contracts: no product OS-specific imports, branches, suffix-based implementations,
identity/security APIs or containment remain. Runtime filename discovery and
ordinary standard-library operations are permitted; dependency internals are
excluded. Inspect absence of agent-specific/GitHub plugin fields and independent
idle plugin work capability. Record revision, paths, findings and resolutions in
implementation evidence. This is not an additional execution matrix.

### Feasibility and execution budget

The selected cases require module-pinned tooling, writable temporary storage and the
Python 3.12 environment from [Plugin tooling qualification](../aidd/go-development.md#plugin-tooling-qualification).
Provision that environment in the product integration job before enabling the dependent cases; a tooling job does not share its environment with the product job. This setup is a separate approved prerequisite under the AIDD verification boundary. No real Codex, credentials, elevation or live service is needed. Record actual
tool versions without prescribing an OS. Health qualification proves dependencies
only, not application behavior.

Case-owned `plugin_feasibility_integration_test.go` and `testdata/plugin-session.py`
exercise real Go/Python application Session processes: ready, watch/result,
immediate event order, rejection and standard shutdown. A standard-library
loopback roundtrip/bind-conflict probe qualifies local control construction.
These executable design probes are separate from Pending `MVP-*` acceptance.
Record commands, elapsed time and cleanup in the design PR. Pending declarations
compile without nonexistent production symbols.

Reserve 20 s common build/setup, 5 s final cleanup and 10 s contingency per suite.
Hub case caps total 82 s, yielding 117 s inside the existing two-minute scoped
budget; manual cases total 10 s, yielding 45 s. Peak product/fixture children are
five, excluding compiler/runner infrastructure. Use bounded files and no queue
flood. Report warm-cache dependency preparation separately from execution.
A missed budget requires investigation or reviewed revision, never reduced
product deadlines or dropped cases. Ten-second timeout branches use mocked time;
only delivery tests retain one real 30-second deadline. Feasibility probes run
separately with their own measured budget.

```text
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
go test -tags=integration ./cmd/agent-pulse-hub ./cmd/manual-plugin -run=^$ -count=1
go test -tags=integration ./cmd/agent-pulse-hub ./cmd/manual-plugin -run=^TestIntegration -ginkgo.dry-run -ginkgo.v
go test -tags=integration ./cmd/agent-pulse-hub -run="^TestPluginSessionFeasibility$|^TestLoopbackControlFeasibility$" -count=1 -v
go run build/mage.go -d build -w . integration cmd/agent-pulse-hub/testdata/mvp-cases.json <new-report-directory>
```

Unit and integration results are separate. General integration/dry-run permits
Pending and does not establish completion. The scoped command must pass every
ID after implementation; Pending/skipped/missing/filtered cases cannot satisfy
it. Retain machine reports/logs with the existing runner. Design readiness needs
executed feasibility and tagged compilation/registration, not planned commands.

### Delivery verification

The maintainer executes these on the merged implementation with actual Codex and
the [operation guide](windows-operations.md). Record environment/revision/tool
versions, commands, logs and failures. A/B are existing conversations; only A is
registered and has advance permission to acknowledge the marker. Never register
from a subagent or infer identity from history.

| Check / acceptance | Required observations and evidence |
| --- | --- |
| D02 / AC-002, AC-008 | Discover/invoke skill in top-level A, validate identity and retain ID. Missing/conflicting identity fails without guessing. Inspect permission wording. |
| D03 / AC-003, AC-008 | Publish unique marker: A acknowledges source/ID/context without another user message; B receives nothing, no new conversation. Retain A/B views, trigger and queue result separately. |
| D06 / AC-006 | Declared idle interval, daemon identity/start-end UTC/zero work trace plus manual-plugin source inspection. Correlate event receipt, attempt and real response; no latency target. |
| D09 / AC-001, AC-005, AC-009 | Clean build/full walkthrough, public stop plus foreground interruption through same coordinator, configured exit records and daemon exit. Restart needs new registration; retain versions, transcript, state loss/no-retry observations. |
| D10 / AC-010 | Duplicate same ID/one attempt. Trigger during A's work: uninterrupted work then handling, or refusal without retry. Retain actual chronology and outcome. |

CI cannot establish skill discovery, session environment, real Codex compatibility,
acknowledgement or busy behavior. Those acceptance portions remain incomplete
until delivery.

## Migration, operations and risks

Stop the old daemon, inspect leftover trigger files, replace daemon/client and
all configured plugins together, select the local address and re-register.
Previous JSON-lines is not a compatibility layer; no dual transport or restored
IDs. Update the skill invocation/link during implementation of this defined
contract. Keep document paths stable. Rollback stops the daemon and restores
matching binaries/instructions before re-registering; accepted Codex work cannot
be recalled.

Log UTC lifecycle, plugin/request/subscription/delivery IDs, receipt, rejection,
attempts and outcomes. Bound/sanitize diagnostics; do not log raw context or full
commands by default. Context reaches Codex and may appear in process arguments.
Operators own trigger/log retention and inspect unknown outcomes before another
distinct event.

The product decisions proposed for human review are local-caller trust,
exact-path limitations, memory-only loss and once-only unknown outcomes.
The design author must verify the required product-CI Python environment and
measured complete-suite budget before readiness. Implementers pass scoped cases
and source inspection; the maintainer owns real-service checks. Implementation
begins only after human approval and merge under the
[AIDD playbook](../aidd/README.md).

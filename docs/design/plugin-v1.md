---
type: Design
title: Agent Pulse Hub plugin protocol v2
description: Language-neutral input and output contracts carried by hashicorp/go-plugin gRPC communication.
sources:
  - id: mvp-design
    resource: design/mvp.md
  - id: bundled-design
    resource: design/bundled-plugins.md
  - id: runtime-state-design
    resource: design/trigger-runtime-state.md
---

# Agent Pulse Hub plugin protocol v2

## Purpose and authority

This is the authoritative application interface for input and output plugin
authors in any language, daemon implementers, and interoperability testers.
It derives from the [governing MVP design](mvp.md#specification-documents),
which owns configuration, registration control, subscription identity, event
admission, routing decisions, and the bundled model-plugin profiles. This
specification owns handshake configuration, the gRPC application service,
message encoding, exchange ordering, and application deadlines.
The optional runtime-state extension derives from
[trigger runtime state](trigger-runtime-state.md), which owns state storage,
checkpoint scheduling, coordinated shutdown, and recovery.

The daemon owns integration-independent control and decisions. Plugins carry
out requested service-specific operations and report results. An input plugin
continues a requested watch and reports matching events; it need not receive
another request for each event. An output plugin owns destination validation,
service-specific identity, delivery, and interpretation of service acceptance.
Neither the daemon nor the input contract requires a coding-agent session or
a particular service schema. These boundaries implement
[requirements/mvp](../requirements/mvp.md) FR-008, NFR-002, NFR-005, and C-002.

The stable filename `plugin-v1.md` identifies this specification document.
Its application and framework-negotiation version is now **2**. Version 1
input-only plugins are incompatible and must be replaced; there is no
compatibility mode or fallback.

Bundled plugins use the user-facing roles **trigger** (input) and **action**
(output), with source and usage materials under `plugins/triggers/<name>/` and
`plugins/actions/<name>/`. The [bundled migration design](bundled-plugins.md)
governs that layout. Directory terminology does not change version 2: literal
role fields and dispense names remain `input` and `output`.

## Framework and transport

Use module-pinned `github.com/hashicorp/go-plugin` v1.8.0, gRPC v1.84.0, and
protobuf v1.36.12. Only `plugin.ProtocolGRPC` is allowed. The framework launches
the executable with its configured argument array and stops the configured
process. Do not introduce a product-owned launcher, containment mechanism,
stdio application transport, or process-shutdown RPC. Dependency-internal OS handling
is permitted; descendant-process termination is not a product guarantee.

| Setting | Required value |
| --- | --- |
| `HandshakeConfig.ProtocolVersion` | `2` |
| `HandshakeConfig.MagicCookieKey` | `AGENT_PULSE_HUB_PLUGIN` |
| `HandshakeConfig.MagicCookieValue` | `plugin-v2` |
| `PluginSet` key and `Dispense` name | `input` for an input instance; `output` for an output instance |
| `ClientConfig.AllowedProtocols` | Only `plugin.ProtocolGRPC` |
| gRPC service | `agentpulse.plugin.v2.Plugin` |
| Full application method | `/agentpulse.plugin.v2.Plugin/Session` |

The cookie detects a wrong launch entry point; it provides no authentication
or isolation. The host constructs `plugin.Client`, obtains its protocol, and
dispenses the name matching the configured role. A Go plugin uses
`plugin.Serve` and `GRPCPlugin.GRPCServer`. Stdout belongs to framework
negotiation and framework-managed output; bounded diagnostics go to stderr.
The daemon attributes both process and framework-forwarded stderr to the
configured plugin through `ClientConfig.Stderr` and `SyncStderr`.

## Application service and encoding

```proto
syntax = "proto3";
package agentpulse.plugin.v2;

import "google/protobuf/wrappers.proto";

service Plugin {
  rpc Session(stream google.protobuf.BytesValue)
      returns (stream google.protobuf.BytesValue);
}
```

The host opens one bidirectional `Session` per configured instance. It
multiplexes requests for that instance. Both peers serialize outgoing messages;
gRPC preserves order within each direction. The host processes incoming
messages in that order. A second active Session is rejected with gRPC
`ALREADY_EXISTS` without replacing the first. Generated bindings or direct
gRPC method registration are allowed; no custom protobuf messages are needed.

Each binary `BytesValue.value` contains exactly one strict UTF-8 JSON object
of at most 65,536 bytes. There is no delimiter, JSON-lines framing, or base64
application encoding. Set the gRPC encoded-message bound to 65,540 bytes and
check the JSON payload bound separately. The four extra bytes cover the
maximum protobuf field envelope.

Reject BOMs, invalid UTF-8, unpaired surrogate escapes, duplicate keys at every
object depth, non-object payloads, unknown fields, missing/null/mistyped fields,
and wrong-direction or wrong-role message types. Names are case-sensitive.
Every message has exactly its defined fields, including `version` with the
JSON number token `2`. Preserve exact decimal JSON numbers in argument objects;
do not round through binary floating point. Nonempty strings contain at least
one Unicode scalar value and are not automatically trimmed.

Discard and log malformed size-bounded plugin messages, then process the next
gRPC message. Malformed results do not complete or extend pending requests.
An over-limit payload, transport/decoding failure, or stream closure makes that
instance unavailable. Invalid daemon messages cause the plugin to report an
error and exit without guessing a watch, destination, or delivery. The daemon
checks every outgoing payload's encoded size before sending it. A rejected
oversized local request does not stop a healthy plugin.

## Message schema

Every application message has `version` and `type`, plus the following fields.
The listed direction and role are mandatory. Request IDs are unique within
an instance lifetime, including across operation types. Request, subscription,
and delivery IDs are opaque strings; recipients echo them without parsing or
normalization. The daemon generates fresh IDs for new operations and routes.
[Local persistence](state-persistence.md) deliberately restores subscription
IDs across daemon runs; request and delivery IDs remain fresh. A plugin does
not manufacture them.

| Direction / role / type | Additional fields | Meaning |
| --- | --- | --- |
| Plugin to host / either / `ready` | `role`: `input` or `output`, matching configuration and dispense name; input may additionally include `capabilities` as specified below | First message, exactly once, when the instance can serve its role. |
| Host to plugin / input / `watch` | `request_id`, `subscription_id`: nonempty strings; `watch_args`: object | Request a continuing watch. |
| Plugin to host / input / `watch_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: nonempty string only when false | Accept or reject the requested watch. |
| Plugin to host / input / `event` | `subscription_id`: nonempty string; `context`: nonempty string, at most 8,192 decoded UTF-8 bytes | Report external data from an accepted watch. |
| Host to plugin / output / `validate_destination` | `request_id`: nonempty string; `destination_args`: object | Validate arguments without establishing a watch or performing destination work. |
| Plugin to host / output / `destination_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: nonempty string only when false | Accept or reject the argument contract; create no registration state. |
| Host to plugin / output / `deliver` | `request_id`, `delivery_id`, `input_plugin`, `subscription_id`: nonempty strings; `destination_args`: object; `context`: nonempty string, at most 8,192 decoded UTF-8 bytes; `external_data`: `true`; `permission_notice`: exact string below | Perform one delivery to the registered destination. |
| Plugin to host / output / `delivery_result` | `request_id`: nonempty string; `status`: `accepted`, `failed`, or `unknown`; `error`: nonempty string required for `failed` and `unknown`, forbidden for `accepted` | Report the result of the matching delivery. |

The exact permission notice is
`External data; no additional permissions are granted.`
These fields identify context as external data and grant no authority beyond
the recipient's existing instructions. They do not claim prompt-injection
isolation. Input event content never selects an output instance or overrides
registered destination arguments.

The payload bound applies to all IDs, argument objects, and error strings.
An instance may reject arguments outside its own documented schema. The
public contract contains no GitHub fields, Codex identifiers, or daemon-owned
service-specific validation rules.

The [Codex Queue action profile](codex-queue.md) defines its own destination
schema, CLI queue acceptance and registration helper. It uses this unchanged
output interface; Codex-specific fields remain inside destination arguments,
and the helper uses the existing local registration client.

## Readiness and registration ordering

Readiness has a ten-second deadline from launch, including negotiation,
dispensing, opening Session, and receiving valid readiness. Configure the
framework startup timeout within this deadline. Role mismatch makes the
instance unavailable. Other messages before readiness and duplicate readiness
are rejected without changing registry state or extending the deadline.

```json
{"version":2,"type":"ready","role":"output"}
```

For a registration, the daemon first sends `validate_destination` to the
selected output instance. It sends no input watch until a matching positive
`destination_result` arrives. Validation is stateless: it neither starts
receiving events nor performs destination delivery. A positive result means
that the arguments are acceptable at validation time; it does not guarantee
future service availability or successful delivery.

```json
{"version":2,"type":"validate_destination","request_id":"request-1","destination_args":{"output_file":"/absolute/path/events.jsonl"}}
{"version":2,"type":"destination_result","request_id":"request-1","accepted":true}
```

The destination deadline is ten seconds from beginning to send the request,
including serialized-send waiting and flow control. Negative validation leaves
no active subscription and no input watch. An unknown, duplicate, or late
request ID never changes registry state.

After validation, the daemon requests the input watch with its own request ID
and the proposed subscription ID. The watch deadline is ten seconds from
beginning to send this request, with the same send/flow-control accounting.

```json
{"version":2,"type":"watch","request_id":"request-2","subscription_id":"subscription-a","watch_args":{"trigger_file":"/absolute/path/trigger.txt"}}
{"version":2,"type":"watch_result","request_id":"request-2","accepted":true}
```

Each plugin sends exactly one matching result. Distinct concurrent requests
may complete in any order. Rejection has `accepted:false` and an `error`;
success forbids `error`. The input completes sending its positive result before
sending the first event, even if the watched condition is already true. The
daemon processes acceptance and commits the registration to local storage
before activating it and processing the next message on that input stream,
provided both plugins remain available. The
[local persistence design](state-persistence.md#commit-and-activation) owns the
save decision. During startup every saved route must be accepted before the
hub opens its registration/event gate; earlier events are discarded, not buffered. Client
receipt of registration success is not a prerequisite for event emission.

If saving fails, or the output becomes unavailable after the watch request has
been issued,
registration cannot become active. An already accepted or uncertain watch
may continue until input shutdown, but its route is an inactive tombstone:
no event is delivered and no registration reuses it. The governing design owns
capacity accounting and duplicate-registration behavior for this state. There
is no per-watch cancellation or new rollback RPC.

```json
{"version":2,"type":"event","subscription_id":"subscription-a","context":"PULSE-MODEL-001"}
```

Events before activation, with an unknown ID, belonging to another input
instance, or involving an unavailable input/output instance are discarded and
logged. Ordinary events have no acknowledgement and expose no delivery result
to the input. Repeated valid events remain distinct; plugins do not retransmit for
confirmation. The daemon's [routing rules](mvp.md) bind each admitted event
to its registered output and arguments.

## Optional trigger runtime state

An input opts in by adding exactly `"capabilities":["runtime_state_v1"]` to
`ready`. Omission means the ordinary v2 contract above; an empty array is also
nonparticipating. Duplicate or unknown capabilities, other array values, or a
capabilities field on output readiness are malformed. The host sends new
messages only to participating inputs. Old v2 inputs keep their exact artifacts
and message shapes. A participating input requires a host supporting this
extension; old strict hosts can reject the additional readiness field.

Every message below includes `version: 2` and `type`. Existing strict encoding,
size, request-ID and subscription ownership rules apply. The state envelope,
32,768-byte bound, schema ownership, storage and recovery are defined by
[runtime state](trigger-runtime-state.md#interfaces-and-data).

| Direction / input message | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to plugin / `state_watch` | `request_id`, `subscription_id`, `watch_args` as for `watch`; `state`: null or state envelope | Replaces ordinary `watch` for participating inputs, even without prior state. Validate and apply state before starting the watch; reply with ordinary `watch_result`. |
| Plugin to host / `state_event` | `subscription_id`, `context` as for `event`; `event_id`: canonical positive decimal string through uint64 maximum | Replaces ordinary `event`; counter strictly increases per watch within this Session. |
| Host to plugin / `delivery_notice` | `subscription_id`, `event_id`: matching strings; `status`: `accepted`, `failed`, `unknown`, or `discarded` | One terminal outcome per valid fresh participating event. `accepted` alone establishes confirmed delivery; `discarded` means not admitted/attempted. |
| Host to plugin / `checkpoint` | `request_id`, `subscription_id`: nonempty strings; `quiesce`: boolean | Apply all earlier notices for this watch before extracting a restorable snapshot. When true, stop emitting before returning and remain quiescent until Session closure. |
| Plugin to host / `checkpoint_result` | `request_id`: nonempty string; `accepted`: boolean; `state`: envelope required only when true; `error`: nonempty bounded string required only when false | Complete only the matching pending checkpoint. Snapshot includes all preceding accepted notices; incompatible/oversized/unextractable state is a failure. |

Checkpoint and restoration error text must identify the operation and reason
without including state payload values or external event context.

The host never sends ordinary `watch` to a participating input or new messages
to a nonparticipant. Participating inputs never send ordinary `event`. An
ordinary nonparticipating input has no delivery notices or progress guarantee.
Input and host serialize processing as well as emission where ordering affects
state: finishing a result send precedes first event emission; consuming a notice
precedes its following checkpoint snapshot. The input may serialize these
operations around its service tasks without pausing other watches.

Each valid fresh `state_event` receives one terminal notice while the Session
remains usable, including `discarded` for a staged startup watch or closed gate.
Notice failure during shutdown is a visible continuity failure, never success.
Invalid, foreign, repeated and pre-watch events are discarded/logged without a
notice. A failed/unknown/discarded update has no completed-notification guarantee;
the host never retries it. An input must not wait indefinitely for its acceptance
or treat a discarded startup event as completed progress. At uint64 exhaustion
report a visible watch failure; never wrap the event counter.

Checkpoint requests are pipelined with one outstanding per watch. A single
ten-second batch deadline includes notice/request sending and all responses;
the [governing design](trigger-runtime-state.md#periodic-checkpoints-and-save-failures)
defines batching and final-stop ordering. A checkpoint rejection or timeout
reports unsaved progress but does not kill an otherwise healthy input; expire
the correlation and ignore late replies. Malformed replies never extend the
deadline. Actual stream/transport failure keeps existing instance-unavailability
behavior. This checkpoint-specific rule does not change ten-second watch/
destination or thirty-second delivery deadlines.
The host drops unsent expired checkpoint requests and ignores late replies;
expired queued requests cannot appear in a later batch. Size validation reserves
the full state allowance with watch arguments at registration/restoration as
specified by the governing design, preserving the overall application bound.

```json
{"version":2,"type":"ready","role":"input","capabilities":["runtime_state_v1"]}
{"version":2,"type":"state_watch","request_id":"r1","subscription_id":"s1","watch_args":{"source":"A"},"state":null}
{"version":2,"type":"watch_result","request_id":"r1","accepted":true}
{"version":2,"type":"state_event","subscription_id":"s1","event_id":"1","context":"U1"}
{"version":2,"type":"delivery_notice","subscription_id":"s1","event_id":"1","status":"accepted"}
{"version":2,"type":"checkpoint","request_id":"r2","subscription_id":"s1","quiesce":true}
{"version":2,"type":"checkpoint_result","request_id":"r2","accepted":true,"state":{"schema":"cursor-v1","payload":{"confirmed":["U1"]}}}
```

## Delivery outcomes and failures

The daemon attempts each admitted event once. If its captured output becomes
unavailable before sending, the daemon records `failed` without sending;
otherwise it sends one `deliver`. Its source name,
subscription ID, destination arguments, and context come from the captured
registration and event. Neither event content nor plugin diagnostics can alter
this route.

```json
{"version":2,"type":"deliver","request_id":"request-3","delivery_id":"delivery-a","input_plugin":"manual","subscription_id":"subscription-a","destination_args":{"output_file":"/absolute/path/events.jsonl"},"context":"PULSE-MODEL-001","external_data":true,"permission_notice":"External data; no additional permissions are granted."}
{"version":2,"type":"delivery_result","request_id":"request-3","status":"accepted"}
```

A delivery has a 30-second deadline from beginning to send it, including
serialized-send waiting and flow control. `accepted` means the output confirms
acceptance under its destination contract; it does not establish a human reply
or durable completion. `failed` means the output can establish that acceptance
under its destination contract did not occur, including explicit rejection
before destination work. `unknown` means that acceptance can neither be confirmed
nor ruled out. No result causes a retry.

A valid `failed` or `unknown` result does not by itself make a healthy output
unavailable. A delivery timeout or transport failure makes that output
unavailable and records an unconfirmed in-flight attempt as `unknown`.
Malformed or mismatched results never count as successful confirmation; a
subsequent valid matching result may still complete before the unchanged
deadline. Destination/watch timeout, failed send, stream failure, and process
exit make the corresponding instance unavailable and invoke `Client.Kill()`.
Other available routes continue. No instance reconnects or restarts automatically.

On normal shutdown, close registration and event admission, discard queued
events under the governing shutdown rules, complete the
[participating-input outcome/checkpoint fence](trigger-runtime-state.md#normal-stop-and-confirmed-delivery-fence),
then cancel stream activity and call
`Client.Kill()` for every started or partially started instance. Input plugins
release watches and outputs cease operations when their stream/server stops.
The framework provides graceful shutdown and fallback termination. Observe
configured-process exit; stream closure alone is insufficient. No product
shutdown message or descendant termination guarantee is added. The optional
checkpoint phase has its own bounded protocol deadline; it is not a process
shutdown RPC. An interrupted delivery whose acceptance is unconfirmed is unknown.

## Cross-language and standalone callers

A non-Go implementation imports standard gRPC/protobuf bindings, not Go product
packages. The approved Python fixture environment uses the pins in
`build/testdata/plugin-tooling/requirements.txt`: grpcio and health checking
1.84.0, protobuf 7.36.2, and typing-extensions 4.16.0. Implement this recipe for
either role:

1. Check `AGENT_PULSE_HUB_PLUGIN=plugin-v2`; bind a local gRPC server at
   `127.0.0.1:0` before announcing its port.
2. Register `agentpulse.plugin.v2.Plugin/Session` with binary `BytesValue`
   serialization/deserialization. Direct stream-method registration is allowed.
3. Register `grpc.health.v1.Health/Check`, returning `SERVING` for `plugin`, and
   `/plugin.GRPCController/Shutdown` with empty protobuf request/response. Its
   handler stops the server, releases operations, and exits the configured process.
4. Print and flush `1|2|tcp|127.0.0.1:<port>|grpc` to stdout. The first value is
   the framework core protocol version; the second is application version 2.
   Emit only diagnostics on stderr, never application JSON on stdout.
5. On the host-opened Session, send the matching readiness role, handle only
   that role's requests, preserve correlations and ordering, and observe
   stream cancellation and controller shutdown.

The application needs no framework broker callbacks or stdio RPCs. A minimal
non-Go server may leave these unused endpoints unimplemented. Go servers use
the framework-provided endpoints normally. Recheck the recipe against the
pinned framework source and language fixture when upgrading dependencies.

A standalone verification caller can use the same framework launch, dispense,
and Session sequence without starting the hub daemon. For an input plugin it
requests a watch, applies that plugin's source stimulus, and observes events.
For an output it validates a destination, sends a delivery with the required
external-data fields, and observes the result and its destination. The caller
supplies opaque test IDs; it implements no other plugin and needs no other
plugin's live service.

Future service-plugin evidence must record that plugin's target service and
environment, stimulus/request, observable event or delivery result, and relevant
failures. The plugin's service check and the hub's actual-model end-to-end
check establish different behavior. Neither substitutes for the other; this
MVP defines the boundary without claiming a future plugin's integration success.

## Compatibility and conformance verification

Replace version 1 input-only executables and Codex-specific registration
arguments together with the revised daemon/client. The document path remains
stable, but the service, cookie, role selection, and payload version change.
There is no dual-interface support or migration of registrations that an older
nonpersistent daemon never saved. Saved-state compatibility and restoration
follow [local persistence](state-persistence.md). Nonparticipating v2 messages
remain unchanged; runtime-state participation uses the optional extension above.
Unsupported framework/application versions fail visibly.
An incompatible future interface requires a distinct version.

[MVP verification](mvp.md#verification-strategy) governs case IDs and paths.
The following are implementation obligations, rather than executed results.
Broad schema/decision matrices belong in unit tests with mocked external
capabilities; selected assembled command/process/stream/file behavior belongs
in integration tests.

| Coverage | Required observation |
| --- | --- |
| Both roles, framework selection, readiness, shutdown | Correct role and gRPC negotiation; readiness gates operations; startup failure is visible; standard cleanup stops configured processes. |
| Strict schemas, Unicode, nested duplicates, direction, versions, payload/context bounds | Invalid input activates no route and performs no delivery; bounded malformed messages permit recovery; oversized transport input isolates its instance. |
| Sequential destination/watch acceptance, immediate event, concurrent and duplicate results | No active route before both accept; no result/event race; foreign or late correlations never activate or retarget a route. |
| Destination rejection and output loss after watch dispatch | No active registration or delivery; accepted orphan watches remain inactive and consume governed capacity until shutdown. |
| Delivery correlation, rejection, unknown acceptance, real deadline | Exactly one attempt; invalid result never establishes success; timeout is unknown and isolates output; no retry/restart. |
| Event ownership, plugin failures, duplicate registration, restart | Registered recipient is fixed; unavailable routes reject new events; separate available routes continue; equivalent active registration keeps its ID; saved IDs survive restart only after current plugin acceptance and complete startup restoration. |
| Independent-language replacements for each role | Both executables can be substituted without daemon code changes; registration and routing succeed through public interfaces. |
| Actual bundled model pair and standalone boundary | Ordinary trigger/observation interfaces establish hub routing; standalone callers can exercise either role without a daemon or another service. |

Tagged compilation, Pending case registration, tooling qualification, and
minimal feasibility probes establish their stated preparation evidence only.
Implemented acceptance requires the actual approved cases to pass. The
clean-build README walkthrough is evaluated during delivery with ordinary
model-plugin interfaces, without actual Codex or live services.

---
type: Design
title: Agent Pulse Hub plugin protocol v1
description: Language-neutral application contract carried by hashicorp/go-plugin gRPC communication.
sources:
  - id: mvp-design
    resource: design/mvp.md
    title: Minimal external event delivery MVP
---

# Agent Pulse Hub plugin protocol v1

## Purpose and authority

This is the authoritative application interface for plugin authors in any
language and for daemon implementers and interoperability testers. It derives
from the [governing MVP design](mvp.md#specification-documents), which owns
configuration, local registration control, subscription identity, event
admission, Codex delivery, and the bundled manual-plugin profile. This document
owns the plugin handshake configuration, gRPC application service, message
encoding and validation, exchange ordering, and application deadlines.

A plugin watches its source and reports context. It never receives a Codex
session identifier or credentials and never calls an agent API. The configured
plugin instance identifies the source; neither plugin name nor recipient is
supplied in an application message. No service-specific schema is required.

## Framework and transport

Use the module-pinned `github.com/hashicorp/go-plugin` version `v1.8.0` with
gRPC as its only allowed protocol. The framework launches the configured
executable with its argument array, negotiates communication, and stops the
configured process. There is no product-owned process launcher, containment
mechanism, stdio message transport, or shutdown RPC. Framework implementation
details are governed by the pinned dependency, rather than copied into a
second product transport contract.

The Go host and plugin use these values:

| Setting | Required value |
| --- | --- |
| `HandshakeConfig.ProtocolVersion` | `1` |
| `HandshakeConfig.MagicCookieKey` | `AGENT_PULSE_HUB_PLUGIN` |
| `HandshakeConfig.MagicCookieValue` | `plugin-v1` |
| `PluginSet` key and `Dispense` name | `events` |
| `ClientConfig.AllowedProtocols` | Only `plugin.ProtocolGRPC` |
| gRPC service | `agentpulse.plugin.v1.EventPlugin` |
| Full application method | `/agentpulse.plugin.v1.EventPlugin/Session` |

The cookie detects an executable launched through the wrong entry point; it is
not authentication or isolation. Plugins are trusted local programs selected
by the user. The host creates `plugin.Client` with the configured command,
obtains its client protocol, and calls `Dispense("events")`. A Go plugin calls
`plugin.Serve`, registers this service through `GRPCPlugin.GRPCServer`, and
uses the framework's standard gRPC server lifecycle.

The [go-plugin usage contract](https://github.com/hashicorp/go-plugin/blob/v1.8.0/README.md#usage)
describes host and plugin responsibilities. Go plugins receive the framework's
health, controller, broker, and stdio services from `plugin.Serve`. Other
languages supply the required endpoints described under
[cross-language implementation](#cross-language-implementation).

## Application service and encoding

The language-neutral service schema is:

```proto
syntax = "proto3";
package agentpulse.plugin.v1;

import "google/protobuf/wrappers.proto";

service EventPlugin {
  rpc Session(stream google.protobuf.BytesValue)
      returns (stream google.protobuf.BytesValue);
}
```

One daemon opens one bidirectional `Session` stream for each configured plugin
instance. That stream multiplexes all its watches. Both peers serialize their
outgoing messages. gRPC preserves message order within each direction; the
daemon processes received application messages in that order. A plugin does
not establish an independent delivery path. Opening another `Session` while
one is active is rejected with gRPC `ALREADY_EXISTS` and does not replace the
original stream.

Each `BytesValue.value` contains exactly one strict UTF-8 JSON object, at most
65,536 bytes. This payload limit excludes protobuf and gRPC framing. There is
no line delimiter or JSON-lines framing. Configure application send/receive
limits to 65,540 encoded protobuf bytes, sufficient for the maximum payload
and its four-byte field envelope, and validate the payload length separately.
Use binary protobuf `BytesValue`, rather than its JSON/base64 representation.
Generated bindings or direct gRPC method registration are permitted; the
service requires no custom protobuf message types.

Do not send a byte-order mark. JSON strings contain Unicode scalar values:
invalid UTF-8 and unpaired surrogate escapes are rejected without replacement.
Duplicate keys are rejected at every object depth, including `watch_args`.
Each object has exactly its defined fields. Names and type values are
case-sensitive, required fields cannot be null, and `version` is the JSON
number token `1`. Unknown fields, invalid JSON, missing or mistyped fields,
unsupported versions, wrong-direction types, and non-object payloads fail
validation. JSON numbers in `watch_args` retain their exact decimal value,
without binary floating-point rounding.

A malformed size-bounded message from a plugin is logged and discarded; the
daemon continues with the next gRPC message. An over-limit payload, gRPC
decoding/transport failure, or stream closure makes that plugin unavailable.
There is no partial-JSON buffering or delimiter recovery. Readiness and pending
watch deadlines still apply after a discarded message. Once ready, the stream
has no idle deadline when no watch result is pending. The daemon sends only
valid messages. Invalid daemon input makes the plugin report an error on
stderr and exit, without guessing a watch or recipient.

## Message schema

Every application message has `version` and `type`, plus the fields below.
Nonempty strings contain at least one Unicode scalar value; whitespace is not
automatically trimmed.

| Direction / type | Additional fields and validation | Meaning |
| --- | --- | --- |
| Plugin to daemon / `ready` | No additional fields | Ready to accept watches; exactly once per plugin instance. |
| Daemon to plugin / `watch` | `request_id`: nonempty string; `subscription_id`: nonempty string; `watch_args`: JSON object | Propose a watch; the plugin validates its argument schema. |
| Plugin to daemon / `watch_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: nonempty string required only when false, forbidden when true | Accept or reject the matching pending watch. |
| Plugin to daemon / `event` | `subscription_id`: nonempty string; `context`: nonempty string of at most 8,192 decoded UTF-8 bytes | Report external data for an accepted watch. |

Both identifiers are opaque and echoed unchanged, without parsing or
normalization. `request_id` is unique for the plugin instance lifetime. The
daemon generates subscription IDs and does not intentionally reuse them across
runs. Plugins do not manufacture either ID. The payload bound also bounds
identifiers, arguments, and rejection text; there is no smaller application
limit on those fields. A plugin may reject values unsupported by its argument
schema.

## Exchange and ordering

Readiness has a 10-second deadline from process launch, including framework
negotiation, dispensing the interface, opening `Session`, and receiving a valid
`ready`. Configure the framework startup timeout within that deadline. The
plugin sends this first application message when it can serve watches:

```json
{"version":1,"type":"ready"}
```

The daemon sends no watches before readiness. A second `ready` is rejected
without changing registrations. Any other message before readiness is rejected;
the readiness deadline remains unchanged.

For a new registration the daemon sends:

```json
{"version":1,"type":"watch","request_id":"request-1","subscription_id":"subscription-a","watch_args":{"path":"value"}}
```

The plugin writes exactly one matching result:

```json
{"version":1,"type":"watch_result","request_id":"request-1","accepted":true}
```

Or rejects the watch without leaving it active:

```json
{"version":1,"type":"watch_result","request_id":"request-1","accepted":false,"error":"path is already watched"}
```

The watch deadline is 10 seconds from beginning to send the `watch`, including
any wait for serialized sending, flow control, and receiving the matching result.
A malformed result does not complete or extend it. Results for distinct pending
requests may arrive in any order. Unknown or duplicate request IDs are rejected
without changing registry state.

The plugin completes sending its positive `watch_result` before sending the
first event for that watch, even if the watched condition is already true.
The daemon activates the subscription while processing that result, before
processing the next message on the same stream. This ordering avoids an
acknowledgement/event race without an extra commit call or pending-event
buffer. The plugin need not wait for the registration client to receive success.

```json
{"version":1,"type":"event","subscription_id":"subscription-a","context":"A matching change is ready.\nExternal data follows."}
```

Events before acceptance, unknown IDs, and another plugin's IDs are discarded
and logged. Events have no application acknowledgement: plugins receive neither
admission nor delivery results. The daemon applies the governing design's
[admission rules](mvp.md#admission-delivery-and-shutdown), fixes the recipient from
registration, and treats repeated messages as distinct events. Plugins do not
retransmit to obtain delivery confirmation.

## Failure and lifecycle

Missing readiness, watch timeout, failed send, stream failure, or process exit
makes the plugin unavailable. The daemon invalidates that instance's
registrations and invokes its `plugin.Client.Kill()`. Watch timeout can leave
acceptance uncertain; stopping the instance prevents an unconfirmed watch from
remaining active. Other plugin instances remain available. There is no automatic
restart, individual cancellation, or delivery retry. Messages received after
unavailability are rejected. Events already admitted retain their captured
destination under the [governing failure rules](mvp.md#admission-delivery-and-shutdown).

On normal daemon shutdown, close registration and event admission, cancel
application stream activity, and call `Client.Kill()` for every configured
plugin client, including partially started clients. Plugins release watches
when their stream/server is stopped and exit through the framework lifecycle.
There is no application `shutdown` message, shutdown acknowledgement, or separate
product grace period. The pinned framework manages graceful shutdown and its
fallback termination of the configured process. Termination of descendants is
not a product guarantee. Stream closure alone does not prove process exit;
acceptance checks observe the configured process after framework cleanup.

Use stderr for bounded diagnostics, never for application data. Keep stdout
available to go-plugin negotiation and framework-managed output. The daemon
drains process stderr through `ClientConfig.Stderr` and framework-forwarded
stderr through `ClientConfig.SyncStderr`, attributing diagnostics to the
configured plugin. Output cannot alter subscription or recipient identity.

## Cross-language implementation

A plugin in another language implements the same protobuf service and strict
JSON messages without importing Go packages. It uses the framework protocol
compatible with the pinned go-plugin version. A minimal Python implementation
uses `grpcio`, `protobuf`, and `grpcio-health-checking` at the versions pinned
in `build/testdata/plugin-tooling/requirements.txt`. Its executable and arguments
are selected in daemon configuration like the bundled plugin.

1. Verify `AGENT_PULSE_HUB_PLUGIN=plugin-v1` in the child environment and fail
   if the cookie is missing or mismatched. Create a local gRPC server on
   `127.0.0.1:0` before announcing its chosen port.
2. Register `agentpulse.plugin.v1.EventPlugin/Session` as a bidirectional stream
   with binary protobuf `BytesValue` deserialization/serialization. Python may
   use `grpc.stream_stream_rpc_method_handler` and
   `google.protobuf.wrappers_pb2.BytesValue`; no product protobuf compiler or
   generated product binding is required.
3. Register standard `grpc.health.v1.Health` with `Check` returning `SERVING`
   for service name `plugin`, and implement
   `/plugin.GRPCController/Shutdown` with empty protobuf request and response.
   Its shutdown handler stops the server, releases watches, and causes the
   configured process to exit. `google.protobuf.Empty` is wire-compatible with
   the framework's empty controller messages. These endpoints are required for
   the framework health and standard shutdown path.
4. After starting the server, print and flush the negotiation line
   `1|1|tcp|127.0.0.1:<port>|grpc` to stdout. The first value is the framework
   core protocol version; the second is this application's protocol version.
   Do not print application JSON to stdout. Diagnostic text goes to stderr.
5. On the host-opened `Session`, send `ready`, read each proposed watch, send its
   result, and emit events in the required order. Serialize outgoing messages
   from concurrent watch workers. Observe stream cancellation and controller
   shutdown so no watches remain in the configured process after it exits.

The product interface uses no framework broker callbacks or stdio RPCs. A
minimal non-Go server may leave those unused endpoints unimplemented and write
diagnostics directly to process stderr; no application operation depends on
those endpoints. Go plugins use the framework-provided endpoints normally. Do
not add a product replacement for the framework controller, transport, or
process management. Recheck this recipe against the pinned source and language
fixture when upgrading go-plugin.

## Compatibility and conformance verification

This revision replaces the previous stdio JSON-lines transport at this stable
specification path. Existing plugins adopt this gRPC service and framework
launch/lifecycle contract before use. Keeping the JSON field names does not
make a previous stdio executable compatible. There is no dual-protocol support
or fallback, persistence migration, or restored subscription state. Unsupported
framework/application versions fail visibly. Contract changes require design
review; an incompatible future interface uses a distinct version.

The following are required implementation checks, rather than completed product
results. [MVP verification](mvp.md#verification-strategy) allocates selected public
command observations to integration tests and complete input/decision matrices
to unit tests with mocked external capabilities.

| Cases | Decisive result | Design verification |
| --- | --- | --- |
| Framework startup, protocol selection, readiness, standard shutdown | gRPC selected; ready gates registration; missing executable or incompatible version fails visibly; `Client.Kill()` stops the configured process. | V01, V07 |
| Message schemas, missing/null/mistyped/unknown fields, nested duplicates, wrong direction, non-1 version, inconsistent result/error fields | Invalid messages do not activate watches or deliver events; a valid message after a malformed size-bounded payload is processed on the same stream. | V02, V04, V07 |
| Invalid UTF-8, unpaired surrogate, escaped newline, 65,536/65,537 payload bytes, protobuf wrapper | Exact payload bound accepted when otherwise valid; invalid encoding discarded; over-limit input makes only its plugin unavailable. No delimiter rules apply. | V04, V07 |
| Empty context, 8,192/8,193 decoded UTF-8 bytes including multibyte characters | Invalid context rejected without truncation; exact bound accepted when ownership and capacity allow. | V04, V07 |
| Missing/duplicate ready, late/unknown/duplicate result, blocked send, closed stream | Deadlines never extend; uncertain watches invalidate and stop only their plugin; no orphan active registration. | V01, V02, V04 |
| Opaque IDs, multiple watches, immediate event after acceptance, event before acceptance, foreign ID | IDs echoed unchanged; results may be out of request order; only the emitting plugin's active watch admits an event. | V02, V04, V07 |
| Equivalent registrations, repeated events, capacity exhaustion | One watch per equivalent registration; repeated admitted events remain distinct; exhaustion rejects without retry. | V02, V04, V10 |
| Plugin exit, daemon shutdown, restart with old ID | Unavailable plugins cannot admit events; unaffected plugins work; shutdown loses state; old IDs rejected after restart. | V01, V04, V05 |

The non-Go public-contract case uses an independent Python plugin through the
real framework-launched process and application `Session`, observing watch
acknowledgement, event context, and standard process shutdown. Minimal design
feasibility probes exercise that same method and ordering with controlled peers.
The existing `pluginTooling` qualification exercises framework
health/streaming/lifecycle capabilities only; it does not implement this API.
Neither tooling nor feasibility probes establish product registration/routing
correctness or real Codex delivery. Actual Codex demonstrations remain delivery
checks under the governing design.

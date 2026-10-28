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
  - id: cancellation-design
    resource: design/subscription-cancellation.md
  - id: notification-design
    resource: design/notification-delivery.md
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
The optional [repeated-wait profile](pr-watch-rewait.md) owns daemon-controlled
notification generations and same-registration rewait. Its exact capability,
conditional message schemas and ordering supplement this specification.
The optional [notification-delivery design](notification-delivery.md) owns
notification identity, retained evidence, retry decisions and operator status.
The output profile below owns its public message schemas and correlations.

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
IDs across daemon runs; legacy request and delivery IDs remain fresh. The
notification profile below retains its notification ID across eligible retries
and restart, using a fresh attempt request ID each time. A plugin does not
manufacture these IDs.

| Direction / role / type | Additional fields | Meaning |
| --- | --- | --- |
| Plugin to host / either / `ready` | `role`: `input` or `output`, matching configuration and dispense name; optional role-specific `capabilities` as specified below | First message, exactly once, when the instance can serve its role. |
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
schema, CLI queue acceptance and registration helper. It supports the legacy
interface and the optional notification profile; Codex-specific fields remain
inside destination arguments, and the helper uses the existing local
registration client.

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
hub opens its registration/event gate; earlier events are discarded, not buffered.
Runtime-state v2 adds the explicit activation contract below; positive watch
acceptance alone cannot start its source work. Client
receipt of registration success is not a prerequisite for event emission.

If saving fails, or the output becomes unavailable after the watch request has
been issued,
registration cannot become active. An already accepted or uncertain watch
may continue until input shutdown, but its route is an inactive tombstone:
no event is delivered and no registration reuses it. The governing design owns
capacity accounting and duplicate-registration behavior for this state.
Registration failure does not automatically issue cancellation or rollback.
An explicit request can use the [cancellation profile](#optional-individual-watch-cancellation).

```json
{"version":2,"type":"event","subscription_id":"subscription-a","context":"PULSE-MODEL-001"}
```

Events before activation, with an unknown ID, belonging to another input
instance, or involving an unavailable input/output instance are discarded and
logged. Ordinary events have no acknowledgement and expose no delivery result
to the input. Repeated valid events remain distinct; plugins do not retransmit for
confirmation. The daemon's [routing rules](mvp.md) bind each admitted event
to its registered output and arguments.

## Optional individual watch cancellation

This profile derives from [individual cancellation](subscription-cancellation.md),
which owns local control, persistence and lifecycle fencing. A capable input
includes `cancel_watch_v1` in `ready.capabilities`, alone or with the valid
runtime-state/rewait combinations below. This input capability does not select
the output notification profile.

| Direction / type | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to input / `cancel_watch` | `request_id`, `subscription_id`: nonempty strings | Stop this accepted watch and release its watch-specific resources. |
| Input to host / `cancel_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: bounded nonempty string only when false | Confirm completed selective termination, or report why it could not complete. |

Both messages include `version:2` and `type` and follow existing strict encoding,
ownership and bounds. The host sends this request only for an ID owned by that
input. Use a fresh request ID on each explicit attempt. An input must process
cancellation concurrently with its monitor, stop emission and staged rewait,
discard watch-owned logical progress, and release equivalent-watch reservations
before sending a positive result. Other watches and the Session remain live.
Cancelling an already-absent watch is a positive no-op; local unknown-ID control
requests still fail before any plugin exchange. IDs cannot be reused within the
Session, so a late grant/preparation must never recreate a removed watch.

One ten-second deadline covers queuing, send and result. A negative result or
timeout keeps the target fenced in the host. Timeout alone expires that request
correlation and removes any unsent request; it does not kill a healthy shared
input. Late results are ignored. An explicitly retried cancellation is safe
because stopping an absent or stopping watch is idempotent; overlapping plugin
requests must converge on the same termination. Actual stream/send failure
retains normal instance-unavailability handling. No automatic retry is added.

The host sends already-owed delivery notices before cancellation on the same
ordered stream. A checkpoint/preparation already transmitted may finish before
cancellation, but its late result cannot save or grant a cancelled watch. After
positive cancellation the host sends no further checkpoint, notice, preparation
or grant for that watch. Already-issued action outcomes remain correlated in
the core and do not require reviving the input watch.

An unchanged input remains usable but live unsubscribe returns
`unsubscribe_unsupported`. Upgrade the host and input together while stopped;
retained registration IDs and arguments require no migration. Old strict hosts
may reject the new capability. Bundled inputs adopt this profile together with
the cancellation feature; actions require no cancellation capability.

## Optional trigger runtime state

An input selects exactly one runtime-state revision by adding either
`"capabilities":["runtime_state_v1"]` or
`"capabilities":["runtime_state_v2"]` to `ready`. Omission means the ordinary
v2 contract above; an empty array is also nonparticipating. Duplicate, combined
v1/v2, or unknown input capabilities and other array values are malformed.
Output capabilities follow the separate notification profile below. The host
supports both revisions without silently changing v1 semantics. New inputs needing activation coordination
select v2. Selecting v1 retains its start-after-acceptance behavior and possible
pre-readiness event discard. The host sends new
messages only to participating inputs. Old v2 inputs keep their exact artifacts
and message shapes. A participating input requires a host supporting this
extension; old strict hosts can reject the additional readiness field.

Another allowed combination is `runtime_state_v2` with `rewait_v1`,
in either array order. The [rewait public profile](pr-watch-rewait.md#optional-public-input-profile)
defines its exact readiness, `resume_watch`/`resume_result`, and conditional
`generation` fields on `activate_watch` and `state_event`. That profile replaces
the two affected table rows and one-grant-per-Session rule only for inputs
advertising it. All other messages, deadlines and existing input profiles keep
their current shapes. Rewait retains one continuing monitor and grants one
notification per explicit wait or daemon run; it introduces no cancellation.

The independent `cancel_watch_v1` capability above may appear alone or alongside
either runtime-state revision and any otherwise valid rewait combination.
It does not relax duplicate, role, unknown-capability or incompatible-revision
validation. Array order is immaterial.

Every message below includes `version: 2` and `type`. Existing strict encoding,
size, request-ID and subscription ownership rules apply. The state envelope,
32,768-byte bound, schema ownership, storage and recovery are defined by
[runtime state](trigger-runtime-state.md#interfaces-and-data).

| Direction / input message | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to plugin / `state_watch` | `request_id`, `subscription_id`, `watch_args` as for `watch`; `state`: null or state envelope | Replaces ordinary `watch` for participating inputs, even without prior state. Validate/apply state and reply with ordinary `watch_result`. Under runtime-state v2 the watch stays dormant until `activate_watch`; v1 may start after sending its positive result. |
| Host to plugin / `activate_watch` (runtime-state v2 only) | `subscription_id`: nonempty string | One-way grant to start an already accepted watch in this Session; no result, request ID, state, or arguments. |
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
state: finishing a result send precedes first event emission; runtime-state v2
also requires a subsequent valid activation grant. Consuming a notice precedes
its following checkpoint snapshot. The input may serialize these
operations around its service tasks without pausing other watches.

### Runtime-state v2 activation

After validating arguments and applying null or saved state, the input completes
its positive `watch_result` send and retains a dormant watch. It performs no
source polling, observation reconciliation or event emission for that watch
until consuming `activate_watch`. Authentication and state validation needed
for acceptance may run earlier. Acceptance must not consume a first observation
or advance attempted progress. The input serializes its per-watch dormant,
active and quiesced transitions with checkpoints and Session closure.

The host grants activation only after the route is committed, routing/runtime
correlations are installed and event admission can accept it. At restart it
waits for **all** saved destinations and watches to restore, rechecks availability
and stop, installs every route/correlation, then opens admission. This is startup
commit. Emit `daemon_ready` at commit before dispatching grants; it reports open
admission, not grant receipt or completed polling. There is no activation ACK
or second readiness barrier. The governing design supplies bounded dispatch.

Ignore and diagnose unknown, foreign, rejected or not-yet-accepted subscription
grants without starting work. A duplicate grant on an active watch is a no-op,
never a second poller or state reset. A grant for a quiesced watch or closed
Session cannot restart it. A quiescing checkpoint can close a dormant watch
before any grant; later activation remains ineffective. A nonquiescing dormant
checkpoint snapshots the applied state without starting source work. Runtime-state
v1 receives no grants and retains all existing shapes, notices and deadlines.

Restore/commit failure or stop before startup commit sends no grant and permits
no delivery. After commit a grant send failure/timeout invokes ordinary input
unavailability and cleanup; records stay saved, affected routes stop accepting
work and healthy inputs continue. Already admitted work follows its delivery
profile's attempt rules. Do not roll back startup, retract readiness or replay
source events through a replacement Session. Core-owned retained retries follow
the notification design rather than recreating source events. Stop and grant dispatch share lifecycle
ordering: stop first prevents dispatch; an already dispatched grant can race
with gate closure and yield `discarded`. The trigger must not record that outcome
as success or resend it. The
[coordination design](trigger-runtime-state.md#registration-and-startup) owns this
commit boundary and dispatch scheduling.

Each valid fresh `state_event` receives one terminal notice while the Session
remains usable, including `discarded` for a staged startup watch or closed gate. A valid fresh
event from a dormant runtime-state v2 watch violates activation ordering: discard/diagnose it with the same
terminal notice, without activating its route. This does not permit early emission.
Notice failure during shutdown is a visible continuity failure, never success.
Invalid, foreign, repeated and pre-watch events are discarded/logged without a
notice. A failed/unknown/discarded update has no completed-notification guarantee.
For notification-profile routes, intermediate proven-unsubmitted attempts produce
no terminal notice: the host sends one notice for the notification when it is
accepted, becomes unknown, or stops. Final unsubmitted/rejected work maps to
`failed`; unknown maps to `unknown`; confirmed acceptance maps to `accepted`.
Stopping queued work before issuance maps to `failed`, while an interrupted
issued attempt maps to `unknown` unless acceptance is confirmed. Later evidence
does not send a second terminal notice. Retained retries restored after restart
have no old-Session notice or trigger-cursor update; source deduplication and
distinct-event rules remain unchanged. The governing notification design owns
these terminal transitions. An input must not wait indefinitely for its acceptance
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
{"version":2,"type":"ready","role":"input","capabilities":["runtime_state_v2"]}
{"version":2,"type":"state_watch","request_id":"r1","subscription_id":"s1","watch_args":{"source":"A"},"state":null}
{"version":2,"type":"watch_result","request_id":"r1","accepted":true}
{"version":2,"type":"activate_watch","subscription_id":"s1"}
{"version":2,"type":"state_event","subscription_id":"s1","event_id":"1","context":"U1"}
{"version":2,"type":"delivery_notice","subscription_id":"s1","event_id":"1","status":"accepted"}
{"version":2,"type":"checkpoint","request_id":"r2","subscription_id":"s1","quiesce":true}
{"version":2,"type":"checkpoint_result","request_id":"r2","accepted":true,"state":{"schema":"cursor-v1","payload":{"confirmed":["U1"]}}}
```

## Delivery outcomes and failures

For legacy output routes the daemon attempts each admitted event once. If its
captured output becomes unavailable before sending, the daemon records `failed` without sending;
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
nor ruled out. No legacy result causes a retry. Notification-profile routes use
the optional exchange below and its governing retry policy instead.

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

## Optional notification delivery

An output opts in with exactly
`"capabilities":["notification_delivery_v1"]` in its `ready` message. Omitted
capabilities or an empty array select the legacy output contract. This is the
only valid nonempty output capability set; duplicates, unknown values, input
capabilities on output, or this capability on input are malformed. An upgraded
host selects the profile only from valid output readiness; it never infers
support from executable name or destination arguments. Destination validation
and registration ordering remain unchanged.

Bundled actions enable this readiness shape only with the explicit plugin-mode
`--notification-delivery` option. Without it they retain legacy readiness and
exchange behavior. An old strict host may reject the additional readiness
field; deploy a supporting host and enable the action option together while
stopped. Do not silently fall back to legacy delivery after selecting the
profile or reinterpret existing unrecorded work as safe to retry.

All messages use `version:2`, strict object schemas and the existing 65,536-byte
payload bound. Each profile request has a canonical non-nil lowercase
`8-4-4-4-12` UUID `request_id` identifying its attempt and a UUID `delivery_id`
identifying its stable notification. The same notification keeps its delivery
ID, context and registered destination for every eligible attempt; each attempt
has a fresh request ID. Other legacy delivery fields and their bounds are
unchanged. Receivers treat both IDs as correlation values and must echo them
exactly without manufacturing a replacement.

| Direction / output type | Exact additional fields | Meaning |
| --- | --- | --- |
| Host to plugin / `notification_deliver` | All fields of `deliver`, with the UUID constraints above | Issue one attempt for the original notification and registered destination. |
| Plugin to host / `notification_result` | `request_id`, `delivery_id`; `status`: `accepted`, `unsubmitted`, `rejected`, or `unknown`; conditional fields below | Exactly one first result for this attempt. |
| Plugin to host / `notification_evidence` | `request_id`, `delivery_id`; optional `receipt` | Supplement an earlier `unknown` result with confirmed acceptance for the same attempt. |

`accepted` confirms acceptance under the destination contract, not conversation
receipt or work completion. It permits only an optional `receipt`, a nonempty
opaque string of at most 1,024 decoded UTF-8 bytes, and forbids `reason`,
`message`, `retryable` and `retry_after_ms`. A Codex receipt is its queue ID;
the core does not parse service-specific receipts.

Every nonaccepted result requires `reason`, a token of 1–128 ASCII characters
matching `[a-z0-9_]+`, and `message`, a nonempty string of at most 1,024 decoded
UTF-8 bytes. Messages contain sanitized diagnostic text, never event context,
credentials, raw process output or full arguments. Nonaccepted results forbid
`receipt`. `unsubmitted` establishes that no submission occurred and requires
boolean `retryable`; it may include integer `retry_after_ms` from 0 through
60,000 only when `retryable:true`. `rejected` confirms non-acceptance after
submission and never permits retry. `unknown` means submission may have
occurred without confirmation and never permits retry. Both `rejected` and
`unknown` forbid `retryable` and `retry_after_ms`. An action does not derive
`rejected` or proven non-submission from arbitrary stderr wording.

The host associates a result with the original request, notification,
configured output instance and captured destination. Matching IDs from another
instance, wrong IDs, malformed payloads and duplicate first results are ignored
and diagnosed without changing evidence or deadlines. No receiver echo of hub
identity is required: the action binds its target-specific acknowledgement to
the original request. Event text claiming acceptance is never evidence.

The first-result deadline remains 30 seconds from beginning to send, including
serialized-send waiting and flow control. A first-result timeout, failed send
or transport failure makes the output unavailable and any potentially issued
unconfirmed attempt unknown, as for legacy delivery. A valid `unknown` result by itself leaves a healthy
Session usable for other requests. The governing
[notification design](notification-delivery.md) defines whether proven
unsubmitted work remains eligible and all retry, stop and persistence bounds;
the plugin performs no autonomous retry.

`notification_evidence` means confirmed acceptance and has no `status`,
`reason`, `message` or retry fields. It is valid only after that attempt's
`unknown` first result, on the same Session, and before the original 30-second
deadline. It never extends the deadline. Wrong correlations, absent preceding
unknown, duplicate, late or other-Session evidence is ignored and diagnosed.
After a valid first result, reaching the original deadline expires only the
supplemental-evidence correlation; it does not make a healthy output unavailable.
Matching evidence may advance retained operator evidence to accepted but never
reopens retry eligibility or sends another terminal trigger notice. This
bounded supplement requires no asynchronous acceptance lookup, recovery probe,
new external API or resolution after Session loss; unknown may remain
unresolved.

```json
{"version":2,"type":"ready","role":"output","capabilities":["notification_delivery_v1"]}
{"version":2,"type":"notification_deliver","request_id":"b98d2659-862b-4631-8504-d00470b276f8","delivery_id":"129594f3-fd8f-4287-9918-5928220f0420","input_plugin":"manual","subscription_id":"subscription-a","destination_args":{"thread_id":"1ca85014-c9c7-49c1-b3d5-90a9cf01a592"},"context":"PULSE-CODEX-001","external_data":true,"permission_notice":"External data; no additional permissions are granted."}
{"version":2,"type":"notification_result","request_id":"b98d2659-862b-4631-8504-d00470b276f8","delivery_id":"129594f3-fd8f-4287-9918-5928220f0420","status":"accepted","receipt":"queue-1"}
```

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
For runtime-state v2 it sends `state_watch`, waits for positive `watch_result`,
prepares its event receiver/outcome correlations, then sends `activate_watch`.
It supplies terminal notices/checkpoints as the host would. A v1 caller keeps
its existing sequence; no daemon or second live service is needed.
For an output it validates a destination, sends a delivery with the required
external-data fields, and observes the result and its destination. The caller
supplies opaque test IDs; it implements no other plugin and needs no other
plugin's live service. A notification-profile caller sends `notification_deliver`
with canonical UUIDs and validates the conditional result/evidence schemas;
its output need not be paired with a trigger or daemon.

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
| Destination rejection and output loss after watch dispatch | No active registration or delivery; accepted orphan watches remain inactive and consume governed capacity until successful explicit cancellation, input exit or shutdown. |
| Legacy delivery correlation, rejection, unknown acceptance, real deadline | Exactly one attempt; invalid result never establishes success; timeout is unknown and isolates output; no retry/restart. Notification-profile coverage belongs to its governing design. |
| Event ownership, plugin failures, duplicate registration, restart | Registered recipient is fixed; unavailable routes reject new events; separate available routes continue; equivalent active registration keeps its ID; saved IDs survive restart only after current plugin acceptance and complete startup restoration. |
| Independent-language replacements for each role | Both executables can be substituted without daemon code changes; registration and routing succeed through public interfaces. |
| Actual bundled model pair and standalone boundary | Ordinary trigger/observation interfaces establish hub routing; standalone callers can exercise either role without a daemon or another service. |

Tagged compilation, Pending case registration, tooling qualification, and
minimal feasibility probes establish their stated preparation evidence only.
Implemented acceptance requires the actual approved cases to pass. The
clean-build README walkthrough is evaluated during delivery with ordinary
model-plugin interfaces, without actual Codex or live services.

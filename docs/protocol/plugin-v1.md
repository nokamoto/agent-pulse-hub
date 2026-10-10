---
type: Design
title: Agent Pulse Hub plugin protocol v1
description: Language-neutral wire contract for implementing daemon and plugin interoperability.
sources:
  - id: mvp-design
    resource: design/mvp.md
    title: Minimal external event delivery MVP
---

# Agent Pulse Hub plugin protocol v1

## Purpose and authority

This is the authoritative version 1 wire specification for plugin authors in
any language and for daemon implementers and interoperability testers. It
derives from the [governing MVP design](../design/mvp.md#specification-documents),
which defines the architecture, registration and delivery lifecycle, local
control interface, and bundled manual plugin. This specification owns frame
encoding, fields, validation, ordering, wire limits, and plugin deadlines.
Examples illustrate those rules; they do not add fields or require Go types.

A plugin watches its source and reports context. It never receives a Codex
session identifier or credentials and never calls an agent API. The process
that emits a frame identifies the plugin; neither plugin name nor recipient
can be supplied in a frame. No GitHub schema is required.

## Transport and encoding

The daemon launches the configured executable with its argument array as a
child process. Each plugin has private stdin and stdout pipes. The daemon
writes frames to stdin; the plugin writes frames to stdout. Stderr is for
diagnostics and must be drained independently by the daemon. Stdout must not
contain banners, logs, or prompts. Multiplex watches on the same pipes and
serialize writes so bytes from different frames never interleave.

Each frame is one UTF-8 JSON object followed by LF or CRLF. Senders must escape
newlines inside strings. A frame may occupy at most 65,536 bytes, including its
line ending. The limit applies in both directions. Do not send a byte-order
mark. JSON strings must contain Unicode scalar values: invalid UTF-8 and
unpaired surrogate escapes are rejected, never silently replaced. Duplicate
keys are rejected at every object depth, including inside `watch_args`.

Every frame has exactly the fields defined below. Field names and type values
are case-sensitive. Required fields cannot be null. Unknown fields, invalid
JSON, missing or mistyped fields, unsupported versions, and wrong-direction
types are rejected. `version` is the JSON number token `1`; alternate numeric
spellings are not version negotiation. Blank lines are invalid frames.

A malformed but size-bounded LF/CRLF-terminated frame from a plugin is logged
and discarded; reading continues at the next frame, even if the rejected bytes
were invalid UTF-8. An over-limit frame, EOF with a partial frame, or stdout
closure makes that plugin unavailable. Partial input with the pipe still open
is buffered only up to the frame limit; it cannot create a watch or event.
Readiness and pending watch deadlines still apply. There is no idle timeout
after readiness when no watch acknowledgement is pending.

The daemon sends only valid frames. A plugin encountering invalid daemon input
must diagnose it on stderr and exit rather than guess a watch or recipient.
There is no protocol error-response frame in either direction.

## Frame schema

In addition to `version` and `type`, the following fields are required unless
explicitly forbidden. Strings described as nonempty must contain at least one
Unicode scalar value; whitespace is not automatically trimmed.

| Direction / type | Fields and validation | Meaning |
| --- | --- | --- |
| Plugin to daemon / `ready` | No additional fields | Ready to accept watches; exactly once per process. |
| Daemon to plugin / `watch` | `request_id`: nonempty string; `subscription_id`: nonempty string; `watch_args`: JSON object | Propose a watch; the plugin validates the contents of `watch_args`. |
| Plugin to daemon / `watch_result` | `request_id`: nonempty string; `accepted`: boolean; `error`: nonempty string required only when false, forbidden when true | Accept or reject the matching pending watch. |
| Plugin to daemon / `event` | `subscription_id`: nonempty string; `context`: nonempty string of at most 8,192 UTF-8 bytes after JSON escape decoding | Report external data for an accepted watch. |
| Daemon to plugin / `shutdown` | No additional fields | Stop watches, release resources, and exit. |

Both identifiers are opaque and must be echoed unchanged, without parsing,
normalizing, or assuming UUID syntax. `request_id` is unique for the plugin
process lifetime. The daemon generates subscription IDs and does not
intentionally reuse them across runs. Plugins must not manufacture either ID.
The whole-frame bound also bounds identifiers, arguments, and rejection text;
there is no smaller wire limit on those fields. JSON numbers in `watch_args`
retain their exact decimal value, not a rounded binary floating-point value.
An author may reject values unsupported by the plugin's own argument schema.

## Exchange and ordering

The examples below are single-line frames; each is followed by LF on the wire.
The `path` argument is illustrative, not a required argument for all plugins.

Within 10 seconds of process start, the plugin sends:

```json
{"version":1,"type":"ready"}
```

The daemon sends no watches until it processes that readiness frame. A second
`ready` is rejected without changing registrations. Before readiness, any other
plugin frame is rejected; the readiness deadline remains in force.

For a new registration the daemon sends:

```json
{"version":1,"type":"watch","request_id":"request-1","subscription_id":"subscription-a","watch_args":{"path":"value"}}
```

The plugin replies with exactly one result for that request:

```json
{"version":1,"type":"watch_result","request_id":"request-1","accepted":true}
```

Or it rejects the watch without leaving it active:

```json
{"version":1,"type":"watch_result","request_id":"request-1","accepted":false,"error":"path is already watched"}
```

The daemon starts a 10-second deadline when it begins writing the watch frame;
the deadline includes writing and receiving a valid matching result. A malformed
result does not complete or extend that deadline. Results for different pending
requests may arrive in any order. Unknown or duplicate request IDs are rejected
without changing the registry. The daemon processes plugin frames in read order,
activating a watch before the next frame. A plugin must write its accepted result
before the first event for that watch, even when the condition is already true.
It does not need to wait for the registration client to receive success.

```json
{"version":1,"type":"event","subscription_id":"subscription-a","context":"A matching change is ready.\nExternal data follows."}
```

An event before acceptance, with an unknown ID, or with another plugin's ID is
discarded and logged. Events have no acknowledgement: a plugin receives neither
event admission nor delivery results. The daemon applies the
[event admission and delivery rules](../design/mvp.md#bounds-and-event-acceptance)
and fixes the recipient from registration. Repeated event frames are separate
events; do not retransmit in an attempt to obtain delivery confirmation.

On normal shutdown the daemon sends:

```json
{"version":1,"type":"shutdown"}
```

The plugin stops all watches and exits. The daemon allows up to 5 seconds from
the start of shutdown to write this frame and await exit, then terminates the
remaining process tree. A blocked write cannot extend that grace period. Stdin
closure also tells the plugin to stop and exit. There is no shutdown response.

## Failure, lifecycle, and compatibility

Missing readiness, watch timeout, or a failed daemon write makes the plugin
unavailable. The daemon invalidates its registrations and stops its process.
Process exit or stdout closure has the same unavailability effect. No automatic
restart, individual cancellation, or delivery retry exists in v1. Other plugins
remain available. See the governing design's
[failure scenarios](../design/mvp.md#normal-and-failure-scenarios) for unread
frames, events already admitted before failure, and shutdown loss.

The governing design owns [global resource limits](../design/mvp.md#bounds-and-event-acceptance)
and [registration equivalence](../design/mvp.md#registration-state-and-identity).
Equivalent active registrations reuse a watch, and concurrent equivalent
registrations share a pending operation. Plugins must support multiple distinct
watches but may reject arguments they cannot serve. They may poll their source;
they must not ask the coding agent to poll or work while idle.

The [manual test plugin profile](../design/mvp.md#manual-test-plugin-and-skill)
defines `trigger_file` and file consumption. It uses this same public wire
protocol and is not an additional frame type. Protocol v1 carries external data,
not permission to act on it. The daemon's envelope and the skill enforce the
existing-user-instruction boundary described by the design.

Unsupported versions fail visibly. Adding fields, relaxing validation, changing
limits or deadlines, or changing ordering is a contract revision requiring
design review; an incompatible future protocol must use a distinct version.
There is no negotiation or silent fallback in v1. These are resource bounds and
operational deadlines, not throughput or end-to-end latency promises.

## Conformance verification

These are planned implementation checks, not completed test results. A fixture
written without daemon packages must exercise the following cases; the maintainer
reviews its transcripts and assertions under
[MVP verification](../design/mvp.md#verification-strategy). At least one replacement
fixture must use another language to demonstrate the contract is independent of Go.

| Cases | Decisive result | Design verification |
| --- | --- | --- |
| Every frame schema; missing/null/wrong-type/unknown fields; duplicate nested keys; wrong direction; non-1 versions; true with error and false without nonempty error | Invalid frames never activate a watch or cause delivery; a valid framed message after a malformed one is still processed. | V02, V04, V07 |
| LF and CRLF; invalid UTF-8; unpaired surrogate; escaped newline; 65,536 and 65,537 total bytes | Both line endings and exact bound accepted when otherwise valid; invalid encoding discarded; over-limit input makes plugin unavailable. | V04, V07 |
| Empty context; 8,192 and 8,193 decoded UTF-8 bytes, including multibyte characters and escapes | Empty/over-limit context rejected without truncation; exact bound accepted if subscription and queue allow it. | V04, V07 |
| Missing/duplicate ready; watch result after deadline; unknown/duplicate result; failed or blocked write; EOF during a frame | Deadlines never extend; uncertain watches invalidate and stop only their plugin; no orphan active registration. | V01, V02, V04 |
| Non-UUID opaque IDs; multiple pending watches; immediate event after acceptance; event before acceptance; unknown/cross-plugin ID | IDs echoed exactly; matching results may be out of request order; only the correctly owned active watch can admit an event. | V02, V04, V07 |
| Equivalent registrations; repeated events; capacity exhaustion | One watch for equivalent registrations; repeated admitted events remain distinct; exhausted capacity causes rejection, no retry. | V02, V04, V10 |
| Plugin exit; shutdown; restart with an old ID | No new events from unavailable plugins; unaffected plugins still work; shutdown loses pending state; old IDs cannot be used after restart. | V01, V04, V05 |

The real Codex demonstrations and Windows security checks remain required by
the governing design. Passing protocol fixtures alone does not satisfy them.

---
type: Design
title: Agent Pulse Hub plugin protocol v1
description: Language-neutral JSON Lines contract between the daemon and plugin processes.
sources:
  - id: mvp-design
    resource: ../design/mvp.md
    title: Minimal external event delivery MVP design
---

# Agent Pulse Hub plugin protocol v1

This document defines the process interface a plugin must implement to work
with Agent Pulse Hub. Plugins may be written in any language. They do not need
Codex-specific identifiers, Go packages, or network access.

## Process contract

The daemon starts each configured plugin as a child process. The daemon writes
protocol messages to the child's standard input. The plugin writes protocol
messages to standard output and may write human-readable diagnostics to standard
error. Standard output is reserved for protocol frames; do not print banners,
logs, or prompts there.

Each frame is one UTF-8 JSON object followed by a newline. LF and CRLF line
endings are accepted. The complete line, including its newline, must be no more
than 65,536 bytes. A frame without a terminating newline is invalid. The daemon
does not split oversized frames or forward truncated context.

Protocol version 1 is strict:

- Every frame has `"version": 1` and the exact fields listed below.
- Unknown fields and duplicate JSON object keys are rejected.
- JSON values must be valid UTF-8. `watch_args` must be an object.
- A malformed frame within the size limit and terminated by a newline is
  rejected and logged; reading continues, including when its bytes are not valid
  UTF-8. An oversized or unterminated frame makes the plugin unavailable. A
  plugin that closes its output also becomes unavailable.

## Frames

### Readiness

Send exactly one readiness frame within 10 seconds of process start, before
accepting watches or emitting events. If no valid readiness frame arrives before
the deadline, the daemon stops the plugin.

```json
{"version":1,"type":"ready"}
```

### Watch request

The daemon sends a watch request for each new registration:

```json
{"version":1,"type":"watch","request_id":"7e537ada-e4a8-4712-9c76-45c76edc58ee","subscription_id":"4b110967-238e-4ad9-85a0-202c57db8a92","watch_args":{"path":"value"}}
```

`request_id` correlates the response with this request. It is unique for the
plugin process lifetime. `subscription_id` identifies the registration and must
be used on every event for that watch. `watch_args` is the JSON object supplied
by the user; the plugin defines its contents.

Reply with one of these frames:

```json
{"version":1,"type":"watch_result","request_id":"7e537ada-e4a8-4712-9c76-45c76edc58ee","accepted":true}
```

```json
{"version":1,"type":"watch_result","request_id":"7e537ada-e4a8-4712-9c76-45c76edc58ee","accepted":false,"error":"path is already watched"}
```

An accepted response contains no `error` field. A rejected response must contain
a nonempty string `error`. The daemon activates the subscription only after a
positive response. Send the response before any event for that watch. Each watch
request has a 10-second acknowledgement deadline. A timeout or lost
acknowledgement stops the plugin so an unconfirmed watch cannot remain active.

### Event

When a watched condition occurs, send the event context as a JSON string:

```json
{"version":1,"type":"event","subscription_id":"4b110967-238e-4ad9-85a0-202c57db8a92","context":"A matching change is ready."}
```

The decoded context string must be nonempty, valid UTF-8, and no more than 8,192
UTF-8 bytes after JSON escape decoding. The daemon accepts an event only when
its subscription is active, belongs to the emitting plugin, and has queue
capacity. It fixes the recipient from the registration; event content cannot
change that recipient. Invalid, unknown, or cross-plugin subscription IDs are
rejected. Each accepted event receives one delivery attempt; identical event
frames are not deduplicated. Events have no response frame. The plugin receives
neither event acceptance nor delivery results.

### Shutdown

The daemon may ask a plugin to stop gracefully:

```json
{"version":1,"type":"shutdown"}
```

Exit after receiving this frame. The daemon allows up to five seconds for normal
shutdown, then terminates the plugin process tree.

## Lifecycle and limits

Send frames in response to daemon input and as events occur; do not poll the
daemon or call Codex. The daemon reads a plugin's frames in order, activating a
watch before processing a following event. An event sent before its accepted
watch response is rejected.

The daemon supports up to 1,024 active or pending registrations and 128 queued
or in-flight delivery events across all plugins. It starts each plugin once;
it does not restart a failed process. Subscriptions and queued events are held
in memory and are lost when the daemon stops. The daemon does not retry delivery.

## Manual plugin example

The bundled `pulse-manual-plugin` accepts `watch_args` with exactly one string
field, `trigger_file`. It must be an absolute path to a currently absent file in
a writable local directory. The plugin rejects a second watch of the same
normalized path. To emit an event, write nonempty UTF-8 context to a temporary
file in the same directory, then atomically rename it to `trigger_file` without
overwriting an existing file. The plugin claims and removes the file before
sending the event. A failed read, invalid context, or process crash may lose the
trigger; it is not retried.

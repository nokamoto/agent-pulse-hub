# Codex Queue action

The Codex Queue action sends one Agent Pulse Hub event to an existing Codex
conversation. It uses Codex's queue command, so the message waits in that
conversation without interrupting or steering its current work. A confirmed
queue result means Codex accepted the message; it does not mean the conversation
has processed it.

The action is stored under the bundled **action** role and serves the public
plugin protocol v2 **output** role. Its configured name in the examples is
`codex`.

## Prerequisites and build

Use Go 1.26.0 or later from the repository root. Build the hub, manual trigger,
and action from the same revision. Choose absolute output paths and include the
executable suffix reported by `go env GOEXE`:

```text
go env GOEXE
go build -o HUB_EXECUTABLE ./cmd/agent-pulse-hub
go build -o MANUAL_EXECUTABLE ./plugins/triggers/manual
go build -o CODEX_QUEUE_ACTION_EXECUTABLE ./plugins/actions/codex-queue
```

Replace the uppercase path names with the absolute paths you chose. The daemon
configuration must use those same paths.

Choose a trusted native Codex CLI executable that can access the shared Codex
server used by the target conversation. The action requires the CLI to support
`queue --thread <uuid> --message <text>` and the `thread/queue/add` operation.
Shell wrappers, PATH lookup, automatic installation, and alternate delivery
commands are not supported.

Before reporting ready, the action runs `<codex-executable> queue --help` once.
It requires a zero exit status and both literal options `--thread` and
`--message` in the help output. This check does not submit a message or prove
that the selected executable can reach the target's shared server. Record the
CLI version. The [maintainer delivery check](../../../docs/design/codex-queue.md#delivery-quality-checks)
verifies access to the actual shared server and target conversation. It is
separate from local readiness.

## Configure and start the hub

[`examples/daemon.json`](examples/daemon.json) shows the complete configuration
for the manual trigger and this action. Replace the executable placeholders
with absolute paths and serialize the JSON. Keep the names `manual` and `codex`,
roles `input` and `output`, and the shown arguments. `trigger` and `action`
describe directory roles; they are not configuration values.

The action's only daemon argument is the absolute Codex executable path:

```text
CODEX_QUEUE_ACTION_EXECUTABLE --codex-executable CODEX_EXECUTABLE
```

The daemon launches and supervises both plugin processes. Do not launch the
action executable in plugin mode yourself. Start the hub in the foreground
with the materialized configuration and a loopback address:

```text
HUB_EXECUTABLE daemon --config DAEMON_CONFIG_PATH --address LOOPBACK_ADDRESS
```

In the foreground hub's JSON logs, wait for `plugin_ready` records whose
`plugin`, `role`, and `available:true` values match both configured plugins.
Then wait for `daemon_ready` and its `restored_registrations` count before
creating a route or publishing. The count is zero with fresh state.

By default, accepted registrations are stored in a directory named after the
absolute configuration pathname with `.state` appended. The optional
`daemon --state-dir ABSOLUTE_STATE_DIRECTORY` selects another dedicated local
directory. Reuse the same configuration/state on restart, protect saved
arguments like configuration, and do not edit or copy live state.

## Install the registration skill

The action ships the
[`register-codex-queue` skill](skills/register-codex-queue/SKILL.md). Copy the
entire `skills/register-codex-queue` directory into a skills directory used by
your Codex installation, then follow that installation's procedure for
discovering skills in the official [Build skills guide](https://learn.chatgpt.com/docs/build-skills).
Confirm that `register-codex-queue` is available in your installation before
invoking it. In the current top-level conversation, ask for example:
“Register this conversation with the configured Codex Queue route.” The
skill's source in this repository is authoritative; there is no fixed home
path or automatic installer.

The skill needs these values from your hub configuration:

Use the [watch-arguments instructions below](#direct-registration-and-destination-arguments)
for both skill and direct registration.

- Absolute paths to the action executable and hub client executable.
- The hub's loopback address.
- The configured input plugin name and its watch-arguments file path.
- The configured output plugin name, which must refer to this Codex Queue
  action.

The helper reads `CODEX_THREAD_ID` and `CODEX_SESSION_ID` from the skill's
invoking process. It requires both to be canonical, non-nil UUIDs with the same
value. Invoke the skill directly in the top-level conversation. Do not delegate
registration or set, copy, or replace either identity variable. Environments
that cannot provide both matching values are unsupported.

The helper invokes the existing hub client once, using a temporary destination
file containing only the validated thread ID. If it cannot remove that file,
it writes a `register-current warning:` line to stderr before reporting the
outcome; the warning does not change the registration result. Confirmed success
exits with code 0 and prints the hub's successful JSON response, including the
subscription ID. Retain that ID together with the daemon address for explicit
cancellation. A valid client rejection exits with code 1 and reports its
`code` and `message` on stderr. Local validation or process-start failures also
exit with code 1 and write a stderr line beginning with
`register-current failed:`. If the client starts but the result cannot be
confirmed, the helper exits with code 1 and writes a stderr line beginning
with `register-current outcome unknown:`; a registration may exist. The helper
never retries an uncertain registration.

## Direct registration and destination arguments

[`examples/destination.json`](examples/destination.json) documents the direct
hub-client destination schema. It must contain exactly one `thread_id` string
with a canonical lowercase `8-4-4-4-12` UUID value. The skill path generates
this file temporarily; a direct client user supplies it explicitly.

Register the manual watch and Codex destination through the hub client after
the daemon is ready:

```text
HUB_EXECUTABLE register --input-plugin manual --watch-args-file WATCH_ARGS_PATH --output-plugin codex --destination-args-file DESTINATION_ARGS_PATH --address LOOPBACK_ADDRESS
```

Use the manual trigger's
[`examples/watch.json`](../../triggers/manual/examples/watch.json) for the
watch-arguments shape. Replace the complete placeholder string with an
absolute path whose parent exists and is writable; write valid JSON with a
serializer. The hub does not expand placeholders.

## Publish and observe

The manual trigger accepts nonempty UTF-8 plain text from one watched file,
between 1 and 8,192 bytes; it does not require a JSON structure. Publish by
writing a temporary sibling file, closing it, and renaming it onto the watched
path. Use a unique marker in the event so you can identify the resulting
message later in the target conversation. To follow the action result, inspect
the foreground hub's JSON logs: with one marked event published at a time,
find its `event_admitted` record by time and active subscription, then follow
that `delivery_id` through `delivery_attempt` and `delivery_result`. These log
records do not include the event context, so the marker itself cannot be
matched directly to a log entry. The event context is treated as external data
and encoded as JSON in the queue message; it cannot change the registered
destination or grant the conversation additional authority.

The action starts one Codex process for each valid event. It uses the registered
thread ID as the only recipient and passes the message as an argument, without
a shell. A successful result requires a zero exit, valid output, and one exact
queue acknowledgement naming the same thread. The result `accepted` confirms
queue acceptance only. Verify later conversation processing separately.

| Result | Meaning |
| --- | --- |
| `accepted` | Codex confirmed that it queued the message for the registered thread. |
| `failed` | A local validation error or process-start failure occurred before queue acceptance. |
| `unknown` | Codex started, but the action could not confirm whether the message was queued. |

An unknown result can follow successful queue acceptance. Inspect the target
conversation before publishing another event for the same purpose. The action
does not retry. If the target is busy, queue delivery waits without interrupting
it; the action never substitutes a steering or new-conversation command.

## Cancel one subscription

Use the unchanged `subscription_id` returned by the registration you want to
remove, and its configured hub address. Ask the bundled skill to cancel that
explicit ID, or run its direct command yourself:

```text
HUB_EXECUTABLE unsubscribe --subscription-id REGISTERED_SUBSCRIPTION_ID --address LOOPBACK_ADDRESS
```

Replace the placeholders with your retained ID and configured values. This
operation does not use `register-current`, either Codex identity variable, or
the original argument files. Never infer the target from the conversation.
Require exit zero and `{"ok":true,"subscription_id":"REGISTERED_SUBSCRIPTION_ID"}`
with the exact requested ID. Only that watch and its saved registration/progress
are removed. An already-issued queue request may still complete, and an
accepted Codex message may still be processed. Cancellation cannot recall it.

Keep an unrelated subscription B registered with its own returned ID and a
different manual trigger path. After success, publish distinct A/B markers as
described above. Observe B's accepted delivery and later message processing in
its registered conversation, with no new A `delivery_attempt`. Stop normally
and wait for daemon/plugin exits, then restart from the same configuration and
state: A remains absent, B is restored with its saved ID. The root
[local example](../../../README.md#cancel-one-subscription) shows the two-watch
publication and restart sequence using file output.

`unknown_subscription` includes an already-removed ID and is a failure, not
proof of which caller removed it. `unsubscribe_unsupported` requires upgrading
the daemon and input together while stopped; reuse the saved ID after restart.
For `unsubscribe_busy`, wait for the executing request's result before an
explicit retry. `cancellation_rejected`, `plugin_unavailable` and `storage_error`
can stop new attempts while leaving the registration saved: report the
diagnostic's failing stage and whether the removal request was durably saved.
Before that request is saved, restart may restore the watch; afterwards startup
completes deletion before readiness. Repair the cause before
an explicit same-ID retry or normal restart. Transport loss or a mismatched
response ID leaves the outcome uncertain. Do not automatically retry or select
another ID. Follow the [cancellation recovery guide](../../../docs/design/windows-operations.md#cancel-one-subscription)
without modifying live state or discarding a pending intent.

## Stop and recover

Stop the hub with its existing `stop --address` command and wait for both
configured plugin processes to exit. Uncancelled registrations and their IDs
survive normal shutdown. Reuse the same configuration/state, restore manual
trigger absence deliberately while stopped, then restart and wait for
`daemon_ready` with the saved route count before publishing a new event. No
re-registration or original temporary destination file is needed. Startup
revalidates the destination with the current action and re-establishes its
watch; any saved route that cannot be restored prevents the whole startup.

Queued/in-flight hub events and incomplete registrations are discarded. The hub
cannot retract a message that Codex already accepted and does not replay Codex
work, collect stopped events, or guarantee crash recovery. For invalid saved
state or a leftover lock, stop all owners and preserve state before following
the [offline recovery procedure](../../../docs/design/windows-operations.md).

If the compatibility check fails, select a compatible CLI executable and
shared server, then restart deliberately. If delivery is unknown, inspect the
target conversation before publishing another event. If registration is
unknown, the foreground hub's `subscription_active` logs may help the hub
operator investigate. They report plugin names and a subscription ID, but do
not identify the helper invocation or its watch and destination arguments. A
matching plugin pair may describe an earlier or different route, so it cannot
confirm this registration. The hub has no route status or list command, and a
missing log record does not prove that no route exists. Leave the outcome
unknown and ask the hub operator to investigate before another deliberate
attempt. Keep local
machine access, Codex storage, and event content under the operator's existing
access controls.

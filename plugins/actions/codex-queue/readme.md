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

Obtain the source with Git, then use Go 1.26.0 or later from the repository root:

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
```

Build the hub, manual trigger,
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

The action requires the absolute Codex executable path:

```text
CODEX_QUEUE_ACTION_EXECUTABLE --codex-executable CODEX_EXECUTABLE
```

For retained notification evidence and safe automatic retry, append the optional
valueless `--notification-delivery` flag once. In the materialized daemon JSON,
the action's `args` becomes:

```json
["--codex-executable", "/absolute/path/to/codex", "--notification-delivery"]
```

Replace the path with your selected native executable. Omitting the option keeps
the one-attempt mode. Upgrade host and action together while stopped and preserve
the entire stopped state tree first; older hosts can reject the new capability.
Removing the option cannot downgrade a route with retained notification records.

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
absolute path that is absent at registration and whose parent exists and is
writable; write valid JSON with a
serializer. The hub does not expand placeholders.

## Publish and observe

The manual trigger accepts nonempty UTF-8 plain text from one watched file,
between 1 and 8,192 bytes; it does not require a JSON structure. Publish by
writing a temporary sibling file, closing it, and renaming it onto the watched
path. Use a unique marker in the event so you can identify the resulting
message later in the target conversation. In default mode, follow the action
result in the foreground hub's JSON logs: with one marked event published at a time,
find its `event_admitted` record by time and active subscription, then follow
that `delivery_id` through `delivery_attempt` and `delivery_result`. These log
records do not include the event context, so the marker itself cannot be
matched directly to a log entry. With `--notification-delivery`, use the
[notification inspection commands below](#inspect-notifications-and-diagnose-stopping)
instead: this mode does not emit those delivery attempt/result logs.
The event context is treated as external data
and encoded as JSON in the queue message; it cannot change the registered
destination or grant the conversation additional authority.

The action starts one Codex process per delivery attempt. It uses the registered
thread ID as the only recipient and passes the message as an argument, without
a shell. A successful result requires a zero exit, valid output, and one exact
queue acknowledgement naming the same thread. The result `accepted` confirms
queue acceptance only. Verify later conversation processing separately.

The following log results apply to default mode. Notification mode exposes
acceptance and retry state through the inspection commands below.

| Result | Meaning |
| --- | --- |
| `accepted` | Codex confirmed that it queued the message for the registered thread. |
| `failed` | A local validation error or process-start failure occurred before queue acceptance. |
| `unknown` | Codex started, but the action could not confirm whether the message was queued. |

An unknown result can follow successful queue acceptance. Inspect the target
conversation before publishing another event for the same purpose. The action
does not retry. If the target is busy, queue delivery waits without interrupting
it; the action never substitutes a steering or new-conversation command.

## Inspect notifications and diagnose stopping

With `--notification-delivery` enabled, retain the successful registration's
`subscription_id`. After publishing, list that subscription's notifications and
select a notification ID using its admission time:

```text
HUB_EXECUTABLE notifications --subscription-id REGISTERED_SUBSCRIPTION_ID --address LOOPBACK_ADDRESS
HUB_EXECUTABLE notification --notification-id NOTIFICATION_ID --address LOOPBACK_ADDRESS
```

Replace the placeholders with your hub path, saved subscription ID, selected
notification ID and configured address. The list returns UUID-ordered IDs,
`created_at`, `acceptance`, `retry_state`, `attempt_count` and `reason`. It is
not newest-first. Pass a non-null `next_after` as `--after` to continue;
`--limit` accepts 1–20. New IDs can sort before a cursor, so restart listing
when checking for new admissions. Cancelled subscriptions can still have history.

Detail reports the captured `destination_args.thread_id`, original subscription,
`created_at`/`expires_at`, acceptance and retry state, plus each attempt's UUID,
ordinal, reservation/issue/finish times, status, reason, retry guidance and
receipt. A null `issued_at` does not prove non-submission. Context, watch
arguments, environment and process argv are not included. Protect the local
state tree, which does retain context and routing values; explicit destination
inspection is available to trusted local operators.

Both commands are read-only: they neither submit nor retry. The detail command
assembles bounded chunks internally. If the view changes, it restarts once;
continued change fails with `notification_changed` rather than printing a mixed
record. An empty list or `notification_not_found` is not proof that no message
ever reached Codex.

Read `acceptance` independently of `retry_state`:

| Observation | Meaning and operator action |
| --- | --- |
| `not_attempted` / `in_progress` acceptance | No result yet. Inspect again without publishing another event. |
| `unsubmitted` with `scheduled` retry | Every previous attempt is proven unsubmitted and the latest failure is retryable. The hub waits until `next_attempt_at`; it can make at most three attempts, with minimum two seconds after the first result and ten after the second, or longer destination guidance. The notification, destination and context stay fixed; attempt IDs change. |
| `unsubmitted` with `permanent` retry | A nonretryable pre-launch failure, such as missing executable, permission or invalid configuration, stopped work. Stop/wait, preserve state, repair the cause and restart for future distinct events; this notification does not become eligible. |
| `unsubmitted` with `exhausted` retry | The finite attempt budget is consumed. Restart does not reset it. Inspect attempt reasons; there is no manual retry. |
| `unknown` acceptance | The queue process may have submitted. Nonzero exit, timeout or absent/mismatched ACK after start cannot prove non-submission. Stop retry immediately and inspect the destination separately; uncertainty may remain unresolved. |
| `rejected` acceptance | Confirmed non-acceptance after submission is not proof of non-submission; no retry. |
| `queue_accepted` acceptance | The registered thread's matching ACK confirmed queue acceptance. `receipt` is its queue token, not conversation-receipt or work-completion evidence. No resend after restart. |
| `blocked` retry | Scheduled work cannot issue because of `notification_storage_error`, `clock_regression` or `plugin_unavailable`. The first applicable reason in that order is shown; `storage_blocked` separately reports the global storage gate. For storage/plugin failures, stop/wait, preserve state, repair and restart; there is no same-process recovery or automatic plugin restart. A regressed clock must catch up before issuance or expiry cleanup. |
| `cancelled` / `expired` retry | No new attempt can issue. A previously issued request can still have acceptance evidence; that evidence cannot revive the watch or retry. |

Only recognized temporary process-resource failures **before** Codex starts can
be retryable. The action starts at most one queue process per request and never
retries itself; the hub makes eligible later attempts without an operator retry
command or an agent polling turn. Merely passing readiness or observing a
successful delivery does not demonstrate that a transient failure occurred.
A bounded matching late acknowledgement can improve retained evidence without
reopening retry; this action does not start a lookup process to resolve unknown.

Evidence expires seven days after admission. Expiry never makes old work fresh;
not-found after cleanup means a retention limit. The store retains at most
1,024 notifications, rejecting new admission with `notification_capacity_exceeded`
until normal expiry/cleanup frees space. It never evicts unexpired evidence.
These limits and local inspection do not provide general deduplication of
separate source events. Republishing context, re-registering or selecting a new
state directory is not safe recovery of an uncertain notification.

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

In default mode, queued/in-flight hub events and incomplete registrations are discarded. The hub
cannot retract a message that Codex already accepted and does not replay Codex
work, collect stopped events, or guarantee crash recovery. For invalid saved
state or a leftover lock, stop all owners and preserve state before following
the [offline recovery procedure](../../../docs/design/windows-operations.md).

In notification mode, normal stop saves known results, records uncertain issued
attempts as unknown, and retains eligible queued or waiting unsent work. After
successful saves and process exits, restart with the same configuration/state
and inspect the same notification ID. Only still-eligible records resume under
the original identity, due time and remaining budget. Missing safety records or
corrupt state block startup: preserve the stopped tree and restore its original
matching evidence/configuration, never an older snapshot. If evidence cannot be
restored, leave it blocked. Do not delete safety files or remove the opt-in flag
to force startup. Rollback to older binaries on a state tree with notification
records is unsupported. Notification evidence survives successful unsubscribe
until expiry; removing source progress is not permission to remove that evidence.

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

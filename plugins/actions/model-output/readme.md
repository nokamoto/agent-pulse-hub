# Model-output action

The model-output action receives routed event context and appends one JSONL
record to a local file. It is stored under the bundled **action** role and uses
the public plugin protocol v2 **output** role. Its daemon configuration name
remains `model`.

## Prerequisites and build

Use Go 1.26.0 or later from the repository root. Build the hub, manual trigger,
and model-output action from the same revision. Build this executable with:

```text
go build -o MODEL_OUTPUT_EXECUTABLE_PATH ./plugins/actions/model-output
```

Replace `MODEL_OUTPUT_EXECUTABLE_PATH` with the absolute output path you choose,
including the executable suffix reported by `go env GOEXE`. The configured
`executable` value must be that same absolute path. The paired
[manual trigger](../../triggers/manual/readme.md) has its own build command and
examples.

## Configure and run

[`examples/daemon.json`](examples/daemon.json) shows the complete daemon
configuration for the manual trigger and this action. Before using it, replace
both executable placeholder strings with absolute paths and serialize the JSON.
Keep the instance names `manual` and `model`, the public roles `input` and
`output`, and the empty `args` arrays as shown; `trigger` and `action` describe
directory roles and are not configuration values.

To retain per-notification evidence and enable core-owned safe retry, set only
the `model` entry's `args` to `["--notification-delivery"]`. This optional flag
takes no value and may appear once. Without it, the action keeps its one-attempt
mode. Stop and wait before changing configuration, preserve the entire stopped
state tree, and upgrade host and action together; older hosts may reject the
capability. A route with retained notifications cannot downgrade by removing it.

[`examples/destination.json`](examples/destination.json) shows this plugin's
arguments. Set `output_file` to an absolute file path in an existing writable
parent directory. Start with an absent file; if the file already exists, it
must pass the plugin's JSONL validation. Replace the complete placeholder
string and serialize the JSON. The daemon does not expand placeholders.

The daemon launches the plugin and owns its process lifecycle. Do not launch the
plugin executable directly. Start the hub in the foreground, then register the
manual watch and this destination through the local client. Replace each
uppercase placeholder with the corresponding executable, absolute JSON path,
or loopback endpoint before running these command forms:

```text
HUB_EXECUTABLE daemon --config DAEMON_CONFIG_PATH --address LOOPBACK_ADDRESS
HUB_EXECUTABLE register --input-plugin manual --watch-args-file WATCH_ARGS_PATH --output-plugin model --destination-args-file DESTINATION_ARGS_PATH --address LOOPBACK_ADDRESS
```

By default, registrations are stored beside the configuration in a directory
whose name is the absolute configuration pathname plus `.state`. Select another
dedicated local directory with `daemon --state-dir ABSOLUTE_STATE_DIRECTORY`
when needed, and reuse it on restart. Protect saved arguments like configuration;
do not modify or copy live state.

Wait for `daemon_ready` and its `restored_registrations` count before registering
or publishing. Registration
validates the destination but does not create the output file. The complete
build, paired registration, trigger publication, observation, and shutdown
procedure is in the [model-plugin operations guide](../../../docs/design/windows-operations.md).

## Observe results and failures

After a trigger is routed, the action appends one UTF-8 JSON object followed by
LF. Each record contains `delivery_id`, `input_plugin`, `subscription_id`,
`context`, `external_data`, `permission_notice`, and `observed_at`. The context
is visible in the file; do not put secrets in trigger content. A successful
write and close returns `accepted`, but this is not an fsync or durable-storage
guarantee.

The destination parent must exist and be writable. An existing destination
must be a readable regular file accepted by the JSONL validator. Invalid
arguments, a missing/unwritable parent, an unreadable or invalid destination,
or an unavailable plugin prevents normal delivery. An output-open failure
before writing begins reports `failed`. A write or close failure after writing
starts can report `unknown`; so can loss of confirmation after the write.
Inspect the destination after `unknown`, because a complete or partial record
may already be present.

In notification mode, the first write invocation is the submission boundary.
Only a recognized temporary resource error opening the file before any write
is retryable `unsubmitted`; path, permission and unclassified pre-write errors
are permanent `unsubmitted`. Any write/close failure after invoking write is
`unknown`, including a zero-byte failed write. Full write and close is accepted.
The action never retries, repairs or deduplicates records; the hub alone issues
eligible later attempts. Use one output instance per file; multiple routes in
that instance may share it. Do not let another process modify the file while
that instance is using it. The file format stays the same.

Follow the local walkthrough's
[notification inspection steps](../../triggers/manual/walkthrough.md#retain-notification-evidence-and-enable-safe-retry)
using the saved subscription ID and an ID returned by `notifications`.
`notification` shows the captured destination, acceptance, retry state and each
attempt's ID/times/outcome; both commands are read-only. `queue_accepted` is the
shared acceptance label here for successful file output, not an agent receipt
or a crash-durability guarantee. All previous attempts must be proven unsubmitted
and the latest failure retryable for up to three total attempts under one
notification ID, with minimum two/ten-second waits or longer guidance.
Unknown, permanent failure and exhaustion stop retry. Republishing the same
context is a new event, not safe recovery of uncertain output.

## Recovery

The default mode does not retry delivery. Plugins never restart automatically.
Stop the daemon, wait for plugin exits, and preserve the damaged
destination and stopped state tree. Completed registrations survive normal
shutdown with their IDs; in default mode queued/in-flight events and incomplete
registrations do not. Restore valid destination conditions at the saved path, or deliberately
remove the affected saved registration while stopped before selecting fresh
output/trigger paths and registering a new route. Inspect trigger/claim leftovers
and restore trigger absence. Restart with the same configuration/state, wait for
`daemon_ready`, then publish a deliberately chosen distinct event. Recovery does
not retry the uncertain event or repair its record. There is no stopped-event
collection, replay, or crash-recovery guarantee.
See the [operations guide](../../../docs/design/windows-operations.md)
for the paired recovery procedure.

In notification mode, successful normal stop also retains evidence and eligible
unsent work. Restart from the same configuration/state and inspect the original
notification ID: only still-eligible work resumes with its remaining budget.
Unknown and accepted work is never resent. Evidence lasts seven days from
admission; expiry stops scheduling and not-found does not prove non-submission.
At 1,024 retained notifications, admission fails until normal expiry frees space.
Storage/plugin failure blocks issuance until normal stop, repair and restart;
a backward clock must catch up before issuance or expiry cleanup. Never delete
safety records, use an older snapshot or roll back binaries on a tree containing
notifications. Preserve the stopped tree and restore original matching evidence;
if unavailable, leave it blocked. See the
[status diagnosis guide](../codex-queue/readme.md#inspect-notifications-and-diagnose-stopping)
for shared states and the [notification policy](../../../docs/design/notification-delivery.md)
for recovery limits.

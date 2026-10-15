# Model-output action

The model-output action receives routed event context and appends one JSONL
record to a local file. It is stored under the bundled **action** role and uses
the public plugin protocol v2 **output** role. Its daemon configuration name
remains `model`.

## Prerequisites and build

Use Go 1.26.0 or later from the repository root. Build the hub and both model
plugins from the same revision. Build this executable with:

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

Wait for both plugins to report ready before registering. Registration
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

## Recovery

No delivery is retried and the daemon does not automatically restart the
plugin. Preserve and inspect a damaged destination, stop the daemon and wait
for plugin exits, then choose fresh absent output and trigger paths in an
existing writable directory. Restart the daemon, wait for both plugins to
report ready, then register again and publish only a deliberately chosen
distinct event; recovery does not retry the uncertain event or repair its
record. See the [operations guide](../../../docs/design/windows-operations.md)
for the paired recovery procedure.

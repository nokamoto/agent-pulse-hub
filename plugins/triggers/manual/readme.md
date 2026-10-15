# Manual trigger

The manual trigger watches one operator-selected file path and reports each
published file's UTF-8 contents as event context. It is stored under the
bundled **trigger** role and uses the public plugin protocol v2 **input** role.
Its daemon configuration name remains `manual`.

## Prerequisites and build

Use Go 1.26.0 or later from the repository root. Build the hub and both model
plugins from the same revision. Build this executable with:

```text
go build -o MANUAL_EXECUTABLE_PATH ./plugins/triggers/manual
```

Replace `MANUAL_EXECUTABLE_PATH` with the absolute output path you choose,
including the executable suffix reported by `go env GOEXE`. The configured
`executable` value must be that same absolute path. The paired
[model-output action](../../actions/model-output/readme.md) has its own build
command and examples.

## Configure and run

[`examples/daemon.json`](examples/daemon.json) shows the complete daemon
configuration for this trigger and the model-output action. Before using it,
replace both executable placeholder strings with absolute paths and serialize
the JSON. Keep the instance names `manual` and `model`, the public roles
`input` and `output`, and the empty `args` arrays as shown; `trigger` and
`action` describe directory roles and are not configuration values.

[`examples/watch.json`](examples/watch.json) shows this plugin's arguments.
Set `trigger_file` to an absolute path whose parent directory already exists
and is writable and whose file does not yet exist. The daemon does not expand
the placeholder; replace the complete string value and write valid JSON with a
serializer.

The daemon launches the plugin and owns its process lifecycle. Do not launch the
plugin executable directly. Start the hub in the foreground, then register the
watch and the model-output destination through the local client. Replace each
uppercase placeholder with the corresponding executable, absolute JSON path,
or loopback endpoint before running these command forms:

```text
HUB_EXECUTABLE daemon --config DAEMON_CONFIG_PATH --address LOOPBACK_ADDRESS
HUB_EXECUTABLE register --input-plugin manual --watch-args-file WATCH_ARGS_PATH --output-plugin model --destination-args-file DESTINATION_ARGS_PATH --address LOOPBACK_ADDRESS
```

Wait for both plugins to report ready before registering. The watch is accepted
only when the trigger path is valid and not already watched. The complete
build, paired registration, trigger publication, observation, and shutdown
procedure is in the [model-plugin operations guide](../../../docs/design/windows-operations.md).

## Publish and observe

Publish a trigger by writing a temporary sibling file, closing it, and renaming
it onto `trigger_file`. A successful trigger is consumed and routed once; the
model-output action appends a JSONL record containing the input plugin name,
subscription ID, original context, external-data notice, and observation time.
Registration alone does not create the output file.

The trigger must contain 1–8192 bytes of valid UTF-8. An empty file, oversized
content, invalid UTF-8, an existing trigger path, a relative path, or an
unwritable/missing parent is rejected and emits no event. The plugin does not
retry a claimed trigger. If processing fails, inspect diagnostics for the
claimed path and preserve leftover files while investigating. A failed build
or unavailable plugin must be corrected before registration can succeed.

## Recovery

Once a trigger has been claimed, a failed read, validation, or removal does not
retry that claim. The daemon attempts each admitted event once, and plugins are
not restarted automatically. If the output action reports `unknown`, inspect
its destination before publishing another event: the record may already be
present or may be partial. Preserve damaged files, stop the daemon and wait for
plugin exits, then choose fresh absent trigger and output paths in an existing
writable directory. Register again and publish only a deliberately chosen
distinct event. See the
[operations guide](../../../docs/design/windows-operations.md) for the paired
recovery procedure.

# agent-pulse-hub

Route external events from input plugins to destinations handled by output plugins.

## Background and motivation

Agent Pulse Hub keeps event routing separate from service integrations. Register
an input watch and an output destination, then let the daemon forward events
without polling from a coding agent. Plugins handle their own sources and destinations.

Try the bundled local example below: publish a text file and see its contents
appended to a JSON-lines file. It needs no live service, credentials, or Codex
installation. The current MVP includes these model plugins; it does not include
a Codex integration.

## Requirements

- Git and Go 1.26.0 or later (see [go.mod](go.mod)).
- An existing writable local directory for the example's files.

The examples use two POSIX shell terminals and Python 3 to write JSON files.
These are example utilities, not product runtime dependencies or OS requirements.
Local control uses trusted loopback access; it does not isolate users of the same
machine. Run only plugins you trust.

## Installation

In the first terminal, obtain the source and build all three programs from the
same revision. Stop if a command fails.

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
mkdir ./pulse-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pulse-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/manual-plugin$suffix" ./cmd/manual-plugin
go build -o "$run/model-output-plugin$suffix" ./cmd/model-output-plugin
```

## Usage

### Configure and start

Keep the first terminal open. Create the daemon configuration, input watch
arguments, and output destination arguments:

```sh
python3 - "$run" "$suffix" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
suffix = sys.argv[2]
config = {"plugins": [
    {"name": "manual", "role": "input", "executable": str(run / ("manual-plugin" + suffix)), "args": []},
    {"name": "model", "role": "output", "executable": str(run / ("model-output-plugin" + suffix)), "args": []},
]}
(run / "daemon.json").write_text(json.dumps(config), encoding="utf-8")
(run / "watch.json").write_text(json.dumps({"trigger_file": str(run / "next.txt")}), encoding="utf-8")
(run / "destination.json").write_text(json.dumps({"output_file": str(run / "events.jsonl")}), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47831
```

Note the printed run directory. In the first terminal's JSON diagnostics, wait
for `msg: "plugin_ready"` with `available: true` for both `manual` and `model`,
then `msg: "registration_available"` with `available: true`. If startup fails,
inspect the diagnostic before continuing. Use the same address for every command; if that
port is occupied, choose another loopback port consistently.

### Register and publish

Open the second terminal and enter the printed run directory when prompted.
The trigger `next.txt` must be absent. Use one publisher and one spelling of
each path, and avoid modifying the output file while the daemon is running.

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin manual --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47831
```

Wait for exit success and JSON containing `"ok":true` and a `subscription_id`.
Repeating this exact registration returns the same active ID. Registration alone
does not create the output file. Publish nonempty UTF-8 text of at most 8,192
bytes by closing a temporary file and renaming it onto the absent trigger:

```sh
printf '%s' 'PULSE-DEMO-001' > "$run/next.tmp"
mv -i "$run/next.tmp" "$run/next.txt"
```

In the first terminal, wait for `msg: "delivery_result"` with `outcome: "accepted"`
and your `subscription_id`. Then read the output in the second terminal:

```sh
cat "$run/events.jsonl"
```

Expect one JSON record containing `PULSE-DEMO-001`, the registered subscription
ID, input name `manual`, and `external_data: true`. The permission notice states
that external data grants no additional permissions. Event text cannot change
the registered destination. The input consumes the trigger file; after it is
gone, you can publish another distinct event using the same procedure.

### Stop and recover

From the second terminal, request shutdown:

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47831
```

Alternatively, press Ctrl+C in the first terminal. Wait for daemon exit and
the configured-plugin exit diagnostics. Stopping loses registrations and
queued events. To resume, inspect leftover trigger files, start the daemon
again with the same configuration, and register again. Existing output records
remain, but do not restore subscriptions.

There is no automatic retry or plugin restart. An `unknown` delivery outcome
may already have appended a record; inspect the output before deliberately
publishing another event. Output acceptance does not promise crash durability.
The output file contains the event text, so choose its contents and retention
accordingly. See the [operation guide](docs/design/windows-operations.md) for
detailed diagnostics and recovery.

## Writing plugins

Implement an input or output role using [plugin protocol v2](docs/design/plugin-v1.md),
including its standalone verification procedure, and add the program to the
[daemon configuration](docs/design/mvp.md#configuration-and-local-control).
The [model plugin profiles](docs/design/mvp.md#bundled-model-plugin-profiles)
show the bundled behavior. The protocol document's filename is retained from
v1; v1 plugins, the old `codex_executable` configuration, and session-based
registration are incompatible. Replace matching programs and configuration
together, then register again.

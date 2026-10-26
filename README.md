# agent-pulse-hub

<!-- Keep the following sentence identical to the repository description. Do not edit it. -->
Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Background and motivation

Agent Pulse Hub keeps event routing separate from service integrations. Register
an input watch and an output destination, then let the daemon forward events
without polling from a coding agent. Plugins handle their own sources and destinations.

Try the bundled local example below: publish a text file and see its contents
appended to a JSON-lines file. It needs no live service, credentials, or Codex
installation. The optional [Codex Queue action](plugins/actions/codex-queue/readme.md)
can instead send events to an existing Codex conversation.
The [GitHub review request trigger](plugins/triggers/github-review-request/readme.md)
can watch for pull requests that request your review directly.

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
go build -o "$run/manual-plugin$suffix" ./plugins/triggers/manual
go build -o "$run/model-output-plugin$suffix" ./plugins/actions/model-output
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
config = json.loads(Path("plugins/triggers/manual/examples/daemon.json").read_text(encoding="utf-8"))
executables = {"manual": "manual-plugin", "model": "model-output-plugin"}
for plugin in config["plugins"]:
    plugin["executable"] = str(run / (executables[plugin["name"]] + suffix))
watch = json.loads(Path("plugins/triggers/manual/examples/watch.json").read_text(encoding="utf-8"))
watch["trigger_file"] = str(run / "next.txt")
destination = json.loads(Path("plugins/actions/model-output/examples/destination.json").read_text(encoding="utf-8"))
destination["output_file"] = str(run / "events.jsonl")
(run / "daemon.json").write_text(json.dumps(config), encoding="utf-8")
(run / "watch.json").write_text(json.dumps(watch), encoding="utf-8")
(run / "destination.json").write_text(json.dumps(destination), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47831
```

Note the printed run directory. In the first terminal's JSON diagnostics, wait
for `msg: "plugin_ready"` with `available: true` for both `manual` and `model`,
then `msg: "daemon_ready"` with `restored_registrations: 0` for this fresh setup.
If startup fails,
inspect the diagnostic before continuing. Use the same address for every command; if that
port is occupied, choose another loopback port consistently.

Successful registrations are saved in `daemon.json.state` beside this example's
configuration. By default the state directory is the absolute configuration
pathname with `.state` appended; `daemon --state-dir <absolute-directory>`
selects another dedicated local directory. Reuse the same configuration and
state on restart. Do not edit, move, copy, or share a live state directory.
Argument values are retained there, so protect it like configuration.

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
the configured-plugin exit diagnostics before restarting. Normal shutdown
retains completed registrations and their IDs. It discards queued and in-flight
hub events and incomplete registrations; it cannot retract accepted output.

To resume this example, inspect leftover triggers while stopped, including files
the manual trigger renamed for processing (see claimed paths in diagnostics).
Restore trigger absence deliberately. Start the same daemon command
with the same configuration and state. Wait for `daemon_ready` with
`restored_registrations: 1`, then publish a new distinct event as above without
registering again. Its output must carry the saved subscription ID. The original
watch and destination argument files are not needed for restoration. Equivalent
explicit registration still returns that active ID.

Startup revalidates every saved route. An invalid state record, unavailable
referenced plugin, invalid destination, or already-present manual trigger
prevents the whole daemon from becoming ready. Stop all owners and preserve
state before deliberate repair; follow the [operation guide](docs/design/windows-operations.md#8-demonstrate-deliberate-saved-state-recovery)
for recovery, including a leftover state lock after abnormal termination.

There is no automatic retry or plugin restart. An `unknown` delivery outcome
may already have appended a record; inspect the output before deliberately
publishing another event. There is no replay, stopped-event collection, or
forced-termination/power-loss recovery guarantee. Output acceptance does not
promise crash durability.
The output file contains the event text, so choose its contents and retention
accordingly. See the [operation guide](docs/design/windows-operations.md) for
detailed diagnostics and recovery.

### Send events to Codex

Use the [Codex Queue action guide](plugins/actions/codex-queue/readme.md) to build
and configure the optional action with the manual trigger. It requires a trusted
native Codex executable supporting `queue --thread --message` and access to the
target conversation's shared server. The guide covers deliberate installation
and discovery of the bundled registration skill. Invoke it in the top-level
conversation with the configuration values it requests; both inherited Codex
identity variables must be present and equal. The skill never invents a target.

Publish only after confirmed registration. An `accepted` result means the event
was queued for the registered conversation; observe its later processing
separately. Inspect the target after an `unknown` result before deliberately
submitting another event. A restart restores saved routes and revalidates the
destination; it does not replay Codex work.

### Watch GitHub review requests

Follow the [GitHub trigger guide](plugins/triggers/github-review-request/readme.md)
to build the trigger from the same source revision and run a separate example
that writes pull request URLs to a local file. Supply a user token through the
daemon environment, or use saved GitHub CLI authentication. Environment-token
users do not need the GitHub CLI. The guide includes configuration, registration,
output inspection, and normal restart.

The trigger watches open requests addressed directly to the authenticated user
across repositories visible to that credential; it has no repository filter.
Team-only requests are excluded. Polling can miss a cancellation and re-request
between observations. Read the guide's search coverage and recovery limits
before relying on notifications. GitHub Enterprise Server 3.19.x is the designed
API target; real-service compatibility verification remains incomplete.

## Writing plugins

Implement an input or output role using [plugin protocol v2](docs/design/plugin-v1.md),
including its standalone verification procedure, and add the program to the
[daemon configuration](docs/design/mvp.md#configuration-and-local-control).
The [model plugin profiles](docs/design/mvp.md#bundled-model-plugin-profiles)
show the bundled behavior. The protocol document's filename is retained from
v1; v1 plugins, the old `codex_executable` configuration, and session-based
registration are incompatible. Replace matching programs and configuration
together, then register again.

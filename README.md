# agent-pulse-hub

Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Background and motivation

Agent Pulse Hub lets external events wake an existing Codex chat. This quickstart
uses the bundled manual plugin: publish a text file and see Codex reply.

## Requirements

- Git and Go (see [go.mod](go.mod)).
- Codex Desktop whose bundled CLI supports `codex queue`.

The examples use a POSIX shell and Python 3 to write JSON configuration files.
These example tools do not define a product OS requirement.

## Installation

In the first terminal, obtain the source and build both programs.
Stop if a command fails.

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
mkdir ./pulse-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pulse-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/manual-plugin$suffix" ./cmd/manual-plugin
```

Open the cloned folder as the project for your existing Codex chat.

## Usage

### Configure and start

In your existing Codex chat, ask it to locate the bundled CLI first:

```text
Find the absolute path of the CLI executable bundled with the Codex app hosting
this chat. Inspect the running process or app installation to identify it;
do not assume the first codex on PATH belongs to this app. Report the path,
or explain if it cannot be identified.
```

Continue only once that path is identified. In the first terminal, enter it
when prompted. Keep this terminal open
and note the printed run directory. Before configuring the daemon, confirm that
the CLI help lists `--thread` and `--message`; stop if it does not.

```sh
printf 'Absolute Codex executable path: '
IFS= read -r codex
"$codex" queue --help
```

After checking the help, write the configuration and start the daemon:

```sh
python3 - "$run" "$codex" "$suffix" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
config = {
    "codex_executable": sys.argv[2],
    "plugins": [{"name": "manual", "executable": str(run / ("manual-plugin" + sys.argv[3])), "args": []}],
}
(run / "daemon.json").write_text(json.dumps(config), encoding="utf-8")
(run / "watch.json").write_text(json.dumps({"trigger_file": str(run / "next.txt")}), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
printf 'Hub executable: %s\n' "$run/agent-pulse-hub$suffix"
printf 'Watch file: %s\n' "$run/watch.json"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json"
```

Wait for `plugin_ready` (`manual`) and `registration_available` in this terminal.

### Register the conversation

Send this in your Codex chat, replacing `<hub executable>` and `<watch file>`
with the printed paths. Wait for Codex to confirm registration.

```text
Use $register-pulse to register this conversation with hub executable
<hub executable>, plugin manual, and watch arguments from <watch file>.
When PULSE-DEMO-001 arrives, reply with that message.
```

### Trigger and stop

After the registration turn finishes, open a second POSIX shell terminal. Ensure
`next.txt` is absent and use only this terminal to publish. Write and close a
temporary file, then rename it so the plugin reads the complete text.

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
printf '%s' 'PULSE-DEMO-001' > "$run/next.tmp"
mv -i "$run/next.tmp" "$run/next.txt"
```

Your Codex chat should reply without another message from you. For problems, see the
[operations guide](docs/design/windows-operations.md).

Stop from the second terminal, or press Ctrl+C in the first:

```sh
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" stop
```

Wait for the daemon to exit. Stopping loses watches and pending events; restart
and register again before publishing. Inspect leftover files if a run fails.

## Writing plugins

Implement the [plugin protocol v1](docs/design/plugin-v1.md) and add your executable
to the [daemon configuration](docs/design/mvp.md#configuration-and-local-control).

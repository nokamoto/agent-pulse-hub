# agent-pulse-hub

Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Background and motivation

Agent Pulse Hub lets external events wake an existing Codex chat. This quickstart
uses the bundled manual plugin: publish a text file and see Codex reply.

## Requirements

- Git, Go (see [go.mod](go.mod)), and PowerShell 7 for this Windows walkthrough.
- Codex Desktop whose bundled CLI supports `codex queue`.

## Installation

In the first PowerShell terminal, obtain the source and build both programs.
Stop if a command fails.

```powershell
git clone https://github.com/nokamoto/agent-pulse-hub.git
Set-Location agent-pulse-hub
New-Item -ItemType Directory ./pulse-demo | Out-Null
$run = (Resolve-Path ./pulse-demo).Path
go build -o "$run\agent-pulse-hub.exe" ./cmd/agent-pulse-hub
go build -o "$run\manual-plugin.exe" ./cmd/manual-plugin
```

Open the cloned folder as the project for your existing Codex chat.

## Usage

### Configure and start

Continue in the first terminal. With Codex running, list its executable paths
and enter the one used by your Codex app when prompted. Keep this terminal open
and note the printed run directory. Before configuring the daemon, confirm that
the CLI help lists `--thread` and `--message`; stop if it does not.

```powershell
Get-Process codex | Select-Object -ExpandProperty Path -Unique
$codex = Read-Host 'Absolute Codex executable path'
& $codex queue --help
$config = @{
    codex_executable = $codex
    plugins = @(@{ name = 'manual'; executable = "$run\manual-plugin.exe"; args = @() })
}
$config | ConvertTo-Json -Depth 4 | Set-Content "$run\daemon.json" -Encoding utf8NoBOM
@{ trigger_file = "$run\next.txt" } | ConvertTo-Json | Set-Content "$run\watch.json" -Encoding utf8NoBOM
Write-Output "Run directory: $run"
& "$run\agent-pulse-hub.exe" daemon --config "$run\daemon.json"
```

Wait for `plugin_ready` (`manual`) and `registration_available` in this terminal.

### Register the conversation

Send this in your Codex chat, replacing `<run directory>` with the printed path.
Wait for Codex to confirm registration.

```text
Use $register-pulse to register this conversation with hub executable
<run directory>\agent-pulse-hub.exe, plugin manual, and watch arguments from
<run directory>\watch.json.
When PULSE-DEMO-001 arrives, reply with that message.
```

### Trigger and stop

After the registration turn finishes, open a second PowerShell terminal. Ensure
`next.txt` is absent and use only this terminal to publish. Write and close a
temporary file, then rename it so the plugin reads the complete text.

```powershell
$run = Read-Host 'Run directory printed in the first terminal'
Set-Content "$run\next.tmp" 'PULSE-DEMO-001' -Encoding utf8NoBOM -NoNewline
[System.IO.File]::Move("$run\next.tmp", "$run\next.txt")
```

Your Codex chat should reply without another message from you. For problems, see the
[operations guide](docs/design/windows-operations.md).

Stop from the second terminal, or press Ctrl+C in the first:

```powershell
& "$run\agent-pulse-hub.exe" stop
```

Wait for the daemon to exit. Stopping loses watches and pending events; restart
and register again before publishing. Inspect leftover files if a run fails.

## Writing plugins

Implement the [plugin protocol v1](docs/design/plugin-v1.md) and add your executable
to the [daemon configuration](docs/design/mvp.md#configuration-and-local-control).

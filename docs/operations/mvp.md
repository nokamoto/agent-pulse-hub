---
type: Playbook
title: Build and run the MVP on Windows
description: Build Agent Pulse Hub, register a Codex conversation, trigger a manual event, and stop the daemon.
sources:
  - id: mvp-requirements
    resource: ../requirements/mvp.md
    title: Minimal external event delivery MVP requirements
  - id: mvp-design
    resource: ../design/mvp.md
    title: Minimal external event delivery MVP design
---

# Build and run the MVP on Windows

This guide builds the daemon, the manual file-trigger plugin, and the
registration command from a clean checkout. The daemon runs in a foreground
PowerShell window. A Codex skill registers the current conversation, and the
manual plugin emits an event when a file is atomically moved into place.

## Prerequisites

- Windows 10 or later and Go 1.26 or later.
- A Codex CLI executable at the supported version `codex-cli
  0.162.0-alpha.2`. The daemon checks `codex --version` at startup and refuses
  any unrecognized version.
- A Codex conversation in which the registration skill can read
  `CODEX_THREAD_ID` and `CODEX_SESSION_ID`.

The Codex queue adapter supports only the version above. Updating that version
requires verifying the CLI response format before claiming support.

## Build

From the repository root, run the repository checks and build the three
executables:

```powershell
go run build/mage.go -d build -w . format
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . lint
go run build/mage.go -d build -w . guardrails

New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\pulse-daemon.exe .\cmd\pulse-daemon
go build -o .\bin\pulse-register.exe .\cmd\pulse-register
go build -o .\bin\pulse-manual-plugin.exe .\cmd\pulse-manual-plugin
```

## Configure and start the daemon

The configuration requires absolute paths to the Codex executable and every
plugin executable. Create a local JSON configuration. Replace the Codex path if
`codex.exe` is not on `PATH`:

```powershell
$codexExe = (Get-Command codex.exe -ErrorAction Stop).Source
$version = (& $codexExe --version).Trim()
if ($version -ne 'codex-cli 0.162.0-alpha.2') {
    throw "Unsupported Codex CLI version: $version"
}

$config = @{
    codex_executable = $codexExe
    plugins = @(
        @{
            name = 'manual'
            executable = (Join-Path (Get-Location) 'bin\pulse-manual-plugin.exe')
            args = @()
        }
    )
}
$configPath = Join-Path (Get-Location) 'pulse-config.json'
$utf8NoBom = New-Object System.Text.UTF8Encoding -ArgumentList $false
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 5), $utf8NoBom)
```

Open a separate PowerShell window, change to the repository root, and start the
daemon. Leave it running:

```powershell
& .\bin\pulse-daemon.exe --config .\pulse-config.json
```

Look for `daemon_ready` and `plugin_ready` in the log output. Invalid
configuration or an unsupported Codex version stops startup. A plugin startup
failure is logged; other configured plugins can still run.

## Register the current conversation

In the top-level Codex conversation that should receive the event, invoke the
repository skill `register-pulse-session`. Choose the configured plugin and
provide its watch arguments. For the manual plugin, choose an absolute
`trigger_file` path that does not exist yet and is inside a writable local
directory, such as:

```json
{"trigger_file":"C:\\Users\\<user>\\AppData\\Local\\Temp\\pulse-demo.json"}
```

Before registering, tell the conversation what to do with the next event. For a
basic demonstration, authorize it to reply once with the recognizable context
from that event. The skill will verify that both session environment variables
contain the same valid UUID, invoke `pulse-register`, and report the
subscription ID. It will not guess a missing session identity or delegate
registration to a subagent.

The manual plugin rejects a missing or relative `trigger_file`, an existing
target file, or a path whose parent directory is absent, unwritable, or
non-local. It also rejects a second watch of the same normalized path. Fix the
path and register again after a rejection.

## Trigger and observe an event

In a PowerShell window, use the exact trigger path registered above. The
temporary file must be in the same directory so the final move is atomic:

```powershell
$triggerPath = 'C:\Users\<user>\AppData\Local\Temp\pulse-demo.json'
if (Test-Path -LiteralPath $triggerPath) {
    throw "The trigger file already exists: $triggerPath"
}
$temporaryPath = Join-Path (Split-Path -Parent $triggerPath) ('.pulse-' + [guid]::NewGuid() + '.tmp')
$utf8NoBom = New-Object System.Text.UTF8Encoding -ArgumentList $false
[System.IO.File]::WriteAllText($temporaryPath, 'A review comment is ready: change 42.', $utf8NoBom)
[System.IO.File]::Move($temporaryPath, $triggerPath)
```

The plugin removes the trigger file after claiming it. For each event, correlate
`event_admitted`, `delivery_attempt`, and `delivery_result` by `delivery_id`.
The result's `outcome` is `accepted`, `failed`, or `unknown`; `delivery_discarded`
records an event dropped during shutdown. Queue acceptance means Codex
accepted the message for the registered conversation; it does not confirm that
the conversation completed the requested action. Do not trigger another event
to compensate for a failed or unknown delivery result.

## Stop and recover

Press **Ctrl+C** in the daemon window. The daemon stops accepting registrations,
signals the plugins, and terminates remaining child processes after a five
second grace period. The in-memory registry is cleared, queued events may be
dropped, and an in-flight result may be unknown. Delivery is attempted once and
is never retried. After restarting, register every watch again; old subscription
IDs are no longer active.

## Limits and diagnostics

- Protocol frame: 65,536 bytes including newline.
- Event context: 8,192 UTF-8 bytes.
- Active or pending registrations: 1,024.
- Queued or in-flight events: 128 total, delivered by one worker.
- Plugin readiness and watch acknowledgement: 10 seconds each.
- Codex version check: 10 seconds; each queue command: 30 seconds.
- Rendered Windows Codex command line: at most 24,000 UTF-16 code units.

Logs use UTC timestamps and include lifecycle, plugin, subscription, delivery,
and outcome information. They do not include raw event context or complete
command lines. A plugin that becomes unavailable loses its registrations and is
not restarted automatically. A busy session is queued by Codex when accepted;
an unrecognized or rejected queue response is reported as unknown or failed,
with no retry.

For the wire-level contract and examples, see [plugin protocol v1](../protocol/plugin-v1.md).

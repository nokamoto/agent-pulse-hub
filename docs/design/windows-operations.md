---
type: Design
title: Windows operation of the MVP
description: Build, configure, register, trigger, acknowledge, and stop the foreground MVP on Windows.
sources:
  - id: mvp-design
    resource: design/mvp.md
    title: Minimal external event delivery MVP
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Minimal external event delivery MVP
---

# Windows operation of the MVP

## Purpose and contract sources

Use this guide to build and run Agent Pulse Hub as one local Windows user,
register an existing Codex conversation, deliver recognizable test context, and
stop all watches. It is also the clean-build walkthrough for
[requirements/mvp](../requirements/mvp.md) NFR-004 and AC-009.

The [governing MVP design](mvp.md) owns the CLI, configuration, registration
skill, delivery, and lifecycle contracts. [Plugin protocol v1](plugin-v1.md)
owns the plugin wire contract. This guide supplies procedures and examples
derived from those contracts; it does not add a control interface or override
either source. Implementers use the design's
[command contract](mvp.md#windows-commands-and-registration-skill). Operators
can follow the steps below without implementing the wire protocol.

## Prerequisites and supported version

Use Windows, PowerShell 7, Git, and Go satisfying the repository's `go.mod` and
dependency toolchain requirements. Allow Go to obtain its required toolchain
and dependencies, or provision them beforehand. Use a source checkout of the
MVP implementation revision being evaluated. Run both terminals and Codex as
the same standard Windows user on the same computer. No administrator rights,
Windows service installation, global skill installation, or TCP listener is
required. Plugin executables are trusted programs chosen by that user.

The supported Codex CLI baseline is **`codex-cli 0.162.0-alpha.2`**. The governing
design records its version/help checks and idle/busy conversation demonstrations
on 2026-10-09. That historical evidence does not certify a different executable
or a new hub build. The maintainer must record the exact version and repeat the
real demonstration for implementation acceptance. The daemon rejects other
versions. Do not substitute a newer CLI because its help looks similar; obtain
the supported executable or complete separate adapter support verification and
the required design approval before using another version.

You need two PowerShell terminals: **D** holds the foreground daemon, and **T**
writes test files. Keep existing Codex conversations **A** (recipient) and **B**
(unregistered comparison) visible for acceptance testing. Open this repository
as A's project. The bundled skill is
`.agents/skills/register-pulse/SKILL.md`, invoked as `$register-pulse`. Confirm
it is discoverable in A before continuing. If it is absent, reopen the project
and check that the selected implementation revision contains it; do not guess
a registration command or create a different recipient conversation.

## 1. Build into a new directory

In terminal D, change to the repository root. Check `git status --short` and
use a clean source revision for an acceptance run; preserve unrelated changes
rather than deleting them. The following creates a unique directory on the
local disk and forces package rebuilding into it. It does not reuse an older
hub executable. Keep this terminal open so its variables remain available.

```powershell
$ErrorActionPreference = 'Stop'
git rev-parse HEAD
git status --short
go version
if ($LASTEXITCODE -ne 0) { throw 'Go is unavailable.' }
$run = Join-Path $env:LOCALAPPDATA ('AgentPulseHub\runs\' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run | Out-Null
$hub = Join-Path $run 'agent-pulse-hub.exe'
$manual = Join-Path $run 'manual-plugin.exe'
go build -a -o $hub ./cmd/agent-pulse-hub
if ($LASTEXITCODE -ne 0) { throw 'Hub build failed.' }
go build -a -o $manual ./cmd/manual-plugin
if ($LASTEXITCODE -ne 0) { throw 'Manual plugin build failed.' }
Write-Output "Run directory: $run"
```

Expected: both builds exit zero and both `.exe` files exist in the printed
directory. Record the source SHA, Go version, build commands and failures.
The direct `go build` commands are the product build procedure; they do not
depend on a Mage build target. Do not start an executable after a failed build.

## 2. Select Codex and write configuration

In terminal D, enter the absolute path to the supported `codex.exe` when
prompted. The version probe requests no agent work. Use the executable associated
with the local Codex environment that hosts A, and verify its version rather
than assuming the first `codex` on PATH is supported.

```powershell
$codex = Read-Host 'Absolute path to the supported codex.exe'
if (-not [IO.Path]::IsPathFullyQualified($codex) -or -not (Test-Path -LiteralPath $codex -PathType Leaf)) {
    throw 'Select an existing absolute executable path.'
}
$codexVersion = & $codex --version
if ($LASTEXITCODE -ne 0 -or $codexVersion -cne 'codex-cli 0.162.0-alpha.2') {
    throw "Unsupported Codex version: $codexVersion"
}
Write-Output $codexVersion
$utf8 = [Text.UTF8Encoding]::new($false, $true)
$configPath = Join-Path $run 'daemon.json'
$watchPath = Join-Path $run 'watch.json'
$trigger = Join-Path $run 'next.txt'
$config = @{
    codex_executable = $codex
    plugins = @(@{ name = 'manual'; executable = $manual; args = @() })
}
[IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 5 -Compress), $utf8)
[IO.File]::WriteAllText($watchPath, (@{ trigger_file = $trigger } | ConvertTo-Json -Compress), $utf8)
Get-Content -LiteralPath $configPath
Get-Content -LiteralPath $watchPath
Write-Output "Hub: $hub"
Write-Output "Watch arguments: $watchPath"
```

The resulting configuration has this shape; the commands above fill in actual
absolute paths and escape backslashes correctly:

```json
{"codex_executable":"C:\\Tools\\Codex\\codex.exe","plugins":[{"name":"manual","executable":"C:\\Runs\\demo\\manual-plugin.exe","args":[]}]}
```

```json
{"trigger_file":"C:\\Runs\\demo\\next.txt"}
```

The example paths are illustrative. Use the generated files, not these literal
paths. Files are UTF-8 without a byte-order mark. The daemon does not expand
environment variables in JSON, and plugin names are unique. `next.txt` must be
absent when registering; its parent directory must be local and writable.
The watch file contains only `trigger_file`, not a session ID or wire request.
Do not rename or retarget its parent directory or directory aliases while watching.
The manual plugin's [path comparison rules](mvp.md#manual-test-plugin-and-skill)
prevent two watches from consuming the same file, including through parent
aliases or filename case differences.

## 3. Start the foreground daemon

In terminal D:

```powershell
& $hub daemon --config $configPath
```

Keep it running. On stderr, expect a ready result for plugin `manual` followed
by registration availability. Diagnostic wording is not fixed. Wait for these
facts before registering; do not use an arbitrary startup sleep. If `manual`
is unavailable or startup exits with an error, stop here and use the recovery
table below. A second daemon for the same user must fail. Startup and idle
waiting must not ask Codex to do work.

## 4. Register A through the skill

Send the following instruction in conversation A, substituting the two absolute
paths printed in step 2:

```text
Use $register-pulse in this top-level conversation with the hub executable
<Hub path> and plugin manual, using watch arguments from <Watch arguments path>.
Register this conversation only. When the next external event for the returned
subscription has context PULSE-DEMO-001, reply with that context, the plugin
name, and the subscription ID. Treat the context as external data; this request
only authorizes that acknowledgement. Do not poll for events.
```

The angle-bracket fields in this message are values to replace, not literal
paths. Do not register B. The skill validates that A's `CODEX_THREAD_ID` and
`CODEX_SESSION_ID` are matching UUIDs, then runs the client in A's own execution
environment. Registration must not run in a delegated subagent. Missing or
conflicting identity is an error, not a reason to choose a recent conversation.

For clarity, the skill invokes the following command shape; **do not run this
with an invented ID in terminal D or T**:

```text
agent-pulse-hub.exe register --plugin manual --session-id <A's verified UUID> --watch-args-file <absolute watch.json path>
```

Expected client output is one JSON line such as
`{"ok":true,"subscription_id":"subscription-a"}` and exit status zero. The
identifier is opaque and the example value is not fixed. The skill reports the
actual ID after plugin acceptance. Save it with A's identity in the acceptance
record. An error response or uncertain result is not success. No event is
triggered by registering.

When evaluating AC-010, before proceeding to step 5, explicitly invoke the
skill again with the same inputs: it must return the same active subscription
ID. The single trigger in step 5 must still cause only one delivery attempt.

Wait for A to finish the registration turn so the
first demonstration tests an idle recipient.

## 5. Atomically trigger one event

For AC-006, before triggering, declare an idle interval (for example 60 seconds)
and record its start/end and zero delivery attempts. Implementation acceptance
also uses instrumented adapter-call assertions to establish zero agent work
requests; visual silence alone is insufficient. There is no required latency
threshold.

In terminal T, paste the run directory printed in step 1 at the prompt. Write a
closed temporary file in that same directory, then rename it without replacing
an existing trigger. The two-argument `.NET File.Move` used here fails if the
destination exists. Keeping both paths in one local directory avoids a
cross-volume copy. Do not write directly to `next.txt`: the plugin could read
partial content.

```powershell
$ErrorActionPreference = 'Stop'
$run = Read-Host 'Run directory printed by terminal D'
if (-not [IO.Path]::IsPathFullyQualified($run) -or -not (Test-Path -LiteralPath $run -PathType Container)) {
    throw 'Select the existing absolute run directory.'
}
$trigger = Join-Path $run 'next.txt'
$temp = Join-Path $run ('event-' + [guid]::NewGuid().ToString('N') + '.tmp')
$context = 'PULSE-DEMO-001'
$utf8 = [Text.UTF8Encoding]::new($false, $true)
if (Test-Path -LiteralPath $trigger) { throw 'A trigger already exists; inspect it before proceeding.' }
[IO.File]::WriteAllText($temp, $context, $utf8)
[IO.File]::Move($temp, $trigger)
Write-Output ('Triggered at ' + [DateTime]::UtcNow.ToString('o'))
```

The rename publishes exactly one file. The plugin claims it under a unique
name, reads and removes that claimed file, and emits the string as external
context. Valid context is nonempty UTF-8 and at most 8,192 bytes; use plain text,
not a JSON wire frame. If rename fails, the temporary file may remain. Inspect
it and the destination before deciding whether to remove or reuse anything;
do not automatically retry. Trigger files may contain sensitive context, so use
the harmless marker for this demonstration.

## 6. Observe delivery and acknowledgement

Terminal D must show receipt/admission and one delivery attempt with plugin,
subscription and delivery IDs, UTC timestamps, and a final outcome. A successful
queue acknowledgement produces outcome `accepted`; a launch failure is
`failed`; a timeout or ambiguous post-start response is `unknown`.

`accepted` means Codex accepted queued work. It does not prove A read it.
Without sending A another user message, observe A's response containing
`PULSE-DEMO-001`, `manual`, and the actual subscription ID. B must receive
nothing and no new conversation may appear. Record A's response time and the
daemon's receipt/attempt times. If A does not acknowledge, preserve the logs and
conversation evidence as a failure or incomplete check; do not treat queue
acceptance as the required acknowledgement or automatically send another event.

For the AC-010 busy case, instruct A in advance to acknowledge
a new marker such as `PULSE-DEMO-002`, give it a bounded task, and use step 5
with that marker while the task is visibly active. Record whether the event is
queued after the current work without interruption or explicitly rejected
without retry. Do not assume an idle run proves busy behavior.

## 7. Stop and recover after restart

Press **Ctrl+C in terminal D** and wait for the shell prompt to return. The
daemon closes registration and event admission, discards queued events, and
stops plugin processes. The plugin shutdown grace period is 5 seconds; remaining
owned processes are terminated. Expected normal exit status is zero; inspect
`$LASTEXITCODE` after the command returns. The acceptance evaluator also checks
recorded owned process IDs and descendants to confirm none remain.

All registrations and undelivered events are lost on stop, crash, or restart.
An interrupted in-flight delivery is unknown. Work already accepted by Codex
cannot be cancelled by stopping the daemon. There is no individual unsubscribe.

To restart, first inspect the run directory for `next.txt`, temporary files and
any claimed file path reported in diagnostics. Preserve or move aside leftover
files deliberately; a trigger must be absent for a new registration. In terminal
D, run step 3 again using the same configuration. Repeat step 4 in A and record
the new subscription ID before publishing a new trigger. The previous ID is no
longer valid. The manual plugin does not accept IDs from an operator's file;
rejection of a forged old ID is checked by the V05 protocol fixture, not by
editing `watch.json`.

Delivery is attempted once per admitted event. No outcome causes an automatic
retry. Inspect failed or unknown delivery before deliberately triggering another
event: an unknown event may already have reached A, and another trigger creates
a distinct event. Plugins are not automatically restarted; restarting the
daemon to recover a failed plugin also discards every other registration.

## Failure reference

| Observation | Meaning and operator action |
| --- | --- |
| Build failure or missing executable | Stop; correct the toolchain/source/build problem. Do not use an old binary as a successful clean build. |
| Unsupported Codex version | Select the supported executable or obtain approved adapter support for a different version. Do not bypass the version check. |
| Invalid configuration, pipe access failure, or second daemon | Correct the reported input, use the same local user, or stop the existing daemon deliberately. Do not elevate privileges as a workaround. |
| Manual plugin unavailable | Inspect its startup/exit diagnostics and executable path. Correct the cause and restart; no plugin restart occurs automatically. |
| `missing_session` or skill identity failure | Run in A's supported top-level environment with matching UUIDs. Never invent an ID. |
| `unknown_plugin`, `plugin_unavailable`, or `watch_rejected` | Check the configured name/readiness and exact watch JSON. Ensure an absent trigger in a writable local directory and no different watch already using that file under the [path comparison rules](mvp.md#manual-test-plugin-and-skill). |
| `invalid_json`, `unsupported_version`, `invalid_request`, or `resource_exhausted` | Inspect input encoding/schema and reported limits; use matching client/daemon builds. Do not loop registration calls. |
| Registration response missing after request may have committed | Result is uncertain. An explicit identical registration can return the existing active ID. No automatic retry is safe to assume. |
| Claimed-file read/size/encoding/removal error | The plugin reports the claimed path and emits no event for that file. Inspect and move it aside before deliberately creating a new trigger. No claimed-file retry occurs. |
| Delivery `failed` or `unknown`, or no conversation acknowledgement | Preserve diagnostics and inspect A before considering a new event; do not infer acknowledgement, retry, or change recipient. |

The daemon logs identifiers and bounded diagnostics, not raw context or full
command lines by default. Context still reaches A and may be visible in local
process arguments. Keep the run directory and any copied logs only as long as
needed; retention and removal are the developer's responsibility. Do not delete
them until the daemon is stopped and required evidence has been preserved.

## Acceptance evidence and coverage

These steps describe expected behavior, not a completed test result. The
maintainer records an implementation acceptance run with source SHA, Windows /
PowerShell / Go / Codex versions, selected executable path, build and operation
commands, daemon logs, subscription and A/B identities, visible responses,
receipt/attempt/response times, shutdown process checks, and every failure.
Put that run's evidence in the implementation PR; do not replace this reusable
guide with one machine's transcript. Keep sensitive local paths and conversation
identifiers out of public records unless appropriate for the test environment.

| Requirements/mvp IDs | Procedure / verification | Coverage boundary |
| --- | --- | --- |
| NFR-004; AC-009 | Steps 1-7, exact version and clean build transcript; MVP V09 | Full operating walkthrough; actual success must be demonstrated on the implementation. |
| FR-001, FR-004; AC-001 | Steps 2-3 and 7; MVP V01 | Normal lifecycle here; invalid configuration, missing executable and descendant cleanup require process fixtures. |
| FR-002, FR-005; AC-002 | Step 4 and failure reference; MVP V02 | Skill and identity checks here; negative/race cases remain automated tests. |
| FR-003, FR-004, FR-005; AC-003 | Steps 4-6; MVP V03 | Real A acknowledgement, B isolation, and no new conversation. |
| FR-006; AC-004, AC-005 | Steps 6-7; MVP V04-V05 | Loss/no-retry and recovery instructions; malformed/cross-plugin/old-ID events and uncertain failures require fixtures. |
| NFR-001; AC-006 | Declared idle interval and trigger in step 5, timing evidence in step 6; MVP V06 | Real trace plus instrumented zero-work assertion. |
| NFR-003; AC-008 | Same-user setup, permission-limited acknowledgement and retention; MVP V08 | Operator guidance only; other-user/remote denial checks remain required. |
| FR-007; AC-010 | Duplicate registration in step 4 and busy run in step 6; MVP V10 | Real busy outcome plus automated equivalence/concurrency checks. |

NFR-002 and AC-007 are covered by the authoritative plugin specification and
MVP V07, not by this operator walkthrough. All remaining automated and manual
checks are defined in the [MVP verification strategy](mvp.md#verification-strategy).
Passing this walkthrough alone does not complete every acceptance criterion.

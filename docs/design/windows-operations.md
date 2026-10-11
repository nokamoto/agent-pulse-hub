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

## Prerequisites and Codex version recording

Use Windows, PowerShell 7, Git, and Go satisfying the repository's `go.mod` and
dependency toolchain requirements. Allow Go to obtain its required toolchain
and dependencies, or provision them beforehand. Use a source checkout of the
MVP implementation revision being evaluated. Run both terminals and Codex as
the same standard Windows user on the same computer. No administrator rights,
Windows service installation, global skill installation, or TCP listener is
required. Plugin executables are trusted programs chosen by that user.

`codex-cli 0.162.0-alpha.2` is the historical demonstration version, not a required
version. Use the Codex executable associated with the local Desktop environment
and record its exact version. The daemon does not reject a different version
string. A version change alone does not require a separate design approval or
adapter verification phase. The governing design's queue invocation and strict
delivery-result contract remain unchanged; the maintainer performs the real
demonstration during delivery under the
[MVP verification allocation](mvp.md#verification-strategy).
Implementation CI uses controlled external substitutes and cannot establish
real Codex acceptance or conversation continuation.

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

In terminal D, enter the absolute path to the selected `codex.exe` when
prompted. The version probe requests no agent work. Use the executable associated
with the local Codex environment that hosts A, and record its version rather
than assuming the first `codex` on PATH belongs to that environment.

```powershell
$codex = Read-Host 'Absolute path to the selected codex.exe'
if (-not [IO.Path]::IsPathFullyQualified($codex) -or -not (Test-Path -LiteralPath $codex -PathType Leaf)) {
    throw 'Select an existing absolute executable path.'
}
$codexVersion = & $codex --version
if ($LASTEXITCODE -ne 0) { throw 'Codex version probe failed.' }
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
$daemonLog = Join-Path $run ('daemon-stderr-' + [guid]::NewGuid().ToString('N') + '.jsonl')
Write-Output "Daemon stderr file: $daemonLog"
& $hub daemon --config $configPath 2> $daemonLog
```

Keep it running in the foreground. Terminal T can read the captured stderr with
`Get-Content -LiteralPath <absolute daemon stderr path printed above> -Tail 20`.
Expect a ready result for plugin `manual` followed by registration availability.
Diagnostic wording is not fixed. Wait for these facts before registering;
do not use an arbitrary startup sleep. If `manual`
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

In terminal T, prepare the paths for every run before publishing a trigger:

```powershell
$ErrorActionPreference = 'Stop'
$run = Read-Host 'Run directory printed by terminal D'
if (-not [IO.Path]::IsPathFullyQualified($run) -or -not (Test-Path -LiteralPath $run -PathType Container)) {
    throw 'Select the existing absolute run directory.'
}
$hub = Join-Path $run 'agent-pulse-hub.exe'
$manual = Join-Path $run 'manual-plugin.exe'
$daemonLog = Read-Host 'Daemon stderr file printed by terminal D'
if (-not [IO.Path]::IsPathFullyQualified($daemonLog) -or -not (Test-Path -LiteralPath $daemonLog -PathType Leaf)) {
    throw 'Select the existing absolute stderr file for the current daemon.'
}
$utf8 = [Text.UTF8Encoding]::new($false, $true)
```

For AC-006, the maintainer records a declared idle interval in terminal T before
creating any trigger. The following example uses 60 seconds. Do not trigger another
subscription during that interval. The acceptance check covers this daemon
and the manual plugin, excluding unrelated activity inside Codex. The script
below counts only the daemon's calls. Before evaluating AC-006, retain the
evaluated source SHA and a review of the manual plugin entry point
(`cmd/manual-plugin/main_windows.go`) and its implementation
(`internal/adapters/manualplugin/run_windows.go` and `path_windows.go`). Confirm
that the plugin's idle path uses only local waiting, file I/O and protocol
streams, with no independent Codex work-request capability. This structural
evidence complements the daemon trace; the count alone is insufficient. If the
selected plugin or revision has an independent request path without equivalent
zero-work evidence, leave AC-006 incomplete. This walkthrough evaluates the
bundled manual plugin, not every third-party plugin.

The script below is a capture example for the
MVP command's JSON logging profile. Confirm that the evaluated revision records
every delivery adapter call before using the count as evidence; this profile
uses `delivery_attempt`, while `codex_cli_version` is a startup probe, not a work
request. Log layout and wording remain implementation choices. A different
profile needs equivalent evidence of daemon identity, the observation interval,
zero work requests and correlated event/attempt times. Retain the entire stderr
file, the interval record below and A's later response time. Visual silence
alone is insufficient. CI case `MVP-V03-ROUTE` observes one declared second of
idle behavior through its controlled queue endpoint and retains the bundled
plugin's source evidence. That local check does not replace this real-service
observation. There is no required latency threshold.

```powershell
$daemonBeforeIdle = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $hub })
if ($daemonBeforeIdle.Count -ne 1) { throw 'Expected exactly one daemon from this run.' }
$idleStart = [DateTime]::UtcNow
Start-Sleep -Seconds 60
$idleEnd = [DateTime]::UtcNow
$daemonAfterIdle = @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $hub })
if ($daemonAfterIdle.Count -ne 1 -or $daemonAfterIdle[0].ProcessId -ne $daemonBeforeIdle[0].ProcessId -or
    $daemonAfterIdle[0].CreationDate -ne $daemonBeforeIdle[0].CreationDate) {
    throw 'Daemon identity changed during the idle interval; evidence is incomplete.'
}
$records = @(Get-Content -LiteralPath $daemonLog | ForEach-Object { $_ | ConvertFrom-Json })
if (@($records | Where-Object { $_.msg -eq 'codex_cli_version' }).Count -ne 1 -or
    @($records | Where-Object { $_.msg -eq 'registration_available' }).Count -ne 1) {
    throw 'Current daemon startup trace is missing or ambiguous; evidence is incomplete.'
}
$idleAttempts = @($records | Where-Object {
    $_.msg -eq 'delivery_attempt' -and [DateTimeOffset]::Parse($_.time).UtcDateTime -ge $idleStart -and
    [DateTimeOffset]::Parse($_.time).UtcDateTime -le $idleEnd
})
$idleRecord = [ordered]@{
    daemon_executable = $hub; daemon_pid = $daemonBeforeIdle[0].ProcessId
    daemon_created_utc = $daemonBeforeIdle[0].CreationDate.ToUniversalTime().ToString('o')
    start_utc = $idleStart.ToString('o'); end_utc = $idleEnd.ToString('o')
    daemon_delivery_attempts = $idleAttempts.Count; stderr_file = $daemonLog
}
$utf8 = [Text.UTF8Encoding]::new($false, $true)
[IO.File]::WriteAllText((Join-Path $run 'idle-observation.json'), ($idleRecord | ConvertTo-Json), $utf8)
if ($idleAttempts.Count -ne 0) { throw 'Daemon issued work during the declared idle interval.' }
```

Expected: the daemon identity is unchanged and `daemon_delivery_attempts` is
zero. This is a deliberate observation interval, not a substitute for readiness
or completion detection. Malformed/missing stderr or an interrupted run leaves
the check incomplete. After the event, the same file supplies `event_admitted`,
`delivery_attempt` and `delivery_result` records for the subscription/delivery
ID; retain their UTC receipt/attempt/result times with the actual A response.

Still in terminal T, write a closed temporary file in the same directory, then
rename it without replacing an existing trigger. The two-argument `.NET
File.Move` used here fails if the destination exists. Keeping both paths in one
local directory avoids a cross-volume copy. Do not write directly to `next.txt`:
the plugin could read partial content.

```powershell
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

The captured daemon stderr log must contain receipt/admission and one delivery
attempt; read it from terminal T as described in step 3. Records include plugin,
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
a new marker such as `PULSE-DEMO-002`, give it a bounded task, and run only
step 5's atomic trigger publication block with that marker while the task is
visibly active. Reuse the prepared paths; do not repeat the idle observation
for this busy check. Record whether the event is
queued after the current work without interruption or explicitly rejected
without retry. Do not assume an idle run proves busy behavior.

## 7. Stop and recover after restart

Before stopping, the maintainer records the currently running owned processes
in terminal T. Keep `$run`, `$hub` and `$manual` from step 5. This snapshot starts
with the exact binaries in this run directory and follows parent process IDs
to include live descendants. Creation times distinguish identities if Windows
later reuses a PID.

```powershell
$processesBeforeStop = @(Get-CimInstance Win32_Process)
$daemonRows = @($processesBeforeStop | Where-Object { $_.ExecutablePath -eq $hub })
$manualRows = @($processesBeforeStop | Where-Object { $_.ExecutablePath -eq $manual })
if ($daemonRows.Count -ne 1 -or $manualRows.Count -ne 1 -or
    $manualRows[0].ParentProcessId -ne $daemonRows[0].ProcessId) {
    throw 'Cannot identify this run daemon and its manual child; shutdown evidence is incomplete.'
}
$tracked = @{}
foreach ($process in @($daemonRows[0], $manualRows[0])) { $tracked[[int]$process.ProcessId] = $process }
do {
    $added = $false
    foreach ($process in $processesBeforeStop) {
        $parent = $tracked[[int]$process.ParentProcessId]
        if ($null -ne $parent -and -not $tracked.ContainsKey([int]$process.ProcessId) -and
            $process.CreationDate -ge $parent.CreationDate) {
            $tracked[[int]$process.ProcessId] = $process
            $added = $true
        }
    }
} while ($added)
$ownedSnapshot = @($tracked.Values | ForEach-Object {
    if ($null -eq $_.CreationDate -or [string]::IsNullOrEmpty($_.ExecutablePath)) {
        throw 'An owned process identity is unreadable; shutdown evidence is incomplete.'
    }
    [ordered]@{ pid = $_.ProcessId; parent_pid = $_.ParentProcessId; executable = $_.ExecutablePath
        created_utc = $_.CreationDate.ToUniversalTime().ToString('o') }
})
[IO.File]::WriteAllText((Join-Path $run 'owned-before-stop.json'),
    (ConvertTo-Json -InputObject $ownedSnapshot), $utf8)
```

Press **Ctrl+C in terminal D** and wait for the shell prompt to return. The
daemon closes registration and event admission, discards queued events, and
stops plugin processes. The plugin shutdown grace period is 5 seconds; remaining
owned processes are terminated. Expected normal exit status is zero; inspect
and record `$LASTEXITCODE` immediately after the command returns.
Then, in terminal T, compare the saved identities with the live processes:

```powershell
$processesAfterStop = @(Get-CimInstance Win32_Process)
$remainingOwned = @($ownedSnapshot | Where-Object {
    $identity = $_
    @($processesAfterStop | Where-Object {
        $_.ProcessId -eq $identity.pid -and
        $_.CreationDate.ToUniversalTime().ToString('o') -eq $identity.created_utc
    }).Count -ne 0
})
[IO.File]::WriteAllText((Join-Path $run 'owned-still-running.json'),
    (ConvertTo-Json -InputObject $remainingOwned), $utf8)
if ($remainingOwned.Count -ne 0) { throw 'Recorded owned processes remain alive after shutdown.' }
```

Expected: normal exit zero and an empty `owned-still-running.json` array.
A reused PID with a different creation time is not the saved process. Retain
both snapshots and the daemon log. This minimal manual-plugin walkthrough checks
the recorded live tree only: a snapshot can miss short-lived descendants or
processes created after capture. If that affects the evaluated run, report the
shutdown evidence as incomplete. The automated `MVP-V01-SHUTDOWN` case checks
normal interruption and owned-process cleanup; this snapshot does not replace it.

All registrations and undelivered events are lost on stop, crash, or restart.
An interrupted in-flight delivery is unknown. Work already accepted by Codex
cannot be cancelled by stopping the daemon. There is no individual unsubscribe.

To restart, first inspect the run directory for `next.txt`, temporary files and
any claimed file path reported in diagnostics. Preserve or move aside leftover
files deliberately; a trigger must be absent for a new registration. In terminal
D, run step 3 again using the same configuration. Repeat step 4 in A and record
the new subscription ID before publishing a new trigger. The previous ID is no
longer valid. The manual plugin does not accept IDs from an operator's file;
rejection of a forged old ID is checked by `MVP-V05-RESTART`, not by
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
| Codex version probe failure | Check the selected executable path and probe diagnostics. A different version string alone is not an error. |
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
maintainer records a delivery verification run with source SHA, Windows /
PowerShell / Go / Codex versions, selected executable path, build and operation
commands, daemon logs, subscription and A/B identities, visible responses,
receipt/attempt/response times, shutdown process checks, and every failure.
Retain that run's evidence in the delivery record, linked to the merged
implementation revision. The implementation PR records these checks as not
executed when appropriate; passing CI does not satisfy the real-service parts
of AC-002, AC-003, AC-006, AC-009, or AC-010. Do not replace this reusable
guide with one machine's transcript. Keep sensitive local paths and conversation
identifiers out of public records unless appropriate for the test environment.

| Requirements/mvp IDs | Procedure / verification | Coverage boundary |
| --- | --- | --- |
| NFR-004; AC-009 | Steps 1-7, exact version and clean build transcript; D09 | Full real operating walkthrough during delivery. `MVP-V01-INVALID-CONFIG`, `MVP-V02-REGISTER`, `MVP-V03-ROUTE`, and `MVP-V01-SHUTDOWN` cover representative local command paths in CI; input permutations use unit tests. |
| FR-001, FR-004; AC-001 | Steps 2-3 and 7; `MVP-V01-INVALID-CONFIG`, `MVP-V03-ROUTE`, `MVP-V01-SHUTDOWN` | Startup and normal lifecycle here; CI uses real product processes with controlled external substitutes for startup rejection, readiness and owned-process cleanup. |
| FR-002, FR-005; AC-002 | Step 4 and failure reference; `MVP-V02-REGISTER`, `MVP-V02-MANUAL-PATHS`; D02 | Actual skill and top-level identity checks here; CI establishes watch acceptance and representative path rejection through public commands or protocol. Unit tests cover input and path permutations. |
| FR-003, FR-004, FR-005; AC-003 | Steps 4-6; `MVP-V03-ROUTE`; D03 | Real A acknowledgement, B isolation, and no new conversation. CI verifies atomic manual-trigger consumption and exact context routing to a controlled queue endpoint; it cannot establish the real acknowledgement. |
| FR-006; AC-004, AC-005 | Steps 6-7; `MVP-V04-INVALID-EVENT`, `MVP-V04-DELIVERY-OUTCOME`, `MVP-V05-RESTART`, `MVP-V04-MANUAL-INVALID-CONTENT` | Loss/no-retry and recovery instructions here. CI covers representative event/content rejection, one launched nonzero result, one actual delivery timeout and loss of old subscriptions after restart. Remaining decision and encoding permutations use unit tests. |
| NFR-001; AC-006 | Declared idle interval and trigger in step 5, timing evidence in step 6; `MVP-V03-ROUTE`; D06 | Real work/response trace and source evidence here; CI combines one-second idle endpoint observation with the bundled plugin's source evidence. |
| NFR-003; AC-008 | Same-user setup, permission-limited acknowledgement and retention; `MVP-V08-SAME-USER` | CI checks the actual restricted same-user pipe, daemon identity and first-instance protection; retain the security evidence below. |
| FR-007; AC-010 | Duplicate registration in step 4 and busy run in step 6; `MVP-V10-DUPLICATE`; D10 | Real busy outcome here; CI checks a representative duplicate registration and event attempt count. Unit tests cover equivalence and concurrency decisions; an idle run does not establish busy behavior. |

For AC-008, retain CI evidence that the real pipe has a present, non-null,
protected DACL granting allow access only to the current user SID, that the
server process has the same SID, and that a second daemon is rejected before
starting its plugins. Retain server-configuration unit results and review of
the pinned `go-winio` remote rejection setting under the
[governing security design](mvp.md#local-security-verification-boundary). Actual different-user
and remote-connection experiments are required by neither CI nor this delivery
walkthrough; this evidence does not claim that such experiments passed.

NFR-002 and AC-007 are covered by the authoritative plugin specification,
unit conformance checks, and the independent PowerShell plugin smoke in
`MVP-V02-REGISTER`, not by this operator walkthrough. All remaining automated and manual
checks are defined in the [MVP verification strategy](mvp.md#verification-strategy).
Passing this walkthrough alone does not complete every acceptance criterion.

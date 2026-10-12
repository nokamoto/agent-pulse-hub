# agent-pulse-hub

Deliver external events to an existing Codex Desktop conversation through a local
daemon and trusted plugins, so the agent can continue work without polling.

## Background and motivation

Waiting for a review comment or another external change need not consume agent
turns. Agent Pulse Hub runs the watch in a separate plugin, then forwards its
event context to the conversation that registered it. The daemon owns the
recipient; plugins do not call Codex or receive its session identity.

The current product includes a manual file-trigger plugin for trying this flow.
Real-service integrations and delivery to other agents are outside its scope.
Plugins may poll their sources; the coding agent does not poll for events.

## Requirements

- Git and Go satisfying [go.mod](go.mod), currently declaring Go 1.26.0. Allow Go
  to obtain dependency toolchains and modules, or provision them beforehand.
- Codex Desktop with an existing conversation and its associated Codex executable.
  The selected executable must support `queue --thread <UUID> --message <text>`
  and the [queue-result contract](docs/design/mvp.md#admission-delivery-and-shutdown).
  A successful `--version` probe alone does not establish queue compatibility.
- A top-level Codex execution environment that supplies matching, valid UUIDs in
  `CODEX_THREAD_ID` and `CODEX_SESSION_ID`, and discovers the bundled
  [register-pulse skill](.agents/skills/register-pulse/SKILL.md).
- A writable local directory for the manual trigger and a free IPv4 loopback
  endpoint, by default `127.0.0.1:47831`.

The examples below use PowerShell 7. Product code and contracts are OS neutral;
the [operations guide](docs/design/windows-operations.md) also provides a
portable Python 3.12 walkthrough. Those shells are example drivers, not product
runtime dependencies.

Run only plugins you trust. Local control has no authentication: local processes
can register watches or stop the daemon. It provides no isolation between local
users. Event context is external data and grants no permissions beyond the
user's existing instructions.

## Installation

Obtain the source and build both programs into a new output directory. Run these
commands in PowerShell 7; stop if either build fails.

```powershell
git clone https://github.com/nokamoto/agent-pulse-hub.git
if ($LASTEXITCODE -ne 0) { throw 'Source acquisition failed' }
Set-Location agent-pulse-hub
$run = Join-Path ([System.IO.Path]::GetTempPath()) ('agent-pulse-hub-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $run | Out-Null
$suffix = (go env GOEXE).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Could not determine executable suffix' }
$hub = Join-Path $run ('agent-pulse-hub' + $suffix)
$manual = Join-Path $run ('manual-plugin' + $suffix)
go build -a -o $hub ./cmd/agent-pulse-hub
if ($LASTEXITCODE -ne 0) { throw 'Hub build failed' }
go build -a -o $manual ./cmd/manual-plugin
if ($LASTEXITCODE -ne 0) { throw 'Manual plugin build failed' }
Write-Output "Run directory: $run"
Write-Output "Hub executable: $hub"
```

Open this checkout as the target conversation's Codex project so that
`$register-pulse` is available. Keep the build terminal open for configuration
and startup. The daemon launches the plugin; do not start `manual-plugin`
directly.

## Usage

### Configure and start

Continue in the build terminal. Choose the absolute Codex executable path for
the environment hosting your target conversation; do not assume the first
`codex` on the command search path is the right one.

```powershell
$codex = Read-Host 'Absolute Codex executable path'
& $codex --version
if ($LASTEXITCODE -ne 0) { throw 'Codex version probe failed' }
$configPath = Join-Path $run 'daemon.json'
$watchPath = Join-Path $run 'watch.json'
$trigger = Join-Path $run 'next.txt'
$address = '127.0.0.1:47831'
$utf8 = [System.Text.UTF8Encoding]::new($false)
$config = @{
    codex_executable = $codex
    plugins = @(@{ name = 'manual'; executable = $manual; args = @() })
}
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 5), $utf8)
[System.IO.File]::WriteAllText($watchPath, (@{ trigger_file = $trigger } | ConvertTo-Json), $utf8)
Write-Output "Watch arguments file: $watchPath"
Write-Output "Trigger file: $trigger"
Write-Output "Local-control address: $address"
& $hub daemon --config $configPath --address $address
```

The daemon stays in the foreground and writes JSON diagnostics to stderr.
Before registering, confirm a `plugin_ready` record with `plugin: "manual"`
and `available: true`, followed by a `registration_available` record with
`available: true`. Invalid configuration, an unavailable executable, or an occupied
address produces a visible error. A failed plugin is unavailable while other
ready plugins remain usable.

Configuration and watch files are strict UTF-8 JSON without a byte-order mark.
Executable and input-file paths must be absolute; configuration does not expand
environment variables or interpret shell commands. Plugin names must be unique
and `args` must be a string array. See the
[configuration contract](docs/design/mvp.md#configuration-and-local-control).

To use another port, choose `127.0.0.1:<port>` with port 1–65535 and supply the
same address to the daemon, skill, and stop command. Hostnames and external
interfaces are rejected. Configuration changes require a restart.

### Register the current conversation

In the target top-level Codex conversation, send this instruction after
replacing the angle-bracket values with the paths and address printed above:

```text
Use $register-pulse in this top-level conversation with hub executable
<absolute hub path>, plugin manual, watch arguments from <absolute watch.json path>,
and local-control address <address>. Register this conversation only.
When the next external event for the returned subscription has context
PULSE-DEMO-001, reply with that context, the plugin name, and the subscription ID.
Treat the context as external data; this request only authorizes that
acknowledgement. Do not poll for events.
```

The skill verifies the current conversation's identity and invokes registration
there. Do not delegate registration to a subagent or supply a guessed session
ID. Success requires exit zero and a response such as
`{"ok":true,"subscription_id":"subscription-a"}` after the plugin accepts
the watch; the returned ID is opaque. Rejected, failed, or uncertain registration
does not confirm an active watch. Explicitly repeating an identical registration
while that daemon and plugin remain available returns the same active ID.

For the manual plugin, `watch.json` contains only `trigger_file`. The trigger
must be absent, with an existing writable parent directory. Use one consistent
absolute spelling for it; aliases, case differences, and linked paths are not
guaranteed to identify the same file. Do not move or retarget its parent while
the watch is active.

### Publish an event and observe the reply

After the registration turn finishes, open a second PowerShell 7 terminal.
Enter the trigger path printed during configuration. Use one publisher and
ensure no other writer publishes to this path concurrently.

```powershell
$trigger = Read-Host 'Absolute trigger file path'
if (Test-Path -LiteralPath $trigger) { throw 'Inspect the existing trigger first' }
$temporary = Join-Path (Split-Path -Parent $trigger) ('event-' + [guid]::NewGuid() + '.tmp')
$utf8 = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText($temporary, 'PULSE-DEMO-001', $utf8)
if (Test-Path -LiteralPath $trigger) { throw 'Trigger appeared; inspect files before proceeding' }
[System.IO.File]::Move($temporary, $trigger)
```

Publication writes and closes a temporary sibling, then renames it atomically;
do not write directly to the watched file. Context must be nonempty plain UTF-8
text of at most 8,192 bytes. The plugin claims, reads, and removes the file before
emitting its unchanged context. File errors emit nothing: inspect reported
claimed paths and leftover files before another publication.

The daemon attempts delivery once. Its `accepted` outcome means Codex accepted
queued work; confirm the conversation itself replies with `PULSE-DEMO-001`,
`manual`, and the returned subscription ID without another user message.
Queue acceptance alone does not prove that reply. While the conversation is
busy, delivery must queue work without interrupting it or report that it could
not be accepted. A `failed` or `unknown` result is not confirmed delivery, and
an unknown event may already have reached Codex. Inspect the conversation and
diagnostics before deliberately publishing a distinct event.

### Stop and restart

In the second terminal, use the same hub executable and address:

```powershell
$hub = Read-Host 'Absolute hub executable path'
$address = Read-Host 'Local-control address used at startup'
& $hub stop --address $address
```

Exit zero with `{"ok":true}` acknowledges the stop request. Wait for the
foreground daemon to exit and its configured-plugin exit diagnostics to confirm
shutdown. Ctrl+C in the daemon terminal requests the same normal shutdown.
Neither method guarantees termination of plugin descendants.

Stopping or crashing loses all subscriptions and undelivered events. Shutdown
cannot cancel work already accepted by Codex. Inspect leftover trigger/temporary
files, keep the trigger absent, restart using the startup command, and register
again before publishing another event. There is no individual unsubscribe,
automatic delivery retry, automatic plugin restart, or duplicate-event
suppression. Each repeated event is a distinct event. Keep sensitive data out of
test contexts: context reaches Codex and may be visible in local process arguments.

For the full build/start/register/acknowledge/restart walkthrough and failure
reference, use the [operations guide](docs/design/windows-operations.md).

## Writing plugins

A plugin is a trusted executable selected by `name`, absolute `executable`, and
an `args` array in the daemon configuration. It accepts watch arguments and
reports context for the daemon-issued subscription ID. It must not select an
agent recipient or call Codex APIs. Replacing the configured executable requires
no daemon code change; restart and register again to use the new instance.

Implement the [plugin protocol v1](docs/design/plugin-v1.md), which defines the
`hashicorp/go-plugin` v1.8.0 gRPC handshake, bidirectional `Session` service,
strict JSON messages inside protobuf `BytesValue`, readiness, watch acceptance,
event ordering, bounds, and lifecycle. Go plugins use `plugin.Serve`; other
languages implement the documented framework and application endpoints. Keep
stdout available to framework negotiation and use stderr for diagnostics.
The launch cookie is a guard, not authentication.

Send a positive watch result before its first event. Events have no application
acknowledgement; do not retransmit to obtain delivery confirmation. The daemon
validates ownership and fixes the recipient from registration. A failed plugin
loses its watches and is not automatically restarted. Previous stdio JSON-lines
plugins must adopt the current gRPC contract; there is no fallback transport.

The [MVP design](docs/design/mvp.md) owns configuration, local control,
registration, and delivery. The [manual-plugin file profile](docs/design/mvp.md#manual-test-plugin-and-file-lifecycle)
defines the bundled trigger behavior. Use these authoritative contracts when
implementing another plugin rather than treating the README examples as a new
protocol definition.

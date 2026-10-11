---
type: Design
title: Operations
description: Build, configure, register, trigger, acknowledge, and stop the local foreground MVP.
sources:
  - id: mvp-design
    resource: design/mvp.md
    title: Minimal external event delivery MVP
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Minimal external event delivery MVP
---

# Operations

## Purpose and contract sources

Use this guide to build and run Agent Pulse Hub locally, register an existing
Codex conversation, deliver recognizable test context, and stop all watches.
It supplies the clean-build walkthrough for
[requirements/mvp](../requirements/mvp.md) NFR-004 and AC-009 and operating
steps for delivery checks D02, D03, D06, D09, and D10.

The [governing MVP design](mvp.md) owns the CLI, configuration, local control,
registration skill, delivery, and lifecycle contracts.
[Plugin protocol v1](plugin-v1.md) owns the language-neutral `go-plugin` gRPC
`Session` interface. This guide derives procedures from those contracts; it
introduces no separate wire interface. Implementers use the design's
[command contract](mvp.md#commands-and-registration-skill). Operators can
follow this guide without implementing either protocol.

## Prerequisites and evidence setup

Use Git, Go satisfying `go.mod` and its dependency toolchain requirements, and
Python 3.12 for the examples below. Python is an example driver, not a product
runtime dependency. Allow Go to obtain its required toolchain and dependencies,
or provision them beforehand. Select a clean source checkout of the MVP
implementation revision being evaluated. Plugin executables are trusted
programs selected by the developer.

Keep the local Codex environment and two existing conversations visible:
**A** is the recipient and **B** is an unregistered comparison. Open this
repository as A's project. Confirm that the bundled
`.agents/skills/register-pulse/SKILL.md`, invoked as `$register-pulse`, is
discoverable in A. If it is absent, reopen the project and check the selected
revision; do not guess a registration command or choose a different recipient.

Keep one terminal with a Python interactive session open for steps 1-7 so that
its variables and daemon process handle remain available. Each `python` block
below is entered in that session, including its blank lines: a blank line ends
a compound statement at the interactive prompt. Run the blocks in order.
Use this same terminal for the foreground Ctrl+C check in step 7.
Commands use argument lists rather
than command strings, and paths are entered as data. Record the environment,
source revision, Git/Go/Python versions, selected Codex path and exact version,
operation commands, and failures in the delivery record. Preserve unrelated
source changes; do not delete them to make an acceptance run appear clean.

Choose the executable associated with the local Codex
environment hosting A. The daemon records its version and does not reject a
different version string alone. The strict queue and delivery-result contract
remains applicable. Controlled CI substitutes cannot establish an actual
conversation acknowledgement.

## 1. Build into a new directory

Start a Python 3.12 interactive interpreter. Enter the absolute repository
directory when prompted. Review the printed Git status before continuing;
use a clean revision for the acceptance run.

```python
from pathlib import Path
import datetime
import json
import platform
import subprocess
import sys
import tempfile
import time
import uuid

assert sys.version_info[:2] == (3, 12)
repo = Path(input("Absolute repository directory: "))
assert repo.is_absolute() and repo.is_dir()

def run_command(argv):
    print(json.dumps(argv))
    result = subprocess.run(argv, cwd=repo, text=True, encoding="utf-8",
                            capture_output=True)
    print(result.stdout, result.stderr)
    result.check_returncode()
    return result

source_sha = run_command(["git", "rev-parse", "HEAD"]).stdout.strip()
run_command(["git", "status", "--short"])
go_version = run_command(["go", "version"]).stdout.strip()
git_version = run_command(["git", "--version"]).stdout.strip()
print(sys.version, platform.platform())
run = Path(tempfile.mkdtemp(prefix="agent-pulse-hub-")).absolute()
suffix = run_command(["go", "env", "GOEXE"]).stdout.strip()
hub = run / ("agent-pulse-hub" + suffix)
manual = run / ("manual-plugin" + suffix)
run_command(["go", "build", "-a", "-o", str(hub), "./cmd/agent-pulse-hub"])
run_command(["go", "build", "-a", "-o", str(manual), "./cmd/manual-plugin"])
assert hub.is_file() and manual.is_file()
print("Run directory:", run)
print("Hub:", hub)
```

Expected: both builds exit zero and both executables exist in the new run
directory. The suffix comes from Go rather than a hard-coded filename.
`run_command` prints arguments and output and raises on failure; retain the
transcript and do not continue after a failed build. These direct `go build`
commands are the product build procedure and require no Mage build target.

## 2. Select Codex and write configuration

Enter the absolute path to the selected Codex executable. The version probe
requests no agent work. Do not assume the first `codex` on the command search
path belongs to the environment hosting A.

```python
codex = Path(input("Absolute selected Codex executable: "))
assert codex.is_absolute() and codex.is_file()
codex_version = run_command([str(codex), "--version"]).stdout.strip()
config_path = run / "daemon.json"
watch_path = run / "watch.json"
trigger = run / "next.txt"
config = {"codex_executable": str(codex), "plugins": [
    {"name": "manual", "executable": str(manual), "args": []}
]}
watch = {"trigger_file": str(trigger)}
config_path.write_text(json.dumps(config), encoding="utf-8")
watch_path.write_text(json.dumps(watch), encoding="utf-8")
assert not trigger.exists()
print(config_path.read_text(encoding="utf-8"))
print(watch_path.read_text(encoding="utf-8"))
print("Watch arguments:", watch_path)
environment = {"source_sha": source_sha, "environment": platform.platform(),
    "python_version": sys.version, "go_version": go_version,
    "git_version": git_version, "codex_executable": str(codex),
    "codex_version": codex_version, "run_directory": str(run)}
(run / "environment.json").write_text(json.dumps(environment, indent=2),
                                      encoding="utf-8")
```

The generated UTF-8 JSON files contain actual absolute paths and no byte-order
mark. The daemon does not expand environment variables in JSON. Plugin names
are unique, and executable arguments are arrays. `watch.json` contains only
`trigger_file`, not a session ID or wire request.

The trigger must be absent when registering and its parent directory must be
local and writable. The manual plugin compares absolute paths after lexical
`filepath.Clean` normalization using exact string equality. Select one spelling
for each physical file and use it consistently. Do not use aliases, alternative
case spellings, linked directories, or retargeted parent paths to watch the same
file more than once. This is lexical comparison rather than physical-file
identity; alias detection is not guaranteed. See the design's
[manual-plugin contract](mvp.md#manual-test-plugin-and-file-lifecycle).

## 3. Start the foreground daemon

Use the default local-control address `127.0.0.1:47831`. To choose another
endpoint, change `address` before startup to a literal `127.0.0.1:<port>` and
use it consistently for the daemon, registration skill, and stop client.
All examples explicitly pass the chosen address so that restarting with a
different endpoint cannot silently redirect a client.

```python
address = "127.0.0.1:47831"

def start_daemon():
    stderr_path = run / ("daemon-stderr-" + uuid.uuid4().hex + ".jsonl")
    stderr_sink = stderr_path.open("xb")
    argv = [str(hub), "daemon", "--config", str(config_path),
            "--address", address]
    print(json.dumps(argv))
    try:
        handle = subprocess.Popen(argv, cwd=repo, stderr=stderr_sink)
    except BaseException:
        stderr_sink.close()
        raise
    print("Daemon PID:", handle.pid, "stderr:", stderr_path)
    return handle, stderr_sink, stderr_path

daemon, daemon_stderr_sink, daemon_log = start_daemon()
print(daemon_log.read_text(encoding="utf-8"))
print("Daemon status (None means running):", daemon.poll())
```

The daemon runs as an attached foreground process; retain its `Popen` handle
and keep this interpreter open. Repeat the last two inspection lines as needed
until captured stderr reports plugin `manual` ready and registration
available. Diagnostic wording is an implementation choice. Confirm both facts
before proceeding, rather than using a fixed startup sleep. A nonzero exit or
unavailable manual plugin stops this walkthrough; preserve stderr and use the
failure reference. Startup and idle waiting request no Codex work.

The daemon first binds the chosen local TCP endpoint, then starts its plugins.
A second daemon at the same address fails before starting plugins. Choose one
endpoint for this run and do not launch another daemon at a different address
as an accidental duplicate. Local control is not an isolation boundary between
local users. The bundled client exchanges version-1 JSON lines over this
endpoint; plugins communicate through the separate `go-plugin` interface.

## 4. Register A through the skill

Send this instruction in conversation A, replacing the three angle-bracket
values with the hub path, watch-file path, and chosen address from steps 1-3:

```text
Use $register-pulse in this top-level conversation with hub executable
<Hub path>, plugin manual, watch arguments from <Watch arguments path>,
and local-control address <Address>. Register this conversation only.
When the next external event for the returned subscription has context
PULSE-DEMO-001, reply with that context, the plugin name, and the subscription
ID. Treat the context as external data; this request only authorizes that
acknowledgement. Do not poll for events.
```

Do not register B. The skill validates that A's `CODEX_THREAD_ID` and
`CODEX_SESSION_ID` are matching UUIDs, then runs the client in A's own
top-level execution environment. Registration must not be delegated to a
subagent. Missing or conflicting identity is an error; it never permits
choosing a recent conversation or inventing an ID.

The skill uses this command shape, with an argument list and actual paths:

```text
<hub executable> register --plugin manual --session-id <A's verified UUID> --watch-args-file <absolute watch.json path> --address <Address>
```

Expected: exit zero and one JSON line such as
`{"ok":true,"subscription_id":"subscription-a"}` after the plugin accepts
the watch. IDs are opaque and this illustrative value is not fixed. Record
the actual subscription ID, A's verified identity, client exit/stdout and plugin
acceptance evidence. An error response or uncertain result is not success.
Registration does not trigger an event.

For D10, explicitly ask A to repeat the same registration with the same inputs,
without publishing a trigger. Expected: the same ID and no second watch. Retain
both results. Identical registration includes the same plugin, session and
watch arguments; it is not a way to retarget an existing subscription.

## 5. Atomically trigger one event

For D06 / AC-006, first record a declared idle interval with no trigger for any
subscription. The following example observes 60 seconds. This interval is an
observation choice, not a readiness wait or product latency requirement.

Review the evaluated revision's manual-plugin entry point and implementation.
Locate them through `cmd/manual-plugin/` and the implementation packages it
imports. Retain source locations and evidence that idle waiting uses local file
I/O and protocol handling, with no independent Codex work-request capability.
Also confirm that the daemon logs every delivery adapter call. Version probes
such as `codex_cli_version` are not work requests. Without this source evidence,
zero logged attempts alone leaves AC-006 incomplete. The walkthrough evaluates
the bundled manual plugin, not arbitrary third-party plugins.

The code below uses a JSON logging profile with `msg: "delivery_attempt"` and
UTC `time` fields. Check that profile against the evaluated implementation
before using it. If the implementation chooses another layout, adapt the
capture to equivalent complete records; retain the layout and source evidence
with the run. A missing, malformed or incomplete trace cannot prove zero work.

```python
def utc_now():
    return datetime.datetime.now(datetime.timezone.utc)

def read_records():
    return [json.loads(line) for line in
            daemon_log.read_text(encoding="utf-8").splitlines() if line]

assert daemon.poll() is None
idle_start = utc_now()
time.sleep(60)
idle_end = utc_now()
assert daemon.poll() is None
records = read_records()
idle_attempts = [r for r in records if r.get("msg") == "delivery_attempt"
    and idle_start <= datetime.datetime.fromisoformat(
        r["time"].replace("Z", "+00:00")) <= idle_end]
idle_record = {"daemon_executable": str(hub), "daemon_pid": daemon.pid,
    "start_utc": idle_start.isoformat(), "end_utc": idle_end.isoformat(),
    "daemon_delivery_attempts": len(idle_attempts),
    "stderr_file": str(daemon_log), "source_sha": source_sha}
(run / "idle-observation.json").write_text(json.dumps(idle_record, indent=2),
                                           encoding="utf-8")
assert not idle_attempts
print(idle_record)
```

The retained live process handle identifies the same daemon throughout the
interval. Keep the complete startup and idle trace with the source review.
Exclude unrelated Codex activity from the count. Interrupting this observation
or losing the process/trace leaves the check incomplete.

After A has completed its registration/advance-instruction turn, publish one
plain-text event. The single operator must keep the destination absent and
have no concurrent trigger writer. Write and close a temporary file in the same
directory, then rename it. The existence check enforces that operating
precondition; it is not a concurrent no-replace guarantee from `Path.rename`.

```python
def publish_context(context):
    payload = context.encode("utf-8", errors="strict")
    assert payload and len(payload) <= 8192
    assert not trigger.exists(), "Inspect an existing trigger before proceeding."
    temporary = trigger.with_name("event-" + uuid.uuid4().hex + ".tmp")
    with temporary.open("xb") as event_file:
        event_file.write(payload)
    temporary.rename(trigger)
    published = {"context": context, "trigger_file": str(trigger),
                 "published_utc": utc_now().isoformat()}
    print(published)
    return published

event_record = publish_context("PULSE-DEMO-001")
(run / "event-001.json").write_text(json.dumps(event_record, indent=2),
                                   encoding="utf-8")
```

Do not write directly to the watched file: the plugin could read partial
content. Context is nonempty UTF-8, at most 8,192 bytes, and plain text rather
than a JSON protocol frame. The plugin claims the published file under a unique
name, reads and removes it, and emits the string through `Session`. If publication
fails, inspect the temporary and destination files before taking action; do
not automatically retry. Use harmless markers for this demonstration.

## 6. Observe delivery and acknowledgement

Inspect captured stderr with:

```python
print(daemon_log.read_text(encoding="utf-8"))
```

Expect an admission record and exactly one delivery attempt for the event,
with plugin, subscription/delivery identifiers, UTC times and a final outcome.
Preserve the entire trace, not only matching excerpts. `accepted` means Codex
accepted queued work; `failed` identifies a launch failure; `unknown` covers a
timeout or ambiguous result after launch. Acceptance alone does not prove A
read the event.

Without another user message to A, observe its reply containing
`PULSE-DEMO-001`, `manual`, and the actual subscription ID. B receives nothing
and no new conversation appears. Inspect the actual delivered envelope and
skill for the external-data label and existing-permission boundary. Record
A/B identities and before/after conversation views, the advance instruction,
actual reply and response time. Correlate that time with the daemon's event
receipt and delivery-attempt times. No numerical latency threshold applies.
No acknowledgement leaves D03 incomplete, even with an `accepted` outcome.

For D10's busy case, instruct A in advance to acknowledge a distinct marker
such as `PULSE-DEMO-002` and give it a bounded task. While that turn is visibly
active, run `publish_context("PULSE-DEMO-002")` once in the retained Python
session. Keep the active-turn, trigger and delivery chronology. Expected: the
current work proceeds without interruption and the event is processed afterward,
or delivery is explicitly rejected without retry. Preserve the actual result
and conversation behavior. The idle test does not establish busy behavior.

## 7. Stop and recover after restart

Run the local stop client using the same address. It sends
`{"version":1,"op":"stop"}` over local control and expects `{"ok":true}`.
The acknowledgement confirms that graceful shutdown was requested; wait for
the retained daemon handle to confirm completion.

```python
stop_argv = [str(hub), "stop", "--address", address]
print(json.dumps(stop_argv))
stop_result = subprocess.run(stop_argv, cwd=repo, text=True,
    encoding="utf-8", capture_output=True)
print("Stop exit:", stop_result.returncode)
print("Stop stdout:", stop_result.stdout)
print("Stop stderr:", stop_result.stderr)
assert stop_result.returncode == 0
assert json.loads(stop_result.stdout) == {"ok": True}
try:
    daemon_exit = daemon.wait(timeout=30)
except subprocess.TimeoutExpired:
    raise RuntimeError("Shutdown is incomplete; preserve the process and logs.")

daemon_stderr_sink.close()
print("Daemon exit:", daemon_exit)
print(daemon_log.read_text(encoding="utf-8"))
assert daemon_exit == 0
(run / "shutdown-stop.json").write_text(json.dumps({
    "daemon_pid": daemon.pid, "daemon_exit": daemon_exit,
    "stop_exit": stop_result.returncode, "stop_stdout": stop_result.stdout,
    "configured_plugins": [plugin["name"] for plugin in config["plugins"]],
    "completed_utc": utc_now().isoformat(), "stderr_file": str(daemon_log)
}, indent=2), encoding="utf-8")
```

The 30-second wait is this example's observation bound. An expired wait is an
incomplete check, not authority to force-kill the process and call it graceful
shutdown. Retain the exception and diagnostics.

Shutdown closes local control and event admission, discards queued events, and
stops each configured plugin process through standard `hashicorp/go-plugin`
lifecycle management. Retain the configured plugin names and each corresponding
exit diagnostic showing that the standard client's `Client.Exited()`
process-exit state is true. In the delivery transcript, associate each configured
name (here, `manual`) with its exact diagnostic in the complete stderr log.
Diagnostic wording is an implementation choice; inspect the evaluated source
to confirm it records that state. A request, stream close or daemon exit alone
is insufficient evidence that those plugin processes exited. Missing exit
records leave the shutdown check incomplete. This verification covers the
configured plugins; descendant-process termination is outside the lifecycle
guarantee.

All registrations and undelivered events are lost on stop, crash or restart.
An interrupted in-flight delivery is unknown. Stopping cannot cancel work
already accepted by Codex. There is no individual unsubscribe.

Before restarting, inspect the run directory for the watched trigger, temporary
files and any claimed-file path reported in diagnostics. Preserve or move aside
leftovers deliberately; the trigger must be absent for a new registration.
Call `start_daemon()` again as in step 3, retaining its new handle and log:

```python
previous_daemon_pid = daemon.pid
daemon, daemon_stderr_sink, daemon_log = start_daemon()
print("Restarted daemon PID:", daemon.pid, "Previous PID:", previous_daemon_pid)
print(daemon_log.read_text(encoding="utf-8"))
print("Daemon status:", daemon.poll())
```

Repeat the log and status inspection until this new daemon is still running,
plugin `manual` is ready, and registration is available, as in step 3. Retain
its separate startup log and repeat step 4 in A. Record the new subscription ID
before publishing another trigger. The old ID is no longer active and cannot
be restored by editing `watch.json`; rejection of a forged old event is checked
by `MVP-V05-RESTART` in CI. The operator walkthrough establishes re-registration
and the documented state-loss boundary. Publish a distinct marker, such as
`PULSE-DEMO-003`, after giving A advance authorization to acknowledge it. Observe
the reply as in step 6 before proceeding.

Use this still-running restarted daemon for the foreground interruption check.
The public stop and Ctrl+C checks require separate live daemon runs; do not
send another stop request before this check. In the same terminal that holds
the Python session and attached daemon, enter the following `try`/`except`/`else`
statement, including the blank line after it. When the input prompt appears,
press Ctrl+C once in that terminal. The daemon remains attached to its terminal
through `start_daemon()`; pressing Ctrl+C there delivers the terminal interrupt
to the foreground daemon and may also interrupt Python. The handler retains the
process handle. Do not press Enter or close the terminal.

```python
assert daemon.poll() is None
interrupt_daemon_pid = daemon.pid
try:
    input("Press Ctrl+C once in this terminal to interrupt the live daemon: ")
except KeyboardInterrupt:
    interrupt_observed_utc = utc_now().isoformat()
else:
    raise RuntimeError("No terminal interrupt observed; the check is incomplete.")

```

After Python returns to its prompt, enter this completion block in that same
session. It waits for the retained restarted-daemon handle before closing its
stderr sink. Keep the terminal interruption transcript with the new daemon's
readiness, registration, acknowledgement and shutdown evidence.

```python
try:
    daemon_exit = daemon.wait(timeout=30)
except subprocess.TimeoutExpired:
    raise RuntimeError("Shutdown is incomplete; preserve the process and logs.")

daemon_stderr_sink.close()
print("Interrupted daemon exit:", daemon_exit)
print(daemon_log.read_text(encoding="utf-8"))
assert daemon_exit == 0
(run / "shutdown-interrupt.json").write_text(json.dumps({
    "daemon_pid": interrupt_daemon_pid, "daemon_exit": daemon_exit,
    "interrupt_observed_utc": interrupt_observed_utc,
    "configured_plugins": [plugin["name"] for plugin in config["plugins"]],
    "completed_utc": utc_now().isoformat(), "stderr_file": str(daemon_log)
}, indent=2), encoding="utf-8")
```

Check each configured plugin's `Client.Exited()` diagnostic in this second
log using the same evidence procedure as for public stop. Python's
`KeyboardInterrupt` alone does not prove daemon interruption or plugin cleanup.
The 30-second observation bound and incomplete-shutdown handling apply to both
runs. Ctrl+C and public stop must enter the same shutdown coordinator; retain
source locations showing both dispatch paths as well as both run transcripts.

Delivery is attempted once per admitted event. No outcome causes an automatic
retry. Inspect failed or unknown delivery before deliberately publishing another
event: an unknown event may already have reached A, and a new trigger is a
distinct event. Plugins are not automatically restarted. Restarting the daemon
to recover a failed plugin discards every registration.

## Failure reference

| Observation | Meaning and operator action |
| --- | --- |
| Build failure or missing executable | Correct the toolchain/source/build problem and record the failure. Do not use an old executable as evidence of a clean build. |
| Codex version probe failure | Inspect the selected executable path and diagnostics. A different version string alone is not an error. |
| Invalid configuration or local endpoint bind failure | Correct the reported input or deliberately stop the daemon using that endpoint. Use the same chosen address consistently. |
| Unavailable daemon during registration or stop | Inspect the retained process handle, current address and startup log. Preserve uncertain results; do not assume a request succeeded. |
| Manual plugin unavailable | Inspect startup/exit diagnostics and the configured executable. Correct the cause and restart deliberately; there is no automatic plugin restart. |
| `missing_session` or skill identity failure | Use A's supported top-level environment with matching UUIDs. Never invent an ID or delegate registration. |
| `unknown_plugin`, `plugin_unavailable`, or `watch_rejected` | Check the configured name/readiness and exact watch JSON. Require an absent trigger, writable parent and one consistent lexical path per physical file. |
| `invalid_json`, `unsupported_version`, `invalid_request`, or `resource_exhausted` | Inspect encoding/schema and reported limits. Use matching client/daemon builds; do not loop registration calls. |
| Missing registration response after possible commit | The result is uncertain. An explicit identical registration can return the existing active ID. No automatic retry is implied. |
| Claimed-file read/size/encoding/removal error | No event is emitted for that file. Inspect the reported claimed path and move it aside deliberately before another trigger. No claimed-file retry occurs. |
| Delivery `failed` or `unknown`, or absent acknowledgement | Preserve diagnostics and inspect A before a new event. Do not infer acknowledgement, retry or change recipient. |
| Stop acknowledgement without exit evidence | Keep waiting within the declared observation bound and inspect diagnostics. Daemon completion and configured-plugin exit evidence are both required. |

The daemon logs identifiers and bounded diagnostics, not raw context or full
command lines by default. Context still reaches A and may be visible in local
process arguments. Retention and removal of the run directory and copied logs
are the developer's responsibility. Preserve required evidence and stop the
daemon before removing run files. Keep sensitive paths and conversation IDs out
of public records when inappropriate for the test environment.

## Acceptance evidence and coverage

These procedures state expected behavior rather than completed test results.
The maintainer executes them during delivery and links complete run evidence
to the merged implementation revision. Record checks as not executed when
appropriate. Passing CI, static Pending cases, or local substitutes cannot
establish the real-service portions of AC-002, AC-003, AC-006, AC-009 or AC-010.
Keep this reusable guide separate from a particular run's transcript.

| Requirements/mvp IDs | Procedure and delivery evidence | CI or coverage boundary |
| --- | --- | --- |
| NFR-004; AC-009 | Steps 1-7; D09 retains environment/version/build transcript, commands, logs, real reply, shutdown and recovery evidence, and every failure. | Public command paths are checked in CI. The actual clean-build operating walkthrough and Codex acknowledgement run during delivery. |
| FR-001, FR-004, C-001; AC-001 | Steps 2-3 and 7; D09 records readiness and configured-plugin exit through the standard lifecycle. | `MVP-V01-INVALID-CONFIG`, `MVP-V03-ROUTE` and `MVP-V01-SHUTDOWN` establish representative startup and shutdown wiring. |
| FR-002, FR-005; AC-002 | Step 4; D02 records skill discovery, top-level matching UUID validation, CLI result/ID and plugin acceptance. | `MVP-V02-REGISTER` covers assembled registration; unit tests cover input and identity permutations. |
| FR-003, FR-004, FR-005; AC-003 | Steps 4-6; D03 retains advance authorization, A/B identities/views, context, source/subscription and A's unsolicited acknowledgement. | `MVP-V03-ROUTE` uses a controlled queue endpoint and cannot prove a real conversation reply. |
| FR-006; AC-004, AC-005 | Steps 6-7 and failure reference; D03/D09 separate queue result from reply and document loss/no-retry/re-registration. | `MVP-V04-INVALID-EVENT`, `MVP-V04-DELIVERY-OUTCOME`, `MVP-V04-MANUAL-INVALID-CONTENT` and `MVP-V05-RESTART` establish representative failure and restart behavior. |
| NFR-001; AC-006 | Steps 5-6; D06 retains same-handle idle interval, zero calls, complete trace, evaluated plugin/daemon source evidence and receipt/attempt/response chronology. | `MVP-V03-ROUTE` observes a controlled endpoint; the actual response time belongs to delivery. |
| NFR-003; AC-008 | Steps 3-6; D02/D03 use local control and inspect actual external-data and permission wrappers. | Local-control integration and source review establish the endpoint boundary. No local-user isolation claim is made. |
| FR-007; AC-010 | Steps 4 and 6; D10 retains same-ID duplicate results, one attempt for one event, busy-turn chronology, actual queue outcome and absence of retry. | `MVP-V10-DUPLICATE` establishes assembled duplicate behavior; an idle run cannot replace the real busy-target check. |

NFR-002 and AC-007 use the authoritative plugin specification, dependency and
product-source inspection, unit conformance checks, and the independent
language-neutral plugin smoke defined by the
[MVP verification strategy](mvp.md#verification-strategy). This walkthrough
does not establish every acceptance criterion by itself.

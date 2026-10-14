---
type: Design
title: Model-plugin operations
description: Build, register, trigger, observe, and stop the local trigger/action model-plugin example.
sources:
  - id: mvp-design
    resource: design/mvp.md
  - id: bundled-design
    resource: design/bundled-plugins.md
  - id: mvp-requirements
    resource: requirements/mvp.md
    title: Plugin-based event routing MVP
---

# Model-plugin operations

## Purpose and contract sources

The manual **trigger** uses the public `input` role; the model-output **action**
uses `output`. Keep those literal configuration values and CLI flags unchanged.
The [bundled migration design](bundled-plugins.md) governs source paths and
plugin README/example contracts; this guide specifies their paired operation.

This guide supplies the reusable local walkthrough for
[requirements/mvp](../requirements/mvp.md) NFR-004 and AC-009. Build the daemon,
registration client, model input, and model output from the same revision;
register an input watch and output destination, trigger recognizable context,
observe the destination file, and stop. No live service, credentials, Codex
installation, or coding-agent session is needed.

The [governing MVP design](mvp.md) owns configuration, CLI, local control,
registration identity, routing, and the model-plugin profiles.
[Plugin protocol v2](plugin-v1.md) owns public role messages and framework
lifecycle. This guide derives its procedures from those contracts and adds no
independent wire interface. The stable path `windows-operations.md` is retained;
the procedures do not select an operating system or support matrix.

The maintainer runs the clean-build README walkthrough during delivery and
records actual results. These instructions define the expected procedure;
they are not a report that any check has passed.

## 1. Prepare a clean build and evidence directory

Use Git, Go satisfying `go.mod`, and a terminal at the repository root. The
examples use a Python 3.12 interactive interpreter as a portable process/file
driver. Python is an example utility, not a product runtime dependency; the
example uses only its standard library. Launch it from a POSIX shell with:

```sh
python3
```

On another shell, launch the equivalent Python 3.12 interpreter. Keep it open
through the walkthrough and enter the following blocks in order. Record the
actual OS, shell, Python/Go versions, source revision, commands, stdout,
stderr, and failures in the delivery evidence. Preserve the terminal transcript.

```python
import datetime
import json
import pathlib
import platform
import subprocess
import tempfile
import time
import uuid

repo = pathlib.Path.cwd().resolve()
assert (repo / "go.mod").is_file(), "Start at the repository root."
run = pathlib.Path(tempfile.mkdtemp(prefix="agent-pulse-hub-model-")).resolve()
print("Evidence directory:", run)
print("OS:", platform.platform(), "Python:", platform.python_version())
print(subprocess.run(["git", "rev-parse", "HEAD"], cwd=repo,
    check=True, text=True, capture_output=True).stdout.strip())
print(subprocess.run(["go", "version"], cwd=repo,
    check=True, text=True, capture_output=True).stdout.strip())
suffix = subprocess.run(["go", "env", "GOEXE"], cwd=repo,
    check=True, text=True, capture_output=True).stdout.strip()
hub = run / ("agent-pulse-hub" + suffix)
manual = run / ("manual-plugin" + suffix)
output = run / ("model-output-plugin" + suffix)

for target, package in [(hub, "./cmd/agent-pulse-hub"),
                        (manual, "./plugins/triggers/manual"),
                        (output, "./plugins/actions/model-output")]:
    argv = ["go", "build", "-o", str(target), package]
    print(json.dumps(argv))
    result = subprocess.run(argv, cwd=repo, text=True,
        encoding="utf-8", capture_output=True)
    (run / (target.name + ".build.log")).write_text(
        result.stdout + result.stderr, encoding="utf-8")
    print("Build exit:", result.returncode)
    assert result.returncode == 0, result.stderr

```

Each output path is in a new directory, so an old binary cannot satisfy a
failed clean build. `go env GOEXE` supplies the compiler's executable naming
convention without hard-coding an OS suffix. Stop on any build failure and
retain its log.

## 2. Configure both plugins and select destinations

Choose a literal IPv4 loopback endpoint and pass it to every daemon/client
command. The example uses `127.0.0.1:47831`. A conflicting listener makes daemon
startup fail before plugins start; do not accidentally launch another instance
at a different address. Local control is trusted local access, not isolation
between OS users.

```python
address = "127.0.0.1:47831"
trigger = run / "trigger.txt"
destination_a = run / "destination-a.jsonl"
destination_b = run / "destination-b.jsonl"
config_path = run / "config.json"
watch_path = run / "watch.json"
destination_path = run / "destination.json"
config = {"plugins": [
    {"name": "manual", "role": "input", "executable": str(manual), "args": []},
    {"name": "model", "role": "output", "executable": str(output), "args": []}
]}
config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")
watch_path.write_text(json.dumps({"trigger_file": str(trigger)}), encoding="utf-8")
destination_path.write_text(json.dumps({"output_file": str(destination_a)}),
    encoding="utf-8")
assert not trigger.exists()
assert not destination_a.exists() and not destination_b.exists()
print("Watch:", watch_path, "Destination arguments:", destination_path)
print("A:", destination_a, "Unregistered B:", destination_b)
```

The model input requires an absent absolute `trigger_file` in an existing
writable directory. It compares lexically cleaned paths by exact string,
without physical-file identity guarantees. Use one spelling per trigger and
one publisher. Avoid aliases and moving or retargeting the parent directory.
Publish only after registration succeeds.

The model output accepts exactly an absolute `output_file` with an existing
writable parent. The file is absent or is an ordinary file containing valid
UTF-8 JSON-lines records. Use the fresh absent path above for this walkthrough.
Destination validation performs no write and creates no file; it does not
guarantee that a later open or write will succeed. Observe the file directly
with ordinary file-reading tools. Avoid external modification while delivery
is in progress. Appending, closing, and an `accepted` result do not promise
fsync durability or crash recovery.

## 3. Start and inspect readiness

```python
def start_daemon():
    log_path = run / ("daemon-" + uuid.uuid4().hex + ".jsonl")
    sink = log_path.open("xb")
    argv = [str(hub), "daemon", "--config", str(config_path),
            "--address", address]
    print(json.dumps(argv))
    try:
        handle = subprocess.Popen(argv, cwd=repo, stderr=sink)
    except BaseException:
        sink.close()
        raise
    print("Daemon PID:", handle.pid, "Log:", log_path)
    return handle, sink, log_path


daemon, daemon_sink, daemon_log = start_daemon()
print(daemon_log.read_text(encoding="utf-8"))
print("Daemon status (None means running):", daemon.poll())
```

Repeat the last two inspection lines until the same daemon is still running,
both `manual` input and `model` output are ready, and registration is
available. Use observable readiness rather than a fixed startup sleep. A
missing executable, handshake/role mismatch, or unavailable model plugin
stops this walkthrough; preserve diagnostics. Starting and waiting request
no destination work. Readiness is distinct from destination registration.

## 4. Register and repeat the same registration

```python
register_argv = [str(hub), "register", "--input-plugin", "manual",
    "--watch-args-file", str(watch_path), "--output-plugin", "model",
    "--destination-args-file", str(destination_path), "--address", address]

def register_once():
    print(json.dumps(register_argv))
    result = subprocess.run(register_argv, cwd=repo, text=True,
        encoding="utf-8", capture_output=True)
    print("Register exit:", result.returncode,
          "stdout:", result.stdout, "stderr:", result.stderr)
    assert result.returncode == 0
    response = json.loads(result.stdout)
    assert response["ok"] is True and response["subscription_id"]
    return response["subscription_id"]


subscription = register_once()
assert register_once() == subscription
assert not trigger.exists() and not destination_a.exists()
print("Active subscription:", subscription)
```

Success appears only after the output accepts destination arguments and the
input accepts the watch. No destination is inferred. Retain both CLI results
and plugin acceptance diagnostics. Repetition returns the same active ID and
adds no watch or delivery. Registration does not create an event or write the
output file. An error or lost response is not success; explicit identical
registration may resolve response loss while the original route remains
active. The client does not retry automatically.

Changing output plugin or destination arguments creates a different
registration identity. It does not retarget an existing subscription. The
model input rejects another independent watch of the same cleaned trigger
path; choose a fresh trigger when deliberately registering a different route.
Do not register B in this walkthrough.

## 5. Observe idle behavior and publish one event

For AC-006, declare the observation interval and retain complete daemon logs
and destination observations. The one-second interval below is an example
choice, not a latency target or readiness wait. Inspect the evaluated model
input/output and daemon source to establish that idle paths make no independent
destination-work request. Zero log entries alone do not establish this if
logging is incomplete.

```python
def utc_now():
    return datetime.datetime.now(datetime.timezone.utc)


def output_records(path):
    if not path.exists():
        return []
    content = path.read_text(encoding="utf-8")
    if content and not content.endswith("\n"):
        raise ValueError("Output record has no terminating newline yet.")
    return [json.loads(line) for line in content.splitlines()]


def publish_event(context):
    stamp = utc_now()
    sibling = trigger.with_name("publish-" + uuid.uuid4().hex + ".tmp")
    sibling.write_text(context, encoding="utf-8")
    assert not trigger.exists()
    sibling.rename(trigger)
    print("Published:", stamp.isoformat(), "Context:", context)
    return stamp


idle_started = utc_now()
before = output_records(destination_a)
time.sleep(1)
idle_ended = utc_now()
assert output_records(destination_a) == before == []
assert not destination_b.exists()
print("Idle interval:", idle_started.isoformat(), idle_ended.isoformat())
print(daemon_log.read_text(encoding="utf-8"))

context = "PULSE-MODEL-001 recipient=" + str(destination_b)
published = publish_event(context)
```

Confirm the log trace contains zero output-delivery attempts during the idle
interval. An incomplete trace leaves the observation incomplete. The model
input polls only its local trigger file; no coding agent polls. The context
mentions B deliberately: external event content cannot override registered A.
Publish nonempty strict UTF-8 text of at most 8,192 bytes by closing a temporary
sibling file and renaming it onto the absent trigger. This is a single-publisher
procedure; no race-proof no-replace guarantee is supplied.

## 6. Observe the actual model destination

```python
def wait_for_records(expected_count):
    deadline = time.monotonic() + 35
    records = []
    while time.monotonic() < deadline:
        try:
            records = output_records(destination_a)
        except (ValueError, UnicodeDecodeError):
            records = []
        if len(records) >= expected_count:
            break
        assert daemon.poll() is None, daemon_log.read_text(encoding="utf-8")
        time.sleep(0.1)
    assert len(records) == expected_count, "Incomplete output observation; retain logs."
    return records


records = wait_for_records(1)
record = records[0]
assert record["input_plugin"] == "manual"
assert record["subscription_id"] == subscription
assert record["context"] == context
assert record["delivery_id"] and record["observed_at"]
assert record["external_data"] is True
assert record["permission_notice"] == "External data; no additional permissions are granted."
assert not destination_b.exists()
observed = utc_now()
print(json.dumps(record, ensure_ascii=False))
print("Observed:", observed.isoformat())
print(daemon_log.read_text(encoding="utf-8"))
```

The model output appends one JSON object plus newline and closes the file
before reporting acceptance. Its observation time is UTC. The 35-second wait
is an example observation bound, not a product performance target. Reading
while an append is incomplete may temporarily expose an incomplete record;
retain that observation and re-read after the output completes rather than
calling it accepted. Observe the actual file and daemon delivery result;
output-plugin acceptance and the record are separate evidence.

Retain event receipt, delivery attempt, output `observed_at`, and operator
observation times, correlated by delivery/subscription IDs. Verify one attempt
and one record, exact context/source/ID at A, no B output, and trigger/claim
consumption. These observations establish the model path, without claiming
real-service or Codex compatibility. For a failed/unknown result, preserve the
record if any and complete diagnostics. A destination may already contain the
record when acceptance is unknown; publishing another trigger is a distinct
event, never an automatic retry.

## 7. Stop, restart, and interrupt

```python
stop_argv = [str(hub), "stop", "--address", address]
print(json.dumps(stop_argv))
stop_result = subprocess.run(stop_argv, cwd=repo, text=True,
    encoding="utf-8", capture_output=True)
print("Stop exit:", stop_result.returncode,
      "stdout:", stop_result.stdout, "stderr:", stop_result.stderr)
assert stop_result.returncode == 0
assert json.loads(stop_result.stdout) == {"ok": True}
daemon_exit = daemon.wait(timeout=30)
daemon_sink.close()
assert daemon_exit == 0
print(daemon_log.read_text(encoding="utf-8"))
```

The stop acknowledgement confirms a shutdown request, not completed exit.
Observe daemon completion and both configured-plugin process exits through
standard `go-plugin` cleanup. Match `manual` and `model` to their complete
configured-process exit diagnostics, backed by the library's `Client.Exited()`
state in the evaluated source. Stream close or daemon exit alone is insufficient.
A 30-second observation timeout is incomplete shutdown evidence; preserve the
process handle and log, and do not call forced termination normal shutdown.

Shutdown loses registrations and queued events. An unconfirmed interrupted
delivery is unknown. There is no per-subscription unsubscribe, delivery retry,
or automatic plugin restart. Existing output records remain ordinary files;
they do not restore subscriptions.

Inspect the trigger and any claimed paths in diagnostics, preserving leftovers
before reuse. Restart the same configuration:

```python
old_subscription = subscription
daemon, daemon_sink, daemon_log = start_daemon()
print(daemon_log.read_text(encoding="utf-8"))
print("Daemon status (None means running):", daemon.poll())
```

Inspect both role readiness and registration availability again as in step 3.
Then register and publish a distinct marker, retaining the existing A records:

```python
subscription = register_once()
assert subscription != old_subscription
before = output_records(destination_a)
context = "PULSE-MODEL-002"
published = publish_event(context)
records = wait_for_records(len(before) + 1)
record = records[-1]
assert record["subscription_id"] == subscription
assert record["input_plugin"] == "manual" and record["context"] == context
assert record["external_data"] is True
assert record["permission_notice"] == "External data; no additional permissions are granted."
assert not destination_b.exists()
print(json.dumps(record, ensure_ascii=False))
print(daemon_log.read_text(encoding="utf-8"))
```

The CI restart case verifies explicit old-ID rejection; this walkthrough
observes the required new registration and retained output file.

Use the restarted live daemon for a separate foreground-interruption check.
In the same terminal holding the Python interpreter and attached daemon,
enter the following block and press Ctrl+C once when prompted. Do not send
another stop request first or close the terminal.

```python
assert daemon.poll() is None
try:
    input("Press Ctrl+C once to interrupt the live foreground daemon: ")
except KeyboardInterrupt:
    interrupted = utc_now()
else:
    raise RuntimeError("No terminal interrupt observed; the check is incomplete.")


daemon_exit = daemon.wait(timeout=30)
daemon_sink.close()
assert daemon_exit == 0
print("Interrupt:", interrupted.isoformat())
print(daemon_log.read_text(encoding="utf-8"))
```

Retain the separate run's readiness and both configured-plugin exit diagnostics.
Python's `KeyboardInterrupt` alone does not prove daemon or plugin termination.
The stop and interruption paths enter the same shutdown coordinator; retain
source evidence and both run transcripts. No descendant-process termination
check is required.

## Failures, retention, and verification boundaries

| Observation | Meaning and operator action |
| --- | --- |
| Invalid configuration, executable, role, or endpoint | Correct the reported input and retain diagnostics; require both model roles before continuing. |
| Rejected watch/destination or unavailable plugin | No active subscription is established. Check argument files, trigger absence, lexical path conflicts, and output parent/file validity. |
| Output exit after an input watch request | The route stays inactive; its residual watch may consume capacity until daemon shutdown. It cannot deliver or be reused. Stop/restart deliberately before reusing a conflicted model trigger. |
| Missing registration response | Acceptance may be uncertain. An explicit identical request can recover the same still-active ID; the client never guesses a destination. |
| Invalid trigger encoding/size or claim/read/remove failure | No event is emitted for that file. Inspect the claimed path and preserve leftovers before a new publication; no claim retry occurs. |
| Output open failure before writing | The plugin reports `failed`; no retry occurs. Correct the destination before deliberately publishing a distinct event. |
| Output write/close failure or lost confirmation | Outcome is `unknown`; inspect A before another trigger because a record may already be present. |
| Plugin timeout, transport failure, or exit | Affected routes become unusable; separate available routes continue. Recovery by daemon restart loses every registration. |
| Stop response without complete exits | Shutdown evidence is incomplete. Retain the process/logs within the declared observation bound. |

Context is external data and grants no new permissions. The daemon uses bounded,
sanitized diagnostics without raw context or full command lines by default.
Context is intentionally visible in the model output file. Operators own
retention of trigger files, destinations, and evidence logs; stop the daemon
before removing run files and omit sensitive data from public evidence.

| Requirement / acceptance | Evidence allocation |
| --- | --- |
| FR-001, FR-004, C-001; AC-001 | Actual model-role startup and standard configured-process shutdown; command integration plus the walkthrough. |
| FR-002, FR-007; AC-002, AC-010 | Ordinary registration and repeated active ID, one watch/attempt; rejection and changed-destination matrices use the governed CI cases and unit tests. |
| FR-003, FR-004, FR-008; AC-003 | Actual model trigger and output file, exact source/ID/context, A/B isolation and attempted retargeting through external content. |
| FR-006; AC-004, AC-005 | Governed CI failure/old-ID cases; walkthrough loss-of-state, deliberate recovery, and failure/no-retry instructions. |
| NFR-001; AC-006 | Declared idle interval, zero delivery requests and destination records, source inspection, correlated receipt/attempt/observation times. |
| NFR-003; AC-008 | Local client registration and visible external-data/permission fields in output. |
| NFR-004, NFR-005; AC-009 | Maintainer executes the clean-build README usage case with ordinary programs during delivery and retains complete evidence. |
| NFR-002, NFR-005, C-002; AC-007, AC-011 | Interface/source inspection, independent-language replacement cases, and the standalone verification boundary in the plugin specification. This walkthrough does not replace those checks. |

The hub suite exercises actual bundled model plugins without live-service
dependencies. Future service-plugin checks exercise their own target service
independently and keep their evidence separate. No future plugin integration
success is claimed by this walkthrough. Pending, dry-run, tooling, and
feasibility results do not establish implemented routing or completed delivery.

For malformed or partial output, stop the daemon and await configured-plugin
exits. Preserve the damaged file and inspect trigger/claim leftovers. Choose
fresh absent output and trigger paths in an existing writable directory, write
those paths into the destination and watch argument files using JSON
serialization, then restart and register. Publish only a deliberately chosen
subsequent distinct event; this procedure does not retry the uncertain event
or repair its output. An `unknown` outcome can also leave a complete valid
record, so inspect it before deciding whether damaged-file recovery applies.

## Source-layout migration

Stop the daemon and wait for plugin exits before replacing binaries. Build the
manual trigger from `./plugins/triggers/manual` and model-output action from
`./plugins/actions/model-output`; the former `cmd/` plugin build paths are
removed. Binary output names and public v2 contracts remain compatible. Update
configured executable paths only if the build outputs move, then start and
register again. Inspect leftover triggers and output records before recovery.
Rollback uses the prior revision and its source paths; it cannot undo output.

Each plugin supplies `examples/daemon.json`; manual supplies `examples/watch.json`
and model-output supplies `examples/destination.json`. These contain explicit
path placeholders. Replace whole JSON string values through serialization,
using the chosen absolute paths from steps 1–2; the daemon does not expand
placeholders. Both daemon templates describe the same pair. The procedure above
generates equivalent runtime JSON directly. During delivery, also materialize
and run the shipped templates as specified by BUNDLE-D01 in the migration design.

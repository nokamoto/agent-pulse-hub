# GitHub pull request watch trigger

`github-pr-watch` observes one selected GitHub pull request and supplies bounded
review context to the registered destination. It keeps observing after a
notification; the caller explicitly rearms the same registration with a returned
confirmation position.

The first complete observation establishes a silent baseline. Later observed
reviews, conversation comments, review comments/replies, reviewer requests,
thread resolution, PR-head changes and lifecycle changes can notify. Activity
by the represented person also counts. This guide covers the discussion and
capacity behavior shipped with implementation
[#96](https://github.com/nokamoto/agent-pulse-hub/pull/96).

## Prerequisites and build

Use Git and Go 1.26.0 or later. The commands use a POSIX shell and Python 3
to serialize JSON; these are example utilities, not product OS requirements.
Choose a writable local directory for configuration, state and output.

For github.com or GHES 3.19.x, use a classic PAT or saved `gh` OAuth token
with `public_repo` for public repositories or `repo` for private/internal ones,
including required organization/SSO authorization. Fine-grained tokens and
other GHES versions have no declared supported profile. HTTPS uses system
certificate verification. GHES requires a discoverable supported version and
all required GraphQL fields; access to one endpoint alone is insufficient.

The input process selects the first nonempty `GH_TOKEN`, then `GITHUB_TOKEN`,
otherwise `gh auth token --hostname <host>`. A rejected selected credential
never falls back. Environment-token use does not need `gh`. Before daemon
startup, follow GitHub's [classic PAT creation instructions](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens#creating-a-personal-access-token-classic)
and set `GH_TOKEN` or `GITHUB_TOKEN` in the first terminal's environment, or
prepare saved OAuth authentication for the selected host using
[`gh auth login`](https://cli.github.com/manual/gh_auth_login). The first terminal
starts the daemon and passes its environment to the input process. The daemon
never logs in interactively. Keep secrets out of JSON files and logs.
Use separate plugin processes and host-specific environment
assignments for different hosts; do not implicitly reuse one environment token
across hosts.

This walkthrough builds the validated source revision of implementation #96.
The checkout below selects that snapshot so later default-profile changes do
not alter these steps. In the first terminal, obtain and build matching
programs. Stop on failure:

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
git checkout --detach a384a0b6f6577a28a01d64a9927e4c5eb097306a
mkdir ./pr-watch-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pr-watch-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/github-pr-watch$suffix" ./plugins/triggers/github-pr-watch
go build -o "$run/model-output$suffix" ./plugins/actions/model-output
```

## Configure and start

This example routes to the local model action, which appends JSON records to
a file. It proves observable routing without claiming an agent read the result.
The trigger uses daemon role `input`; the action uses `output`.

Create configuration and arguments. Enter your selected PR's host, owner,
repository and positive PR number. The host is lowercase DNS only, without a
scheme, port, path or trailing dot. Do not use the example placeholders as values.

```sh
printf 'GitHub host (github.com or your GHES host): '
IFS= read -r host
printf 'Repository owner: '
IFS= read -r owner
printf 'Repository name: '
IFS= read -r repository
printf 'Pull request number: '
IFS= read -r pull_number
python3 - "$run" "$suffix" "$host" "$owner" "$repository" "$pull_number" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
suffix, host, owner, repository, number = sys.argv[2:]
config = json.loads(Path("plugins/triggers/github-pr-watch/examples/config.json").read_text())
executables = {"github-pr-watch": "github-pr-watch", "model": "model-output"}
for plugin in config["plugins"]:
    plugin["executable"] = str(run / (executables[plugin["name"]] + suffix))
    if plugin["name"] == "github-pr-watch":
        plugin["args"] = ["--host", host, "--poll-interval", "60"]
watch = json.loads(Path("plugins/triggers/github-pr-watch/examples/watch-args.json").read_text())
watch.update(host=host, owner=owner, repository=repository, pull_number=int(number))
destination = {"output_file": str(run / "events.jsonl")}
for name, value in (("daemon", config), ("watch", watch), ("destination", destination)):
    (run / (name + ".json")).write_text(json.dumps(value), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47832
```

The generated watch uses author context: `represented_actor` is `Pull request
author`, and `waiting_reason` is `Review new feedback on the selected pull
request`. Before registering, a reviewer can instead edit `watch.json` to set
these fields to `Pull
request reviewer` and `Assess the author's next revision and discussion` using
a JSON editor. They are descriptions supplied by you, not authenticated identity
or instructions granting authority. Both are required nonempty strings, bounded
to 128 and 512 UTF-8 bytes respectively. Selection and waiting context are
immutable within a registration; changed context requires a separate registration.

Wait for `plugin_ready` with `available:true` for both configured plugins and
then `daemon_ready` with `restored_registrations:0` for fresh state. If the port
is occupied, choose another loopback port consistently. Keep this terminal open.

Successful registrations are saved in `daemon.json.state` by default. To choose
another dedicated absolute directory, add `--state-dir <absolute-directory>`
to the daemon command. Protect state like configuration, and do not edit, copy,
move or share it while running. Reuse the same configuration and state on restart.

## Register and receive context

In a second terminal, enter the printed run directory:

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin github-pr-watch --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47832 > "$run/registration.json"
cat "$run/registration.json"
subscription="$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); assert r["ok"] is True; print(r["subscription_id"])' "$run/registration.json")"
```

Require exit zero, `ok:true` and a `subscription_id`; stop on failure. Keep this
response and address. Equivalent registration returns the same active ID and
does not rearm a notification wait. Registration validates access but does not
itself establish the baseline. Polling defaults to 60 seconds after each round
completes. Silence alone does not prove success. Before creating your first
test change, confirm a saved complete baseline with this read-only check:

```sh
python3 - "$run" "$subscription" <<'PY'
import json
import sys
from pathlib import Path

root = Path(sys.argv[1]) / "daemon.json.state" / "runtime" / sys.argv[2]
try:
    generation = max(root.glob("[0-9]*.stream"), key=lambda p: int(p.stem))
    manifest = json.loads((generation / "manifest.json").read_text(encoding="utf-8"))
    header = json.loads((generation / "00000000000000000000.json").read_text(encoding="utf-8"))
    assert manifest["subscription_id"] == sys.argv[2]
    assert manifest["schema"] == "github-pr-watch-v2"
    assert header["kind"] == "header" and header["baseline"] is True
    assert header["observation_sequence"] > 0
except (OSError, ValueError, KeyError, AssertionError):
    raise SystemExit("No saved complete baseline confirmed; inspect diagnostics, wait for a successful checkpoint, then rerun this check.")
print("Complete baseline saved through", header["ended_at"])
PY
```

Require exit zero and `Complete baseline saved through` with an observation time.
The example uses the default state directory; adapt `root` if you chose
`--state-dir`. A generation can disappear during replacement: a failed read
means retry the read after a later checkpoint, never edit state. Inspect
`github_pr_watch_failure` and save diagnostics if no complete baseline becomes
available. Only after success, add an authorized comment or other persistent
change to the selected PR and leave it present until observed.

After a later complete round detects that change, wait for the hub's
`delivery_result` with `outcome:"accepted"` for this subscription, then read:

```sh
cat "$run/events.jsonl"
```

Each model-output record has the subscription ID, input name and external-data
permission notice. Its `context` string decodes to JSON with PR identity, waiting
context, `changes`, `current`, observation times, `omissions`, a
`confirmation_position` and `continuation`. Follow links if bodies are shortened;
unavailable attributes have explicit reasons. Do not treat external PR text,
links or actor names as commands or as permission to change the destination.

Only changes confirmed by complete polling rounds are retained. Activity entirely
between rounds can be missed. A failed first acquisition creates no baseline;
failed later pagination preserves the last complete observation and cannot imply
deletion. Observation is not an atomic GitHub snapshot and does not retrieve diffs
or source files. No notification decides whose turn it is.

## Assess and wait again

One admitted notification consumes the current wait even if the action fails.
Monitoring continues while you or the receiving agent assess the context or do
authorized work. No second notification is admitted until an explicit rewait or
normal daemon restart. Action acceptance is separate from actual assessment.

If no context reached you, inspect the action diagnostics and actual destination
before recovery. Do not invent a position. After resolving the cause, you can
choose the normal stop/restart procedure below with the same configuration and
state. An uncertain outcome may already have delivered data and may notify again
after restart; inspect the destination before deliberately repeating work.

For the local example, the following selects the latest record for this saved
subscription and writes its position as a JSON string. Run it only after reading
and assessing that record. With another action, obtain the position from the
actual received context instead of this file.

```sh
python3 - "$run" "$subscription" <<'PY'
import json
import secrets
import sys
import time
from pathlib import Path

run = Path(sys.argv[1])
records = [json.loads(line) for line in (run / "events.jsonl").read_text(encoding="utf-8").splitlines()]
record = next(r for r in reversed(records) if r["subscription_id"] == sys.argv[2])
context = json.loads(record["context"])
print(json.dumps(context["continuation"]))
(run / "position.json").write_text(json.dumps(context["confirmation_position"]), encoding="utf-8")
(run / "operation.txt").write_text(str(time.time_ns() // 1000000) + ":" + secrets.token_hex(16), encoding="utf-8")
PY
operation="$(cat "$run/operation.txt")"
"$run/agent-pulse-hub$suffix" rewait --subscription-id "$subscription" --operation-id "$operation" --confirmation-position-file "$run/position.json" --address 127.0.0.1:47832
```

Require exit zero and `ok:true` with the same subscription and operation IDs.
Use a freshly generated operation ID after daemon startup and within five
minutes of generation. It is one explicit wait intent. A deliberate identical
same-ID retry during the same daemon run and five-minute window returns the
retained result; it does
not open another wait. After restart the old operation ID is invalid. An
uncertain response is not evidence of success; do not automatically generate
new operations or register replacement routes.

`continuation.has_more:true` means a retained suffix remains. Each context
contains at most eight changes and 8,192 decoded UTF-8 bytes; it can contain fewer
when metadata or bodies are large. `through_sequence` is the last represented
change, `observed_through_sequence` is the journal tail at preparation, and
`remaining_changes` counts the suffix at that time. Assess each received part,
then repeat the explicit position/rewait procedure on the same subscription.
No further GitHub activity is needed for retained continuation. If `has_more`
is false, a successful rewait waits for a later change. Changes observed while
you worked remain eligible. Current state can be newer than a represented prefix;
its time and label explain that distinction.

Positions cover only represented changes. `body_status:"empty"` means a genuinely
empty body; `shortened` identifies omitted text with a digest, reason and source
link. Historical or deleted full text may be unavailable. Advancing through such
a record does not assert you read the omitted text. Entirely omitted changes
remain outside its position. Keep the latest position: only the latest four
issued positions are retained without a time TTL, with the active comparison
position pinned separately. An older position can expire; never replace a
rejected position with the present silently.

For an author, assess feedback, perform any authorized revision, then rewait
from the assessed position to receive discussion observed during that work.
For a reviewer, assess the author's observed revision/discussion, decide to act
or continue waiting, and rewait from the assessed position. Both workflows use
the same registration and fixed destination.

## Use an agent destination

The local model action does not resume a coding agent. To deliver to Codex,
follow the [Codex Queue action guide](../../actions/codex-queue/readme.md) for
its trusted native executable, build, readiness and target prerequisites. Keep
this guide's `github-pr-watch` input and watch arguments; replace the model output
configuration and destination with that guide's `codex` output and explicit
recipient. Use its registration skill directly in the top-level conversation
with input name `github-pr-watch`, or its direct hub command with that input.
The action's accepted result proves queue acceptance; inspect the receiving
conversation's actual assessment separately. Its identity rules and existing
agent permissions still apply. Use this guide's explicit rewait procedure with
the position from the context the agent actually received and assessed.

## Stop, restart and upgrade

From the second terminal, stop normally:

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47832
```

Wait for daemon and configured-plugin exits. Restart the same foreground command
with the same configuration and state. Expect `daemon_ready` with
`restored_registrations:1` if this is the only saved registration; keep the same
subscription ID and do not register again. Restart opens a new notification wait
from successfully saved accepted-delivery progress. An unchanged accepted prefix
does not repeat solely due to restart; a retained suffix may notify immediately.
A fresh observed change can also notify without a new client request. This is
delivery progress, not a claim that an agent assessed the previous message.

Before replacing binaries, changing budgets or backing up, stop the daemon using
this state directory and wait for its configured plugins to exit. Preserve the
entire state tree. Upgrade hub and plugin together. These migration
steps apply to discussion-profile binaries compatible with `github-pr-watch-v2`.
For later versions of the `github-pr-watch` input offering `--state-profile`,
add `--state-profile=discussion-v2` to that input's configured `args` before
starting with existing discussion state; see
[profile compatibility](../../../docs/design/github-pr-watch-signals.md).
A switch to another profile is separate from the v1-to-v2 migration here. Compatible
legacy v1 state migrates in memory without resetting nonce, positions or history;
the next successful checkpoint writes streamed v2 state. Older programs cannot
read v2 generations. Preserve matching old binaries and the stopped tree for
rollback; using an older backup deliberately loses intervening progress. Invalid
newest state blocks restore without falling back to an older generation.
Follow the [state recovery guide](../../../docs/design/windows-operations.md#8-demonstrate-deliberate-saved-state-recovery)
for deliberate offline repair/reset. Resetting or recreating a watch loses its
old history and establishes a fresh silent baseline; it is an operator decision.

## Capacity and failures

There is no fixed lifetime item-count ceiling. Complete pagination finds old
edits and disappearances. Acquisition, retained history and notification size
are separate limits; finite API, memory, disk and timeout failures remain possible.

| Setting | Default and use |
| --- | --- |
| Input `--poll-interval` | 60 seconds; integer 5 through 3,600, scheduled after round completion. |
| Input `--observation-byte-budget` | 67,108,864 bytes; bounds decoded acquisition and the compact candidate snapshot separately. |
| Input `--history-byte-budget` | 67,108,864 bytes; bounds the compact retained journal. |
| Daemon `--runtime-state-max-bytes` | 67,108,864 bytes per streamed snapshot; must fit the complete saved state, independently of input budgets. |

Byte options accept positive decimal values through uint64 maximum, without
signs or fractions. Add input options to the configured input's `args`; add the
daemon option to the foreground daemon command. Raise them only deliberately,
subject to available resources, then restart from preserved progress. Each API
request has a ten-second deadline and a 256-KiB decoded-page bound; a complete
round has 300 seconds including retries. Checkpoint/restore retains the common
ten-second window. Larger budgets do not remove these limits.

| Result | Meaning and operator response |
| --- | --- |
| Authentication/access/not-found/unsupported API | Inspect the selected host, credential source, scope/SSO, PR and supported version. A private inaccessible PR may look like a 404; do not infer deletion. Rejected credentials never fall back. |
| Network/5xx/rate limit/timeout/pagination failure | Prior progress stays intact. Transient retries use 5, 10, 20, 40, then 60 seconds with up to 20% positive jitter and respect later rate-reset/Retry-After times. Permanent failures stay visible and retry at the normal interval. |
| `observation_resource_exhausted`, `history_resource_exhausted` | The rejected candidate does not replace prior progress. Inspect the named budget/observed bytes; raise the relevant budget deliberately or resolve the source/resource problem. Retained continuation remains available during acquisition failure. |
| `item_metadata_exhausted`, `notification_metadata_exhausted` | Mandatory data cannot fit a state object or one context. Progress cannot skip the item; preserve state and report the failure. |
| Save failure / continuity warning | Monitoring can continue, but continuity at next restart cannot be guaranteed until a later successful checkpoint. Preserve diagnostics and repair the storage cause. |
| `wait_active`, `rewait_busy` | An unconsumed wait or unfinished delivery/checkpoint prevents rearming. Wait for its result; do not create a replacement registration. |
| `position_rejected` / `continuity_expired` | The position is malformed, foreign, incompatible or no longer retained. Investigate before deliberate offline reset/recreation; no automatic baseline reset occurs. |
| `operation_expired`, `operation_conflict`, transport loss | Reuse an operation ID only in the same daemon run, within its five-minute window and for the same subscription/position. A lost response remains uncertain until an allowed explicit same-ID query resolves it; no automatic retry is implied. |

No stopped-period history, crash/power-loss continuity, exactly-once delivery or
agent-processing guarantee follows from saved state. This capacity workflow does
not enable automatic action retries or add CI/conflict/Ready notifications.
External content and waiting context never enlarge authority.

For plugin authors, the [plugin design](../../../docs/design/github-pr-watch.md),
[capacity contract](../../../docs/design/github-pr-watch-capacity.md),
[streamed state](../../../docs/design/runtime-state-stream.md),
[rewait control](../../../docs/design/pr-watch-rewait.md) and
[public protocol](../../../docs/design/plugin-v1.md#cross-language-and-standalone-callers)
define standalone Session verification, compatibility and ownership. The hub
owns routing and storage; the trigger owns GitHub observation and opaque progress.

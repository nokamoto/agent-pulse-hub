# GitHub pull request watch trigger

Watch one selected pull request and deliver its changes and current review
context to a fixed destination. An author can wait for review feedback; a
reviewer can wait for a response or revision. The receiving agent decides
whether to act, ask for clarification, or wait again under its existing
instructions. This trigger does not decide whose turn it is or write to GitHub.

This walkthrough first writes notifications to a local JSON-lines file so you
can inspect them. The final section connects the same trigger to an agent.

## Prerequisites

- Obtain the repository and Go 1.26.0 or later as described in the
  [root README](../../../README.md#installation).
- Choose a PR you may read on github.com or GitHub Enterprise Server (GHES)
  **3.19.x**. Other GHES versions are unsupported. GHES must expose its version
  metadata and required GraphQL fields, with a system-trusted HTTPS certificate.
- Use a classic personal access token or saved `gh` OAuth login with
  `public_repo` for public repositories or `repo` for private/internal ones,
  plus required organization/SSO authorization. Fine-grained tokens are not a
  declared supported profile. Although these scopes can permit writes, the
  plugin makes read-only queries.
- Use a writable local working directory. The examples use two POSIX shell
  terminals and Python 3.7 or later as example utilities, not product OS requirements.

The plugin selects the first nonempty `GH_TOKEN`, then `GITHUB_TOKEN`, then
`gh auth token --hostname <host>`. Environment-token users do not need `gh`.
It never falls back after the selected credential is rejected and never logs
in interactively. Configure credentials in the daemon's environment before
starting it; do not put secrets in example JSON or command history.

Use one configured plugin process per host, and make its `--host` match the
watch's `host`. Separate hosts need separately scoped credentials and processes;
do not implicitly reuse a global environment token across them. The example
below configures one host. API access uses verified HTTPS without redirects.

## Build and configure

From the repository root, build all programs from the same revision. Stop if
a command fails. Use a new directory for this walkthrough:

```sh
mkdir ./pr-watch-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pr-watch-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/github-pr-watch$suffix" ./plugins/triggers/github-pr-watch
go build -o "$run/model-output$suffix" ./plugins/actions/model-output
```

Materialize the [daemon](examples/config.json), [watch](examples/watch-args.json),
and [destination](examples/destination-args.json) examples. Enter your actual
host, repository, PR number, and waiting context at the prompts:

```sh
printf 'Host (github.com or your GHES hostname): '; IFS= read -r host
printf 'Repository owner: '; IFS= read -r owner
printf 'Repository name: '; IFS= read -r repository
printf 'PR number: '; IFS= read -r pull_number
printf 'Why are you waiting? '; IFS= read -r waiting_reason
printf 'Person or role you represent: '; IFS= read -r represented_actor
python3 - "$run" "$suffix" "$host" "$owner" "$repository" "$pull_number" "$waiting_reason" "$represented_actor" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
suffix = sys.argv[2]
source = Path("plugins/triggers/github-pr-watch/examples")
config = json.loads((source / "config.json").read_text(encoding="utf-8"))
watch = json.loads((source / "watch-args.json").read_text(encoding="utf-8"))
watch.update(host=sys.argv[3], owner=sys.argv[4], repository=sys.argv[5],
             pull_number=int(sys.argv[6]), waiting_reason=sys.argv[7],
             represented_actor=sys.argv[8])
executables = {"github-pr-watch": "github-pr-watch", "model": "model-output"}
for plugin in config["plugins"]:
    plugin["executable"] = str(run / (executables[plugin["name"]] + suffix))
config["plugins"][0]["args"] = ["--host", watch["host"], "--poll-interval", "60"]
destination = {"output_file": str(run / "events.jsonl")}
for name, value in [("config.json", config), ("watch.json", watch),
                    ("destination.json", destination)]:
    (run / name).write_text(json.dumps(value), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/config.json" --address 127.0.0.1:47831
```

All executable and argument-file paths must be absolute. The daemon does not
expand placeholders. Keep `input` / `output` as the configuration roles.
Select a different loopback port consistently if this port is occupied.

`host` is a lowercase hostname without scheme, port, path, or trailing dot.
`owner` and `repository` are path segments, each at most 100 UTF-8 bytes.
`pull_number` is an integer from 1 through 2,147,483,647. `waiting_reason` is
nonempty and at most 512 UTF-8 bytes; `represented_actor` is nonempty and at
most 128 bytes. An author might enter “Waiting for review feedback” and “PR
author”; a reviewer might enter “Waiting for the author's revision” and
“Reviewer for this PR”. This identity may differ from the credential's account.
Do not add a confirmation position to the watch arguments.

In the first terminal, wait for both configured plugins' `plugin_ready` with
`available:true`, then `daemon_ready` with `restored_registrations:0` for fresh
state. Readiness does not prove a completed GitHub observation. Keep the
terminal open to inspect retrieval and delivery diagnostics.

## Register and receive context

In the second terminal, enter the printed run directory:

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin github-pr-watch --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47831
```

Keep the returned `subscription_id`. Success is JSON with `"ok":true` and
that ID; access or argument failures must be resolved before proceeding.
Repeating equivalent registration returns the same ID and does not rearm it.
Watch arguments, waiting context, and destination remain fixed. Changing the
context or destination requires a separate registration and does not supersede
the original watch. There is no individual cancellation command.

The first complete observation after activation becomes a **silent baseline**;
existing PR content is not sent. A failed or incomplete observation cannot
establish it. Polling defaults to 60 seconds after each completed round;
`--poll-interval` accepts 5–3,600 seconds. Registration success is not baseline
confirmation, and there is no baseline-complete diagnostic. For a trial,
allow two configured polling intervals without retrieval errors, then make an
authorized change such as a uniquely marked PR comment. Allow another two
intervals for notification. These waiting periods are a trial procedure, not
proof that a complete observation succeeded. If no result appears, inspect
access, capacity, retrieval diagnostics, and timing before deliberately making
another marked change: the first may have been included in the silent baseline.

After a later observed change, expect `delivery_result` with
`outcome:"accepted"` for your subscription, then inspect:

```sh
cat "$run/events.jsonl"
python3 - "$run/events.jsonl" <<'PY'
import json
import sys
from pathlib import Path
record = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()[-1])
print(json.dumps(json.loads(record["context"]), indent=2, ensure_ascii=False))
PY
```

The outer JSONL record identifies the subscription and contains `external_data:
true` and a `context` string. Decode that string as JSON to read the PR link,
waiting context, observation times, `changes`, `current`, `omissions`, and
`confirmation_position`. This position is an opaque value returned with the
notification; use it to tell the same registration which point you assessed
when waiting again. Changed items and current state are separate.
Acceptance means the action accepted delivery; it does not mean an agent
assessed it or performed work.

## Assess and wait again

One admitted notification consumes the current wait, even if its action outcome
is failed or unknown. Monitoring continues and retains eligible changes while
the agent works. Assess the changes, current state, and omissions under your
existing instructions before returning that notification's position. Do not
infer a position from a timestamp or treat delivery acceptance as assessment.

For this single-registration file example, the following extracts the latest
delivered position. Run it only after assessing that exact record. With shared
output, select the record for your subscription explicitly instead.

```sh
python3 - "$run" <<'PY'
import json
import sys
from pathlib import Path
run = Path(sys.argv[1])
record = json.loads((run / "events.jsonl").read_text(encoding="utf-8").splitlines()[-1])
context = json.loads(record["context"])
(run / "position.json").write_text(json.dumps(context["confirmation_position"]), encoding="utf-8")
PY
printf 'Subscription ID returned by register: '
IFS= read -r subscription_id
operation_id="$(python3 -c 'import secrets,time; print(f"{time.time_ns() // 1_000_000}:{secrets.token_hex(16)}")')"
"$run/agent-pulse-hub$suffix" rewait --subscription-id "$subscription_id" --operation-id "$operation_id" --confirmation-position-file "$run/position.json" --address 127.0.0.1:47831
```

The position file contains one UTF-8 JSON **string**, without BOM, rather than
a watch object. Keep positions out of logs. Successful rewait returns the
same subscription and operation IDs. It installs a new wait; it does not
promise a successful poll or delivery. Retained changes after that assessed
position remain eligible even if they arrived before rewait; no additional
GitHub activity is needed to make them eligible.

Each explicit intent uses a fresh operation ID: Unix milliseconds followed by
`:` and 32 random lowercase hexadecimal digits. Its time must be later than
this daemon Session's start, no later than now, and at most five minutes old.
After an uncertain response, an explicit retry with the **same** ID and
position can recover the result within that Session and window without
creating a second wait. There is no automatic retry. After restart or expiry,
the result remains uncertain; a new ID means a deliberate new intent.

## Coverage, limits, and failures

The trigger observes submitted reviews and dismissal, PR conversation comments,
review comments and replies, head changes, reviewer requests, thread
resolution, and open/closed/reopened/merged state. Edits and disappearance of
previously observed content are changes. Your own activity and automation are
not filtered. Draft reviews, CI/checks, labels, diffs, and source files are
outside coverage. Observations are not atomic; activity that appears and
disappears entirely between rounds may be missed.

Coverage is deliberately bounded: 32 observed items across categories, 32
journal records, the latest four issued positions while their required
history is retained, and at most eight changes in one 8,192-byte notification.
Positions have no time-based TTL, but can expire when supporting history is
pruned. Body text may be shortened with explicit omission reasons, links,
and digests; disappeared bodies may no longer be retrievable. Inspect linked
content when needed before treating omitted information as examined. See the
[full capacity and retention contract](../../../docs/design/github-pr-watch.md#context-and-continuity-bounds)
for byte limits and acquisition bounds.

Retrieval failures are visible diagnostics, not empty observations. The last
valid progress is retained. Transient network/5xx failures back off through
5, 10, 20, 40, and 60 seconds plus up to 20% positive jitter, respecting later
server retry guidance. Permanent failures retry at the normal interval.

| Failure | Operator response |
| --- | --- |
| Authentication rejected, access denied, or unsupported host profile | Fix the selected host's credential, scopes/SSO, or supported GHES version; an invalid higher-priority token prevents fallback. Restart deliberately if startup/registration failed. |
| PR not found or inaccessible | Check host, identity, and access. A private PR may be concealed as not found; do not infer deletion. |
| Rate limit, timeout, pagination, or partial acquisition | Inspect the diagnostic and retry guidance. No incomplete round advances the baseline. |
| `coverage_capacity_exceeded` / `notification_capacity_exceeded` | The PR/history or context exceeds supported bounds. No ordinary notification hides the loss; inspect the PR and linked contract before choosing how to continue. |
| `position_rejected`, including `continuity_expired` | Inspect the PR and continuity error. Do not replace the position with “now”. Accepting lost continuity and starting a new baseline requires an explicit operator choice. |
| `wait_active` / `rewait_busy` | A wait is already armed, or delivery/checkpoint/rewait work is pending. Resolve the current state before another explicit attempt. |
| `operation_expired` / `operation_conflict` | Check Session/time validity or reuse of an ID for different input. Do not generate a new intent merely to hide uncertainty. |
| Failed/unknown action delivery | Inspect the destination before another deliberate action. There is one delivery attempt, no automatic delivery retry, and no exactly-once processing guarantee. |

## Stop and resume

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47831
```

Wait for daemon and configured-plugin exit. Reuse the same configuration and
state directory on restart. The default is the absolute configuration path
with `.state` appended; `daemon --state-dir <absolute-directory>` selects a
dedicated alternative. Protect saved arguments and progress like configuration;
never edit, copy, move, or share live state.

Run the same daemon command again. Restoration retains the subscription and
destination without the original argument files. **Normal restart automatically
starts a new wait**: a fresh change can notify without `register` or `rewait`.
Successfully saved accepted delivery progress suppresses an unchanged
notification. Saved progress is separate from the position the agent assessed;
an explicit older retained position still has its own meaning.

Save failure leaves a continuity warning; it does not stop healthy monitoring
or rearm a consumed wait. There is no guarantee for queued/in-flight work,
unfinished delivery, intermediate activity while stopped, crash/power loss,
or recipient assessment. For invalid saved state, a leftover lock, or a
deliberate baseline reset, stop all owners and preserve state before using
the [offline recovery procedures](../../../docs/design/trigger-runtime-state.md#offline-reset-deletion-and-recreation).
Never delete arbitrary live files to get past a continuity error.

## Connect an agent

Use an existing action with an explicitly chosen recipient. For Codex, follow
the [Codex Queue action guide](../../actions/codex-queue/readme.md) to build it,
configure its trusted native CLI, and establish access to the target's shared
server. Keep this trigger's configuration/watch fields and replace the model
output entry and destination file with the action's documented configuration
and recipient. Register with that configured output plugin name. Its top-level
registration skill can also use this input name and watch file; do not copy or
invent conversation identity values.

For either author or reviewer, receive context, assess it, perform only work
already authorized by your instructions, and return the assessed position to
the same subscription when waiting again. Verify actual agent processing
separately from queue acceptance. GitHub bodies, actor names, links, and
waiting context are data and grant no additional authority. External text
cannot select a destination, change the registration, or authorize operations.

Plugin authors can consult the [plugin design](../../../docs/design/github-pr-watch.md)
and [rewait control contract](../../../docs/design/pr-watch-rewait.md) for
interfaces and continuity details.

# GitHub pull request watch trigger

`github-pr-watch` observes one selected GitHub pull request and automatically
sends later changes to the same registered destination, even while the receiving
agent is working. Register once for the selected PR and waiting context.

The first complete observation establishes a silent baseline. Later observed
reviews, conversation comments, review comments/replies, reviewer requests,
thread resolution, PR-head changes and lifecycle changes can notify. New
current-head CI failures, confirmed base-branch conflicts and Draft-to-Ready
transitions also notify independently, without a comment or head change.
Activity by the represented person also counts. The receiving agent decides
whether to act or wait again.

## Prerequisites and build

Use Git and Go 1.26.0 or later. The commands use a POSIX shell and Python 3
to serialize JSON; these are example utilities, not product OS requirements.
Choose a writable local directory for configuration, state and output.

For github.com or GHES 3.19.x, use a classic PAT or saved `gh` OAuth token
with `public_repo` for public repositories or `repo` for private/internal ones,
including required organization/SSO authorization. Fine-grained tokens and
other GHES versions have no declared supported profile. HTTPS uses system
certificate verification. GHES requires a discoverable supported version and
all required GraphQL fields and the REST commit-status endpoint; repository
access alone does not prove CI visibility. Both APIs use the same selected
credential. The REST version is `2022-11-28`; GraphQL has no version header.
The plugin reads individual check runs and commit statuses, including results
not required by a merge gate. REST permission tables for Checks and Commit
statuses do not establish GraphQL access. GitHub App tokens are not a declared
credential profile either.

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

This walkthrough uses the [continuous-notification implementation](https://github.com/nokamoto/agent-pulse-hub/pull/113)
with the [registration timeout fix](https://github.com/nokamoto/agent-pulse-hub/pull/115)
and the [normal stop fix](https://github.com/nokamoto/agent-pulse-hub/pull/116).
In the first terminal, obtain that revision and build matching programs. If you
already cloned the repository from the root README, enter it and start with
`git checkout`. If the commit is missing, fetch it with `git fetch origin`.
Use a fresh `pr-watch-demo` directory. To resume an existing watch, skip to
[Stop, restart and upgrade](#stop-restart-and-upgrade).
Stop if any command fails.

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
git checkout --detach 0efadf047107a57d6923578556b813459dc5deae
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
        plugin["args"] = ["--host", host, "--poll-interval", "60", "--state-profile", "signals-v3"]
watch = json.loads(Path("plugins/triggers/github-pr-watch/examples/watch-args.json").read_text())
watch.update(host=host, owner=owner, repository=repository, pull_number=int(number))
destination = {"output_file": str(run / "events.jsonl")}
for name, value in (("daemon", config), ("watch", watch), ("destination", destination)):
    (run / (name + ".json")).write_text(json.dumps(value), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47832
```

Wait for `plugin_ready` with `available:true` for both configured plugins and
then `daemon_ready` with `restored_registrations:0` for fresh state. If the port
is occupied, choose another loopback port consistently. Keep this terminal open.

Successful registrations are saved in `daemon.json.state` by default. To choose
another dedicated absolute directory, add `--state-dir <absolute-directory>`
to the daemon command. Protect state like configuration, and do not edit, copy,
move or share it while running. Reuse the same configuration and state on restart.

## Register and receive context

The generated `watch.json` uses author context. For a reviewer, edit that file
in the printed run directory before registering. Set `represented_actor` to
`Pull request reviewer` and `waiting_reason` to `Assess the author's next
revision and discussion`. Both fields describe your waiting context. They are
required nonempty strings limited to 128 and 512 UTF-8 bytes respectively.
They carry no additional authority or authenticated identity. Changing the PR
selection or waiting context requires a separate registration.

In the second terminal, run the following and paste the printed path when prompted.

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin github-pr-watch --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47832 > "$run/registration.json"
cat "$run/registration.json"
subscription="$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); assert r["ok"] is True; print(r["subscription_id"])' "$run/registration.json")"
```

Require exit zero, `ok:true` and a `subscription_id`. Stop on failure. Keep this
response and address. Equivalent registration returns the same active ID.
Registration validates access, and the first complete polling round establishes
the baseline. Polling defaults to 60 seconds after each round completes.
Before creating your first test change, confirm a saved complete baseline with
this read-only check.

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
    assert manifest["schema"] == "github-pr-watch-v4"
    assert header["version"] == 4 and header["profile"] == "signals-v3"
    assert header["signal_version"] == 1
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
`github_pr_watch_failure` and save diagnostics in the first terminal if no
complete baseline becomes available. After success, add an authorized comment
or other persistent change to the selected PR and leave it present until observed.

After a later complete round detects that change, wait in the first terminal
for `delivery_result` with `outcome:"accepted"` for this subscription. Read the
output file from the second terminal.

```sh
cat "$run/events.jsonl"
```

After the first record appears, add another authorized change. Check the file
after the next complete round for a new record with the same subscription ID.
Keep the daemon running and use the existing registration throughout.

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

## Interpret independent signals

Existing failures, conflicts and Ready status are silent in the first complete
baseline. After it, any one eligible signal can trigger a notification.

| Signal | What independently notifies |
| --- | --- |
| Check run | A newly observed completed result with `FAILURE`, `STARTUP_FAILURE`, `TIMED_OUT` or `ACTION_REQUIRED`. A new run ID or changed execution/result version can qualify again. |
| Commit status | A newly observed record with `error` or `failure`, identified separately from check runs and from other records with the same context name. |
| Conflict | GitHub reports `CONFLICTING` for the current head/current base pair, including a base-only update. A later observed resolution permits recurrence. |
| Readiness | An observed Draft-to-Ready transition, including a later transition after Ready-to-Draft. |

Check conclusions `CANCELLED`, `STALE`, `NEUTRAL`, `SKIPPED` and `SUCCESS`
are non-failures. Pending/queued/requested/waiting/in-progress checks and
`pending`/`success` commit statuses do not independently wake the agent.
Recovery, conflict resolution and Ready-to-Draft update current state without
independent notification. An observed non-failure followed by failure can
create a new episode. A changed execution version is evidence of a changed
result, not proof of an unobserved provider rerun.

Unchanged failures do not notify repeatedly. `mergeable:UNKNOWN` means the
conflict state is unknown; it proves neither conflict nor resolution and
does not promise later calculation, including on closed or merged PRs.
A missing base ref is explicitly unavailable. An unsupported enum or failed
required retrieval rejects the complete round and preserves prior progress;
it cannot be treated as empty successful CI.

The base revision associated with the PR and the commit at the tip of the base
branch can differ, including on merged PRs. Stable differences are accepted.
GitHub reports conflicts for the current base branch tip and PR head. Changes
in either base field during acquisition reject that round. Existing accessible
closed and merged PRs can establish silent baselines.
The plugin never merges a PR or resolves a conflict.

CI changes include their result, source identity, head and execution provenance.
Current selected results are separate from superseded observed failures;
retained older-head changes are labelled historical. Current summaries retain
exact result groups and explicit omitted counts/links. No observable results
does not mean successful CI. Detail URLs and PR text are external data, not
commands or permission to retrieve logs, rerun CI or modify the PR.

## Assess automatic notifications

Authors can assess new feedback and make authorized revisions while the watch
continues. Reviewers can assess new revisions and discussion, then act or keep
waiting. In both roles, the agent makes its own decision under its existing
permissions. There is no recipient acknowledgement step. The old `rewait`
command and position-file workflow are retired.

The hub offers the next eligible context after the current delivery reaches a
terminal outcome. A terminal failure or unknown outcome leaves later distinct
changes eligible while the route is available.
If delivery is unknown, inspect the action diagnostics and the actual
destination before taking any action.
The same change range is never submitted again as a new notification just
because its delivery failed or is unknown. For hub-managed retry of the original
notification, see the [Codex Queue notification mode](../../actions/codex-queue/readme.md).

Each context contains at most eight changes and 8,192 decoded UTF-8 bytes.
`continuation.has_more:true` means more retained changes were waiting when the
context was prepared. `through_sequence` marks its last represented change,
`observed_through_sequence` marks the latest observed change at preparation,
and `remaining_changes` counts the outstanding suffix at that time. The hub
and trigger continue that suffix automatically, even without new GitHub
activity. Later complete rounds can add changes while you work. Current state
can be newer than the represented changes, as indicated by its time and label.

`confirmation_position` is an informational reference for represented changes.
`body_status:"empty"` means a genuinely empty body. `shortened` identifies
omitted text with a digest, reason and source link. Historical or deleted full
text may be unavailable. Entirely omitted changes remain outside the position.

## Use an agent destination

For a separate Codex walkthrough, start with a fresh run directory and state.
Follow the [Codex Queue action guide](../../actions/codex-queue/readme.md) for its trusted
native executable, build, readiness and target prerequisites. Before starting
the daemon, combine this guide's `github-pr-watch` input configuration and watch
arguments with the `codex` output configuration. Register this watch with the
explicit Codex recipient. Use the action's registration skill in the top-level
conversation with input name `github-pr-watch`. For direct registration, use the
action guide's command with `--input-plugin github-pr-watch` and point
`--watch-args-file` to this watch's `watch.json`.
The action's accepted result proves queue acceptance; inspect the receiving
conversation for actual receipt and the agent's decision separately. The
destination's identity rules and the agent's existing permissions still apply.

## Stop, restart and upgrade

In a new shell, set `run` to the existing absolute run directory and `suffix`
to the executable suffix used in the build (`.exe` on Windows, empty otherwise).
Prepare the same GitHub authentication environment in the daemon's terminal.
If the daemon is still running, stop it from the second terminal.

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47832
```

In the first terminal, look for `runtime_state_saved` for this subscription
during shutdown. After the daemon and plugins exit, confirm the file
`daemon.json.state/runtime/<subscription_id>/continuous-active.json` is absent
under the run directory. If you set `--state-dir`, look under that directory
instead. The marker is cleared only after the final saves succeed.

In the first terminal, restart with only the last `daemon` command from
[Configure and start](#configure-and-start). Reuse the previous configuration,
address and any `--state-dir` argument. If this is the only saved registration,
expect `daemon_ready` with `restored_registrations:1`. Keep the same subscription
ID and skip registration.
A retained suffix may notify immediately. Restart continues
after the last selected range, including a range whose delivery failed or was
unknown. Add a fresh authorized change after startup and check for a new record
under the original subscription ID.

If startup reports `continuous_progress_unverified`, preserve the stopped
state directory with its marker intact. A previous run left the marker because
continuity was unverified. Restore only from a verified clean backup that
includes the selection progress for every change range that might already have
been submitted. An older checkpoint alone cannot establish that coverage. If
you cannot verify coverage, use the
[offline recovery procedure](../../../docs/design/windows-operations.md#trigger-runtime-state-recovery)
with `DELETE`, then register the watch again. Recreation loses prior
continuity and starts a new silent baseline.

### Upgrade an existing watch

Stop normally and confirm the final save before replacing binaries. Back up the
entire stopped state directory and the matching old binaries. Upgrade the hub
and trigger together and keep the registration's configuration and profile.

The default `--state-profile signals-v3` restores compatible legacy v3
checkpoints. The discussion-only `--state-profile discussion-v2` restores
compatible legacy v1/v2 checkpoints and does not observe CI, conflict or
readiness signals. Either profile writes v4 at the next successful checkpoint.
The existing registration and its observation history carry forward.
Previously attempted changes remain selected, and the remaining history can
notify automatically. An attempted delivery alone provides no evidence of
acceptance.

A profile change does not convert history between discussion and signals.
Keep the compatible profile for an existing watch, or deliberately recreate it
using the offline procedure above to start with the new profile. Invalid or
incompatible newest state blocks startup. Older binaries cannot read v4.
A rollback needs the matching stopped backup and loses any intervening progress.

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
| Save failure / continuity warning | Monitoring can continue. Preserve diagnostics and repair the storage cause. Restart continuity requires a successful final save and removal of the active marker during normal shutdown. |
| `continuous_progress_unverified` | Startup blocked because a prior run could not verify saved continuity. Preserve the state and use the stop and recovery instructions above. |

No stopped-period history, crash/power-loss continuity, exactly-once delivery or
agent-processing guarantee follows from saved state. The trigger does not
retry CI, retrieve CI logs, resolve conflicts or decide whose turn it is.
It does not itself retry action delivery; an action's separately configured
notification profile retains its own retry policy.
External content and waiting context never enlarge authority.

For plugin authors, the [plugin design](../../../docs/design/github-pr-watch.md),
[capacity contract](../../../docs/design/github-pr-watch-capacity.md),
[streamed state](../../../docs/design/runtime-state-stream.md),
[continuous notification](../../../docs/design/pr-watch-rewait.md) and
[public protocol](../../../docs/design/plugin-v1.md#cross-language-and-standalone-callers)
define standalone Session verification, compatibility and ownership. The hub
owns routing and storage; the trigger owns GitHub observation and opaque progress.

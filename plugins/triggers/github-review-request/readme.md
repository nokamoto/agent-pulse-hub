# GitHub review request trigger

Watch open pull requests that request your review directly. Team-only requests
are excluded. The walkthrough writes each new request's pull request URL to a
local JSON Lines file.

## Before you start

[Clone the source](../../../README.md#installation) and work from the repository
root with Go 1.26.0 or later. Build all three executables from the same revision.
The walkthrough uses two POSIX shell terminals and Python 3 to generate JSON.
Arrange for another user to request your review after the watch is ready.

## Authenticate

Use a user credential with read access to the repositories you want to watch.
The trigger needs HTTPS access to GitHub. It uses `GH_TOKEN` when set, otherwise
`GITHUB_TOKEN` when set, then the saved `gh` login for the host. Empty variables
are skipped. A rejected token stops registration without falling back.

Export the token in the daemon's terminal and keep it out of configuration and
logs. Classic PATs and OAuth tokens need `repo` for private repositories. Other
tokens must identify a user and allow issue search. See
[token permissions](../../../docs/design/github-review-request.md#service-and-token-compatibility).

For saved login, put `gh` on PATH and run the commands below in the daemon's
terminal.

```sh
unset GH_TOKEN GITHUB_TOKEN
gh auth login --hostname github.com
gh auth status --hostname github.com
```

### GitHub Enterprise Server

The API profile targets GHES 3.19.x. Live verification is pending. Use the
server's DNS hostname in place of `github.com` in the login and build steps.
HTTPS on port 443 must pass system certificate verification. The server must
support authenticated `/user` and direct-user issue search with pagination
and completeness metadata. See the
[API profile](../../../docs/design/github-review-request.md#service-and-token-compatibility).

For environment tokens, use a separate daemon per host with its own token,
run directory, state and loopback port. Change both `pulse-github-demo`
references for each run directory and use the chosen port throughout.
With both token variables unset, saved host logins can share a daemon. Register
a separate watch for each host.

## Build and start

Run the commands below from the repository root in the first terminal. For
GHES, replace `github.com` on the first line. Stop at the first error.

```sh
host=github.com
mkdir ./pulse-github-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pulse-github-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/github-review-request$suffix" ./plugins/triggers/github-review-request
go build -o "$run/model-output-plugin$suffix" ./plugins/actions/model-output
python3 - "$run" "$suffix" "$host" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
suffix = sys.argv[2]
host = sys.argv[3]
examples = Path("plugins/triggers/github-review-request/examples")
config = json.loads((examples / "daemon.json").read_text(encoding="utf-8"))
executables = {"github-review-request": "github-review-request", "model": "model-output-plugin"}
for plugin in config["plugins"]:
    plugin["executable"] = str(run / (executables[plugin["name"]] + suffix))
watch = json.loads((examples / "watch.json").read_text(encoding="utf-8"))
watch["host"] = host
destination = json.loads((examples / "destination.json").read_text(encoding="utf-8"))
destination["output_file"] = str(run / "events.jsonl")
(run / "daemon.json").write_text(json.dumps(config), encoding="utf-8")
(run / "watch.json").write_text(json.dumps(watch), encoding="utf-8")
(run / "destination.json").write_text(json.dumps(destination), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47832
```

The script uses the [daemon](examples/daemon.json), [watch](examples/watch.json),
and [destination](examples/destination.json) examples. The watch polls every
`60s` by default. Set `poll_interval` to another positive Go duration if needed.
Keep the printed run directory. Before registering, check for `daemon_ready`
and `plugin_ready` with `available: true` for both plugins. Use a different
loopback port throughout if the default port is occupied. Local control is
accessible to other users of the machine.

## Register and receive a URL

Use the second terminal to register the watch. Enter the printed run directory
when prompted.

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin github-review-request --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47832
```

On success, the command returns `"ok":true` and a `subscription_id`. Save the ID.
The watch searches repositories visible to your credential, with no repository
or organization filter.
At watch startup, the trigger reports the search limit of 4,000 GitHub-selected
repositories. Coverage beyond that limit cannot be detected or guaranteed.
The trigger rejects incomplete results and more than 1,000 matches. See the
[search limits](../../../docs/design/github-review-request.md#retrieval-and-supported-coverage).

In the daemon log, wait for `github_review_request_first_complete_observation`
with the returned ID in `registration_id` and `baseline_created: true`. The
first complete search records existing matches silently, so the output file
is still absent. If registration fails or the baseline stays pending, check
the daemon log for watch rejection or `github_review_request_observation_failed`.

Then have another user request your review on a new, accessible open pull
request with no current request to you. After GitHub indexes the request and
the next poll sees it, check for `delivery_result` with `outcome: "accepted"`
and your `subscription_id`, then read the output.

```sh
cat "$run/events.jsonl"
```

Each new JSON Lines record has the exact pull request URL as its `context`.
The file can contain private repository URLs. In this example, failed or
uncertain deliveries are not retried automatically, and unfinished attempts
may be lost on restart. A request can notify again after a complete search
sees it disappear and a later search sees it return.
See the [Codex Queue guide](../../actions/codex-queue/readme.md) to route URLs
to a Codex conversation.

## Stop, restart, or cancel

Use the stop command in the second terminal. Wait for the final
`runtime_state_saved` for your subscription and for the daemon to exit.
A save failure can leave the watch's saved progress out of date.

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47832
```

Restart with the same daemon command, configuration, and GitHub user.
For the retained example watch, expect `restored_registrations: 1` in
`daemon_ready`. The first complete search compares current matches with saved
progress and can notify new matches. An established baseline, even an empty
one, is preserved. Unchanged baseline matches and requests with confirmed
acceptance stay quiet. A watch still waiting for its baseline establishes it
silently after restart.

To cancel just this watch while the daemon is running, use its subscription ID.

```sh
printf 'Subscription ID returned at registration: '
IFS= read -r subscription
"$run/agent-pulse-hub$suffix" unsubscribe --subscription-id "$subscription" --address 127.0.0.1:47832
```

Expect `"ok":true` and the same subscription ID. Cancellation removes the watch
and its saved progress. A later registration establishes a new silent baseline. Issued actions
can still finish. See [cancellation recovery](../manual/walkthrough.md#cancel-one-subscription)
for failed or uncertain results.

## Reset or roll back

The walkthrough stores state in `daemon.json.state` beside `daemon.json`.
Stop all daemons using that state and back up the whole directory before a
reset or upgrade. Incompatible state or a changed GitHub user blocks
restoration. See [offline reset](../../../docs/design/trigger-runtime-state.md#offline-reset-deletion-and-recreation).
After reset, the retained watch establishes a new silent baseline.

The plugin reads compatible v1 checkpoints and writes v2. After a v2 save,
rolling back to a v1-only binary requires restoring the compatible backup or
resetting the affected watch while stopped. A reset on the older binary can
notify requests in the first search. See [checkpoint migration](../../../docs/design/github-review-request.md#restore-upgrade-and-reset)
and
[upgrade and rollback details](../../../docs/design/github-review-request.md#change-impact-and-open-decisions).

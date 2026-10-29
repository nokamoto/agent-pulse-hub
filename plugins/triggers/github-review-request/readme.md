# GitHub review request trigger

Watch open pull requests that request a review directly from your GitHub user.
The example below writes each newly observed request's URL to a local
JSON-lines file through the bundled model-output action. It includes existing
direct requests on the first search, across repositories visible to your
credential. There is no repository or organization filter. Team-only requests
are excluded.

## Prerequisites

Obtain the source with the `git clone` and `cd` commands in the root
[installation instructions](../../../README.md#installation); skip that section's
local-demo builds. Continue below from the repository root with Go 1.26.0 or later.
Build the hub and both plugins from the same revision. This trigger requires
the hub's `runtime_state_v2` checkpoint and activation capability, provided by
the builds below.

You need HTTPS access to GitHub and a user credential that can read the
intended repositories. Use either an environment token or saved GitHub CLI
authentication as described below. The example uses two POSIX shell terminals,
Python 3 to generate JSON, and a new writable local directory. The shell and
Python are example utilities, not product runtime dependencies or OS requirements.
Use only trusted executables. The daemon launches and supervises its plugins;
do not launch the trigger directly.

The designed service profiles are github.com and GitHub Enterprise Server
(GHES) 3.19.x. Real-service delivery verification remains incomplete; this
profile declaration does not establish tested GHES compatibility.

## Choose authentication and host

Set up authentication before starting the daemon. The trigger selects the
first nonempty credential in this order:

1. `GH_TOKEN`
2. `GITHUB_TOKEN`
3. Saved GitHub CLI authentication for the configured host

For an environment token, securely set the selected variable in the terminal
that will run the daemon. No GitHub CLI is required. Do not put the token in
JSON files, watch arguments, shell history, or shared diagnostics. The selected
token is used as-is: if rejected, it produces an error without trying a
lower-priority source.

For saved authentication, install a trusted `gh` executable on `PATH`, leave
both token variables unset, and log in interactively before starting the daemon:

```sh
unset GH_TOKEN GITHUB_TOKEN
gh auth login --hostname github.com
gh auth status --hostname github.com
```

The trigger retrieves saved authentication with `gh auth token --hostname HOST`,
without a shell or interactive prompting, and removes credential override
variables from that child environment. It verifies the credential through the
host's authenticated `/user` endpoint before accepting a watch.

Classic PATs or OAuth tokens need `repo` for private repository search; that
scope also grants write capabilities although the trigger only reads.
Fine-grained PATs and GitHub App user access tokens must be accepted by `/user`
and issue search; repository selection and organization approval still limit
visibility. Installation tokens that cannot resolve a user are unsupported.

`host` defaults to `github.com`. For GHES, use its DNS hostname without a
scheme or path. Requests use HTTPS port 443, normal system certificate
verification, `Accept: application/vnd.github+json`, and API version `2022-11-28`.
Configure GHES certificate trust in the system trust store. Use the GHES
hostname for saved login as well.

When using environment tokens for different hosts, run one daemon per host,
each with its host's token, its own run/configuration and state directories,
and a different loopback control address. Never register a second host on a
daemon carrying another host's token. The example below uses port `47832`;
for a separate GHES daemon choose another port consistently in its start,
register, and stop commands. When both environment token variables are absent,
host-specific saved `gh` logins may share one daemon.

## Build and configure

In the first terminal, use a fresh `pulse-github-demo` directory. Stop if any
command fails. Change `host` before generating the files if using GHES.

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

The [daemon example](examples/daemon.json) defines plugin names, roles, and empty
`args` arrays; the script replaces only executable paths. The [watch example](examples/watch.json)
selects the host and a polling interval of `60s`. The interval accepts any
positive Go duration string, including fractional durations. The
[destination example](examples/destination.json) selects the local output file.
With the empty action `args` array, this example uses default delivery without
automatic retry. Optional retained notification evidence and conservative
retry are described in the root [notification-mode guide](../../../README.md#retain-notification-evidence-and-enable-safe-retry).

Note the printed run directory. Wait for `plugin_ready` with `available: true`
for both configured plugins, then `daemon_ready` with `restored_registrations: 0`.
Authentication is checked when you register, not by daemon readiness alone.
Local control trusts loopback access and does not isolate users of the machine.
If the port is occupied, select another loopback port consistently.

## Register and observe

In the second terminal, enter the printed run directory:

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin github-review-request --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47832
```

Expect exit success and JSON with `"ok":true` and a `subscription_id`.
Retain the returned ID and the daemon address if you want to cancel this watch.
The trigger reports its search coverage limitation at watch startup. Repeating
the same registration returns the same active ID. Once registered, it begins
searching automatically. With no matching requests, no output file is expected.

Have another user request your review directly on an open pull request you can
access, or observe an existing direct request. Allow for search indexing and
the polling interval. In the first terminal, look for `delivery_result` with
`outcome: "accepted"` and your subscription ID. Then inspect the output in the
second terminal:

```sh
cat "$run/events.jsonl"
```

Each JSON record contains the pull request URL, subscription ID, input name
`github-review-request`, and `external_data: true`. The URL is the whole event
context. The permission notice states that external data grants no additional
permissions. Routing remains bound to the registered destination. The output
can contain private repository URLs: protect it and choose its retention
accordingly; do not modify it while the daemon is running.

To send URLs to a Codex conversation instead, use the
[Codex Queue action guide](../../actions/codex-queue/readme.md) to prepare that
action and register its destination with this trigger's watch arguments.

## Cancel this watch

To stop only this subscription while leaving the daemon and other watches
running, use the ID returned by its successful registration:

```sh
printf 'Subscription ID returned at registration: '
IFS= read -r subscription
"$run/agent-pulse-hub$suffix" unsubscribe --subscription-id "$subscription" --address 127.0.0.1:47832
```

Require exit success and `"ok":true` with the exact same `subscription_id`.
Cancellation removes this watch and its saved progress, so normal restart does
not restore it. It does not change review requests on GitHub.
An action already issued may still finish; cancellation cannot
recall accepted output. A later registration creates a fresh watch without the
cancelled watch's suppression history. For rejected, busy, storage, or uncertain
results, follow the root [cancellation instructions](../../../README.md#cancel-one-subscription)
and [recovery guide](../../../docs/design/windows-operations.md#cancel-one-subscription).

## Stop and restart

From the second terminal, stop normally:

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47832
```

Alternatively, press Ctrl+C in the first terminal. Wait for final
`runtime_state_saved` for each retained subscription, configured-plugin exit diagnostics,
and daemon exit before relying on saved progress. A save failure means restart
continuity is not guaranteed even if live monitoring continued.

Registrations and trigger progress are saved in `daemon.json.state` beside this
example's configuration. By default, this directory is the absolute
configuration path plus `.state`; `daemon --state-dir ABSOLUTE_DIRECTORY`
selects another dedicated local directory. Protect the daemon environment,
configuration, and state as operational data. Do not edit, move, copy, or share
live state.

Restart with the same daemon command, configuration, state, host, and user
identity. Wait for `daemon_ready` with `restored_registrations: 1`; do not
register again. This assumes you retained the example's one watch; if you
cancelled it above, expect zero restored registrations instead.
For a retained watch, confirmed requests remain suppressed; a newly observed request
can produce a new URL. Restoration does not need the original argument files.
A new registration starts without prior progress.

An incompatible checkpoint or changed user identity prevents watch restoration;
it is not treated as first use. With the daemon stopped, follow the
[offline recovery procedure](../../../docs/design/windows-operations.md#trigger-runtime-state-recovery).
Preserve state before deliberate recovery. Reset relinquishes previous
suppression and can notify initial matches again. Forced termination and
power-loss recovery are not guaranteed.

## Observation and delivery limits

The trigger searches with `user-review-requested:@me`, excluding team-only
requests, closed pull requests, and items that do not identify open pull
requests. Unchanged matches are suppressed. A complete observation without a
previously present request allows a later reappearance to produce a new event.
A cancellation and re-request between observations may be missed.

Search is eventually indexed and pagination is not an atomic snapshot.
Coverage is at most 4,000 GitHub-selected repositories; exceeding that coverage
cannot reliably be detected or guaranteed. The trigger reports this limit at
watch startup. It rejects incomplete pages, changing counts, duplicates, and
more than 1,000 results instead of treating partial data as an empty set.
A complete observation costs one search request for 0–100 matches and up to
ten for 901–1,000. Registrations poll independently and multiply that load;
increase the interval if needed. The authenticated search limit is shared
with other clients using the credential.

Each HTTP request is limited to ten seconds and 8 MiB; a complete observation
is limited to 120 seconds. Network and service failures use bounded exponential
backoff. Rate limits follow `Retry-After` or exhausted `X-RateLimit-Reset`
guidance. Authentication, permission, unsupported API, and incomplete-search
failures retain progress and produce bounded diagnostics. The interval is
not an end-to-end delivery deadline.

The trigger marks an event attempted before sending it. While the plugin
session remains usable, the hub returns a terminal delivery outcome.
`accepted` means the selected action confirmed acceptance; it does not promise
durable completion. Only confirmed acceptance is saved as confirmed progress.
In this example's default delivery mode, failed, unknown, and discarded outcomes
are not retried automatically, and
unfinished attempts receive no additional restart guarantee. Normal stop
discards queued and in-flight hub events and cannot retract accepted output.
There is no automatic plugin restart, replay, or stopped-event collection.

For the authoritative configuration and recovery contracts, see the
[GitHub trigger design](../../../docs/design/github-review-request.md) and
[operation guide](../../../docs/design/windows-operations.md).

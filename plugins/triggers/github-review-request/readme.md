# GitHub review request trigger

The GitHub review request trigger polls for open pull requests that request a
review directly from the authenticated user. Each event context is the pull
request URL. The plugin uses the public input protocol and the hub's
`runtime_state_v1` checkpoint facility.

## Prerequisites and build

Use Go 1.26.0 or later from the repository root. Build the hub, this trigger,
and the model-output action from the same revision:

```text
go build -o HUB_EXECUTABLE_PATH ./cmd/agent-pulse-hub
go build -o GITHUB_REVIEW_REQUEST_EXECUTABLE_PATH ./plugins/triggers/github-review-request
go build -o MODEL_OUTPUT_EXECUTABLE_PATH ./plugins/actions/model-output
```

Replace each output placeholder with an absolute path, including the executable
suffix reported by `go env GOEXE`. Use those exact paths in the daemon
configuration. The daemon launches and supervises the plugins; do not launch
the trigger executable directly.

## Authentication and host selection

The trigger selects the first nonempty credential in this order:

1. `GH_TOKEN`
2. `GITHUB_TOKEN`
3. Saved GitHub CLI authentication for the configured host

An environment token is used as-is and does not require `gh`. If that selected
token is rejected, the trigger reports an error and does not try a lower
priority source. When both variables are empty, the trigger runs
`gh auth token --hostname HOST` without a shell, with prompting disabled and
credential override variables removed from the child environment. It does not
start an interactive login.

The trigger verifies the selected credential with the host's authenticated
`/user` endpoint before accepting the watch. Use a user credential that can
read the intended repositories. Classic PATs or OAuth tokens need `repo` for
private repository search; that scope also grants write capabilities even
though this trigger only reads. Fine-grained PATs and GitHub App user access
tokens must be accepted by `/user` and issue search, and their repository
selection and organization approval still limit visibility. Installation
tokens that cannot resolve an authenticated user are unsupported.

`host` defaults to `github.com`. For GitHub Enterprise Server, set it to the
instance's DNS hostname, without a scheme or path. The supported GHES profile
is 3.19.x. API requests use HTTPS on port 443, normal system certificate
verification, `Accept: application/vnd.github+json`, and API version
`2022-11-28`.

When using environment tokens for different hosts, run one daemon per host with
that host's token, a distinct state directory, and a distinct loopback control
address. Never register a second host on a daemon carrying another host's
token. When both environment token variables are empty, one daemon can use
host-specific saved GitHub CLI logins for separate registrations. The plugin
does not accept tokens in watch arguments.

## Configure and run

[`examples/daemon.json`](examples/daemon.json) shows the complete daemon
configuration. Replace the executable placeholders with absolute paths and
save the configuration as valid JSON, escaping characters in path strings
where required. Keep the plugin names, roles, and empty `args` arrays as shown.

[`examples/watch.json`](examples/watch.json) shows the host and polling
interval. The interval defaults to `60s` and accepts any positive Go duration
string, including fractional durations. The trigger does not accept repository
or organization filters.

Start the hub in the foreground, wait for `daemon_ready` and its
`restored_registrations` count. In a separate terminal, prepare the selected
output action's destination arguments and register the watch and destination.
The [model-plugin operations guide](../../../docs/design/windows-operations.md#2-configure-both-plugins-and-select-destinations)
shows how to prepare destination arguments for `model-output`. Replace the
uppercase placeholders with the corresponding absolute executable paths, JSON
paths, and loopback address:

```text
HUB_EXECUTABLE daemon --config DAEMON_CONFIG_PATH --address LOOPBACK_ADDRESS
HUB_EXECUTABLE register --input-plugin github-review-request --watch-args-file WATCH_ARGS_PATH --output-plugin model --destination-args-file DESTINATION_ARGS_PATH --address LOOPBACK_ADDRESS
```

Registrations are stored beside the configuration in a directory whose name
is the absolute configuration path plus `.state`, unless you select another
dedicated directory with `daemon --state-dir ABSOLUTE_STATE_DIRECTORY`. Reuse
the same state directory for normal restart. Protect the daemon environment,
configuration, and state directory as you would other credential-bearing
operational data.

## Observation and coverage

The trigger searches with the direct-user qualifier
`user-review-requested:@me`. It excludes team-only requests, closed pull
requests, and items that do not identify an open pull request. Each complete
observation emits one URL for every newly observed request. Unchanged matches
are suppressed. A complete observation without a previously present request
allows a later reappearance to produce a new event. A cancellation and
re-request that both happen between observations may not be detected.

Search is eventually indexed, paginated results are not an atomic snapshot, and
the API does not expose a reliable signal that its repository coverage limit
has been exceeded. Search covers at most 4,000 GitHub-selected repositories;
coverage beyond that limit cannot be detected or guaranteed. The trigger
reports this limitation at watch startup. It also rejects incomplete pages,
changing counts, duplicates, and more than 1,000 results instead of treating
partial data as an empty set. A successful observation uses one search request
for 0–100 matches and up to ten for 901–1,000 matches. Multiple registrations
poll independently and multiply this request load. Increase the polling
interval if needed; GitHub's authenticated search limit is shared with other
clients using the same credential.

Each HTTP request is limited to ten seconds and 8 MiB; a complete observation
is limited to 120 seconds. Network and service failures use bounded exponential
backoff. Rate limits follow `Retry-After` or exhausted `X-RateLimit-Reset`
guidance. Authentication, permission, unsupported API, and incomplete search
failures retain existing progress and are reported in bounded diagnostics.
The polling interval is not an end-to-end delivery deadline.

## Delivery and recovery

The URL is the entire event context. Routing and action selection remain under
the hub's existing registration contract. The trigger marks an event attempted
before sending it. While the plugin session remains usable, the hub reports a
terminal outcome to the trigger for each event. `accepted` means that the
selected output plugin confirmed acceptance under its destination contract; it
does not guarantee durable completion.
Only confirmed `accepted` delivery is saved as confirmed; failed, unknown, and
discarded outcomes are not retried automatically.

The hub stores progress per registration in its runtime state. On normal
restart, confirmed requests remain suppressed, unfinished attempts receive no
additional delivery guarantee, and newly observed requests can emit after the
first complete search. A new registration starts with no prior progress. The
plugin does not own state files. An incompatible saved checkpoint prevents
that watch from starting. Inspect the diagnostic and follow the
[offline recovery procedure](../../../docs/design/windows-operations.md#trigger-runtime-state-recovery)
with the daemon stopped; do not treat the registration as first use.
Successful checkpoint persistence and normal shutdown are required for the
confirmed restart behavior. A checkpoint save failure does not stop live
monitoring, but continuity is not guaranteed.

For the complete build, registration, shutdown, and recovery procedure, see the
[model-plugin operations guide](../../../docs/design/windows-operations.md).

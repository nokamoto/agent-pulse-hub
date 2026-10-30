# agent-pulse-hub

<!-- Keep the following sentence identical to the repository description. Do not edit it. -->
Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Background and motivation

Agent Pulse Hub keeps event routing separate from service integrations. Register
an input watch and an output destination, then let the daemon forward events
without polling from a coding agent. Plugins handle their own sources and destinations.

Try the bundled local example below: publish a text file and see its contents
appended to a JSON-lines file. It needs no live service, credentials, or Codex
installation. The optional [Codex Queue action](plugins/actions/codex-queue/readme.md)
can instead send events to an existing Codex conversation.

## Requirements

- Git and Go 1.26.0 or later (see [go.mod](go.mod)).
- An existing writable local directory for the example's files.

The examples use two POSIX shell terminals and Python 3 to write JSON files.
These are example utilities, not product runtime dependencies or OS requirements.
Local control uses trusted loopback access; it does not isolate users of the same
machine. Run only plugins you trust.

## Installation

In the first terminal, obtain the source and build all three programs from the
same revision. Stop if a command fails.

```sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
mkdir ./pulse-demo
run="$(python3 -c 'from pathlib import Path; print((Path.cwd() / "pulse-demo").as_posix())')"
suffix="$(go env GOEXE)"
go build -o "$run/agent-pulse-hub$suffix" ./cmd/agent-pulse-hub
go build -o "$run/manual-plugin$suffix" ./plugins/triggers/manual
go build -o "$run/model-output-plugin$suffix" ./plugins/actions/model-output
```

## Usage

### Configure and start

Keep the first terminal open. Create the daemon configuration, input watch
arguments, and output destination arguments:

```sh
python3 - "$run" "$suffix" <<'PY'
import json
import sys
from pathlib import Path

run = Path(sys.argv[1])
suffix = sys.argv[2]
config = json.loads(Path("plugins/triggers/manual/examples/daemon.json").read_text(encoding="utf-8"))
executables = {"manual": "manual-plugin", "model": "model-output-plugin"}
for plugin in config["plugins"]:
    plugin["executable"] = str(run / (executables[plugin["name"]] + suffix))
watch = json.loads(Path("plugins/triggers/manual/examples/watch.json").read_text(encoding="utf-8"))
watch["trigger_file"] = str(run / "next.txt")
destination = json.loads(Path("plugins/actions/model-output/examples/destination.json").read_text(encoding="utf-8"))
destination["output_file"] = str(run / "events.jsonl")
(run / "daemon.json").write_text(json.dumps(config), encoding="utf-8")
(run / "watch.json").write_text(json.dumps(watch), encoding="utf-8")
watch["trigger_file"] = str(run / "other.txt")
(run / "watch-b.json").write_text(json.dumps(watch), encoding="utf-8")
(run / "destination.json").write_text(json.dumps(destination), encoding="utf-8")
PY
printf 'Run directory: %s\n' "$run"
"$run/agent-pulse-hub$suffix" daemon --config "$run/daemon.json" --address 127.0.0.1:47831
```

Note the printed run directory. In the first terminal's JSON diagnostics, wait
for `msg: "plugin_ready"` with `available: true` for both `manual` and `model`,
then `msg: "daemon_ready"` with `restored_registrations: 0` for this fresh setup.
If startup fails,
inspect the diagnostic before continuing. Use the same address for every command; if that
port is occupied, choose another loopback port consistently.

Successful registrations are saved in `daemon.json.state` beside this example's
configuration. By default the state directory is the absolute configuration
pathname with `.state` appended; `daemon --state-dir <absolute-directory>`
selects another dedicated local directory. Reuse the same configuration and
state on restart. Do not edit, move, copy, or share a live state directory.
Argument values are retained there, so protect it like configuration.

### Register and publish

Open the second terminal and enter the printed run directory when prompted.
The trigger `next.txt` must be absent. Use one publisher and one spelling of
each path, and avoid modifying the output file while the daemon is running.

```sh
printf 'Run directory printed in the first terminal: '
IFS= read -r run
suffix="$(go env GOEXE)"
"$run/agent-pulse-hub$suffix" register --input-plugin manual --watch-args-file "$run/watch.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47831 > "$run/registration-a.json"
cat "$run/registration-a.json"
subscription_a="$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); assert r["ok"] is True; print(r["subscription_id"])' "$run/registration-a.json")"
```

Stop if registration fails. Expect exit success and JSON containing `"ok":true`
and a `subscription_id`. Keep `registration-a.json` and the daemon address;
the unchanged returned ID is needed to cancel this subscription later.
Repeating this exact registration returns the same active ID. Registration alone
does not create the output file. Publish nonempty UTF-8 text of at most 8,192
bytes by closing a temporary file and renaming it onto the absent trigger:

```sh
printf '%s' 'PULSE-DEMO-001' > "$run/next.tmp"
mv -i "$run/next.tmp" "$run/next.txt"
```

In the first terminal, wait for `msg: "delivery_result"` with `outcome: "accepted"`
and your `subscription_id`. Then read the output in the second terminal:

```sh
cat "$run/events.jsonl"
```

Expect one JSON record containing `PULSE-DEMO-001`, the registered subscription
ID, input name `manual`, and `external_data: true`. The permission notice states
that external data grants no additional permissions. Event text cannot change
the registered destination. The input consumes the trigger file; after it is
gone, you can publish another distinct event using the same procedure.

### Cancel one subscription

Keep this first watch as A. Register a second watch B on the independent
`other.txt` path, using the same input process and output file:

```sh
"$run/agent-pulse-hub$suffix" register --input-plugin manual --watch-args-file "$run/watch-b.json" --output-plugin model --destination-args-file "$run/destination.json" --address 127.0.0.1:47831 > "$run/registration-b.json"
cat "$run/registration-b.json"
subscription_b="$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); assert r["ok"] is True; print(r["subscription_id"])' "$run/registration-b.json")"
```

Stop if registration fails; retain B's response too. With A's earlier output
confirmed and both trigger paths absent, explicitly cancel only A:

```sh
"$run/agent-pulse-hub$suffix" unsubscribe --subscription-id "$subscription_a" --address 127.0.0.1:47831
```

Require exit zero and `{"ok":true,"subscription_id":"<A's returned ID>"}`
with the exact same ID. A's watch, saved registration and trigger progress are
removed; shared plugins and B continue. An action request already issued for A
may still finish or produce output after cancellation. Success prevents new
attempts; it cannot recall earlier output.

Publish distinct text to the cancelled path and to B:

```sh
printf '%s' 'PULSE-A-AFTER-CANCEL' > "$run/next.tmp"
mv -i "$run/next.tmp" "$run/next.txt"
printf '%s' 'PULSE-B-CONTINUES' > "$run/other.tmp"
mv -i "$run/other.tmp" "$run/other.txt"
```

Wait for B's `delivery_result` with `outcome: "accepted"`, then read
`events.jsonl`. Expect `PULSE-B-CONTINUES` with B's saved ID and no
`PULSE-A-AFTER-CANCEL` record or new A `delivery_attempt`. The cancelled
`next.txt` stays unconsumed. Wait for B's trigger to disappear before continuing.
Do not re-register A to check removal: that would create a fresh ID and watch.

A valid unknown or already-removed ID fails with `unknown_subscription`; this
is not another success and does not prove who removed it. Missing or malformed
IDs fail validation. Keep the original ID rather than guessing from conversation
identity or argument files. For other failures:

| Result | What to do |
| --- | --- |
| `unsubscribe_unsupported` | Stop normally, upgrade the daemon and input together, then restart and explicitly use the same saved ID. |
| `unsubscribe_busy` | Wait for the executing request's result before considering an explicit same-ID retry. |
| `cancellation_rejected`, `plugin_unavailable`, `storage_error` | Report the failing stage and whether the removal request was durably saved, as stated in the diagnostic. New attempts can be stopped while the registration remains saved. Repair the cause before an explicit same-ID retry or normal restart; before the removal request is saved restart may restore the watch, afterwards startup completes removal before readiness. |
| Transport loss or a mismatched response ID | Leave the result uncertain and investigate. A later explicit same-ID request can succeed or return not-found; do not automatically retry or select another ID. |

Do not edit live state or discard cancellation intents to bypass recovery.
See the [cancellation recovery guide](docs/design/windows-operations.md#cancel-one-subscription)
for incomplete deletion, upgrades and rollback.

### Stop and recover

From the second terminal, request shutdown:

```sh
"$run/agent-pulse-hub$suffix" stop --address 127.0.0.1:47831
```

Alternatively, press Ctrl+C in the first terminal. Wait for daemon exit and
the configured-plugin exit diagnostics before restarting. Normal shutdown
retains uncancelled registrations and their IDs. In this example's default mode,
it discards queued and in-flight hub events and incomplete registrations; it
cannot retract accepted output. The optional notification mode below retains
evidence and eligible unsent work separately.

To resume this example, inspect leftover triggers while stopped, including files
the manual trigger renamed for processing (see claimed paths in diagnostics).
The unconsumed `next.txt` above belongs to cancelled A; remove that known demo
file while stopped. Restore trigger absence deliberately. Start the same daemon command
with the same configuration and state. Wait for `daemon_ready` with
`restored_registrations: 1`: only B remains. In the second terminal, publish to B
without registering again:

```sh
printf '%s' 'PULSE-B-AFTER-RESTART' > "$run/other.tmp"
mv -i "$run/other.tmp" "$run/other.txt"
```

Wait for B's accepted result, then read the output:

```sh
cat "$run/events.jsonl"
```

The new record must
carry B's saved ID; no A route or new A attempt is restored. The original
watch and destination argument files are not needed for restoration. Equivalent
explicit registration still returns that active ID.

Startup revalidates every saved route. An invalid state record, unavailable
referenced plugin, invalid destination, or already-present manual trigger
prevents the whole daemon from becoming ready. Stop all owners and preserve
state before deliberate repair; follow the [operation guide](docs/design/windows-operations.md#8-demonstrate-deliberate-saved-state-recovery)
for recovery, including a leftover state lock after abnormal termination.

This default mode has no automatic retry. Plugins never restart automatically.
An `unknown` delivery outcome
may already have appended a record; inspect the output before deliberately
publishing another event. There is no replay, stopped-event collection, or
forced-termination/power-loss recovery guarantee. Output acceptance does not
promise crash durability.
The output file contains the event text, so choose its contents and retention
accordingly. See the [operation guide](docs/design/windows-operations.md) for
detailed diagnostics and recovery.

### Retain notification evidence and enable safe retry

To inspect individual notifications, opt in before starting the daemon. Build
the hub and action from the same revision as above. For the local example,
set the `model` entry's `args` to `["--notification-delivery"]`; for Codex,
append `"--notification-delivery"` alongside its `--codex-executable` arguments.
The option takes no value and may appear once. Stop and wait for plugin exits
before changing configuration, and preserve the entire stopped state directory
before upgrading. Older hosts can reject this capability, and a retained
notification route cannot restart with the option removed.

Start with the same configuration/state, wait for readiness, and use an active
subscription's saved ID. Fresh setups still need registration first. Publish
one distinct marker using the closed-file rename procedure above, then inspect:

```sh
printf 'Active subscription ID saved at registration: '
IFS= read -r subscription
"$run/agent-pulse-hub$suffix" notifications --subscription-id "$subscription" --address 127.0.0.1:47831
printf 'Notification ID from the list: '
IFS= read -r notification
"$run/agent-pulse-hub$suffix" notification --notification-id "$notification" --address 127.0.0.1:47831
```

The list reports notification IDs, admission times, acceptance, retry state and
attempt counts. Select by ID/time; UUID order is not newest-first. If `next_after`
is non-null, pass it as `--after` for the next page; `--limit` accepts 1–20.
Restart the list to find newly admitted IDs that sort before a previous cursor.
Detail shows the original destination and individual attempt IDs, times,
outcomes and receipt. Both commands are read-only and send no notification.
Detail assembles bounded chunks internally; if the record keeps changing, it
fails visibly instead of showing mixed or partial JSON.

Read `acceptance` separately from `retry_state`. `queue_accepted` confirms
destination acceptance; for Codex this means queued for the registered thread,
not conversation receipt or work completion. Only when **all** earlier attempts
are proven `unsubmitted` and the latest failure is retryable can the hub retry:
at most three attempts under the same notification ID, with minimum two- and
ten-second waits, or longer destination guidance. `unknown` may already have
reached the destination and stops retry immediately, including after restart.
Permanent failures, rejection and exhaustion also stop. There is no manual
notification retry. Republishing the same text creates a separate event and
is not safe recovery of uncertain delivery.

Evidence is retained for seven days from admission. Expiry stops work; a later
not-found result does not prove it was never submitted. At 1,024 retained
notifications, admission fails with `notification_capacity_exceeded` until
normal expiry/cleanup frees space; unexpired evidence is not evicted.
For blocked work, stop normally, wait for daemon/plugin exits, preserve state,
repair the reported storage/plugin cause, and restart the same configuration
and state. A backward clock blocks issuance and expiry cleanup until it catches
up. Only retained eligible unsent work resumes with its remaining budget;
accepted and unknown work is not resent. Never delete safety files, restore an
older snapshot, or roll back binaries on a state tree containing notifications.
See the [action diagnosis guide](plugins/actions/codex-queue/readme.md#inspect-notifications-and-diagnose-stopping)
for status reasons and recovery limits.

### Send events to Codex

Use the [Codex Queue action guide](plugins/actions/codex-queue/readme.md) to build
and configure the optional action with the manual trigger. It requires a trusted
native Codex executable supporting `queue --thread --message` and access to the
target conversation's shared server. The guide covers deliberate installation
and discovery of the bundled registration skill. Invoke it in the top-level
conversation with the configuration values it requests. `CODEX_THREAD_ID` must
be a canonical, non-nil UUID; `CODEX_SESSION_ID` may be absent, but if present
must be the same canonical UUID. Never set or copy either value to repair an
unsupported environment. The skill never invents a target.

Retain each successful registration's returned `subscription_id` and daemon
address. To cancel one route, explicitly ask the skill to unsubscribe that ID;
it runs the direct hub command shown above without deriving a target from the
conversation or retrying automatically. Cancellation does not need either
Codex identity variable or the original argument files. Verify the exact-ID
success, continued unrelated delivery and normal restart as above.

Publish only after confirmed registration. An `accepted` result means the event
was queued for the registered conversation; observe its later processing
separately. Inspect the target after an `unknown` result before deliberately
submitting another event. A restart restores saved routes and revalidates the
destination; it does not replay Codex work.

### Watch a GitHub pull request

The [GitHub PR watch guide](plugins/triggers/github-pr-watch/readme.md) shows
how authors and reviewers can monitor one selected PR, receive its review and
discussion changes, and explicitly wait again on the same subscription.
Current-head CI failures, confirmed conflicts (including base-only updates) and
Draft-to-Ready transitions can independently resume a wait. The guide explains
signal permissions, UNKNOWN state and deliberate recovery of incompatible saved
discussion state when enabling the default signal profile.
It includes source builds, selected-host authentication, a local output example,
and the optional Codex destination. The first complete observation is silent.
Large backlogs arrive in bounded parts; assess each part and return its position
to request the next one. Normal restart also opens a notification wait from
saved delivery progress. Output acceptance alone does not prove agent assessment.

## Writing plugins

Implement an input or output role using [plugin protocol v2](docs/design/plugin-v1.md),
including its standalone verification procedure, and add the program to the
[daemon configuration](docs/design/mvp.md#configuration-and-local-control).
The [model plugin profiles](docs/design/mvp.md#bundled-model-plugin-profiles)
show the bundled behavior. The protocol document's filename is retained from
v1; v1 plugins, the old `codex_executable` configuration, and session-based
registration are incompatible. Replace matching programs and configuration
together, then register again.

For inspectable delivery and core-owned safe retry, implement the optional
[notification output profile](docs/design/plugin-v1.md#optional-notification-delivery)
under the [notification policy](docs/design/notification-delivery.md). Outputs
report service-specific submission evidence; the hub owns identity, storage
and retry. Inputs keep their source-progress responsibilities. Legacy `failed`
is not retry permission; do not add autonomous action retry or infer acceptance
from event text.

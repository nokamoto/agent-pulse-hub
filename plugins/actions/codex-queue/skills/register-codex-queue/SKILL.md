---
name: register-codex-queue
description: Register the current top-level Codex conversation with an Agent Pulse Hub route that uses the Codex Queue action, or explicitly unsubscribe a registration using its returned subscription ID. Use when asked to register this conversation or cancel one known subscription.
---

# Register or cancel a Codex Queue subscription

For registration, use this skill only for the conversation invoking it. The
registration helper reads the current process environment and refuses to run
unless `CODEX_THREAD_ID` and `CODEX_SESSION_ID` are both canonical, non-nil
UUIDs with the same value. Never supply, copy, set, or rewrite either ID.

Use it when the user asks to register this conversation, for example: “Register
this conversation with the configured Codex Queue route.”

Run the registration directly in this top-level conversation. Do not delegate
the helper to a subagent or another conversation. Do not discover a recipient
from history, a process list, a file, or event content. Do not run a queue probe
or retry an uncertain registration.

For an explicit cancellation request, follow **Cancel one subscription** below.
Cancellation uses the retained registration ID and does not require either
Codex environment ID or the original argument files.

## Collect configuration

Read the user's existing Agent Pulse Hub configuration and determine:

- The absolute path to the built Codex Queue action executable.
- The absolute path to the hub client executable.
- The hub's configured loopback address.
- The configured input plugin name.
- The absolute path to that input plugin's watch-arguments JSON file.
- The configured output plugin name. It must refer to the Codex Queue action.

Use only values established by the user's configuration or supplied by the
user. Ask for any missing value. If the configured route does not use the
Codex Queue action, explain what must be configured and stop. Do not edit the
daemon configuration, restart the hub, build executables, or invent paths as
part of registration.

Confirm that the hub is running and registration is ready. Its foreground JSON
logs can confirm this: find `plugin_ready` records with `available:true` for
both configured plugin names and their input/output roles, then find
`registration_available` with `available:true`. If the logs are unavailable,
ask the user or hub operator to confirm readiness before proceeding. If
readiness cannot be confirmed, stop. The hub owns endpoint, plugin-name, and
watch-argument validation.

## Register once

Invoke the action executable directly, using the absolute paths and names
collected above. Do not add a thread ID, destination file, parent ID, Codex
executable, or other arguments:

```text
ACTION_EXECUTABLE register-current --client-executable HUB_EXECUTABLE --address LOOPBACK_ADDRESS --input-plugin INPUT_PLUGIN_NAME --watch-args-file WATCH_ARGS_PATH --output-plugin CODEX_OUTPUT_PLUGIN_NAME
```

Replace each uppercase placeholder with its exact configured value. Do not use
a shell wrapper or combine executable paths and arguments into one string.
The helper creates and removes the temporary destination file itself, then
invokes the hub client once.

## Report the result

- Report registration as active only when the helper exits with code 0 and
  writes its successful JSON response to stdout with a nonempty
  `subscription_id`. Include that identifier in the result.
- For a valid client rejection, expect exit code 1 and a stderr line beginning
  with `register-current rejected: code=` and containing `message=` with the
  client's diagnostic. Report the rejection and that the route was not
  confirmed active. Do not claim the input watch was removed.
- A local validation or process-start failure exits with code 1 and includes a
  stderr line beginning with `register-current failed:`. Report the specific
  error and stop.
- An unknown outcome after the client started exits with code 1 and includes a
  stderr line beginning with `register-current outcome unknown:`. Say that
  registration may exist but could not be confirmed. The foreground hub's
  `subscription_active` logs may help the hub operator investigate, but they
  report plugin names and a subscription ID without identifying the helper
  invocation or its watch and destination arguments. A matching plugin pair may
  describe an earlier or different route, so it cannot confirm this
  registration. The hub has no route status or list command, and a missing log
  record does not prove that the route is inactive. Leave the outcome unknown
  and ask the hub operator to investigate. Do not retry or treat missing
  evidence as permission for another attempt.

If temporary-file cleanup fails, the helper writes a
`register-current warning:` line to stderr before the outcome line. Report the
warning without changing the registration result.

Registration establishes the hub route only. It does not submit a Codex queue
message or prove that a later event was processed. Never report a queued event
until a separate event delivery has a matching Codex acknowledgement; never
report conversation processing based only on that acknowledgement.

## Cancel one subscription

Use only the unchanged `subscription_id` returned by registration and explicitly
selected by the user for cancellation. Retain it when reporting registration.
Obtain the absolute hub executable path and configured loopback address from
the user's existing configuration. Ask for a missing ID or configuration value;
do not infer a target from conversation identity, history, arguments or event
content. Cancellation does not discover or list registrations.

Invoke the hub client directly once in this top-level conversation:

```text
HUB_EXECUTABLE unsubscribe --subscription-id REGISTERED_SUBSCRIPTION_ID --address LOOPBACK_ADDRESS
```

Replace each placeholder with its established value, passing the executable and
arguments separately. Do not invoke `register-current`, re-register, copy
environment IDs or automatically retry cancellation.

- Report success only when the client exits zero and returns
  `{"ok":true,"subscription_id":"REGISTERED_SUBSCRIPTION_ID"}` with the exact
  requested ID. The watch has stopped and normal restart cannot restore it.
  Other subscriptions continue. An action request already issued may still
  finish or produce output after success; cancellation cannot recall it.
- A nonzero client result with `ok:false` is a failure. Report its `code` and
  bounded `message`. `unknown_subscription` includes an already-removed ID;
  it alone cannot prove who removed the route. Do not choose another target.
- `unsubscribe_unsupported` requires upgrading the input and daemon together
  while stopped, then explicitly using the same saved ID. Do not perform that
  upgrade as part of this skill. `unsubscribe_busy` requires waiting for the
  executing request's result before an explicit retry.
- `cancellation_rejected`, `plugin_unavailable` or `storage_error` can stop new
  events and action requests while leaving the registration saved. Report the
  diagnostic's failing stage and whether the cancellation request was saved
  as a durable intent: before intent commit, restart may restore the watch;
  after commit, normal startup must finish removal before readiness. Ask the
  operator to correct the cause before an explicit same-ID retry or restart.
- If the client reports transport uncertainty or the response ID does not
  match, leave the result uncertain and ask the operator to investigate. A
  later explicit same-ID request may succeed or return not-found. Never treat
  uncertainty or not-found as permission to retry automatically.

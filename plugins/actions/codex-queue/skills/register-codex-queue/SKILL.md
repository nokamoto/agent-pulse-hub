---
name: register-codex-queue
description: Register the current top-level Codex conversation with an Agent Pulse Hub route that uses the Codex Queue action. Use when asked to set up or register this conversation for queued event delivery.
---

# Register the current conversation for Codex Queue

Use this skill only to register the conversation that is invoking it. The
registration helper reads the current process environment and refuses to run
unless `CODEX_THREAD_ID` and `CODEX_SESSION_ID` are both canonical, non-nil
UUIDs with the same value. Never supply, copy, set, or rewrite either ID.

Use it when the user asks to register this conversation, for example: “Register
this conversation with the configured Codex Queue route.”

Run the registration directly in this top-level conversation. Do not delegate
the helper to a subagent or another conversation. Do not discover a recipient
from history, a process list, a file, or event content. Do not run a queue probe
or retry an uncertain registration.

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

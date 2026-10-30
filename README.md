# agent-pulse-hub

<!-- Keep the following sentence identical to the repository description. Do not edit it. -->
Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Background and motivation

Let your coding agent get back to work when something happens: a review arrives,
CI fails, or a pull request becomes ready. Agent Pulse Hub watches external
sources and routes events to a destination you register.

~~~text
GitHub PR changes → Agent Pulse Hub → your Codex conversation
Local text file  → Agent Pulse Hub → a JSON-lines output file
~~~

Trigger plugins watch sources; action plugins deliver events. The daemon
connects them, so your coding agent does not need to poll those services itself.
You can use the bundled plugins or write your own.

## Requirements

- Git and Go 1.26.0 or later to build from source.
- Only plugins you trust: the daemon runs their executables locally.
- For Codex delivery, a native Codex executable supporting `queue --thread --message`
  and access to the target conversation's shared server.

The local walkthrough uses two POSIX shell terminals and Python 3 to generate
configuration. These are example utilities, not product runtime dependencies
or OS requirements. Local control trusts other users of the same machine.

## Installation

Get the source:

~~~sh
git clone https://github.com/nokamoto/agent-pulse-hub.git
cd agent-pulse-hub
~~~

Start with the [local file walkthrough](plugins/triggers/manual/walkthrough.md):
build the hub and two plugins, configure them, and route your first event.
It needs no Codex installation, credentials, or external service.
For a service integration, choose its guide below; each includes its own builds
from the same source revision.

## Usage

The local walkthrough demonstrates this small example:

~~~text
Publish a text file:      next.txt contains "PULSE-DEMO-001"
Trigger:                 manual consumes the file
Action:                  model-output appends a record to events.jsonl
Result (record excerpt): {"context":"PULSE-DEMO-001"}
~~~

Follow the [walkthrough](plugins/triggers/manual/walkthrough.md) for copyable
commands, expected output, and normal stop/restart. Its optional sections cover
cancelling one subscription and inspecting retained notification evidence.

| What you want to do | Guide |
| --- | --- |
| Send events to an existing Codex conversation | [Codex Queue action](plugins/actions/codex-queue/readme.md) |
| Watch review requests addressed directly to your GitHub user | [GitHub review request trigger](plugins/triggers/github-review-request/readme.md) |
| Watch one PR for reviews, discussion, CI failures, conflicts, and readiness | [GitHub PR watch trigger](plugins/triggers/github-pr-watch/readme.md) |

Each guide covers setup, registration, observation, and recovery for that
integration. Codex queue acceptance means queued for the conversation; observe
receipt and task completion separately. GitHub Enterprise Server 3.19.x
compatibility verification for review requests remains incomplete.

## Writing plugins

Implement a trigger (protocol `input`) or action (protocol `output`) using
[plugin protocol v2](docs/design/plugin-v1.md), including its standalone
verification procedure. Add the executable to the
[daemon configuration](docs/design/mvp.md#configuration-and-local-control).
The [bundled plugin profiles](docs/design/mvp.md#bundled-model-plugin-profiles)
show working examples. For inspectable delivery and hub-owned safe retry, see
the [optional notification output profile](docs/design/plugin-v1.md#optional-notification-delivery).

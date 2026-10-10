# agent-pulse-hub
Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

Agent Pulse Hub runs local plugins and sends their events to an existing Codex
conversation. The first release targets Windows and includes a manual file
trigger plugin.

## Start here

- [Windows build and operations guide](docs/operations/mvp.md)
- [Plugin protocol v1](docs/protocol/plugin-v1.md)
- [Register-pulse-session skill](.agents/skills/register-pulse-session/SKILL.md)
- [MVP requirements](docs/requirements/mvp.md)
- [MVP design](docs/design/mvp.md)
- [AI-driven development](docs/aidd/README.md)

The daemon keeps subscriptions in memory. Restarting it clears watches, and
delivery attempts are not retried.

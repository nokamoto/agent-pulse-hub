# agent-pulse-hub
Bring real-time external event triggers to your AI coding agents via lightweight daemon plugins.

## Development

Run the Go formatter and tidy module dependencies:

```sh
go run build/mage.go -d build -w . format
```

Run the Go tests and repository guardrails:

```sh
go test ./...
go run build/mage.go -d build -w . guardrails
```

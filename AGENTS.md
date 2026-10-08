# Agent Instructions

Implementation language: Go.

Write all documentation in English.

Refer to the following directories:

- [Requirements](docs/requirements/)
- [Design](docs/design/)
- [AI-Driven Development rules](docs/aidd/)

## Guardrails

Run all repository guardrails locally with Go:

```sh
go run build/mage.go -d build -w . guardrails
```

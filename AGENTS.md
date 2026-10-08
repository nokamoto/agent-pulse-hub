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

## Go development checks

Run the Mage targets from the repository root. Format Go code and tidy modules before committing:

```sh
go run build/mage.go -d build -w . format
```

Run tests with:

```sh
go run build/mage.go -d build -w . test
```

Run `vet` and `staticcheck` together with the lint check:

```sh
go run build/mage.go -d build -w . lint
```

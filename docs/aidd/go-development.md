---
type: Playbook
title: Go Development Conventions
description: Repository-specific Go package placement, testing, error handling, and checks.
---

# Go Development Conventions

## Scope

These conventions apply to contributors and agents changing Go code in this
repository. Use one module, `github.com/nokamoto/agent-pulse-hub`, declared in
the root `go.mod`. Do not introduce nested modules.

## Package placement

The following table defines the allowed locations for Go source files.
Locations are relative to the repository root. Create directories when they
are needed; empty placeholder packages are not required.

| Location | Responsibility and allowed depth |
| --- | --- |
| `cmd/<command>/` | Executable entry points: startup, dependency wiring, and process exit decisions. Go files belong directly in the command directory; place reusable implementation in `internal/`. |
| `internal/domain/` | Product concepts and pure rules. Nested packages are allowed. |
| `internal/application/` | Use-case coordination and interfaces for required external capabilities. Nested packages are allowed. |
| `internal/adapters/` | External I/O, protocols, and platform-specific implementations. Nested packages are allowed. |
| `tools/guardrails/<validator>/` | Repository validators. Nested packages are allowed. |
| `build/` | Mage build and validation tooling. Go files belong directly in this directory. |
| `tools.go` at the root | Build-tagged tool dependency declarations. No other root Go source files are allowed. |

Command and validator names, and responsibility-specific packages within each
defined internal layer, may be added without changing this policy. Only
`domain`, `application`, and `adapters` are allowed directly under `internal`
for Go code. New layers or placement categories, such as `internal/common`
or `pkg/`, require a policy update and a corresponding guardrail change.

## Architecture and dependency direction

Product code uses three layers with ports and adapters: application code
declares the external capabilities it needs as Go interfaces, and adapters
provide concrete implementations. Dependencies point toward the product's
rules; external protocols and platform details stay in adapters.

| Layer | Responsibilities and examples from the [MVP design](../design/mvp.md) |
| --- | --- |
| `domain` | Subscription, event, and delivery-result concepts and pure rules such as subscription equivalence. No wire-format DTOs, process operations, or Windows or Codex integrations. |
| `application` | Coordinate registration, duplicate-request handling, event acceptance, registry and queue operations, and delivery. Define interfaces for external capabilities at the consuming boundary. |
| `adapters` | Read configuration and implement named-pipe transport, plugin process communication and JSON frames, Codex CLI integration, and manual-plugin file I/O. Translate external representations into application or domain inputs and results. |

The following rules apply to imports between product layers, including their
nested packages. Imports within the same layer must remain acyclic, as Go
requires.

| Importing location | Allowed product imports |
| --- | --- |
| `internal/domain/...` | Other domain packages only |
| `internal/application/...` | Application and domain packages |
| `internal/adapters/...` | Adapter, application, and domain packages |
| `cmd/<command>/` | All three internal layers to assemble the executable |

Internal product packages must not import `cmd`, `build`, or
`tools/guardrails`. Standard-library and third-party imports must respect the
same responsibilities: application code must not bypass its interfaces to
perform external I/O, and domain code must remain independent of external
systems. Interfaces are required where an application use case needs an
external capability, not for every function or type.

`cmd/<command>` is the composition root: it constructs concrete adapters and
passes them to application code. Keep product rules and use-case coordination
in the internal layers. Subdivide a layer by responsibility when needed; do
not create empty packages or duplicate types solely to populate all layers.
Specific package names within each layer are implementation choices consistent
with the relevant design.

## Layout enforcement

The `go-layout` guardrail scans the filesystem, including test files and files
excluded by the current operating system or build tags. It rejects Go files
outside the allowed locations and nested `go.mod` files. It skips `.git` and
directories named `testdata`; use `testdata` only for fixtures, not application
or tooling implementation. Symlinks outside those skipped directories are
rejected rather than followed. Vendored Go source is not an allowed category.
The guardrail also rejects Go files under an undefined `internal` layer.
Package declarations and import dependency direction are outside this check's
scope; reviewers must check the dependency rules above when reviewing Go code.

## Tests

Prefer table-driven tests with named subtests when several inputs and expected
results exercise the same behavior. Include relevant success, failure, and
boundary cases. Use scenario tests when a sequence of state transitions is
clearer than a table. Parallel execution is optional and requires isolation of
shared state and resources.

Keep unit tests runnable without a real Codex installation or external
services. For tests requiring real processes or Windows-specific facilities,
document their prerequisites and execution commands. Wait for observable
conditions with a deadline instead of relying on fixed sleeps in asynchronous
tests. Passing tests on another operating system does not establish Windows
acceptance; follow the platform verification in the relevant design.

## Errors and process exit

Packages below an executable entry point return errors to their callers rather
than terminating the process with `os.Exit` or fatal logging. Add context when
it helps the caller identify the failed operation, and preserve the underlying
error when callers need to inspect it. The executable entry point decides how
to report a failure and which exit status to use.

## Required checks

Run these commands from the repository root before committing Go changes.
Formatting and module tidying are defined by the existing Mage target rather
than duplicated in this document.

```sh
go run build/mage.go -d build -w . format
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . lint
go run build/mage.go -d build -w . guardrails
```

Resolve failures before presenting a change as verified. If an environment
prevents a check, report the unexecuted check and its limitation in the PR.
Guardrails run through the same Mage target locally and in CI.

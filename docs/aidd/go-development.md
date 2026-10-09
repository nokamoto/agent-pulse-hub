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
| `internal/<responsibility>/` | Product implementation grouped by responsibility. Nested packages are allowed. |
| `tools/guardrails/<validator>/` | Repository validators. Nested packages are allowed. |
| `build/` | Mage build and validation tooling. Go files belong directly in this directory. |
| `tools.go` at the root | Build-tagged tool dependency declarations. No other root Go source files are allowed. |

Command, responsibility, and validator names may be added within these
locations without changing this policy. New placement categories, such as
`pkg/`, require a policy update and a corresponding guardrail change. Concrete
product package boundaries and import dependencies belong in the relevant
design; this table does not mandate an architectural layering scheme.

The `go-layout` guardrail scans the filesystem, including test files and files
excluded by the current operating system or build tags. It rejects Go files
outside the allowed locations and nested `go.mod` files. It skips `.git` and
directories named `testdata`; use `testdata` only for fixtures, not application
or tooling implementation. Symlinks outside those skipped directories are
rejected rather than followed. Vendored Go source is not an allowed category.
Package declarations and import dependency direction are outside this check's
scope.

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

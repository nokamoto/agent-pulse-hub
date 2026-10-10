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

## Interface ownership

Define an interface in the package that consumes it, with only the methods
that consumer needs. A provider implements the interface implicitly; it must
not dictate a shared interface merely to expose all of its methods. For
example, an application package that delivers events owns its delivery
interface, and a Codex adapter supplies the implementation.

Do not introduce interfaces solely to mirror concrete types or to make every
type mockable. Use them at the dependency boundaries required by the consuming
code, consistent with the layer rules above.

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

### Test levels and cost

For product behavior, separate unit tests from integration tests by what they
execute, not by measured duration alone. Use the lowest-cost level that can
establish the behavior. A slow unit test does not become an integration test,
and a fast test using real network or process boundaries is still integration.

| Level | Purpose and placement | Dependencies and cost |
| --- | --- | --- |
| Unit | Standard Go `testing` cases beside the code in `internal/domain`, `internal/application`, or `internal/adapters`. Isolated command logic may also have unit tests in `cmd/<command>`. | Test one responsibility in memory. Mock external capabilities such as network, persistent storage, subprocess execution, and time-dependent waiting. No real network connections, subprocesses, or live services. Keep setup small and deterministic. |
| Integration | Ginkgo scenarios directly in `cmd/<command>/*_integration_test.go`, at the command that assembles the behavior under test. Exercise the assembled application, domain logic, and relevant adapters together through the command's public behavior. | Real local processes, filesystem, and OS transport are allowed where the scenario requires them. Replace only external service endpoints such as Codex with controlled local substitutes. Account for build, startup, cleanup, and resource costs. |
| Delivery quality | Checks specified by the approved design and performed during delivery. | Real Codex or other live services, credentials, and environment-dependent behavior. These checks do not gate implementation completion. |

Application unit tests use consumer-owned interfaces and
[GoMock](#interface-mocks) for external capabilities; they must not start a
server or subprocess to exercise an application decision. Test pure domain
rules and adapter parsing or mapping directly without mocks when no external
capability is involved. Do not add interfaces merely to mock pure functions.
Tests of actual product I/O or assembled component interactions belong to the
command-level integration suite, even when they use only local resources.
Repository tooling tests retain their existing standard Go test placement.

Cover input combinations, decisions, and error branches mainly with unit tests.
Use integration cases for wiring, protocol interactions, process lifecycle,
and user-visible paths that unit tests cannot establish. Do not repeat every
unit-test permutation through a process. Integration must retain the real
product components involved in the claimed behavior; mocking the application
itself would not establish their integration. Keep current OS/transport checks
minimal without dropping required behavior.

Before proposing integration scenarios, the design author applies the
[case selection and Pending rules](README.md#design-verification-deliverables):
compare defect-detection value with implementation, maintenance, and execution
cost, and choose the least costly sufficient coverage. For each selected
integration scenario, design records why unit tests are insufficient, its
concrete implementation method, which product components are real, which
external boundaries are substituted, and its setup, runtime, resource, and
cleanup budget. Bound waits and clean up
processes and temporary state. Use separate unit and integration commands and
CI results so expensive checks are visible. Required integration cases still
gate implementation; cost is not a reason to skip them or move them to delivery.

### Unit test style

Prefer table-driven tests with named subtests when several inputs and expected
results exercise the same behavior. Include relevant success, failure, and
boundary cases. Use scenario tests when a sequence of state transitions is
clearer than a table. Parallel execution is optional and requires isolation of
shared state and resources.

### Integration execution

Keep implementation tests runnable in CI without a real Codex installation,
credentials, or live external services. Real local processes and OS-specific
facilities may be used with documented CI prerequisites and commands. Keep
platform-specific infrastructure proportional to the behavior being verified.
Wait for observable
conditions with a deadline instead of relying on fixed sleeps in asynchronous
tests. Passing tests on another operating system does not establish Windows
behavior; follow the implementation and delivery allocation in the approved design.

### Design acceptance tests

Use Ginkgo for command-level integration acceptance scenarios under the
[design verification rule](README.md#design-verification-deliverables).
Design authors these scenarios; implementation adds the unit tests needed for
local behavior. Put all Ginkgo scenario and suite files directly in
`cmd/<command>/` as `*_integration_test.go`, with `//go:build integration`.
Place data fixtures in that command's `testdata/`. Do not put Ginkgo acceptance
suites in `internal/application` or `internal/adapters`, introduce a new
top-level test layout, or hide product implementation in fixtures.

The build tag keeps integration suites out of the default unit-test run.
The integration command must enable the tag and select every suite required
by the approved scope. Design-time compilation and case registration must
also enable it; a default run that excludes all integration files is not
evidence that the cases compile or exist. Keep unit and integration evidence
separate while requiring both for implementation completion.

Pin Ginkgo and any Gomega dependency in `go.mod`; pin a used Ginkgo CLI to the
same module version rather than relying on a global installation. Prepare
missing dependencies and shared runner support separately before a design PR
uses them. A design PR must compile and register its cases with the available
dependencies even while product behavior is Pending. Static Pending does not
exclude undefined Go symbols from compilation.

Give each case a stable ID and concrete inputs, actions, and observable expected
results, either in the case or a directly associated fixture. A descriptive
title with no specified checks is insufficient. Use static `Pending` only when
necessity, a concrete implementation method, and CI feasibility are resolved
under the [design verification rule](README.md#design-verification-deliverables);
test code completion and product wiring may follow during implementation.
Do not substitute runtime `Skip()`. Exercise the command's public
contracts rather than internal application entry points in these scenarios.
Fakes must drive or observe actual product behavior when the case is enabled.

The implementation completion check must compare approved case IDs with a
machine-readable execution report and require all cases in scope to pass.
Neither the standard test exit code nor `--fail-on-pending` alone detects every
missing or skipped case: runtime Skip and filter exclusions require explicit
report checking. Scope suites and reports so future-design Pending cases do
not disable the current completion gate. Record commands, environment, and
case results in the implementation PR. Real-service final quality checks use
the separately documented delivery procedure; do not skip them inside the
required service-free CI suite or claim them as passed.

### Acceptance runner and case inventory

The Mage `test` target runs default Go tests and the build tooling's in-memory
unit tests. It excludes the `integration` tag. To run an approved integration
scope, use the separate target from the repository root:

```sh
go run build/mage.go -d build -w . integration cmd/<command>/testdata/cases.json <new-report-directory>
```

The governing design owns the inventory path and its suite packages and case
IDs. Include the inventory with the design's verification fixtures and review
changes to its scope against the approved design. Each suite package is an
explicit repository-relative directory; product suites remain directly in
`cmd/<command>/`. The inventory is one JSON object with a nonempty `suites`
array. Each suite has a unique `package` and nonempty `cases` array. Case IDs
are unique across the inventory and use letters, digits, dots, underscores,
and hyphens, starting with a letter or digit. For example:

```json
{
  "suites": [
    {"package": "./cmd/example", "cases": ["EXAMPLE-V01", "EXAMPLE-V02"]}
  ]
}
```

This example defines the format, not a product acceptance scope. Give each
Ginkgo `It` exactly one matching `Label("case:<ID>")`. Name the suite's Go
test entry point `TestIntegration...`; the runner uses `-test.run=^TestIntegration`
so ordinary Go unit tests in the same package are not executed again.

The runner uses the module-pinned Ginkgo CLI, enables `integration`, selects
the inventory's case labels, and bounds each suite's execution to two minutes.
Design must demonstrate that its setup and cases fit this budget or obtain a
reviewed tooling budget change before relying on a longer run. The report
directory must be new. The runner rejects an existing report directory to
prevent stale success reports from satisfying a failed invocation. Keep reports outside the
checkout when checking for a clean Git tree.

After execution, the runner reads `report.json` and requires exactly one suite
report per inventory package and exactly one passed `It` per required ID.
It rejects failed invocations, missing or malformed reports, missing cases,
duplicate cases, Pending, runtime Skip, filter exclusions, failed specs,
programmatic focus, and dry runs. Missing or empty inventories fail. Future
cases outside this inventory do not satisfy or disable the current gate.
Retain the JSON report and console logs as separate integration evidence.
CI that gates a product scope must invoke this target with that scope's
approved inventory and upload reports on success and failure.

The separate `integrationTooling` target qualifies the runner using real
Ginkgo reports for passed, Pending, skipped, missing, and failed fixture cases.
These fixtures contain no product behavior. CI runs this qualification on
Linux and Windows and retains its reports independently of the default test
job. Its success establishes tooling behavior only. It does not establish
MVP acceptance, Windows product behavior, or delivery quality. An approved
product inventory and a CI invocation of `integration` are still required for
implementation completion.

### Interface mocks

Use [Uber GoMock](https://github.com/uber-go/mock) for interface mocks in unit
tests. Generate mocks with `go.uber.org/mock/mockgen` and use
`go.uber.org/mock/gomock` for expectations. Tests that do not need an interface
mock can test the real code directly; table-driven tests do not require mocks.

Keep the generator pinned through `tools.go` and `go.mod`, so generation and
the runtime use the same module version. Do not depend on a globally installed
`mockgen` or an unpinned `@latest` command.

Keep generated mocks in the consuming package as `*_mock_test.go` files, using
that package's name. Commit them alongside the tests. Place a `go:generate`
directive next to the consumer-owned interface and regenerate whenever the
interface changes. For example, an interface in
`internal/application/delivery/ports.go` with `package delivery` uses:

```go
//go:generate go run go.uber.org/mock/mockgen -source=ports.go -destination=ports_mock_test.go -package=delivery
```

Run the Mage `generate` target from the repository root, then the remaining
required checks below. It runs `go generate ./...` with the module-pinned tools.
Do not edit generated mocks by hand. Each test or table-driven subtest
that uses mocks creates its own `gomock.NewController(t)`; controller cleanup
and expectation checks are registered with `testing.T` automatically.

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
go run build/mage.go -d build -w . generate
go run build/mage.go -d build -w . format
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . lint
go run build/mage.go -d build -w . guardrails
```

Resolve failures before presenting a change as verified. If an environment
prevents a check, report the unexecuted check and its limitation in the PR.
Guardrails run through the same Mage target locally and in CI.

Go CI runs `generate` before `format`, tests, and lint. After generation and
formatting, it requires a clean Git working tree, including no untracked files.
This detects stale or missing committed mocks as well as formatting and module
changes. Before pushing, review and commit the outputs of the commands above;
CI must be able to reproduce them without changing the checkout.

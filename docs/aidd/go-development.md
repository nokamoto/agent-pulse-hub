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

Apply the [OS-neutral development policy](README.md#os-neutral-development-and-documentation)
to product code, tests, and build tooling. Use `path/filepath` for filesystem
paths and explicit argument lists with `os/exec` for processes. Obtain an
executable suffix from the build environment when needed, rather than assuming
`.exe`; avoid requiring a particular shell for Go/Mage checks.

## Package placement

The following table defines the allowed locations for Go source files.
Locations are relative to the repository root. Create directories when they
are needed; empty placeholder packages are not required.

| Location | Responsibility and allowed depth |
| --- | --- |
| `cmd/<command>/` | Executable entry points: startup, dependency wiring, and process exit decisions. Go files belong directly in the command directory; place reusable implementation in `internal/`. |
| `plugins/triggers/<name>/` or `plugins/actions/<name>/` | Bundled plugin executable entry points and their command-level tests, directly in the plugin directory. Plugin-specific reusable implementation belongs in that plugin's `internal/{domain,application,adapters}/` layers; nested packages within those layers are allowed. |
| `internal/domain/` | Product concepts and pure rules. Nested packages are allowed. |
| `internal/application/` | Use-case coordination and interfaces for required external capabilities. Nested packages are allowed. |
| `internal/adapters/` | External I/O, protocols, and platform-specific implementations. Nested packages are allowed. |
| `tools/guardrails/<validator>/` | Repository validators. Nested packages are allowed. |
| `build/` | Mage build and validation tooling. Go files belong directly in this directory. |
| `tools.go` at the root | Build-tagged tool dependency declarations. No other root Go source files are allowed. |

Command and validator names, and responsibility-specific packages within each
defined internal layer, and bundled plugin names within either role, may be
added without changing this policy. Only
`domain`, `application`, and `adapters` are allowed directly under `internal`
for Go code, including within a plugin. New layers or placement categories,
such as `internal/common`
or `pkg/`, require a policy update and a corresponding guardrail change.

## Architecture and dependency direction

Product code uses three layers with ports and adapters: application code
declares the external capabilities it needs as Go interfaces, and adapters
provide concrete implementations. Dependencies point toward the product's
rules; external protocols and platform details stay in adapters.

| Layer | Responsibilities and examples from the [MVP design](../design/mvp.md) |
| --- | --- |
| `domain` | Subscription, event, and delivery-result concepts and pure rules such as subscription equivalence. No wire-format DTOs, process operations, or OS or Codex integrations. |
| `application` | Coordinate registration, duplicate-request handling, event acceptance, registry and queue operations, and delivery. Define interfaces for external capabilities at the consuming boundary. |
| `adapters` | Read configuration and implement local-control transport, plugin process communication, Codex CLI integration, and manual-plugin file I/O. Translate external representations into application or domain inputs and results. |

The following rules apply to imports between product layers, including their
nested packages. Imports within the same layer must remain acyclic, as Go
requires.

| Importing location | Allowed product imports |
| --- | --- |
| `internal/domain/...` | Other domain packages only |
| `internal/application/...` | Application and domain packages |
| `internal/adapters/...` | Adapter, application, and domain packages |
| `cmd/<command>/` or a bundled plugin root | All three internal layers to assemble the executable |

Internal product packages must not import `cmd`, `build`, or
`tools/guardrails`, or executable plugin roots. Apply the same direction rules
within each plugin's internal layers. Shared framework code may remain in the
root internal layers; hub internals must not depend on plugin-specific internals.
Standard-library and third-party imports must respect the
same responsibilities: application code must not bypass its interfaces to
perform external I/O, and domain code must remain independent of external
systems. Interfaces are required where an application use case needs an
external capability, not for every function or type.

`cmd/<command>` and each bundled plugin root are composition roots: each
constructs concrete adapters and
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

## Bundled plugin artifacts

The [bundled-plugin requirements](../requirements/bundled-plugins.md#functional-requirements)
define the required implementation and usage materials. Every directory directly
under `plugins/triggers/` or `plugins/actions/` is a bundled plugin and must have:

- At least one nonempty, regular, non-test `*.go` file directly in the plugin
  directory. Files starting with `.` or `_` do not count as implementation.
- A nonempty, regular file named exactly `readme.md` directly in that directory.
- At least one nonempty, regular `*.json` file directly in `examples/` within
  that directory. The plugin design chooses filenames and contents; examples
  together must show daemon configuration and watch or destination arguments.

The plugin design chooses source filenames and subdivisions within the allowed
layout. Shared framework code may stay in the root `internal/` layers. Keep
fixtures in `testdata/`; fixtures, test-only Go files, and nested source alone
cannot satisfy the executable implementation artifact. Symlinks and directories
cannot substitute for required files or the `examples/` directory.

The `plugins` guardrail discovers every plugin directory, including empty ones,
and reports the plugin path and each missing artifact. Absent `plugins/` or role
directories are allowed until plugins are introduced; files or symlinks in place
of these directories or plugin entries fail. The model commands
`cmd/manual-plugin/` and `cmd/model-output-plugin/` remain valid until their separately approved migration. Do not add production stubs to a
design PR merely to satisfy presence checks: design-only suites for a plugin not
yet introduced use `cmd/<verification-command>/` and its `testdata/`. Design may
add suites directly to an existing plugin directory and govern their relocation
and inventory changes for implementation.

Run the check through the standard Mage `guardrails` target. Its qualification
fixtures are materialized by `tools/guardrails/plugins/main_test.go` in temporary
directories: complete trigger and action plugins pass; independent removal of
implementation, lowercase README, or examples fails with the path and missing
artifact. Tests also cover empty plugins, test-only source, empty artifacts,
incorrect placement, and symlink substitution. The check establishes presence
only: reviewers must assess README coverage, example correctness, compilability,
and runtime behavior separately under the governing requirements and design.

## Tests

### Test levels and cost

For product behavior, separate unit tests from integration tests by what they
execute, not by measured duration alone. Use the lowest-cost level that can
establish the behavior. A slow unit test does not become an integration test,
and a fast test using real network or process boundaries is still integration.

| Level | Purpose and placement | Dependencies and cost |
| --- | --- | --- |
| Unit | Standard Go `testing` cases beside the code in `internal/domain`, `internal/application`, or `internal/adapters`. Isolated command logic may also have unit tests in `cmd/<command>` or a bundled plugin root. | Test one responsibility in memory. Mock external capabilities such as network, persistent storage, subprocess execution, and time-dependent waiting. No real network connections, subprocesses, or live services. Keep setup small and deterministic. |
| Integration | Ginkgo scenarios directly in `cmd/<command>/*_integration_test.go` or a bundled plugin root as `*_integration_test.go`, at the command that assembles the behavior under test. Exercise the assembled application, domain logic, and relevant adapters together through the command's public behavior. | Real local processes, filesystem, and OS transport are allowed where the scenario requires them. Replace only external service endpoints such as Codex with controlled local substitutes. Account for build, startup, cleanup, and resource costs. |
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
tests. Verify any approved OS-specific behavior on the applicable OS; follow
the implementation and delivery allocation in the approved design.

### Design acceptance tests

Use Ginkgo for command-level integration acceptance scenarios under the
[design verification rule](README.md#design-verification-deliverables).
Design authors these scenarios; implementation adds the unit tests needed for
local behavior. Put all Ginkgo scenario and suite files directly in
`cmd/<command>/` or a bundled plugin root as `*_integration_test.go`, with `//go:build integration`.
Place data fixtures in that command or plugin's `testdata/`. Do not put Ginkgo acceptance
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
missing dependencies and shared runner support in separately approved and merged
prerequisite PRs before a design PR uses them. Same-version direct/indirect
module reclassification for fixture imports and reuse of approved CI environment
pins may accompany the design cases only under the
[minimal fixture preparation boundary](README.md#design-verification-deliverables).
A design PR must compile and register its cases with the available
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

The implementation PR's completion check must compare approved case IDs with a
machine-readable execution report and require all cases in scope to pass.
Neither the standard test exit code nor `--fail-on-pending` alone detects every
missing or skipped case: runtime Skip and filter exclusions require explicit
report checking. The regular CI integration run permits static Pending;
record the separate scoped completion check's commands, environment, and
case results in the implementation PR. Future-design Pending outside that
scope does not prevent this check from passing. Real-service final quality checks use
the separately documented delivery procedure; do not skip them inside the
required service-free CI suite or claim them as passed.

### Acceptance runner and case inventory

The Mage `test` target runs default Go tests and the build tooling's in-memory
unit tests. It excludes the `integration` tag. For normal local verification,
run both unit and integration checks from the repository root:

```sh
go run build/mage.go -d build -w . check
```

The `check` target runs `test` and `integrationAll`, keeping their output and
results separate. Run just the integration checks with:

```sh
go run build/mage.go -d build -w . integrationAll
```

The `integrationAll` target lists packages in the root module with the
`integration` tag, then runs uncached Go tests in every package under `cmd/`
and directly in `plugins/triggers/<name>/` and `plugins/actions/<name>/` with
the `integration` tag and `-run=^TestIntegration`. New command and plugin suites
and cases are included without an inventory selection or workflow change.
Static Pending is allowed; failing tests and compilation failures still fail
the run. Each package has a five-minute execution timeout for the combined
run; the scoped acceptance runner's two-minute suite budget below is unchanged.
CI runs this target on Ubuntu, separately from unit tests. All required Go,
product integration, and tooling checks use Ubuntu; Windows is not a required
CI runner. Runner selection is verification infrastructure, not a product OS
constraint or an OS support guarantee. Keep development OS neutral under the
[OS-neutral policy](README.md#os-neutral-development-and-documentation).

Reports go to a new directory outside the checkout by default. Set
`APH_INTEGRATION_REPORT_DIR` to choose a new report directory. Retain
`test-results.json` (Go test JSON events) and `console.log` on success and
failure. These general reports include Pending and do not themselves establish
that an implementation scope is complete.

For the implementation PR's separate completion check, run its approved scope:

```sh
go run build/mage.go -d build -w . integration cmd/<command>/testdata/cases.json <new-report-directory>
```

The governing design owns the inventory path and its suite packages and case
IDs. Include the inventory with the design's verification fixtures and review
changes to its scope against the approved design. Each suite package is an
explicit repository-relative directory; product suites remain directly in
`cmd/<command>/` or a bundled plugin root. Plugin inventories may live in the
plugin's `testdata/`, using packages such as `./plugins/triggers/manual` or
`./plugins/actions/model-output`. Mixed command/plugin inventories are allowed.
The inventory is one JSON object with a nonempty `suites`
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
Record this scoped target's results in the implementation PR and compare
them with the approved inventory. Regular CI uses `integrationAll`; it does
not need an inventory-specific invocation or a new job for each scope.

The separate `integrationTooling` target qualifies the runner using real
Ginkgo reports for passed, Pending, skipped, missing, and failed fixture cases.
These fixtures contain no product behavior. The fixture in
`build/testdata/plugin-layout/` is copied into a temporary root module with
command, trigger, and action suites to qualify automatic discovery and mixed
acceptance inventories, including failing and missing plugin suites. CI runs this qualification on
Ubuntu and retains its reports independently of the default test
job. Its success establishes tooling behavior only. It does not establish
MVP acceptance, OS-specific product behavior, or delivery quality. An approved
product inventory, passing product cases in CI, and the implementation PR's
scoped report comparison are still required for implementation completion.

### Plugin tooling qualification

The `pluginTooling` target checks the dependencies and local process/RPC
capabilities needed to design go-plugin integration cases. It launches small Go
and Python fixtures through `hashicorp/go-plugin`, exercises the standard gRPC
health service, and checks lifecycle behavior. These are tooling checks, not
product acceptance cases: they define no application protocol and do not verify
registration, event routing, Codex delivery, or an implemented MVP.

Go dependencies are pinned in the root `go.mod`. The Python fixture uses Python
3.12 and the exact package versions in
`build/testdata/plugin-tooling/requirements.txt`. It uses standard health and
empty protobuf messages; no protobuf compiler or generated product bindings
are required. Keep Python packages out of the repository and out of a shared
Python installation by preparing a virtual environment outside the checkout:

First run `python --version` and confirm it is Python 3.12; otherwise use the
path to a Python 3.12 executable in place of `python` in the setup command.

```sh
python -m venv <directory-outside-checkout>
<venv-python> -m pip install -r build/testdata/plugin-tooling/requirements.txt
```

Replace `<venv-python>` with the absolute path to that environment's Python
executable. Set `APH_PLUGIN_PYTHON` to that path in the shell that runs the target;
the value is a single executable path, not a shell command or argument list.
If unset, the target uses `python` from `PATH`. Missing Python or packages fail
qualification rather than skip it. Run from the repository root:

```sh
go run build/mage.go -d build -w . pluginTooling
```

The target retains `test-results.json` and `console.log` in a fresh directory
outside the checkout, printed at startup. `APH_PLUGIN_TOOLING_REPORT_DIR` may
specify that directory explicitly; an existing directory is rejected. Retain
these reports separately from unit tests, product integration results, and
acceptance inventories. A failure requires inspecting the fixture output and
dependency setup, then rerunning with a new report directory.

The existing integration-tooling CI job installs these pinned Python packages
and runs `pluginTooling` separately from `integrationTooling`. Both results feed
the existing Go CI gate; no additional product support matrix is implied.
Local `check` continues to run unit tests and product integration suites only.
Run `pluginTooling` as an additional check when changing its dependencies,
fixtures, target, or CI setup. Passing this qualification supplies prerequisite
evidence; a dependent design still owns its application interface, concrete
acceptance cases, feasibility measurements, and verification budget.

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
go run build/mage.go -d build -w . check
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

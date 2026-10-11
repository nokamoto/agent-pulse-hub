# Agent Instructions

Implementation language: Go.

Write all documentation in English.

After writing or editing Markdown, apply the
[Markdown readability review skill](.agents/skills/review-markdown-readability/SKILL.md)
before presenting the document as complete. This also applies to skill and
workflow documentation. Its clean-context first-read review complements the
document's domain review; it does not replace requirements checks or human
approval.

Refer to the following directories:

- [Requirements](docs/requirements/)
- [Design](docs/design/)
- [AI-Driven Development rules](docs/aidd/)

## Implementation completion

Follow the [implementation completion and delivery boundary](docs/aidd/README.md#implementation-completion-and-delivery-boundary)
and use the [implementation PR template](.github/PULL_REQUEST_TEMPLATE/implementation.md).
Follow the approved cases from
[design verification deliverables](docs/aidd/README.md#design-verification-deliverables).
Pass every scoped approved CI acceptance case; Pending, skipped, missing,
filtered, or deleted cases cannot satisfy completion. Changes that weaken
approved expected behavior require upstream approval and merge. Final quality
checks using actual Codex or external services are designed upstream and
executed during delivery under the completion boundary above.

## Documentation feedback during implementation

Create operating instructions and specifications that developers will continue
referencing after implementation during requirements or design, as required by
the [developer documentation rule](docs/aidd/README.md#developer-documentation-and-upstream-feedback).
If implementation discovers a missing document, stop the current session even
when the approved requirements or design already require it. Apply the same
feedback when a separate specification or its contract, placement, authority,
or relationship to the design needs to change under the
[specification rule](docs/aidd/README.md#specification-documents-and-approved-design).
Preserve work and report the gap, affected documents and contracts, unfinished
implementation and acceptance criteria, and the required upstream phase.
End work without creating or changing the affected documentation, revising
requirements or design, or
starting another session automatically. A human starts the separate upstream
session. Resume implementation only after the required upstream approval and
merge are verified.

## Guardrails

Run all repository guardrails locally with Go:

```sh
go run build/mage.go -d build -w . guardrails
```

## Go development checks

Follow the [Go development conventions](docs/aidd/go-development.md) for
package placement, testing, and error handling.
Apply the [test levels and cost guide](docs/aidd/go-development.md#test-levels-and-cost):
unit tests mock external capabilities; assembled-product Ginkgo integration
tests belong directly in `cmd/<command>/*_integration_test.go` with
`//go:build integration`. Require separate unit and integration results.

Run the Mage targets from the repository root. Format Go code and tidy modules before committing:

```sh
go run build/mage.go -d build -w . format
```

Run unit and all tagged command integration tests locally with:

```sh
go run build/mage.go -d build -w . check
```

For separate results, run the unit tests with:

```sh
go run build/mage.go -d build -w . test
```

Run all command integration tests with:

```sh
go run build/mage.go -d build -w . integrationAll
```

New tagged command suites are included automatically in local checks and CI.
Static Pending is allowed in this run. Verify the implementation PR's approved
scope separately under the [acceptance test conventions](docs/aidd/go-development.md#design-acceptance-tests);
a successful general run alone does not establish implementation completion.

Run `vet` and `staticcheck` together with the lint check:

```sh
go run build/mage.go -d build -w . lint
```

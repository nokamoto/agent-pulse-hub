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

## Specification feedback during implementation

If implementation needs a separate specification document or a change to its
contract, placement, authority, or relationship to the design, stop the current
implementation session under the
[AIDD feedback rule](docs/aidd/README.md#specification-documents-and-approved-design).
Preserve work and tell the human why a separate design session is needed,
which contracts and documents are affected, and what implementation is unfinished.
End work in this session without revising the design or specification or
starting another session automatically. Resume implementation only after the
required upstream approval and merge are verified.

## Guardrails

Run all repository guardrails locally with Go:

```sh
go run build/mage.go -d build -w . guardrails
```

## Go development checks

Follow the [Go development conventions](docs/aidd/go-development.md) for
package placement, testing, and error handling.

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

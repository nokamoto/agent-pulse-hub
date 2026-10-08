---
name: implement-guardrails
description: Implement Go guardrail validators and register them in the shared Mage target used locally and in CI.
---

# Implement guardrails

Guardrails are automated validation tools that enforce consistent formatting and structure for repository artifacts. They run locally and in CI through one Mage target so both environments execute the same checks.

## Overview

Guardrails are standalone Go programs in `tools/guardrails/`. The `Guardrails` Mage target in `build/Magefile.go` runs every guardrail, and `.github/workflows/guardrails.yml` invokes that target. Mage is run without requiring a separately installed Mage binary, through `build/mage.go`.

## Implementation structure

Each guardrail is organized as:

```text
tools/guardrails/$command/
  main.go          # Go implementation of the guardrail
```

All guardrails share:
- Go module configuration (`go.mod`, `go.sum`) at the repository root
- Registration in the `commands` list of `Guardrails` in `build/Magefile.go`
- The same local and CI entry point: `go run build/mage.go -d build -w . guardrails`

## Steps to implement a guardrail

1. **Define validation rules**: Clearly specify what format or structure the guardrail enforces:
   - Files and directories it validates
   - Required fields, field order, and allowed optional fields
   - Error conditions and messages
   - Special cases or nested structures

2. **Implement in Go**: Create `tools/guardrails/$command/main.go` with:
   - A `main()` function that identifies files to validate
   - File parsing and validation functions
   - Error reporting with file paths and specific issue descriptions
   - A non-zero exit on validation failure and a success message on validation pass
   - Explicit reporting of filesystem, parsing, and validation errors; do not silently skip failures or turn them into success-shaped fallbacks

3. **Handle edge cases**:
   - Empty or missing directories (generally valid unless the rule requires their presence)
   - Malformed files (report specific parsing errors)
   - File permission issues
   - Symlinks and special files, where applicable

4. **Register the guardrail in Mage**: Add the invocation to the `commands` list in `build/Magefile.go`, for example:
   ```go
   {"run", "./tools/guardrails/$command"},
   ```
   Keep commands in a clear, deterministic order. The Mage target must return an error if any command fails rather than reporting success.

5. **Test locally**:
   - Run the individual guardrail with `go run ./tools/guardrails/$command` while developing it.
   - Run the complete set with `go run build/mage.go -d build -w . guardrails`.
   - Use focused unit tests or temporary fixtures as appropriate to verify valid and invalid inputs, clear error messages, and non-zero failure exits. Clean up temporary fixtures.
   - Run relevant Go tests and `go mod tidy -diff` if dependencies changed.

6. **Verify CI integration**: Confirm `.github/workflows/guardrails.yml` invokes the shared Mage command. Do not claim a GitHub Actions run passed unless it was actually run and observed.

## Example: requirements format guardrail

See `tools/guardrails/requirements/main.go` for a reference implementation that:
- Validates YAML frontmatter in Markdown files
- Enforces required fields in a specific order
- Rejects unexpected fields
- Checks nested structure compliance
- Validates multiple directories and specific files

## Design principles

- **Fail fast, fail clearly**: Report specific errors with file paths and line numbers when possible
- **Validate structure, not content**: Guardrails check format compliance; business logic validation belongs elsewhere
- **Composable**: Keep each guardrail independent and register it in the shared Mage target
- **Automatable**: All validation must be deterministic and reproducible
- **Explain failures**: Error messages must enable developers to fix issues without external documentation

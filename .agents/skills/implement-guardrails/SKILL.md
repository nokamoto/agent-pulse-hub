---
name: implement-guardrails
description: Implement guardrail validation tools in Go and integrate them into the CI/CD pipeline. Guardrails enforce structural and format compliance for repository artifacts.
---

# Implement guardrails

Guardrails are automated validation tools that enforce consistent formatting and structure for repository artifacts. They run in CI to catch compliance issues early.

## Overview

Guardrails are implemented as standalone Go programs in the `tools/guardrails/` directory and executed automatically in the GitHub Actions workflow `.github/workflows/guardrails.yml`.

## Implementation structure

Each guardrail is organized as:

```
tools/guardrails/$command/
  main.go          # Go implementation of the guardrail
```

All guardrails share:
- Common Go module configuration (`go.mod`, `go.sum` at repository root)
- Execution via `go run ./tools/guardrails/$command`
- Integration into the `.github/workflows/guardrails.yml` CI workflow

## Steps to implement a guardrail

1. **Define validation rules**: Clearly specify what format or structure the guardrail enforces. Document:
   - Files and directories it validates
   - Required fields, field order, and allowed optional fields
   - Error conditions and messages
   - Any special cases or nested structures

2. **Implement in Go**: Create `tools/guardrails/$command/main.go` with:
   - A `main()` function that identifies files to validate
   - File parsing and validation functions
   - Error reporting with file paths and specific issue descriptions
   - Exit code 0 on success, 1 on validation failure
   - Success message to stdout on validation pass

3. **Handle edge cases**:
   - Empty or missing directories (should generally be valid)
   - Malformed files (report specific parsing errors)
   - File permission issues
   - Symlinks and special files (if applicable)

4. **Update CI integration**: Add a step to `.github/workflows/guardrails.yml`:
   ```yaml
   - name: Validate [description]
     run: go run ./tools/guardrails/$command
   ```

5. **Test locally**:
   - Run `go run ./tools/guardrails/$command` and verify output
   - Create test files that should fail validation and verify errors
   - Verify the tool returns correct exit codes

6. **Verify CI**: Confirm the workflow runs and passes on valid files, fails on invalid files.

## Example: requirements format guardrail

See `tools/guardrails/requirements/main.go` for a reference implementation that:
- Validates YAML frontmatter in Markdown files
- Enforces required fields in specific order
- Rejects unexpected fields
- Checks nested structure compliance
- Validates multiple directories and specific files

## Design principles

- **Fail fast, fail clearly**: Report specific errors with file paths and line numbers when possible
- **Validate structure, not content**: Guardrails check format compliance; business logic validation belongs elsewhere
- **Composable**: Each guardrail is independent; multiple guardrails can run in sequence
- **Automatable**: All validation must be deterministic and reproducible
- **Explain failures**: Error messages must enable developers to fix issues without external documentation

## Files validated by the requirements guardrail

- `docs/requirements/*.md` - All requirement definition files
- `.agents/skills/define-requirements/assets/requirement.md` - Requirement template
- `.agents/skills/*/assets/*.md` - Other skill assets following the same format (if added)

Validation checks:
- YAML frontmatter present and properly formatted
- Required fields: `type`, `title`, `description` (in order)
- Optional fields: `sources` (may appear after required fields)
- No extra/unknown fields permitted
- Field order strictly enforced

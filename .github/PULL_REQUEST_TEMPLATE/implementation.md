<!-- Implementation changes and verification evidence. Follow the AIDD
     implementation boundary; use separate templates for other phase PRs. -->

## Purpose and implementation result

<!-- Describe the resulting behavior and identify the code, tests, CI, and
     permitted updates to existing documentation delivered by this revision. Map changes to approved
     requirement IDs and design sections. Explain why included documentation
     preserves the approved procedures, contracts, and document relationships. Do not create
     operating instructions or specifications for continued developer use here;
     they must be created during requirements or design, including new content
     added to an existing file. -->

## Upstream approval and scope

<!-- Link the merged requirements and design PRs and their source documents.
     Record approval and revision evidence under the handoff rules. If an
     upstream phase needs no update, explain why. For documentation updates
     covered by the specification feedback rule, link the governing design
     and explain why no design decision changes. If required developer
     documentation is missing, record the stop report: the gap, affected
     documents and contracts, unfinished implementation and acceptance criteria,
     and the required upstream phase. Preserve work and end the session; a human
     starts the separate upstream session. Resume only after upstream approval
     and merge are verified. An approved obligation to document does not permit
     creating the document during implementation. -->

[AIDD handoff evidence](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#agent-verification-and-handoff-evidence)
and [developer documentation feedback](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#developer-documentation-and-upstream-feedback)
and [specification feedback](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#specification-documents-and-approved-design).

## Verification and remaining implementation work

<!-- Record required repository check results, separate unit and tagged
     integration commands and results, and the full scoped set of
     approved CI acceptance case IDs and each
     result against the design's expected behavior, compared with the
     machine-readable execution report. Include commands, environment,
     tested revision, and runtime-cost evidence. Every case must pass; Pending,
     skipped, missing, filtered, or deleted cases do not satisfy completion.
     Report failures, checks not run, defects, and risks; incomplete required
     implementation checks block readiness and cannot be moved to delivery.
     Keep reviewer reproduction information here; it does not replace
     documentation for continued developer use. Do not weaken approved
     expectations to pass checks; resolve behavioral changes through upstream
     approval and merge. -->

[Go test levels and cost](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/go-development.md#test-levels-and-cost)
and [acceptance test conventions](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/go-development.md#design-acceptance-tests).

## Subsequent developer delivery

<!-- List identified work beyond the implementation completion boundary, or
     state None identified. Explain why each item is outside implementation
     acceptance criteria. This is a boundary record, not a complete delivery
     plan or authorization to publish, distribute, or deploy. -->

<!-- Identify the designed final quality checks using actual Codex or external
     services, their expected results, and evidence to collect during delivery.
     Their execution and results are outside implementation completion. If
     approved acceptance phases conflict with that boundary, link the separate
     upstream revision resolving the conflict before dependent implementation. -->

[Implementation completion and delivery boundary](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#implementation-completion-and-delivery-boundary).

## Human approval and merge

Review the final revision using the
[AIDD approval routes](https://github.com/nokamoto/agent-pulse-hub/blob/main/docs/aidd/README.md#human-approval-routes).
The responsible human may approve by personally merging the final revision.
Implementation is complete after approval and merge are verified; this does
not establish delivery completion.

# Design review checklist

Apply to new designs and revisions. Evaluate substance, not merely headings.
For a local review, D12 PR metadata may be N/A with a reason; artifact scope
and verification deliverables still require examination.
D01 remains UNVERIFIED until source approval and merge evidence are checked
against the source and design PRs. The design document needs source concept
links and local IDs to explain requirement coverage; it need not repeat PR
references or approval, head, or merge SHAs solely for provenance.

| ID | Pass condition |
| --- | --- |
| D01 | Source requirements are identified by concept and approved merged revision. The requirements PR is merged, and its approval route, human actor, final head SHA, merge time, merge commit SHA, and any explicit approval reference are verified under the [AIDD handoff rules](../../../../docs/aidd/README.md#agent-verification-and-handoff-evidence). Human merge passes without a separate Approve review, including for the human's own PR. The design respects the approved scope and constraints. |
| D02 | Every in-scope functional and nonfunctional requirement and acceptance criterion maps to design and verification. Partial coverage identifies responsible related designs without hiding gaps. |
| D03 | Architecture defines responsibilities, boundaries, dependencies, and interactions consistently with the existing system. |
| D04 | Relevant interfaces and data contracts define inputs, outputs, validation, errors, ownership, lifecycle, and compatibility; non-applicability is explained. |
| D05 | Normal, failure, and boundary scenarios describe outcomes and relevant recovery, concurrency, retry, or idempotency behavior. |
| D06 | Important decisions compare viable alternatives and explain requirement-backed rationale, tradeoffs, and risks at a level appropriate to the change. |
| D07 | Applicable security, privacy, reliability, performance, extensibility, and operational concerns respect approved constraints; targets are not invented. |
| D08 | All [verification conditions below](#d08-verification-conditions) pass for the design's CI acceptance cases and delivery checks. |
| D09 | Changes identify affected designs and implementation, and applicable migration, compatibility, rollout, and rollback impact. Upstream decision changes return to requirements before dependent design. |
| D10 | No open question or unsupported assumption blocks implementation. Deferrable decisions identify impact, owner, and resolution stage; significant risks and human decisions are explicit. |
| D11 | Design documents and separate specifications have stable concept paths under `docs/design/`, UTF-8 Markdown, valid relative links, ordered Design frontmatter and OKF sources as specified in [create-design](../../create-design/SKILL.md), and replaced template prompts. Context describes lasting design decisions rather than authoring-time implementation progress or temporary repository state; necessary existing behavior identifies the affected version or migration condition. The design and docs-layout guardrails pass on the reviewed version. |
| D12 | The complete design PR diff stays within the [design and specification scope](../../../../docs/aidd/README.md#specification-documents-and-approved-design) and its [verification artifact exception](../../../../docs/aidd/README.md#design-verification-deliverables), including deletions and both sides of renames; removed paths are assessed against the base revision. Outside `docs/design/`, only verification `*_integration_test.go` files directly in `cmd/<command>/` and fixtures in that command's `testdata/` are included, with paths governed by the design. No production code or dependency, build, CI, tooling, or shared harness changes are included. The PR has phase:design, links the merged requirements PR and deliverables, and records the reviewed revision, all checklist results, actual checks and limitations, human decisions, and changes since previous review. |
| D13 | The design records whether contracts remain in it or use separate specifications. Paths, purpose and readers, authority and derivation, contract decisions, and constraints on routine implementation choices are clear under the [specification document rules](../../../../docs/aidd/README.md#specification-documents-and-approved-design). Required specifications are included in the design PR or already approved and merged, not deferred to implementation. If no separate specification is needed, the design says it contains the contract. For a design session initiated from implementation feedback, the PR identifies the reported gap and its resolution; implementation remains stopped until upstream approval and merge are verified. |

### D08 verification conditions

Apply the [design verification deliverables](../../../../docs/aidd/README.md#design-verification-deliverables)
and [Go test conventions](../../../../docs/aidd/go-development.md#test-levels-and-cost)
and [completion boundary](../../../../docs/aidd/README.md#implementation-completion-and-delivery-boundary).
D08 passes only when all of the following hold:

- Case selection weighs defect-detection value and coverage against
  implementation complexity and maintenance and execution costs. Necessary
  cases use the least costly sufficient verification; consolidation or removal
  of unnecessary or impractical candidates explains retained requirement and
  acceptance coverage. Product requirements and approved required cases are
  not silently dropped; affected approved decisions have upstream approval
  and merge.
- Ginkgo cases and suite files reside directly in
  `cmd/<command>/*_integration_test.go` with `//go:build integration`;
  fixtures reside in that command's `testdata/`.
- Concrete Ginkgo acceptance cases are initially static `Pending`, without
  dynamic `Skip`. Stable IDs map to requirements and acceptance criteria;
  cases define concrete inputs and actions, observable expected results, and
  failure boundaries. Each selected Pending case has a resolved concrete
  implementation method and CI execution feasibility; only test code
  completion and product wiring remain for implementation.
- Each integration case explains why unit tests cannot establish its behavior,
  identifies real product components and substituted external boundaries, and
  accounts for setup, runtime, resources, and cleanup. Unit tests mock external
  capabilities under the guide; integration does not replace unit coverage.
- Compile and registration commands enable the `integration` tag and demonstrate that all cases
  are available without nonexistent production symbols.
- Separate service-free unit and tagged integration CI commands, environment,
  verification budget, and minimal
  harness feasibility have evidence. Uncertain verification is simplified or
  replaced before any remaining uncertainty is resolved with the smallest
  useful setup. A list of unproven cases and a blocker report do not complete
  selection; necessary cases still lacking a feasible method prevent readiness.
  Required prerequisites are separately approved and merged.
- Final quality checks using actual Codex or external services have expected
  results and planned evidence for delivery. They are not implementation
  execution requirements or completion evidence.
- Plans, Pending cases, and executed feasibility checks are distinguished.
  Approved acceptance-phase conflicts require a separate upstream revision
  before dependent implementation.

### Reporting scope and readiness

For a local diff review, inspect its complete file list for D12 even when PR
metadata is not applicable. Unavailable diff or required PR metadata makes D12
UNVERIFIED. A known violation makes it FAIL. A draft can record unknowns but
does not pass D10 while an implementation blocker remains. Human approval and
merge are subsequent milestones, not outcomes the reviewer can grant.

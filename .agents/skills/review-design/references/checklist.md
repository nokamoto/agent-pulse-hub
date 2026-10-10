# Design review checklist

Apply to new designs and revisions. Evaluate substance, not merely headings.
For a local document-only review, D12 PR metadata may be N/A with a reason.
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
| D08 | Verification methods and levels can decisively evaluate acceptance criteria, including failures and nonfunctional needs. Manual evaluation identifies evidence and evaluators; plans are distinguished from executed results. |
| D09 | Changes identify affected designs and implementation, and applicable migration, compatibility, rollout, and rollback impact. Upstream decision changes return to requirements before dependent design. |
| D10 | No open question or unsupported assumption blocks implementation. Deferrable decisions identify impact, owner, and resolution stage; significant risks and human decisions are explicit. |
| D11 | Documents have stable concept paths, UTF-8 Markdown, valid relative links, ordered Design frontmatter and OKF sources as specified in [create-design](../../create-design/SKILL.md), and replaced template prompts. Context describes lasting design decisions rather than authoring-time implementation progress or temporary repository state; necessary existing behavior identifies the affected version or migration condition. The design guardrail passes on the reviewed version. |
| D12 | The complete design PR diff changes only docs/design/, including deleted and renamed paths. The PR has phase:design, links the merged requirements PR and deliverables, and records the reviewed revision, all checklist results, actual checks and limitations, human decisions, and changes since previous review. |
| D13 | The design records whether contracts remain in it or use separate specification documents. Intended paths, purpose and readers, authority and derivation, fixed contract decisions, and constrained implementation delegation are clear under the [specification document rules](../../../../docs/aidd/README.md#specification-documents-and-approved-design). Approval-relevant contract decisions are present in the design PR; neither placement nor authority is left for implementation to choose. If no separate specification is needed, the design says it contains the contract. |

For a local diff review, inspect its complete file list for D12 even when PR
metadata is not applicable. Unavailable diff or required PR metadata makes D12
UNVERIFIED. A known violation makes it FAIL. A draft can record unknowns but
does not pass D10 while an implementation blocker remains. Human approval and
merge are subsequent milestones, not outcomes the reviewer can grant.

# Requirements review checklist

Evaluate content, not merely headings. Apply to new requirements and revisions.
For a local document-only review, PR metadata may be N/A with an explanation;
for a PR review it must be verified.

| ID | Pass condition |
| --- | --- |
| R01 | Problem, affected users, goals, and rationale are explicit and consistent with the requested outcome. |
| R02 | Included and excluded scope is explicit; priorities are supported by decisions. |
| R03 | Use cases identify actors, triggers, outcomes, and relevant failure or boundary behavior. |
| R04 | Functional requirements use stable, unambiguous IDs and observable behavior without contradictions or unrequested scope. |
| R05 | Relevant nonfunctional needs have evaluable conditions and targets; exclusions have reasons and targets are not invented. |
| R06 | Constraints, dependencies, source evidence, and assumptions are distinguished from optional design choices. |
| R07 | Every functional and nonfunctional requirement maps to acceptance criteria with decisive outcomes and feasible verification methods, including relevant failures. Manual checks identify evidence and evaluators. |
| R08 | No unresolved question or unsupported assumption blocks design. Deferrable decisions identify impact, owner, and resolution stage. |
| R09 | Requirements agree with existing approved requirements, or identify revised decisions and downstream impact. Retired IDs and affected links are addressed. |
| R10 | Documents are English UTF-8 Markdown with YAML type Requirement, stable concept paths, valid relative links, and OKF sources for source documents where applicable. Template prompts are replaced. |
| R11 | The entire requirements PR diff is limited to docs/requirements/, including deleted and renamed paths. No design, implementation, skill, or CI changes are mixed into the phase PR. |
| R12 | The PR has phase:requirements, links deliverables, records changes since prior review, provides actual verification results and limitations, and identifies human decisions and risks. |

An unresolved material priority in R02 or an unevaluable target in R05 fails
readiness; placing it in an open-question table does not make it pass.
R11 is UNVERIFIED when the full diff is unavailable. R12 is UNVERIFIED for a
PR whose metadata cannot be read. Human approval and merge are subsequent
milestones, not checklist outcomes that an agent can grant itself.

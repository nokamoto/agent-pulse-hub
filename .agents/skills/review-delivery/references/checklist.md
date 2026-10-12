# Delivery review checklist

Apply the [delivery phase](../../../../docs/aidd/README.md#4-delivery).
Assess every item for the stated delivery scope and exact reviewed revision.

| ID | PASS criteria |
| --- | --- |
| DEL-001 | Scope, product revision, source requirements/design, implementation handoff, and upstream approval and merge evidence are identified and verified under the handoff rules. Upstream phases needing no update have a reason. |
| DEL-002 | The complete delivery diff contains user-facing documentation and delivery evidence within the phase boundary. Product fixes, changed contracts, developer operating instructions, and infrastructure or rule changes are routed to their prerequisite phases. |
| DEL-003 | README instructions follow the [README rule](../../../../docs/aidd/README.md#user-facing-readme): a first-time human can follow a complete normal usage path without reading contract inventories or acceptance scenarios, and plugin authors have a concise entry point to authoritative details. Necessary setup and limitations remain available at the relevant steps. Instructions cover the delivered scope, preserve the required structure, and agree with authoritative contracts. An unchanged README has a justified scope assessment. |
| DEL-004 | Documented acquisition and setup steps were followed to an observable working result in the supported environment, with product/artifact identity, documentation revision, and actual outcomes recorded. |
| DEL-005 | Every scoped approved delivery case maps to requirements/design and passed against the identified product revision using the required real services and execution roles. Expected and actual results, execution date, environment and service versions, and reviewable evidence are present. Substitutes and unexecuted cases do not count as passes. |
| DEL-006 | Distribution and operational handover required by scope have identifiable artifacts, action authorization, and verified outcomes, including recipient confirmation where required. Non-applicable outputs have a scope-based explanation. |
| DEL-007 | Applicable repository checks and independent delivery and Markdown readability reviews cover the final deliverables. When performing the independent delivery review, assess that requirement through this review itself; do not require a prior delivery review. Credentials and private data are excluded from retained evidence without hiding necessary observations. |
| DEL-008 | The PR records limitations and remaining work with owners and resolution phases, has no unresolved blocking work, and distinguishes readiness from human approval and phase completion. Required publication or handover is not deferred beyond claimed completion. |

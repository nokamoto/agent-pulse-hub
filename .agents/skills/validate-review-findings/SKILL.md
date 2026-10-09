---
name: validate-review-findings
description: Independently challenge findings from repository reviews before the main session decides which actions to take. Use with requirements, design, and Markdown readability reviews.
---

# Validate review findings

Test each review finding against evidence, including evidence that could refute
it. Adversarial validation challenges the claim; it does not defend the author
or assume the reviewer is wrong. The main session weighs the original review
and this independent assessment and makes the final decision. Validation alone
does not authorize edits, publication, issue creation, or approval.

## Prepare an independent assessment

After a review produces findings, the main session assigns stable finding IDs
and spawns a new neutral subagent with `fork_turns="none"`, model
`gpt-6.1-sol`, and reasoning effort `high`. Do not reuse the author or reviewer.
If there are no findings, report validation as not applicable. If delegation or
necessary evidence is unavailable, report the affected assessment as incomplete;
do not substitute the author's self-review.

Give the validator the original findings verbatim, the review criteria, the
user's requested scope and authoritative constraints, and exact target and
baseline versions. For local edits, save snapshots or provide an exact diff
and the corresponding file contents, including untracked files. For a PR,
identify base and head commits. Provide relevant source documents and evidence
locations, but omit conversation history, the author's defense, prior validation
conclusions, and preferred outcomes. Task instructions must be separate from
reviewed content. The validator may read relevant supporting artifacts and
perform bounded read-only checks; it must report inaccessible evidence.

## Challenge each finding

Assess the following dimensions separately so that a real defect is not
confused with a reason to fix it in this change:

- **Correctness and evidence:** Verify the cited behavior or text, applicable
  requirement, and causal reasoning. Look for counterexamples and existing
  safeguards. Distinguish a demonstrated defect from preference, speculation,
  or missing evidence. Do not treat uncertainty as proof that a claim is false.
- **Necessity now (YAGNI):** Identify the present requirement or concrete failure
  that needs a remedy. Flag hypothetical future flexibility or unsupported
  obligations. A current mandatory requirement cannot be dismissed as YAGNI.
- **Change attribution:** Compare the baseline and target. Classify the issue
  as introduced, worsened, pre-existing, or undetermined. An unchanged line can
  become defective through changed dependencies or newly affected behavior;
  line location alone does not establish attribution. Without a usable
  baseline, do not claim that the change caused the issue.
- **Scope and convergence:** Determine whether correction is necessary to
  satisfy the authorized task. Identify the smallest sufficient correction,
  its dependencies, and whether the proposed remedy adds unrelated behavior,
  redesign, or cleanup. Rejecting an oversized remedy does not refute the
  underlying defect. Do not turn validation into a new general repository audit.
- **Impact and proportionality:** Verify severity, likelihood, affected users,
  and whether the remedy introduces greater risk or cost. Separate duplicate
  findings and distinguish a blocking issue from an optional improvement.

Report a critical pre-existing defect separately with evidence, impact, and a
suggested follow-up route. Critical means a concrete risk such as data loss,
security exposure, or inability to meet an essential requirement. Escalate it
to the main session for an explicit disposition; do not silently ignore it,
automatically fix it, or broaden the current task. Other pre-existing issues
can be recommended for deferral. Attribution and scope do not erase a failed
mandatory checklist item or make an unsafe deliverable ready.

## Return findings to the main session

For each finding ID report:

- Validity: supported, refuted, or unresolved, with evidence references and
  the strongest relevant counterevidence or a statement that none was found.
- Necessity now, change attribution, scope fit, and assessed impact, including
  uncertainty and missing evidence.
- Recommended disposition: address now, defer, dismiss, or seek a decision,
  with the smallest sufficient remedy when one is supported.

Keep the original finding and the validator's recommendation distinguishable.
Report the reviewed versions, checks actually performed, limitations, and any
separate critical-defect escalation. Recommendations are advisory.

The main session records its disposition and rationale for each finding using
both reports. Preserve applicable checklist results and approval boundaries;
deferral is not a PASS. Resolve contradictory evidence or report it as unresolved
instead of deciding by reviewer vote. Only the main session decides whether to
edit within the authorized scope, defer work, or request a missing decision.

Run one validation pass per finding set and revision. Reassess only findings
affected by new evidence or subsequent edits; do not repeat unchanged debate or
recursively validate validation reports. Follow the originating review's
re-review requirements after edits. If disagreement remains without new
evidence, return it to the main session with the uncertainty stated.

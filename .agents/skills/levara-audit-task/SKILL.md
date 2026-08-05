---
name: levara-audit-task
description: Independently audit a Levara long-horizon Task against its original objective, authority, Definition of Done, steps, receipts, workspace revision, blockers, and memory candidates. Use before completing high-risk tasks, when DoD requires independent review, or when asked to verify long-task completeness. Do not implement fixes, mutate the plan, claim steps, promote memories, or complete the Task.
---

# Audit a Levara task

Act as a read-only verifier. Do not trust executor summaries without receipts.

## Workflow

1. Call `task_bootstrap` for the supplied `task_id` with enough budget to include all criteria and steps.
2. Call `task_validate(mode=completion)` and independently inspect every returned deficiency.
3. Compare each required criterion with a passing, current receipt.
4. Check that command receipts have exit code 0, artifact receipts have URI and digest, and reviewer/source receipts identify what was actually examined.
5. Treat evidence from a different workspace revision as stale.
6. Check authority boundaries, active blockers, active leases, incomplete steps, and failed attempts hidden by later summaries.
7. Review pending memory candidates: reject speculation, temporary state, code paths, undated events, and discoveries without evidence.
8. Record the audit as a `reviewer` receipt only when an existing audit criterion is present. Otherwise return the audit findings without mutating Task state.

## Verdict

Return one of:

- `pass`: every required criterion is current and evidenced; no critical finding remains.
- `fail`: a requirement is missing, stale, contradicted, or outside authority.
- `blocked`: verification needs unavailable access or a human decision.

Include criterion IDs, receipt IDs, severity, evidence inspected, and required action. Read [references/audit-rubric.md](references/audit-rubric.md) for severity and pass rules.

Never call `task_plan`, `task_step`, `task_checkpoint`, `task_complete`, `save_memory`, or `supersede_memory` while acting as auditor.

---
name: levara-run-long-task
description: Run or resume long-horizon work through Levara Task Runtime with measurable Definition of Done, atomic step leases, checkpoints, receipts, blockers, and durable-memory promotion. Use for multi-step coding, research, data, monitoring, migration, or scheduled work that must survive compaction or continue across agents. Do not use for one-shot questions or tasks without a verifiable outcome.
---

# Levara long task

Use Levara as the operational source of truth. Treat chat history and generated Markdown as views, not task state.

## Select the mode

- `start`: create a Task and plan its first verifiable steps.
- `resume`: bootstrap an existing `task_id` and continue its next eligible step.
- `monitor`: inspect an existing Task; make no changes when no new action is possible.
- `recover`: bootstrap after compaction, restart, expired lease, or agent handoff.
- `finish`: validate, request an independent audit when required, then complete.

## Initialize

1. Call `set_context` with the project collection.
2. Call `wake_up` with that exact collection. Ignore graph entities unless `scope_status=exact`.
3. For `start`, call `task_open` with a non-empty collection, room, objective, authority, risk level, idempotency key, and structured Definition of Done.
4. For other modes, require a stable `task_id` and call `task_bootstrap`.
5. Stop if Task Runtime tools are unavailable; report that `LEVARA_LONG_HORIZON_RUNTIME` must be enabled. Do not emulate server state in chat.

## Execute one verified step at a time

1. Use `task_plan` only before execution starts. Give every required step acceptance criteria and DoD criterion IDs.
2. Refresh with `task_bootstrap` immediately before claiming work.
3. Atomically claim the selected step with `task_step(action=claim)` and the returned task version.
4. Perform the smallest change that can satisfy that step.
5. Record observed evidence through `task_receipt`; never convert a claim or summary into a passing receipt.
6. Record a compact `task_checkpoint` with verified results, failures, workspace revision, next action, blockers, and durable-memory candidates.
7. Pass or fail the step through `task_step`. Retry version conflicts by bootstrapping; do not overwrite newer state.

Use a stable workspace revision. For Git work, record HEAD plus a digest of dirty tracked/untracked state. Re-run affected verification after any later mutation.

## Handle blockers and monitoring

- Create a blocker for payment, publication, production change, destructive action, missing secret, legal risk, or conflicting requirements.
- After the required decision or external condition is satisfied, resolve the exact active blocker IDs through `task_checkpoint(resolved_blocker_ids=[...])`; preserve the decision in the checkpoint summary.
- Do not broaden authority to clear a blocker.
- In `monitor` mode, produce a no-op when external state and eligible steps are unchanged. Do not create duplicate checkpoints or memories.
- Let expired leases be reclaimed through a fresh bootstrap and claim; never impersonate the prior lease owner.

## Finish

1. Call `task_validate(mode=completion)`.
2. If invalid, continue the first actionable missing item; if none is actionable, report the blocker.
3. For high-risk tasks, or when DoD requires review, invoke `$levara-audit-task` with the same `task_id`.
4. Call `task_complete` only with the current version and only after validation succeeds.
5. Report actual receipts, promoted/rejected memories, residual risk, and human decisions required.

## Load specialized guidance

- Coding and migrations: read [references/coding.md](references/coding.md).
- Evidence-backed research: read [references/research.md](references/research.md).
- Data analysis: read [references/data-analysis.md](references/data-analysis.md).
- Scheduled or polling work: read [references/scheduled-runs.md](references/scheduled-runs.md).

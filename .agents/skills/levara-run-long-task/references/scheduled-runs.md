# Scheduled runs

1. Keep a stable `task_id` in the scheduled prompt.
2. Start every run with `set_context`, `wake_up`, and `task_bootstrap`.
3. Compare external state with the last checkpoint before claiming work.
4. If nothing material changed, return a no-op without a new checkpoint or memory candidate.
5. Use a worktree for repository mutations when available.
6. Never wait while holding a lease; release it before an external polling interval.
7. Convert missing approval, secrets, publication, payment, production, or irreversible actions into blockers.
8. Stop scheduling after Task completion or a terminal human-owned blocker.

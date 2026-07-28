# Long-Horizon Runtime alpha report

This is point-in-time acceptance evidence, not the user guide. For setup,
tool semantics, recovery, and security constraints, see
[Long-Horizon Task Runtime](long-horizon-runtime.md).

- Date: 2026-07-14
- Target: local launchd Levara at `http://127.0.0.1:8081/mcp`
- Profile: `LEVARA_MCP_TOOLSET=long-horizon`
- Feature flag: `LEVARA_LONG_HORIZON_RUNTIME=1`

## Acceptance run

Run ID: `alpha-1784042257-b07fc3`

- Result: 10 passed, 0 failed.
- Duration: 176 ms.
- Scenarios: basic completion, stale receipt recovery, step dependencies,
  idempotent retries, scheduled no-op bootstrap, collection isolation,
  medium-risk policy, high-risk reviewer policy, concurrent step claim, and
  verified memory promotion.
- Concurrent claim produced one active lease owner.
- Memory promotion created two verified memories and rejected one unsupported
  discovery.

## Crash recovery

Task `ebc8d2d9-d6c9-43aa-a7b4-09916ad65b83` was checkpointed, the launchd
server was restarted, and a fresh MCP session recovered the checkpoint from
PostgreSQL and completed the task.

## Bootstrap relevance

Task `1d460855-8749-48bd-a481-29cebc8bc09c` used an annotated collection with
relevant memories in the Task room and distractors in another room.

- Returned memories: 9.
- Relevant memories: 9.
- Precision: 100%.
- Bootstrap size: 576 tokens with a 600-token budget.

## Alpha finding

The first multi-task live run found that criterion and step identifiers were
incorrectly global primary keys. The runtime now scopes criterion IDs, step
IDs, and leases by `task_id`. The corrected run passed all ten scenarios and
the subsequent crash-recovery and relevance checks.

The publication review also found that an active blocker had no public
resolution transition. `task_checkpoint` now accepts `resolved_blocker_ids`,
preserves the blocker history, and removes resolved blockers from completion
validation.

## Publication validation

On 2026-07-29, the current branch was built and started against a fresh,
isolated SQLite database with the `long-horizon` tool profile. The acceptance
runner passed all 10 scenarios in 54 ms. The focused blocker lifecycle test,
the every-commit Go suites, profile validation, contract drift check, `go vet`,
`go build`, and the full `go test ./...` suite also passed.

The WebUI CI-equivalent checks passed: production dependency audit, lint,
production build, and 16/16 Playwright tests. The unrestricted development
dependency audit currently reports the upstream `brace-expansion` advisory;
forcing the patched major breaks the ESLint 9 dependency chain, while the CI
and production audit (`--omit=dev`) remains clean.

## Reproduction

```bash
python3 scripts/long_horizon_alpha_eval.py
python3 scripts/long_horizon_alpha_eval.py --bootstrap-eval
python3 scripts/long_horizon_alpha_eval.py --prepare-crash
# Restart the server, then use the emitted task ID:
python3 scripts/long_horizon_alpha_eval.py --resume-crash <task-id>
```

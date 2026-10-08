# Verification evidence

Date: 2026-10-05. This is one bounded fix within T06, not completion of the full task.
Base HEAD: `2eb1dca16b0047918185760b41dcb22dee79090a` plus the source manifest below.
Go 1.27.1, darwin/arm64; existing SQLite fixture and PostgreSQL 16.15 in an isolated loopback test database. PostgreSQL fixtures preserve BOOLEAN and TIMESTAMPTZ types and use a fresh schema with pool=1. No production data or LLM provider was used. After verification, the owned temporary PostgreSQL instance was stopped with `pg_ctl -m fast -w stop`; exit 0, server stopped.

## Reproduction and fix

The original shared regression command was:

```sh
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 -v ./pkg/mcp -run '^TestConsolidationSQLApplyRevert$'
```

RED exited 1: SQLite passed; PostgreSQL failed its mixed apply with `column "is_pinned" is of type boolean but expression is of type integer`, SQLSTATE 42804. Zero skips. The only production change replaces that abstract INSERT's integer 0 with FALSE; bindings and transactions are unchanged.

## Observed focused checks

| Check | Command | Observed result |
|---|---|---|
| New SQL matrix GREEN | `go test -count=1 -v ./pkg/mcp -run '^TestConsolidationSQLApplyRevert$'` | Exit 0; SQLite and PostgreSQL PASS, zero skips |
| Existing consolidation plus new matrix, root rerun | `go test -count=1 -v ./pkg/mcp ./pkg/consolidate -run 'TestConsolidationSQLApplyRevert|Consolidat|AbstractValue'` | Exit 0; 20 top-level tests/22 runs, zero skips |
| Focused race | `go test -race -count=1 -v ./pkg/mcp -run '^TestConsolidationSQLApplyRevert$'` | Exit 0; both dialects PASS, zero skips |
| Generated contract | `make contract-check` | Exit 0; no generated change |
| Planning | `openspec validate fix-consolidation-postgres-boolean --strict` | Exit 0 |
| Formatting | gofmt and `git diff --check` | PASS |

All SQL commands had the isolated PostgreSQL DSN set. Raw logs: `/tmp/levara-consolidation-sql-red.log`, `/tmp/levara-consolidation-sql-green.log`, `/tmp/levara-consolidation-sql-race.log`, `/tmp/levara-consolidation-current-focused.log`.

The matrix checks mixed merge/abstract publication, unpinned abstract and lineage, source retirement, unchanged survivor and other-collection pinned control, failed revert rollback after source reactivation, successful revert restoring exact original rows, and a later abstract INSERT error rolling back an earlier merge. Expected foreign-key/missing-column failure causes are asserted, not merely any error.

The independent read-only reviewer inspected the actual two-file diff and RED/GREEN/race logs. No blocking finding remained within the boolean fix. It did not edit files or mutate shared memory/Task state.

## Integration and remaining boundaries

The current combined `make test-commit` exited 0: S0–S4 green, log `/tmp/levara-roadmap-current-test-commit.log`. MCP ran freshly (33.641s) and HTTP ran freshly (121.959s); unchanged access/profile/audit/workspace/core/server packages were cached. The gate includes the current strengthened T02 test, all ten T03 guide files, T04/T05 implementation and this literal fix. The existing opt-in DCD load baseline is outside this acceptance scope; no PostgreSQL consolidation case was skipped. Final strict validation, contract-check, source-digest and whitespace checks also passed.

This direct SQL fixture does not prove owner isolation, async credential/context propagation, engine hall policy, vector/outbox lifecycle, crash recovery or real LLM quality. Those remain open T06 work and issues I13/I16–I18. No public schema, dependency, migration, flag, commit, push or deployment changed. Unrelated worktree edits are preserved.

Levara MCP is unavailable: local artifacts are not Task Runtime receipts/completion or durable memory. The 32-task goal remains active.

## Verified source manifest

```text
1551a29279ffaac44687dc44c22d3e99729b140d7e60ed32a437f5cccc5f59ca  pkg/mcp/tool_consolidate.go
cd633e50653f6081fb5e1bb41eca38588a3161c8b9b7b3dbf7ec80cbbd3bf56e  pkg/mcp/tool_consolidate_sql_test.go
```

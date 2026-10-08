# Proposal

## Why

Consolidation abstract INSERT uses integer `0` for a PostgreSQL BOOLEAN column, so the transaction fails instead of publishing its abstract. SQLite-only consolidation tests missed the incompatibility.

## What Changes

- Use a boolean literal accepted by both SQL backends.
- Add regression coverage for mixed merge/abstract apply, rollback after a later failure, and revert on SQLite and isolated PostgreSQL.
- Record this as the first bounded T06 fix; ownership, async identity, hall semantics and derived-index/recovery remain separate open T06 work.

## Capabilities

### New Capabilities

- `memory/consolidation`: SQL apply/revert atomicity and unpinned generated records on both supported SQL backends.

### Modified Capabilities

None; no main consolidation spec exists yet.

## Impact

One SQL literal in `pkg/mcp/tool_consolidate.go` and one focused test file. No dependency, DDL migration, public schema, dispatch, profile or flag changes. Existing shared/hall behavior is preserved by this fix and is explicitly not accepted as secure owner lifecycle; it remains in T06 and the issue ledger. No production service operation or deployment is included.

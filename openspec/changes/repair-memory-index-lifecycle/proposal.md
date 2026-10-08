# Proposal

## Why

Consolidation now commits index effects atomically, but the existing delete worker ignores restored SQL state and the outbox leaves an identical completed publication unclaimable. This can leave a reverted memory without its derived vector while SQL fallback obscures the failure.

## What Changes

- Reopen completed identical publication jobs when authoritative state requests publication again, retaining pending/running deduplication and stale-claim protection.
- Guard vector deletion against current authoritative SQL state for the affected physical collection, including the restore/delete race.
- Prove source-to-abstract indexed recall and complete revert with actual collection records, SQLite and disposable PostgreSQL, delayed deletion and identical-digest republication.
- Keep the existing SQL outbox, native database transaction/locks and collection APIs. No new queue, dependency, general generation framework, public tool, live migration or deployment.

## Capabilities

### New Capabilities

- `memory/index`: Derived memory-vector effects follow authoritative SQL state through retirement, restoration and repeated publication.

### Modified Capabilities

None: no main OpenSpec capabilities are registered yet. This is the remaining index portion of roadmap T06/T07, separate from the accepted owner lifecycle change.

## Impact

Targets the existing memory-index SQL outbox and HTTP worker, with focused consolidation/recall integration tests. SQLite and PostgreSQL must have equivalent behavior. Public MCP arguments, profiles and feature flags remain compatible; generated contract drift is checked. Reopening completed publication jobs is an intentional internal scheduling change. No live schema migration or production restart is authorized.

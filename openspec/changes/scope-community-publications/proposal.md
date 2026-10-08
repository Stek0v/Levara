# Proposal

## Why

Global community summaries have no durable proof of the graph inputs and source publications they used. T16 also requires closing the selected-tenant and response-lifetime gaps in `list_communities`, so scoped search cannot expose global context through another tool.

## What Changes

- Admit authenticated global community consumers only for an active instance administrator without a selected tenant; retain verified credential and role authority through actual response Close.
- Bound completed response delivery by both request and verified credential expiry, preserving cancellation during SQL acquisition.
- Capture immutable detection and prompt inputs, preserve exact source and endpoint assertions through parent summaries, and publish communities atomically after a final liveness check.
- Make mirrored SQL generation and provenance authoritative for summary retrieval; reject stale or unverified vector hits before model use.
- Preserve trusted anonymous local compatibility explicitly. Legacy summaries are never automatically marked verified; authenticated use requires rebuilding provable inputs.
- Repair PostgreSQL placeholder/conflict syntax and rollback behavior in community replacement/incremental paths.

## Capabilities

### New Capabilities

- `graph/community-publications`: global community admission, source provenance, atomic rebuild and authoritative summary delivery.

### Modified Capabilities

None: no main community capability exists in the current OpenSpec inventory.

## Impact

Community storage, incremental rebuild, orchestrator integration, MCP/REST consumers, shared protected-response lifetime, and additive PostgreSQL/SQLite schema columns. Public tool names and response shapes remain compatible; authenticated legacy/unprovable summaries become unavailable. No new dependencies, algorithms, per-project community feature, deployment, production migration, or Neo4j parity acceptance. Generated contracts have one root owner and are checked even when descriptors stay unchanged.

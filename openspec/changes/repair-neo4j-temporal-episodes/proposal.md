# Proposal

## Why

The native SQL writer preserves exclusive relationship history, but the Neo4j writer merges by endpoints/type without dataset identity or episode closure. Repeated transitions can overwrite history or leave multiple current targets; missing endpoints can silently report success.

## What Changes

- Share the existing exclusive relationship vocabulary without changing native SQL behavior.
- Make Neo4j current-assertion transitions atomic and dataset-scoped, with stable active retries and fresh return episodes.
- Preserve closed episode properties/IDs and successor links, including mixed-case exclusive types and concurrent batches.
- Reject missing endpoints and roll back the complete batch with zero success counts.
- Project explicit graph-store edge IDs into existing Neo4j properties.
- Scope: writer episode/identity semantics and focused live/native SQL checks. Non-goals: GraphStore traversal redesign, caller ACL expansion, schema migration, deployment and subsecond Neo4j snapshot parity.

## Capabilities

### New Capabilities
- `graph/temporal-episodes`: atomic dataset-scoped current relationship transitions retain historical episodes.

### Modified Capabilities
None; no main specs exist yet.

## Impact

Existing graphdb writer, graphstore edge identity adapter and native orchestrator exclusive-type lookup. No new dependencies or public tool/route/SQL-schema changes. One internal Neo4j identity uniqueness constraint is required for concurrent global ID allocation. Canonical SQL remains authoritative and preserves microsecond episode time; Neo4j keeps the existing numeric Unix-second path envelope. Existing caller-supplied temporal properties require explicit validation/preservation in the implementation design, not silent history reopening. No live migration or production restart. A verified isolated Neo4j/APOC sandbox supplies live evidence.

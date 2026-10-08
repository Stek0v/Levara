# Proposal

## Why

The Neo4j neighborhood adapter always supplies a rejected empty label and ignores hop depth. It cannot fulfill the shared graphstore traversal contract or preserve actual endpoint/type projection.

## What Changes

- Implement bounded case-insensitive seeded undirected neighborhood reads matching native SQL traversal sets.
- Preserve actual endpoint IDs and node types even when edge endpoint metadata is missing.
- Add focused owned-sandbox tests for depth, direction, boundary, cycles, cancellation and duplicate names.
- Scope: adapter topology reads only. Non-goals: caller/publication authority, temporal path selection, episode writing, gRPC legacy subgraph redesign or deployment.

## Capabilities

### New Capabilities

- `graph/neighborhood-traversal`: bounded undirected topology context from named seeds.

### Modified Capabilities

None; the main spec inventory is empty.

## Impact

Graphstore Neo4j adapter and focused tests. Reuse the existing managed reader and fixed internal node label; keep strict external label validation. No public route, schema, flag or dependency change. Source-authorized temporal graph reads retain their independent guards. No migration or production operation.

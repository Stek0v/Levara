# Design

## Context

See proposal.md. Native SQL traversal is undirected, case-insensitive by seed name, clamped to 1..8 and capped at 100 contexts. Its current ordering is not deterministic. The legacy gRPC subgraph reader is a different contract.

## Goals / Non-Goals

Repair only adapter topology reads. Do not broaden external label validation, add lazy write backfills, expose a new HTTP route or claim source publication authority.

## Decisions

- Use the existing managed Query reader with fixed internal __Node__ label and parameterized names. Clamp hops before interpolating the bounded integer.
- Find distinct frontier nodes at distance 0 through hops-1 from named seeds; emit each frontier's incident relationships in traversal direction. This matches SQL depth semantics, including reverse contexts, without adding induced edges between unvisited boundary nodes.
- Project actual endpoints, names and type property with safe dynamic-label fallback. Deduplicate by source ID/type/target ID, deterministic sort, cap at 100. Do not deduplicate different same-name node IDs.
- Leave ReadSubgraph, external safeLabel and SQL writer unchanged. Compare SQL/Neo4j result sets where the 100 cap does not truncate; do not claim identical arbitrary SQL truncation ordering.

## Risks / Trade-offs

- Dense graph path enumeration → bounded 8 hops and distinct frontier; avoid new traversal framework.
- Topology does not carry source authority → preserve independent fenced public graph reads.
- Type label fallback → prefer explicit type, omit internal labels.

## Migration Plan

No migration or deployment. Owned disposable Neo4j and native SQL fixtures supply exact-set tests. Code rollback restores prior adapter behavior without data mutation.

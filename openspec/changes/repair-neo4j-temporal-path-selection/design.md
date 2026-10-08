# Design

## Context

See proposal.md. SQL PathBetween filters edgeVisibleAt before BFS. Neo4j currently unwinds shortest paths before filtering individual relationships. Neo4j5 supports relationship predicates on the same shortest-path MATCH.

## Goals / Non-Goals

Goals: preserve existing flat edge-union API while choosing routes from snapshot-visible relationships. Non-goals: episode writer semantics, caller authority changes, schema migration and production rollout.

## Decisions

Move the validity condition into all(r IN relationships(p) WHERE ...) directly after the shortest-path MATCH, before UNWIND. Keep existing bounded allShortestPaths, native driver and cursor format. Enumerating arbitrary paths would increase query cost and duplicate traversal code. Preserve path inclusive upper bounds; query_entity has a different half-open contract and is unchanged. Add a total ordering tie-break for parallel historical relationships if live pagination exposes ambiguity.

Use a fresh private loopback Neo4j/APOC database only; the existing live helper deletes all nodes and is unsafe against any other target. Seed fixtures using direct writes, not the still-unrepaired episode writer. Read-only source comparison and focused native SQL tests establish unchanged SQLite/PostgreSQL semantics. No profile/feature-flag/public schema changes.

## Risks / Trade-offs

- Predicate planner behavior → live expired-shortcut and invalid-fragment checks, not only string assertions.
- Legacy validity or parallel edge order → explicit live history/boundary/pagination fixtures.
- Docker disk exhaustion → preserve first startup failure; isolated host data/log/plugin directories, no unrelated resource pruning.

## Migration Plan

No migration or deployment. Build/test local derived Neo4j query; rollback changes the query only and must not relax existing caller authorization.

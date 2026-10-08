# Proposal

## Why

Neo4j selects shortest paths before removing temporally invalid relationships. An expired shortcut can hide a longer valid route or expose a fragment of an invalid route, unlike the existing SQL path contract.

## What Changes

- Apply whole-path temporal visibility during Neo4j shortest-path selection.
- Preserve integer Unix-second snapshots, inclusive validity bounds, AsOf0 history, hop limits and edge pagination.
- Verify live isolated Neo4j/APOC against native SQLite/PostgreSQL temporal fixtures and error cases.
- Scope: existing PathBetween query and focused tests. Non-goals: episode writer repair, schema migration, authorization expansion, deployment or completing all original T15 requirements.

## Capabilities

### New Capabilities
- `graph/temporal-paths`: shortest paths are selected from the graph visible at the requested snapshot.

### Modified Capabilities
None; the project has no main specs yet.

## Impact

Existing optional Neo4j path implementation and tests. Public MCP/REST schemas and error shapes remain unchanged; current contract drift is checked. No new dependencies, SQL changes or live migrations. Tests target only a verified fresh private Neo4j database; protected caller authority stays in existing layers.

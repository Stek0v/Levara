# Proposal

## Why

Notebooks were selected for removal in the product audit, but their execution handlers and optional UI remain reachable when the old flag is enabled. Retire that surface while preserving existing SQL notebooks and cells for administrator-controlled backup and recovery.

## What Changes

- **BREAKING** Remove all ten legacy notebook REST operations and the WebUI notebook page; old clients receive the existing default-off 404 behavior.
- Ignore every legacy LEVARA_NOTEBOOKS value, including enabled values, without failing startup.
- Retain both SQL tables, indexes, constraints and every stored field; no migration, automatic deletion or public export shim.
- Require populated SQLite/PostgreSQL preservation and real populated backup/verify/restore evidence before acceptance.
- This explicit T31 sunset contract replaces the historical F1 planning wait condition for this authorized implementation. No published release interval is claimed or proved.

## Capabilities

### New Capabilities

- `notebook-retention`: Permanent retirement of notebook execution with retained SQL history and verified administrative recovery.

### Modified Capabilities

None; the main specification inventory is empty.

## Impact

Notebook-only registration in internal/http/api.go, notebook implementation/tests and the WebUI notebook page. Root integration owns route inventory, generated contracts and product documentation. No dependencies, generic migrations, live data changes or core memory workflow changes. Deployment and publication are outside this change.

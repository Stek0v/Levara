# Tasks

All Go commands use GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'. Only the verified owned Neo4j/APOC container on loopback Bolt 17687 may be used by destructive test helpers. Preflight PostgreSQL on 63530 before dialect checks. Preserve exact revisions, actual terminal results, logs and failures in evidence.md.

## 1. Episode writer

- [x] 1.1 Share the exclusive vocabulary, implement locked atomic dataset-scoped transitions, active retry and historical envelope validation/preservation; DoD: focused live episode tests pass for A→B→A, retry, mixed case, datasets, nonexclusive types, legacy missing metadata, explicit supported/invalid bounds, closed-ID conflict, missing endpoints, rollback, cancellation, edge-only/crossed concurrency and disjoint current/closed same-ID competition. Internal unique identity reservations commit or roll back with the relationships and are absent from graph reads.
- [x] 1.2 Project adapter edge identity and reject conflicting IDs; DoD: focused adapter checks show explicit ID round-trip and zero-effect conflict rejection.

## 2. Integration acceptance

- [x] 2.1 Run full graphdb live tests, native graph episode SQLite/PostgreSQL parity tests, adapter checks, contract drift and strict OpenSpec validation; DoD: exact source revision frozen during gates, every claimed gate exits zero, independent diff review accepted and current evidence recorded. Original T15 remains separate until its full criteria are satisfied.

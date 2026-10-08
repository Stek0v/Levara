# Tasks

All Go commands use GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'. Destructive helpers run only on verified owned Neo4j/APOC at loopback Bolt 17687. Root serializes Go commands and freezes source/docs during gates. Implementation waits until the temporal-episodes worker releases pkg/graphstore/neo4j.go.

## 1. Neighborhood adapter

- [x] 1.1 Replace the invalid legacy reader call with bounded managed traversal, correct actual endpoint/type projection, dedup/order/100 cap; add focused owned live tests and document behavior in this change. DoD: 1/2/3 hops, clamps, incoming edges, boundary exclusion, cycles, same-name seeds, missing edge metadata, missing seeds and cancellation pass; external label validation and legacy gRPC reader stay unchanged.

## 2. Acceptance

- [x] 2.1 Compare exact nontruncated result sets against native SQLite/PostgreSQL, run full graphstore/graphdb live tests and contract/strict/diff checks, independent review; DoD: actual zero exits on frozen revision, logs/failed gates retained in evidence.md and ledger I220 reconciled. Topology success alone does not accept source-authorized T15.

# Evidence

Bounded change accepted2/2; topology success remains distinct from source-authorized graph queries.

## Implemented behavior

The adapter uses managed reads with a fixed internal node label, case-insensitive name parameters and clamped hop depth. It emits incident edges of frontier nodes at distances0..hops-1, preserving SQL undirected traversal orientation. Actual endpoints/type projections work without edge endpoint metadata; ID-based dedup and ordering cap output at100. Legacy gRPC reader and strict external label validation remain unchanged.

Owned live tests cover exact depths, incoming/boundary edges, triangle/self-loop cycles, parallel dedup, same-name distinct node multiplicity, label fallback, missing/empty/nil inputs, long-chain clamps, stable first100 contexts, cancellation and closed-backend errors. Exact nontruncated DTO multisets are checked against native SQLite and PostgreSQL. Mid-iteration driver failure is not separately injected; existing managed reader iteration-error propagation is source-reviewed.

## Current acceptance — 2026-10-07

Root observed actual terminal exits on frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:78a318d5a780d9ec43d67c8f539988f7d0e67f3699d65d82edcbb0858ab903ac` (366 untracked files). Source/docs remained unchanged through functional gates and postgate revision capture.

- Full live graphdb + graphstore session92329: exit0,60 leafPASS/0FAIL/0SKIP, both packagesPASS. Log `/tmp/levara-t15-episode-neighborhood-live-first.jsonl`, SHA256 `60ab8aa442125cc1cde19c8d1d4ab4be3f59e5fba598ab30b8f8e565a74db293`.
- Original native Graph|Entity|AsOf|Temporal|Assertion matrix session78502: exit0,240 leafPASS/0FAIL/1SKIP, all five packagesPASS (graph, orchestrator, graphstore, MCP, HTTP). Log `/tmp/levara-t15-native-final-matrix.jsonl`, SHA256 `2ca0b139efa768e70912a265cf6fedeb0ba635b8b0ba06e864a6593355a31626`. Sole skip: opt-in TestPipeline_DeepSeek_ExtractsGraph; not passing provider-quality evidence. Both SQL mirrors ran.
- Contract session46789: actualexit0; quiet `/tmp/levara-t15-episodes-neighborhood-contract.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- Both changes passed strict OpenSpec; diffcheck exit0 and fresh Gortex detect succeeded. Root alone formatted through stdout/guarded edits and ran commands.

Owned disposable Neo4j/APOC5.26.31 container `levara-t15-20261007-0843`, exact ID `d855b30e1ca156faa57a750219002720ba5a6ffc988f74b135ffaca61ef19091`, loopback Bolt17687. Actual private readiness/empty0/0 preflight and historical startup/path failures remain in the accepted [path evidence](../repair-neo4j-temporal-path-selection/evidence.md). PostgreSQL63530 was ready before gates. No production service, migration or unrelated Docker cleanup.

All Go commands retained `-skip=^TestMemoryREST`; the user-forbidden reproduction was not run. Live gates additionally set owned NEO4J_TEST_* and dedicated LEVARA_TEST_POSTGRES_DSN. Native matrix used the dedicated SQL DSN. GOFLAGS also included `-p=1 -ldflags=-w -count=1`; contract omitted count only.

Independent reviewer parsed both actual JSONL logs/hashes and reviewed combined source/tests; accepted both bounded changes and original T15 within the declared supported scope. Authenticated graph reads remain SQL source/publication/endpoints-authorized, or503 without SQL. Topology adapter success does not establish a new Neo4j ACL. Neo4j seconds and SQL microseconds remain separate precision contracts. No provider quality or deployment claim.

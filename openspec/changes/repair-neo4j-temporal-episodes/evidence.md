# Evidence

Bounded change accepted3/3; original T15 accepted after live parity and native authority matrix.

## Implemented behavior

Shared ten-type vocabulary preserves native SQL wrapper behavior. Neo4j writes lock identity candidates then endpoints and commit node/edge/reservation effects atomically. Active retries retain ID/start, A→B→A keeps closed history, case/dataset scopes and nonexclusive coexistence remain distinct. Explicit closed imports preserve envelopes/evidence without superseding current; conflicts and missing endpoints fail with zero success counts. Internal identity nodes are excluded from graph reads; their exact label is rejected at caller input. Lazy constraint readiness retries after cancellation/failure and covers non-bootstrap callers.

Tests cover current/closed disjoint same-ID competition, crossed and edge-only batches, history/successor boundaries, explicit forms/range/fraction validation, legacy repair, occupied ID hints, rollback/reservation cleanup, dynamic APOC names, cancellation and bootstrap without EnsureSchema. Adapter ID projection/conflict is live-checked. Independent source review found rounded JSON-number validation before tests; root repaired it with exact bounded decimal validation, including whole-batch invalid-bound checks. No failing runtime gate for that source-found issue is invented.

## Current acceptance — 2026-10-07

Root observed actual terminal exits on frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:78a318d5a780d9ec43d67c8f539988f7d0e67f3699d65d82edcbb0858ab903ac` (366 untracked files). Source/docs remained unchanged through functional gates and postgate revision capture.

- Full live graphdb + graphstore session92329: exit0,60 leafPASS/0FAIL/0SKIP, both packagesPASS. Log `/tmp/levara-t15-episode-neighborhood-live-first.jsonl`, SHA256 `60ab8aa442125cc1cde19c8d1d4ab4be3f59e5fba598ab30b8f8e565a74db293`.
- Original native Graph|Entity|AsOf|Temporal|Assertion matrix session78502: exit0,240 leafPASS/0FAIL/1SKIP, all five packagesPASS (graph, orchestrator, graphstore, MCP, HTTP). Log `/tmp/levara-t15-native-final-matrix.jsonl`, SHA256 `2ca0b139efa768e70912a265cf6fedeb0ba635b8b0ba06e864a6593355a31626`. Sole skip: opt-in TestPipeline_DeepSeek_ExtractsGraph; not passing provider-quality evidence. Both SQL mirrors ran.
- Contract session46789: actualexit0; quiet `/tmp/levara-t15-episodes-neighborhood-contract.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- Both changes passed strict OpenSpec; diffcheck exit0 and fresh Gortex detect succeeded. Root alone formatted through stdout/guarded edits and ran commands.

Owned disposable Neo4j/APOC5.26.31 container `levara-t15-20261007-0843`, exact ID `d855b30e1ca156faa57a750219002720ba5a6ffc988f74b135ffaca61ef19091`, loopback Bolt17687. Actual private readiness/empty0/0 preflight and historical startup/path failures remain in the accepted [path evidence](../repair-neo4j-temporal-path-selection/evidence.md). PostgreSQL63530 was ready before gates. No production service, migration or unrelated Docker cleanup.

All Go commands retained `-skip=^TestMemoryREST`; the user-forbidden reproduction was not run. Live gates additionally set owned NEO4J_TEST_* and dedicated LEVARA_TEST_POSTGRES_DSN. Native matrix used the dedicated SQL DSN. GOFLAGS also included `-p=1 -ldflags=-w -count=1`; contract omitted count only.

Independent reviewer parsed both actual JSONL logs/hashes and reviewed combined source/tests; accepted both bounded changes and original T15 within the declared supported scope. Authenticated graph reads remain SQL source/publication/endpoints-authorized, or503 without SQL. Topology adapter success does not establish a new Neo4j ACL. Neo4j seconds and SQL microseconds remain separate precision contracts. No provider quality or deployment claim.

# Temporal path repair — accepted 2026-10-07

This bounded change is accepted3/3 after current live/backend gates and independent review. Original T15 remains open for Neo4j episode writer parity; roadmap23/32. No production deployment, live migration or user-forbidden TestMemoryREST reproduction.

## Scope and frozen source

The Neo4j query applies whole-path visibility on the same shortest-path MATCH before UNWIND; ordering adds relationship ID/elementId to the existing src/tgt/type order. Public DTO/cursor/hop/default/inclusive-second contracts stay intact. New direct-write live fixtures avoid the still-unrepaired episode writer. Existing test cleanup now uses a consumed write transaction; rollback fixture uses a genuinely unsupported property instead of a valid dashed relationship name. Production Query remains read-only and BatchWrite semantics are unchanged.

Final source revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:5dadfbdb1877fc978fe8d3ac6a57e78d88effbd0b28f624f4c8bde10e8fcd50c`. Acceptance metadata is updated only after gates. SQL/HTTP gate preceded the final test-only rollback oracle correction; shared production path source remained unchanged.

## Disposable prerequisite

Pinned image sha256:c7d25c0eeebfe125718d58b72ed5d663c85d0479047733845eb2237c67ce8069. Only owned container levara-t15-20261007-0843, IDd855b30e1ca156faa57a750219002720ba5a6ffc988f74b135ffaca61ef19091, codex.task label, loopback HTTP17474/Bolt17687, new private host data/log/plugin directories and128MiB tmpfs/tmp. Actual cypher-shell31940exit0: Neo4j Kernel5.26.31, APOC5.26.31, nodes0/relationships0 before any destructive helper. Log /tmp/levara-t15-neo4j-empty-ready-preflight.log SHA70c7ff09734e3319b599b025006013bae76220f47319b03e13d9d1a5f36ee7e9; exact ownership/mount log SHA2a9ac03ed06ac309686904bb0a314a3a06bb10abb958176cc461c298a68ad750. Optional Jansi native library warning on noexec/tmp did not prevent successful queries.

Two earlier startup failures were Docker disk ENOSPC: first data/log writes, then Jetty static browser extraction. Logs retained at /tmp/levara-t15-neo4j-start-first.log SHA60457d0e972a7fc14ea673de5b46f69a48db90dba2fb722899ef56f80b13b948 and /tmp/levara-t15-neo4j-start-host-first.log SHA3938db486ae86eb37a1ebf04ca0551425e2c1d3d8caf55326c5c9c3a449227a5. Only these exact owned failed containers removed; no global Docker pruning or existing service touched.

## Observed gates

- First live30306exit1:9PASS/12FAIL/0SKIP. All live cases failed existing cleanup read-access restriction before exercising paths. /tmp/levara-t15-path-live-first.jsonl SHA64f424e90b6b161ae3592b69084594ee730c4c7cbf5d19a2354537959454a1e4.
- After write-helper repair29024exit0:21PASS/0FAIL/0SKIP. /tmp/levara-t15-path-live-write-helper.jsonl SHA5fa5e837f20c21949ab7d7dac2966becb579cb550c4c57fbc1c2c104d4483e3c.
- First full live15792exit1:33PASS/1FAIL/0SKIP, old dashed-name rollback oracle. /tmp/levara-t15-graphdb-live-full-first.jsonl SHAfedd6d7eba4f2d54cf1c7cd725a82197a116abe6e53755bbfc4a3b2e51119e0e.
- Final full live58551exit0:34PASS/0FAIL/0SKIP, packagePASS. /tmp/levara-t15-graphdb-live-full-fixed.jsonl SHAf56ff48c7551cbb9aeb22d06a672c980b3a171637ec185d78b388931d6864ee3. Command `GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST' NEO4J_TEST_URL=bolt://127.0.0.1:17687 NEO4J_TEST_USER=neo4j NEO4J_TEST_DATABASE=neo4j go test ./pkg/graphdb -timeout=3m -json` with the disposable test password supplied. Covers actual longer-route selection, invalid fragments, future/expired/boundary/legacy/history, default4/cap8/exacthops, malformed cursor/missing endpoint, three exact parallel-history page unions and actual rollback/happy writes.
- SQL/HTTP12150exit0:35PASS/0FAIL/0SKIP, both packagesPASS, native SQLite/PostgreSQL document authority and temporal/pagination/held-body semantics. /tmp/levara-t15-path-sql-authority.jsonl SHA6ab94137b264230959a70c8d9b45e380b6daa1e7d47fc9946c7b50dfbd0acb1b. PostgreSQL63530 ready before command; `GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST' go test ./pkg/graphstore ./internal/http -run 'TestSQLGraphStorePath|TestGraphPath' -timeout=3m -json` with existing explicit private PostgreSQL DSN.
- Current full/core contract22392exit0; quiet log /tmp/levara-t15-path-contract-final.log SHAe3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855. Strict34634exit0, /tmp/levara-t15-path-openspec-strict.log SHA2a2fe82c3cedc36c0a60dc54aa4f2c9e001da6389c7d517395236a0e87e4322f. Diff check0 and fresh Gortex detection succeeded.

Independent reviewer parsed final34/0/0 and SQL35/0/0 logs, checked query/helper/oracle and accepted bounded scope. Parallel pagination fixtures have unique IDs: missing/duplicate-ID fallback ordering is source-reviewed, not a separate runtime claim. Storage path success does not establish a new caller ACL or finish episode supersession. Existing authenticated paths remain SQL-protected; original T15 stays open.

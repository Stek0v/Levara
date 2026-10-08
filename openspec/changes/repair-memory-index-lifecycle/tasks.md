# Tasks

## 1. Repeatable outbox publication

- [x] 1.1 Reproduce completed-digest dedupe preventing republication, then reopen completed jobs in both enqueue APIs without replacing pending/running claims; DoD: `go test -count=1 -v ./pkg/memoryindex` passes on SQLite and disposable PostgreSQL with recorded RED, completed-repeat, pending/running dedupe, transactional rollback and stale-Finish ABA tests. Preflight: verified isolated PostgreSQL DSN; no skipped SQL acceptance.
- [x] 1.2 Document repeat enqueue scheduling and retained retry/claim boundaries; DoD: source-to-guide review and `go test ./docs` pass with no assertion of cross-store atomicity.

## 2. Current-state vector effects

- [x] 2.1 Reproduce delayed delete removing a restored active vector, then guard deletes with the existing SQL/collection namespace; DoD: `go test -count=1 -v ./internal/http -run 'MemoryIndex'` includes recorded RED/GREEN, restored/retired/missing/other-collection cases, SQL failure and cancelled/bounded acquisition, both SQL. Preflight: isolated SQL plus temporary collection roots and local provider only.
- [x] 2.2 Hold native PostgreSQL/SQLite-WAL protection until actual vector effect returns and check complete captured upsert classification after embedding; DoD: deterministic independent-WAL-writer and PostgreSQL fence tests, pool=1, cancellation-after-acquisition and late embed/content/type/owner/collection/retirement checks pass without skips. Embedding stays outside SQL locks, including migration dual-write callbacks; the native deferred-hook API preserves the existing synchronous wrapper, and memory shadow publication rechecks SQL after its own Embed.
- [x] 2.3 Document SQL-dominant vector effects, failure/retry limits and coarse short-lock scope; DoD: relevant guide/model statements agree with observed behavior and `go test ./docs` passes.

## 3. Consolidation integration

- [x] 3.1 Prove actual source→abstract→indexed recall→revert, identical source republication, delayed delete and repeated drain/recovery with owner/collection controls; DoD: local provider/vector integration tests pass both SQL and verify physical vector records plus a query absent from memory key/value, so lexical fallback cannot mask missing publication. Retain transaction rollback evidence.
- [x] 3.2 Independently review the combined current diff, run targeted race plus `make test-commit`, `make contract-check` and `openspec validate repair-memory-index-lifecycle --strict`, record RED/GREEN/current hashes and update issue/backlog status; DoD: no unresolved scope blocker, no skipped PostgreSQL acceptance. Close T06 only after its complete DoD; remaining T07 evidence/history work stays open.

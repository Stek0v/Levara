# Design

## Context

See proposal.md for motivation. The existing outbox deduplicates on memory ID, operation and digest; its completion guards already compare running status, attempts and next-run marker. The worker has fixed stripes around vector effects and content/namespace checks for upsert, but delete bypasses SQL and upsert checks can race with SQL writers. Consolidation already commits source deletes/generated upsert and revert effects in its SQL transaction.

## Goals / Non-Goals

**Goals:** Finish the remaining T06 vector lifecycle with runnable SQLite/PostgreSQL regressions and actual indexed recall. Keep pool=1 usable and stale-claim finalization harmless.

**Non-Goals:** General queue generations, new public APIs/profiles/flags, live migrations, paid providers, all other T07 historical/evidence recall work or the unrelated llm_calls counter.

## Decisions

1. Reuse native outbox conflict handling to reopen completed work. Pending/running duplicates remain unchanged. Reopened work gets a fresh existing opaque job ID so resetting its retry budget cannot create a stale-claim ABA match; no schema addition. Pending/running/failed duplicates keep their identity; owner/collection mismatch cannot silently change the namespace. Both transaction and nontransaction enqueue must behave identically. SQL rollback must restore the previous completed row.
2. Keep embedding outside SQL protection and existing vector stripes. Before native collection publication/deletion, acquire a short read transaction on one connection: PostgreSQL table SHARE lock conflicts with authoritative memory writes; SQLite reserves its writer with the existing native zero-row UPDATE pattern before reading, since a read transaction alone does not stop another WAL writer. Query through that transaction to avoid pool=1 self-deadlock. Transaction lifetime follows the actual synchronous vector effect, including observer cancellation; release with rollback afterward. Validate captured upsert content/type/owner/collection against active SQL state, and skip deletes of active rows in the same physical collection.
3. Native collection Insert can synchronously call a migration network hook. Add a concrete deferred-hook method preserving existing validation/stamping/count refresh; the existing Insert wrapper invokes it immediately. Memory workers release SQL and stripe before callback. The memory migration callback captures active SQL and matches canonical memory metadata before embedding SQL key/value, then uses the same short fence and captured-row check for shadow publication. Generic document hooks retain their existing behavior. Callback metadata is borrowed until invocation; the worker's owned JSON bytes remain unchanged.
4. Test actual collections and the existing worker, with a local embedding endpoint. Indexed recall uses query text absent from source key/value; direct vector presence and provider/search observations prevent SQL LIKE fallback from hiding failures. Existing owner lifecycle guards remain the authority boundary.

## Risks / Trade-offs

- PostgreSQL table SHARE is coarse → hold it only around the local vector effect, never embedding; narrower row/gap protection is warranted only after measured contention.
- SQLite WAL writers and pool=1 → native zero-row UPDATE writer reservation, bounded acquisition/statements, one transaction connection and deterministic checks through independent WAL connections.
- Reopening a completed duplicate spends another embedding call → only an authoritative request/reconcile does so; pending/running deduplication is preserved.
- SQL/vector state cannot commit atomically → SQL remains truth and retry/reconcile converges; failures are observable rather than pretending two-store atomicity.
- Stale claim after retry-budget reset → rotate the existing opaque primary ID while retaining the existing CAS predicates and a regression that actually makes attempts coincide.

## Migration Plan

No new tables or columns are planned. Test only disposable database schemas and temporary vector roots. Contract-check confirms public descriptor stability. Production rollout/restart is outside this task; an authorized later rollout can reconcile missing vectors through the existing startup path.

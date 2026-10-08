# Tasks

## 1. Prepared records and authoritative inventory

- [x] 1.1 Add internal attempt salt/deferred retirement and preserve prepared IDs on errors; test same-generation rechunk, partial upsert, cancellation and zero-chunk replacement with `go test ./pkg/workspace -run 'Indexer|Generation' -skip '^TestMemoryREST'` (no external service), documenting internal compatibility.
- [x] 1.2 Add cloned candidate manifest, generation file inventories and persisted exact-ID retirements; test absent versus empty inventory, current-ID protection and cleanup retry with `go test ./pkg/workspace -run 'Manifest|GC|Generation' -skip '^TestMemoryREST'` (no external service); document additive sidecar fields.

## 2. Complete batch publication and retrieval

- [x] 2.1 Publish reindex/reconcile batches once under current authority and project locking, honor DeleteMissing scope and validate source digests; cover second-file read/embed/upsert failure, manifest save failure, same-generation changes and empty branch with `go test -race ./internal/http -run 'Workspace.*(Reconcile|Generation|Reindex)' -skip '^TestMemoryREST'` (dedicated PostgreSQL and SQLite).
- [x] 2.2 Admit workspace results by exact manifest membership and actual collection in local and authenticated modes, refresh lexical state; verify unpublished/wrong-collection exclusion and actual source→citation→read with `go test ./internal/http ./pkg/mcp -run 'Workspace.*(Search|Context|Eval|Generation)' -skip '^TestMemoryREST'` (dedicated PostgreSQL), and update search guide.

## 3. Recovery and watcher

- [x] 3.1 Fence running-job recovery across processes, reload current job before mutation and preserve expiry/target/terminal rules; cover waiting recovery behind completed worker, retries and dead letters with `go test -race ./internal/http -run 'Workspace.*(Recovery|Job)' -skip '^TestMemoryREST'` (dedicated PostgreSQL and native child processes).
- [x] 3.2 Bootstrap watcher from committed digests/persisted pending state; coalesce outstanding branch jobs and clear pending only after current inventory success. Test actual stop/edit/restart, same-size/mtime edits, last-file deletion and blocked-provider repeated scans with `go test -race ./internal/http -run 'Workspace.*Watch' -skip '^TestMemoryREST'` (dedicated PostgreSQL); update watcher guide.
- [x] 3.3 Persist and recover interrupted attempts and history replacement state using existing sidecars; test restart before/after publication, exact attempt cleanup and retained old-tree recovery with `go test -race ./internal/http -run 'Workspace.*(Recovery|Restore|Generation)' -skip '^TestMemoryREST'` (dedicated PostgreSQL and native child processes); document explicit crash boundary.

## 4. Safe retirement

- [x] 4.1 Replace manifest-local collection Drop with exact obsolete-ID cleanup and retry persisted retirements; test two projects sharing collection, delete/save failure and authoritative file preservation with `go test ./pkg/workspace ./internal/http -run 'Workspace.*GC|GCGenerations|Retirement' -skip '^TestMemoryREST'` (dedicated PostgreSQL), updating GC guide.

## 5. Integration acceptance

- [x] 5.1 Freeze combined source and run targeted native SQLite/PostgreSQL race plus `GOFLAGS='-p=1 -ldflags=-w -count=1 -json -skip=^TestMemoryREST' make test-commit`; retain failures and terminal counts/hashes with matched pre/post manifests (dedicated PostgreSQL).
- [x] 5.2 Run generators/contract-check, strict OpenSpec and diff checks; independently audit source, contracts, all criteria and observed logs, reconcile original T20/roadmap/issues/evidence and Task checkpoint (no production service required).

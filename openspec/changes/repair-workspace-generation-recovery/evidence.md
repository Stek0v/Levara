# T20 — accepted 2026-10-07

Original roadmap DoD and all 10 change tasks accepted by independent source/evidence review. Active generation is published once for a complete validated batch; failed preparation preserves prior manifest truth. Attempt records, running jobs, watcher digests and restore journals recover under existing authority and process locks. GC removes exact obsolete IDs and preserves shared collections. Explicit retained historical search keeps source-scoped authorization; ordinary search stays active-only. REST and both MCP transports retain authorization through body Close, including empty results.

Frozen tested revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:8a611dac222a80bbbf73eb3b82cbc3b0fd8f76b863be8714d3af31599093c464` (263 untracked files). Whole-content snapshots before/after native, full and contract gates matched.

| Check | Actual result | Log SHA-256 |
|---|---|---|
| Native race, SQLite and dedicated disposable PostgreSQL | exit 0; 692 leaf PASS, 0 FAIL, 0 SKIP; 3 packages PASS | cd91ae3fb87cafb51a68603b45e5a4ab6a95ebdaa5bfc7eab6163894d5c65f1a |
| make test-commit, S0–S4 | exit 0; 4260 PASS, 0 FAIL, 2 existing opt-in SKIP; 11 packages PASS | 2a24778594e3f3cda53145a59fa4db5b4b48e56ca57fcccd3c406bb609620f85 |
| Full/core contract-check after generators | exit 0 | e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 |
| OpenSpec strict validation | exit 0 | 91391a908874a011972bc4acfbd6557688a6282c1f075c0c3916579ad417b2bb |
| git diff --check and independent review | exit 0; no open acceptance blockers | runtime review receipt d81a2882-f4ae-4221-8ef5-fd00f1cbfd3d |

Commands:
```sh
go test -p=1 -ldflags=-w -count=1 -race -json ./pkg/workspace ./pkg/mcp ./internal/http -run 'Workspace|Confined|Atomic|ProcessLock|Task.*(Executor|Workspace)|Indexer|GCGenerations' -skip '^TestMemoryREST'
GOFLAGS='-p=1 -ldflags=-w -count=1 -json -skip=^TestMemoryREST' make test-commit
GOFLAGS='-p=1 -ldflags=-w' make contract-check
openspec validate repair-workspace-generation-recovery --strict
git diff --check
```

Both SQL dialects used dedicated test stores. Full-gate skips: TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality. User-excluded REST owner-spoofing reproduction remains excluded.

Corner cases covered: later-file provider failure, same-generation retry, missing/deleted/zero-chunk files, legacy unknown inventory, mid-provider external edit and retry, cross-process completed-job preservation, running-job restart, watcher offline changes and coalescing, journal-only recovery, Task lease/read/CAS recovery, partial/inactive/full publication versus dead-letter ownership, exact GC with foreign shared physical/lexical records, stale historical membership and subsequent generic search, real signed JWT/tenant transport bodies with partial Read and Close, zero-hit metadata after access revocation.

Failed diagnostic history is preserved in [central evidence](../../../docs/product/decomposition-evidence-2026-10-05.md): initial package 24/5 then repair 29/0; HTTP build failure; development 57/5; generation 44/0; watcher 35/2; first frozen 625/5; focused 37/0; second frozen 688/4 leaf counts with all seven failing branches retained; focused MCP sender repair 22/0; final frozen 692/0. Earlier failures are not acceptance receipts.

Limits: no cross-store or power-loss atomicity claim; process-crash fixtures model persisted intermediate states, not syscall kill barriers. External editors do not join cooperative locks. Empty-result tests exercise the actual verified-credential transfer boundary; REST provider-error branch is source audited, not claimed as a provider-hook test. No production rollout performed.

Acceptance metadata changes only tasks/evidence and the three roadmap documents. Functional content map before these edits: 1482 files, SHA-256 `869c6423cd6e4985f08e7c3aa81583903a64db334b88d7b2fc77b0704f18419d`; final continuity check must match exactly.

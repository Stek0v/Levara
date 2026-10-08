# T26 — immutable ledger, native leases and completion

Date: 2026-10-07. Original roadmap T26. Scope: defensive changes in existing Task Runtime, no new executor or dependencies.

## Implemented behavior

Receipt/checkpoint replay stores a SHA256 digest of the normalized applied request. Exact retries, including stale base versions, return the existing immutable ID and current task version. Changed applied payload conflicts before side effects. Object ordering and ignored inputs normalize; ordered arrays remain ordered. Historical empty digests are explicitly unverifiable and never guessed, while their existing rows remain readable.

Authoritative validation query, scan and iteration failures reject completion. Completion checks live write authority in a bounded SQL transaction, including already-completed replay, and retains cooperative project locks through commit/rollback. SQLite reserves its writer before project locks; PostgreSQL serializes memory writes before project locks. Cancellation and credential expiry roll back and release SQL/FS guards before subsequent database work.

## Original DoD and observed checks

| Requirement | Native evidence |
| --- | --- |
| Single claim winner | Separate OS processes, common start gate, actual shared SQLite/PostgreSQL rows |
| Expired/reclaimed lease | SIGKILL winner, natural expiry, positive reclaim, stale actor/current stale-version denial |
| Immutable replay | Full authoritative ledger equality on exact retry and changed payload rejection, both SQL dialects |
| Current revision/real artifacts | Actual authorized artifact hash, separate process replacement, denial with unchanged ledger, byte restoration positive control |
| Retained completion fence | Actual post-hash barrier and cooperating writer, pending writer and native lock probes, commit then writer release |
| Blocker/reviewer policy | Dependency cycle, blocker denial/resolution, missing/stale/failed/current reviewer cases |
| Validation unavailable | Offline native tables and scan errors fail closed with unchanged ledger |
| Atomic promotion/rollback | Actual SQL trigger failures in memory insert, completion event and index outbox; rollback preserves version/owner/history; same-version retry succeeds |

## Native final gate

Expanded `go test -race -count=1 -timeout=20m -json -skip '^TestMemoryREST' -run 'Task|Receipt|Completion|Bootstrap|Authority|MemoryCommitEvidence' ./pkg/mcp ./internal/http ./cmd/server`, dedicated PostgreSQL 127.0.0.1:63530 and SQLite.

Terminal exit0, 684 leaf PASS / 0 FAIL / 0 SKIP, all three packages PASS, no failed parents.
Log: `/tmp/levara-t26-final-native-race.jsonl`.
SHA256: `0e13265911f5a1bcaeec30e6a9e3fa9e6fa60c9fb552acd2f3e4565cb8e2eab6`.
Before/after revision unchanged: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2878800f0e59d5be878c32c714581ae6053ff28013150a69c813080d8855b629`, 310 nonignored untracked files.
Native receipt: `be5d7f04-eb55-44c0-82fb-d2dc6272cbab` (runtime v337).
Independent read-only review confirms actual native mapping including trigger-backed promotion rollback; summary alone is not used as a command receipt.

## Development history — preserve failures

| Log under /tmp | Observed terminal result |
| --- | --- |
| levara-t26-replay-first.jsonl | exit1, 0 PASS / 34 FAIL / 0 SKIP |
| levara-t26-replay-repair-race.jsonl | exit0, 34 / 0 / 0 |
| levara-t26-process-schema-first.jsonl | exit0, 8 / 0 / 0 |
| levara-t26-policy-first.jsonl | exit1, 8 / 6 / 0 |
| levara-t26-policy-repair-race.jsonl | exit0, 48 / 0 / 0 |
| levara-t26-posthash-first.jsonl | exit1, 4 / 2 / 0; actual false completion after replacement in both SQL dialects |
| levara-t26-guard-repair-race.jsonl | exit0, 56 / 0 / 0 |
| levara-t26-cancel-authority-race.jsonl | exit0, 6 / 0 / 0 |
| levara-t26-local-serialization-race.jsonl | exit0, 2 / 0 / 0, both dialects |
| levara-t26-final-contract.log | exit2, full generated inventory drift |
| levara-t26-final-contract-repair.log | exit2, core generated inventory drift |
| levara-t26-final-contract-green.log | exit0 after exact native generator output for full/core |

No failed or interrupted attempt is claimed as passing evidence.

## Limits

The artifact guard fences cooperating workspace writers; arbitrary OS writes, external backend replacement and power-loss atomicity between filesystem and SQL are not claimed. Process authority checks use actual production MCP dependencies and private verified caller context but are direct-core, not public HTTP authentication transport proof. REST owner-spoof tests are excluded per explicit user instruction. No live migrations, deployment or production restarts.

Integration first attempt: `/tmp/levara-t26-final-integration.log`, exit2, 1631/1/0 at S1; later stages did not run. Frozen121e7445 before/after equal, SHA b3077c8c8e3d952fae04366775736c3307853074acb74568e49bd76def253447. Expected lock-mode oracle repaired (completion ShareRowExclusiveLock, save RowExclusiveLock); NOWAIT task-row and actual publication positive controls preserved. Focused repair race `/tmp/levara-t26-lockorder-repair-race.jsonl`: exit0,2/0/0,SHA d3ef74ac2cc1b9c011de3795494c47d69b8d319a71110437549ca35d7d24bbc4. Independent source review confirms no weakening.

## Whole integration and acceptance

Whole `make test-commit` with dedicated PostgreSQL and `GOFLAGS='-p=1 -ldflags=-w -count=1 -timeout=20m -json -skip=^TestMemoryREST'` completed exit0, S0–S4 green, all11 packages PASS: **4445 PASS / 0 FAIL / 2 SKIP**, no failed parents.
Log `/tmp/levara-t26-final-integration-repair.log`, SHA256 `156a7e9f44afde188bd31dd9c8c7e31c833b66210453fd383c3f41f3cb6fa0c2`.
Skips: TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality; neither is passed coverage.
Before/after source revision identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:747b68facfc99498750ceaa4ebb549705e8ed9901f223236d23d1cf6799b027e`,310 untracked.
Functional1523-file maps identical: SHA256 `c4e77fa542f821c51385275dceb29eccb4d2f1c9c669af83f011aafefa221760`; only11 named acceptance metadata files excluded by /tmp/levara-t26-functional-map.py.
Whole passing runtime receipt `03272a7d-05a8-4c50-aacc-ea4d03469d21`, v341.

Actual after-integration contract-check session4222 terminal exit0, /tmp/levara-t26-final-contract-after-integration.log. Installed strict OpenSpec and git diff --check terminal exit0.
Independent read-only raw-log/source review confirms the original T26 mapping, both SQL atomic trigger-backed promotion rollback, frozen maps, actual counts and preserved failed history. Its conditional contract requirement is fulfilled by the root's observed terminal exit0.
Original T26 accepted; own change8/8. Roadmap20/32. Remaining tasks and external prerequisites remain open; global Goal/Task not completed.

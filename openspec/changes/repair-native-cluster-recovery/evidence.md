# T23 — native cluster recovery acceptance, 2026-10-07

Original T23 accepted for the bounded experimental native scope. Clustering remains experimental. This acceptance does not establish production readiness, power-loss durability, linearizable reads, or every possible network partition.

## Final observed evidence

Frozen source: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:3bf5d61d051737ab01598890e6c0fe6fff6d2397c91ce2b92a43220d530544e2`.
Native and whole integration before/after revision manifests agree, including all 301 nonignored untracked files. Functional map: 1516 files, SHA256 `1229537889a9e06c795de4f388dc8e0238f5c76d03a7d3676fe3e5509ce3ff5c`; nine explicitly listed acceptance-metadata files excluded.

- Native five-package race: actual exit0, 462 PASS / 0 FAIL / 0 SKIP; cluster, store, production server, backup library and backup CLI all passed. Log `/tmp/levara-t23-alias-final-native-race.jsonl`, SHA256 `13261879f0acf5d1a0f5e070fefdc3db7b894ea657350b96800f3b08132c338d`. Receipt `3b42b68f-2f2b-4fdc-9110-29a924c99915`.
- Whole `make test-commit`: actual terminal exit0, S0–S4, 4374 PASS / 0 FAIL / 2 opt-in SKIP; all eleven package statuses passed, no failed test branches. Log `/tmp/levara-t23-alias-final-integration.log`, SHA256 `0ab239ba972f51608654a0e41f9c7dbe8f49e1053cee1a12091f2e5318210bcd`. Receipt `7a6bd248-baca-4acc-8132-9005ff3f5793`.
- `GOFLAGS='-p=1 -ldflags=-w' make contract-check`: actual exit0, `/tmp/levara-t23-final-contract.log`.
- Installed `openspec validate repair-native-cluster-recovery --strict` and `git diff --check`: actual exit0.
- Native tests use race/count1; whole integration uses count1/20m package budget. Every Go test command excludes `^TestMemoryREST` per explicit user instruction; no REST owner-spoof reproduction was performed.
- Skipped opt-ins `TestDCDVSALoadBaseline` and `TestT11NativeRAGLocalQuality` are not passing coverage.

Dedicated PostgreSQL at 127.0.0.1:63530 and SQLite were exercised by the integration gate. No production service or database was restarted or migrated.

## Original criterion and corner-case mapping

| Criterion / corner case | Actual proof |
|---|---|
| Failover, restart and replay | Three independent native TCP/Bolt/file-snapshot Raft processes; leader killed, replacement elected and new write acknowledged, old member restarted and converged |
| Snapshot durability / torn snapshot | Real Raft snapshot future and native image recovery; malformed/truncated/null/dimension-mismatched FSM inventory rejected while prior nonempty state and native reopen remain intact |
| Network partition, replica lag | Independent primary/replica/proxy roots; proxy drops active HTTP stream and blocks reconnect; offline insert/delete and admission-window recovery converge, then independent WAL-only reopen |
| Crash during WAL apply | Actual incomplete physical frame observed, child frozen with native SIGSTOP and killed; another process reopens acknowledged prefix, appends a new acknowledged write, third process proves it survives |
| Duplicate/update/delete | Exact native inventories, duplicate listener replacement/cleanup, contiguous sequence and missing-delete rejection controls |
| Unavailable quorum | One of three native Raft members cannot falsely acknowledge; uncertain write resolved by later authoritative state |
| Metadata and overflow | Actual queue overflow/resnapshot; larger-than-1MiB aggregate JSON inventory; object/array/bytes/RawMessage/map/string parity with and without listeners, exact source/replica metadata and both WAL reopens |
| Cancellation | Production bootstrap and live worker cancellation join before store close; unsupported multi-shard join fails before data-root creation |
| WAL corruption and limits | Invalid complete or impossible partial frame fails before metadata rebuild and preserves bytes; only feasible incomplete final physical tail repaired/synced; exact 1MiB ID/metadata admitted and reopen succeeds |
| Unavailable source metadata | Nonempty source read failure prevents HTTP/Raft snapshot and checkpoint staging; recoverable WAL unchanged and reopen restores acknowledged metadata; legitimate zero-length metadata preserved |

Independent read-only reviewers examined actual source, native logs and frozen manifests. Final combined-gate review is recorded in the Task ledger; summaries alone are not command evidence.

## Implementation boundaries

HTTP replication supports one DirectNode shard, same-stream versioned snapshot plus contiguous live updates; unsupported Raft/multi-shard bootstrap fails. Mixed old/new peers fail closed. This stream is not a quorum or automatic-failover protocol. Direct store writes outside the DirectNode fence are unsupported.

Raft timing uses a leader lease compatible with its configured heartbeat. Startup validates the existing WAL before rebuilding metadata; writers, snapshot restore and checkpoint agree on representable limits. Historical empty-ID or oversized invalid WAL now fails closed without rewriting data; no silent migration is claimed. The legacy format has no checksum and cannot detect corruption that resembles a valid frame.

Checked complete inventory prevents source read errors from becoming empty metadata. Active batch admission uses the existing native JSON serialization once for both local WAL and replica entry. Queue entries own caller bytes. Worker shutdown joins cancellation before native storage closes.

Not proved: all-voters-alive Raft TCP partition, machine power loss, snapshot installation/compaction under every fault, linearizable reads. These are retained experimental limits, not hidden passed scenarios.

## Preserved development history

Earlier source revisions and fixtures failed; they are not relabeled passing. The corresponding raw logs remain under `/tmp/levara-t23-*`, with immutable digests in earlier Task receipts and this issue ledger.

| Log suffix (prefix /tmp/levara-t23-) | Observed outcome and correction |
|---|---|
| native-first.jsonl | 0 PASS / 3 FAIL: production Raft constructor and HTTP lost-history baseline |
| raft-native-repair-race.jsonl; http-gap-repair-race.jsonl | 2/0 each, focused repairs only |
| cluster-first-full-race.jsonl | 37/2, native stream/fixture failures |
| cluster-second-full-race.jsonl | Build failed: misplaced existence check/missing JSON import |
| cluster-third-full-race.jsonl | Shutdown panic before joined worker fix |
| cluster-fourth-full-race.jsonl | 54/0 |
| final-native-cluster-server-race.jsonl | 247/0, before production shutdown integration |
| shutdown-final-native-race.jsonl | Build failed on obsolete production import |
| native-final-race.jsonl | 340/0 |
| metadata-alias-first.jsonl | 0/4: queued caller-byte aliases |
| accepted-code-native-race.jsonl | 344/0, earlier revision only |
| fsm-inventory-first.jsonl; fsm-repair-full-cluster-race.jsonl | 4/1 then65/0, null inventory rejection |
| interrupted-native-first.jsonl | 0/1, interrupted native apply |
| wal-startup-first.jsonl; wal-startup-repair-race.jsonl | 2/12 then15/0 |
| recovery-final-native-race.jsonl | 442/1, old empty-ID shard fixture conflicts with valid admission |
| admission-focused-race.jsonl | 25/0; selector did not execute interrupted-FSM parent, no such proof attributed |
| admission-final-native-race.jsonl; final-native-boundary-race.jsonl | 455/0 then457/0, earlier source only |
| snapshot-source-first.jsonl; snapshot-source-repair-race.jsonl | 0/2 then10/0, source read failure and empty metadata |
| source-final-native-race.jsonl | 459/0, earlier revision only |
| final-integration.log | Deliberately interrupted exit143 after2548 PASS/0 FAIL/1 opt-in SKIP; S3 incomplete, S4 not run. SHA256 b3783d90b8d7220e8d7bf4c8700200574ddd4a0b970e2da57031caef2d97c8ca. Stopped before batch repair, not a passing whole gate |
| batch-parity-first.jsonl; batch-parity-repair-race.jsonl | 2/1 then3/0, native batch serialization parity |
| batch-final-native-race.jsonl | 461/1: stale byte/batch alias expectation; corrected oracle uses original native serialization before input reuse. SHA256 8f9b8d56a43a1639c2aeba8d18cb4f863ad8352d64592be2a1387faa9ef8967b |
| alias-final-native-race.jsonl; alias-final-integration.log | Final current-source successful evidence above |

All prior failures remain visible in [central issue ledger](../../../docs/product/decomposition-issues-2026-10-05.md). Roadmap acceptance:19/32; the overall 32-task objective remains active.

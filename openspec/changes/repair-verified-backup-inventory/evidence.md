# T22 verified local backup evidence — 2026-10-07

T22 accepts the supported local standalone path. External S3/KMS and non-standalone capture remain unsupported prerequisites; this report does not certify them.

## Native proof

Frozen source revision: 2eb1dca16b0047918185760b41dcb22dee79090a+dirty:285ccdcfc2203da233a59df478791c3e105ae07a594439b5b977b74f3229cd9f (287 untracked); whole native before/after maps equal.

Actual full backup/cmd backup race gate exit0: 78 PASS / 0 FAIL / 0 SKIP. /tmp/levara-t22-final-full-native-race.jsonl, SHA256 ba28417f76e4271b05b485145b316a5e7b6247738fa3985562dc4c3ae7c8421f.
Native receipt 630cffe9-8a98-4a89-b6b0-cf5adc898d17. Independent review receipt c4c56332-6013-4dbf-975f-a37b5f399ce6.

Real CreateVerifiedBackup → VerifyArchive restores current-schema SQLite and private PostgreSQL into independent disposable roots after original SQL/raw/structured roots become unavailable. It verifies native ingestion references, retained workspace generations, raw objects and lexical/vector WAL recovery. A separately opened restored trusted-local application proves nonzero BM25 results, current-generation citation metadata and exact workspace_read agreement, including an empty file.

Known active committed inventory checks all files, including zero-chunk empty/whitespace files, safe paths, archive bytes and chunk membership. Bare SHA256 and one legacy sha256: prefix compare exactly, including mixed inventory/chunk representations. Unknown legacy inventory and inactive retained generations keep their existing semantics. Checksum-valid repacked wrong-reference/missing-object archives fail semantic verification.

A native child stops after observable partial archive bytes, is killed, and leaves the previous successful archive/receipt intact. Both OS leases release; the next actual backup verifies after original roots disappear.

## Limits

Process death during archive writing is proved; power-loss durability, every publication boundary and automatic stale-temp cleanup are not. Restored application proof covers trusted-local BM25, not every search strategy or authenticated transport. No production restart, live migration, S3 or KMS was used.

## Failure history

| Log under /tmp | Actual exit | PASS/FAIL/SKIP | SHA256 |
|---|---:|---:|---|
| levara-t22-native-application-first-race.jsonl | 1 | 0/2/0 | 607053ce744d303a82fa5e9268a896826fc571f68f5fc8ded6bba73c74342fa0 |
| levara-t22-native-second-race.jsonl | 0 | 46/0/0 | 3697b74a0dcf0269db062fc39dadbb7b13b40f7eecc2b3ecb3d0638f48d90836 |
| levara-t22-interruption-first-race.jsonl | 1 | 11/1/0 | 4c8459a0d91ca1b4fbc60d344c447510556b7b93bebd52a80658813dac10b162 |
| levara-t22-interruption-repair-race.jsonl | 0 | 1/0/0 | 414b6153f32d9d43a4c8a19c7b2da99234c6d661e9dcbfc68f705d02df2b384e |

The first native failures exposed the real bare-digest contract and a missing browser-session schema in the PostgreSQL fixture. Both were repaired without weakening authority. The first interruption failure was the BSD Go wait helper classifying actual SIGSTOP as Continued; the oracle now checks exact native PID/stop bits/signal.



## Combined integration failure history

The first frozen S0 gate exited2 with36 PASS/1 FAIL/0 SKIP because of a broken central-evidence self-link; /tmp/levara-t21-t22-final-test-commit.jsonl, SHA256 fb3c11586973f7bbeb1ef7453609e52f0eb4799b7ff86d17c6739063d85645fc, receipt46e7472d-6e20-4795-ad3d-58d120231042. Only that metadata link was repaired.
The restarted frozen b7000fed gate exited2 with4152 PASS/1 FAIL/2 opt-in SKIP. Only the immediate SQLite concurrent pool InUse oracle failed; S4 did not run. /tmp/levara-t21-t22-final-repair-test-commit.jsonl, SHA256 cd0b5ad71704b8577b1dfba8f1d8190f088101e2f4543d6a3aaae847cfa9dc40, receipt6142c1c5-455c-44c0-8639-2c6c9d85b536. Both whole pre/post maps equal.
The test-only repair reserves the entire native two-connection pool simultaneously and retains two idle connections before workers start, preserving strict final zero-InUse and convergence assertions. database/sql includes pending opens in InUse; raising MaxOpen alone retained the previous MaxIdle ceiling. Actual race count20 exit0:40 dialect leaf executions, no FAIL/SKIP or failed parents; /tmp/levara-t21-pool-oracle-repair-race.jsonl, SHA256 24cc9c02e53b8e7dec87f74505d8c527a0d98de04c95befd72b3f2e8f90ee6c8, receipt17d142d4-512f-4fb1-9f30-39246cc15f87.
Independent source/functional-map review confirmed ONLY sync_convergence_test.go changed: prior native production/backup receipts carry forward by exact byte continuity; no old failed gate is relabeled passing.

The next frozen full command hit the Go HTTP-package aggregate default10m budget: exit2,4125 completed PASS/0 completed FAIL/2 SKIP, but package FAILED and watcher PostgreSQL branch aborted after only1s. /tmp/levara-t21-t22-pool-repair-final-test-commit.jsonl, SHA256 d94be4319384c704817d4fdb27f9dc9bb2edd188fab7a5a62a6466f0ab9b1e74, receipt f4f0f0bc-5d6f-4bcf-8d4c-3780609dd644. Whole before/after513397 revision equal; this is not a passing gate. S0-S2 were successful on that identical revision. Only S3 is rerun with20m aggregate budget, per-case deadlines unchanged, and S4 runs afterward. Acceptance is assembled from actual per-stage commands on the same frozen source, never a fabricated make exit0.


## Final assembled integration acceptance — 2026-10-07

Frozen revision: 2eb1dca16b0047918185760b41dcb22dee79090a+dirty:5133979cef892f799b39023a7446f6d04330fb2222c8751725460932cca27cb7. Whole before/after maps agree; no source edits during commands.
S0–S2: nine packages passed, 1713 leaf PASS, from /tmp/levara-t21-t22-pool-repair-final-test-commit.jsonl. That whole command failed on the later HTTP aggregate budget and is not relabeled passing.
S3: actual separate command exit0, 2440 PASS / 0 FAIL / 2 opt-in SKIP; /tmp/levara-t21-t22-final-s3-budget-repair.jsonl, SHA256 75fc0534e8fae4694ee322b1db56505e53fef17124118badb59106a8b82ee16b.
S4: actual separate command exit0, 188 PASS / 0 FAIL / 0 SKIP; /tmp/levara-t21-t22-final-s4-budget-repair.jsonl, SHA256 21f4a2bdea8411ade994538bc53b9b3073fed7e1a0e6406a6d45d861411813c0.
Assembled S0–S4: 4341 PASS / 0 FAIL / 2 opt-in SKIP on identical source; no failed parents in successful stages. Go commands used -p=1 -ldflags=-w -count=1; S3/S4 used -timeout=20m. All test commands exclude ^TestMemoryREST per user instruction.
GOFLAGS='-p=1 -ldflags=-w' make contract-check: actual exit0, /tmp/levara-t21-t22-final-assembled-contract.log.
Both OpenSpec changes passed installed CLI strict validation on the same revision (receipt374f64f1-d204-4724-885e-b03c0cea1162).
Functional map: 1504 files, SHA256 e09d6665635e8685f8f77aee205dbdab2b53ec06f848841c03b2da07985fe6fe. Only the seven acceptance metadata files are excluded. Native backup and production sync files retain exact bytes from original native receipts; only concurrent pool test warmup changed, separately proved by40 race executions and independent review.
REST and MCP sync dispatch were source-audited: verified live active global-superuser authority precedes remote I/O/imports; ordinary write credentials cannot invoke instance replication. No REST owner-spoof reproduction was performed.

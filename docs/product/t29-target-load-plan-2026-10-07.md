# T29 target resource/SLO plan — 2026-10-07

Declared before any load result. Original T29 measures target server admission, foreground service and durable recovery; semantic quality remains the separately accepted T11 gate.

## Fixed workload and environment

- Mac14,2: ARM64,8 logical CPUs,16GiB RAM. Raspberry Pi berry8gb: Linux6.12.93 aarch64,4 CPUs,8063MiB RAM,2047MiB swap; initial swap0 and temperature38.05°C. Read-only preflight completed; neither production service is restarted.
- Build Darwin/Linux ARM64 binaries from one frozen combined source revision; record SHA256 and go version metadata. Pure-Go SQLite and no OCR tag; dedicated SQL test databases when the declared audit/outbox phase needs PostgreSQL. Keep isolated roots/ports/credentials and record exact owned PIDs.
- Controlled embedding provider runs separately in each sandbox with identical frozen implementation/configuration and dimension768. This is a deterministic admission/deadline/backlog control, not semantic quality or Pi model inference performance. Available native providers differ (Mac embeddinggemma768, Pi potion256); those are not compared as one model.
- Freeze a generated synthetic corpus before execution:10,000 unique memory keys, identical room/hall/text generation and SHA256 manifest on both hosts. Same settings: GOMEMLIMIT1GiB,1shard, explicit auth, no proxy/gRPC, recorded provider timeout/gate/background concurrency. All traffic uses verified disposable identities; no REST owner-spoofing reproduction.
- Before first measured load: gate capacity4, background concurrency2, HTTP/search deadlines5000ms, background/provider deadlines30s. Mixed500RPS is heartbeat400 + recall50 + save50; use the same controlled provider delay30ms during the10,000-key seed/backlog phase, reset to0 for drain/recovery. Separate generator reports retain each component latency/errors and scheduling lag. Native startup must prove disabled audit has no SQL projection and enabled audit creates JSONL plus projection.
- Reuse benchmark/mcp_load.py with separate MCP sessions and owner-scoped tool arguments; collect actual owned server PID RSS/CPU and authenticated /status snapshots. Do not use run_benchmark.py systemctl restart or process-name RSS aggregation.

## Predeclared gates

| Phase | Workload | Acceptance |
|---|---|---|
| Warm baseline | heartbeat500RPS/60s, audit disabled | errors≤0.1%, achieved≥98%, p95<200ms |
| Durable audit | heartbeat500RPS/600s, audit enabled/SQL projection | errors≤0.1%, achieved≥98%, p95 overhead≤2ms; durable IDs retained |
| Foreground during backlog | heartbeat/recall/save120s while10,000 corpus saves enqueue embedding work | foreground p95<200ms, errors≤0.1%, explicit per-request outcomes; backlog actually observed |
| Provider failure/deadline | owned provider held or delayed beyond declared deadline | bounded response≤configured deadline+1s; no falsely published vector, committed SQL source retained |
| Recovery | stop/restart only the owned sandbox server PID with same data root | acknowledged IDs/content retained; no duplicate durable audit IDs; pending jobs drain within300s after healthy provider |
| Resource | all phases | owned server peak RSS≤1GiB, no OOM/process crash; queue/dead-letter state measured and no unexplained terminal loss |

These preserve the existing release load targets. If a target is missed, record FAIL and diagnose; do not silently lower thresholds after results. Report scheduling lag separately and retain all failed attempts.

## Coverage boundaries

Scheduler Running/Busy and pressure hysteresis are directly checked in native governor tests, since the existing public status DTO does not export those scheduler fields. Status is not proof of scheduler execution. A running background provider batch is not preempted by the foreground gate; stalled-provider measurements must include that bound. Actual binary restart, source inventory and queue drain are hardware-run checks, not inferred from unit tests.

No paid provider, model installation, production deployment, live migration, systemd mutation or unrelated cleanup is required or authorized by this plan. Host services already running are recorded as shared-host interference rather than removed. Final evidence distinguishes controlled server SLO from full inference resources and vendor certification.

## Measurement diagnosis declared before corrected run

First Pi measurement through the Mac SSH tunnel failed audit overhead: p95 increased92.217→123.830ms (+31.613ms), with generator scheduling lag p95=127.781ms/p99=649.327ms. Keep this FAIL and reports. Both workloads still completed at499.8RPS/0errors; transport/generator attribution is not yet established. Corrected Pi isolation runs the identical mcp_load.py directly on Pi loopback: repeat audit-disabled500RPS60s, then audit-enabled500RPS600s, using the same binary/data/database/provider and unchanged <=2ms overhead/<200ms/error<=0.1%/achieved>=98% gates. Installed Pi Python3.11.2/aiohttp3.13.5 and Mac aiohttp3.14.2 are recorded; each within-host comparison uses its unchanged runtime. Do not compare a corrected local audit result against the earlier tunneled baseline. Read the existing SQL audit count before/after the disabled repeat rather than requiring an absent table in a reused owned database.

Local Pi generator with the same tunneled PostgreSQL also failed its baseline:449.627RPS, p95212.140ms and scheduling lag p956159.661ms, despite0errors. Keep that FAIL; no valid baseline exists for the remote-SQL topology. Source confirms required per-call SQL authorization and heartbeat queries; do not bypass those checks. For the declared local standalone hardware acceptance, use a new owned PostgreSQL on Pi loopback19332 and fresh private database/application root, keeping the same server binary, provider, corpus, auth, deadlines/gate and thresholds. Cached postgres16-alpine ARM64 image is pinned by IDsha256:d394728dee24b7791f1969c683f8b8410fe57450713a1db2aff5a500f0f98ab0 (RepoDigestsha256:4e6e670bb069649261c9c18031f0aded7bb249a5b6664ddec29c013a89310d50); no package/image download. Own container limit768MiB/2CPUs, shared_buffers64MiB/max_connections50/max_wal_size256MiB; separate service footprint is reported. Fresh hardware readback:40.8°C/1.8GHz/throttled0x0/MemAvailable4915488KiB. This measures local standalone and does not certify the failed remote-SQL-over-SSH topology. Declare this correction before native local-SQL baseline60s/audit600s; use matching within-host generator and storage for the pair.

## Next bounded Pi diagnostic — declared before execution

Keep the failed gate4/BG2 measurements and all original SLO thresholds. A fresh paired Pi audit-disabled60s/audit-enabled600s experiment may change only `LEVARA_MEMORY_INDEX_WORKER_INTERVAL` from250ms to1s, with a new recorded binary revision, private database/root and the same provider/corpus/auth/gate configuration. Complete startup audit import and ordinary warmup before measurement; retain generator lag, owned process resources, SQL queue/audit state and native profile observations. This tests idle claim-polling contention (eight workers, approximately32→8empty claims/sec), not a proven root cause. Successful claims still drain immediately, so this knob does not cap active backlog throughput. A passing audit diagnostic alone does not close the mixed/drain gate or certify T29.

# T29 controlled hardware observations — 2026-10-07

T29 remains open: Mac passed the declared controlled-provider workload; Pi failed audit overhead, mixed foreground and initial drain gates. No thresholds were reduced.

Measured binaries use revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:cdf14ba58b175ceac553f693791403bd55f9f9fd04a96e343b7d14f4b6ef1aa7`. Subsequent lint cleanup and ledger changes have their own revision; these hardware results do not certify a later binary. Deterministic768-dimensional embeddings test admission/durability, not semantic quality or complete inference resources.

| Gate | Mac | Pi |
|---|---|---|
| Baseline heartbeat500RPS60s | 499.752RPS; p951.9ms; 0errors | 498.323RPS; p95129.317ms; 0errors |
| Audit heartbeat500RPS600s | 499.973RPS; p951.846ms; 0errors | 494.564RPS; p95139.725ms; 0errors |
| Audit p95 overhead≤2ms | −0.054ms, PASS | +10.408ms, FAIL |
| Mixed heartbeat | 399.77RPS; p953.394ms; 0errors | 220.354RPS; p95517.341ms; 440errors |
| Mixed recall_memory | 49.958RPS; p9562.215ms; 0errors | 45.491RPS; p954956.842ms; 460errors |
| Mixed save_memory | 49.98RPS; p955.466ms; 0errors | 47.864RPS; p954573.179ms; 354errors |
| Seed/successful mixed ACKs | 10000+6000, PASS | 9936+5646;64seed/354mixed responses failed |
| Initial healthy drain≤300s | 18.423s/all16000completed, PASS | 301.866s:13174completed/2566pending/6running, FAIL |
| Held-provider foreground bound | 5.005s, PASS | 5.002s, PASS |
| Same3jobs/native768 recovery after healthy release | 0.053s, PASS | 0.112s, PASS |
| Prior authoritative SQL sources retained | 16000exact rows, PASS | 15746exact rows, all successful ACKs retained;164unacknowledged rows separately accounted |
| Audit after restart | 370408canonical/670679SQL unique;670648prior IDs retained | 369549canonical/SQL unique;369545prior IDs retained |

Pi15746 sources include all9936seed+5646mixed acknowledged values and164durable rows from unacknowledged failed responses. These164 are not counted as successful ACKs. Postrestart queue completion does not replace the original300s FAIL. Native recovery timing starts after server readiness/provider release; startup/WAL replay is separate.

RSS observations: Mac404 valid samples peaked256737280bytes; Pi307samples peaked184877056bytes. Fixed observed PIDs cover audit/mixed measurements; they do not provide postrestart continuous resource evidence. No claim that negative Mac overhead is a causal improvement.

All failed phases/logs are retained in the issue ledger I244–I253. Postrestart profiles show WAL/SQL/collection-lock contention alongside audit import, but do not establish the cause of the original mixed failure. BG concurrency is per-call batch concurrency, not reserved foreground slots.

## Immutable report hashes

- Mac `baseline-report.json`: SHA256 `9a4852abfacce40eafec86961b7fbb6e55dbf0cd6aac46832420537222233cd5`.
- Mac `audit-report.json`: SHA256 `9c465cf4549cf76d37839db4592a5bcbbc3f95405e88045bce2d100580e9847b`.
- Mac `seed-report.json`: SHA256 `9620c502a91ac0e461b0132a2ee030df6413408aa07c4774aadcc001b0209c69`.
- Mac `drain-proof.json`: SHA256 `b09e6593fc490e6c6443211107e1db04ccffba88aeb56235cc27b438d045c0b2`.
- Mac `ack-source-retention.json`: SHA256 `579e31061bb324603937b72ab7f79a3ddff583e0f0533d4371b3752d876ee0ea`.
- Mac `deadline-proof.json`: SHA256 `00e5d0d5d5c46a666a497d351dea0392f5b71ec97886e5691ce026127b7ceb71`.
- Mac `deadline-recovery.json`: SHA256 `6384824f1f4ede700085b8cc31ee9181c7fc9bd24b109de68b5dbb04e7eb4a1c`.
- Mac `recovery-phase-exits.json`: SHA256 `bbda6dc766c58e329527eb6595673347bb95456a738c6d37825170982255bc22`.
- Mac `restart-retention-proof.json`: SHA256 `2a504c8a0c1035ed1c8f796e181ca626efb55a0e13f43c5b88e6067c0c822420`.
- Pi `baseline-report.json`: SHA256 `be50f00a11b457687115a8e782e956e177b4de1fbbdf7ed7d4ff60facd69b402`.
- Pi `audit-report.json`: SHA256 `5ccbfdae7d541eb1adac93e0428226d628262848e9b6bde1982802583947384a`.
- Pi `seed-report.json`: SHA256 `b482c317c2d3199dbc968f4834f0d741dee641ad422bc6314467215fd7b5a80d`.
- Pi `drain-proof.json`: SHA256 `c2896779115eb888a2df07a8fbdcace6825be19b954f329a726088d0359eeec5`.
- Pi `ack-source-retention.json`: SHA256 `d11916406ea22ec79a31d2c97151e918bc9a9edf1aff7e5f60ea82980fb71679`.
- Pi `deadline-proof.json`: SHA256 `852b30316511118cc5d0b0c87bfae1cfd005841cfee8909947996a573667f0b0`.
- Pi `deadline-recovery.json`: SHA256 `80feb34576e2101a41591c0a5e277c75c512c3b76725ca4620abcb72247416c3`.
- Pi `recovery-phase-exits.json`: SHA256 `200cc62baf9150fbde384e9a783a29209b3a0823b88f52c818165e1cb50d3f62`.
- Pi `restart-retention-proof.json`: SHA256 `94457a89324e08a000f06cbf16678ac469a6afacebf2519b36901a799e0d38c6`.


## Pi storage diagnostic — declared before execution, 2026-10-07

The idle1s pair passed: baseline30000/0errors498.506RPS p95149.594ms; audit300000/0errors499.885RPS p95113.224ms, observed delta−36.37ms. This is not causal proof that slower empty-queue polling improves latency. Under backlog the same configuration failed: seed8980ACK/1020timeouts; mixed heartbeat182.094RPS/p95893.391ms/1059errors, recall41.923/p955675.448/833errors, save39.176/p956794.474/1053errors; initial drain301.809s with14297completed/1running, FAIL. No failed ACK is counted successful.

After seed had already failed, the remaining mixed phase was instrumented for diagnosis and cannot be accepted as an uninstrumented performance pass. Its native PostgreSQL snapshot shows22backends waiting WALWrite and1WALSync; Go snapshots show22/24transactions committing and174/171SQLpool waiters, with0count-refresh and0native-vector FlushAsync waiters. These observations localize this episode to durable PostgreSQL WAL I/O and pool starvation; earlier runs may have other contributors. Preserve all logs/profiles and failed thresholds.

Actual sandbox storage is `/dev/mmcblk0p2` (SD); existing writable `/mnt/nvme` is `/dev/sda1` (ext4,192GiB free when observed). Next trial creates a fresh isolated PostgreSQL container/database and native vector root on this existing NVMe volume. It migrates no existing data. Keep binary SHA256 `59fc8b4ad75a09cabe7a8542c27617ea62b62b3ba661d8c0d95cdd9b5c420139`, worker interval1s, ordinary browser-session identity, provider/corpus/gate4/BG2, deadlines and all SLO unchanged. Capture actual `fsync=on` and `synchronous_commit=on`; never relax durability to meet SLO. Use healthy readiness and121warmups, paired60s/600s at500RPS, then10kseed/mixed/drain and exact-source/restart/native-retention checks. Do not close T29 from the audit pair alone or erase the SD limitation if NVMe passes.

Clone the **actual** original PostgreSQL profile: image16.13ARM64,2CPU quota,pids128,shared memory64MiB,shared_buffers64MB,max_connections50,max_wal_size256MB,min_wal_size80MB. Inspect revealed Docker Memory=0 (no explicit hard limit), rather than the previously assumed768MiB limit; report this mismatch explicitly. New loopback port19335 and task-owned NVMe root are isolated from existing databases. Compare resource observations only for the recorded live PID; source remains frozen during measurements.

Artifacts: local `/tmp/levara-t29-pi-idle-evidence-20261007`; remote task-owned idle diagnostic root. Audit outcome SHA256 `2f789387fa4b1228e3c03d0838f5510fd8b4abc0032cc02ab0de95afc39da2f6`; baseline outcome `2654ff77ed19d19bc215f1fb0707a2ae1f8d6dffd98580a716978ab018699225`. Prior atomic-final binaries/reports remain separate.

## NVMe observed diagnostics and strict next acceptance (2026-10-07)

The initial NVMe audit pair passed: baseline498.521RPS/p95137.449ms and audit499.849RPS/p9597.522ms, both0errors. A negative measured overhead is not causal proof of improvement. The workload had10000seed+6000mixedACKs/0errors, but mixedHB331.782RPS/p95301.247ms,recallp95318.252ms,savep95353.384ms failed. Drain completed16000jobs in approximately64s; native recovery and exact16000source/370000prioraudit-ID retention passed. Foreground/background deadlines were5.001493s/30.115937s; three held jobs recovered with768-dimensional native vectors0.135985s after healthy provider release.

Review found that these NVMe helpers actually set provider delay0ms during seed/mixed, contrary to the frozen30ms backlog requirement. Treat these measurements as diagnostics; neither restart retention alone nor an unasserted RSS sample validates full ACK correctness/resource DoD. Keep the original failures and this fixture mismatch visible.

A20s mixed diagnostic CPU profile attributed21.26% cumulativeCPU to repeated full MCP schema construction on admission; SQLpool waiters also concentrated in active-user/epoch/browser-session checks. Admission now uses a private name set with live profile/feature flags; discovery schema construction and all auth boundaries remain unchanged. Focused race/parity checks passed. Its uninstrumented larger-corpus diagnostic (initial22003rows) still failed: heartbeat372.506RPS/p95269.451ms,recallp95307.219ms,savep95348.107ms, all0errors. It is not a matched initial-corpus acceptance comparison.

Next fresh strict trial uses a new disposable database/native root on the existing owned NVMe container, preserving its actual2CPU/no explicit Docker memory cap/PostgreSQL durability configuration and unchanged SLO. Record the new source manifest and binary digest before execution. Provider delay is30ms during seed and mixed, reset0 before drain clock. Require actual mixed-phase backlog; reconcile all10000seed+6000mixed ACKs to exact SQL values/scope/source/job IDs; require≤1GiB peak live-server memory and no failed PID samples/owned PostgreSQL OOM; explicitly assert baseline audit-off adds no SQL events. Then repeat native deadline/restart/source/audit retention. T29 remains open until every gate passes.

Fresh target acceptance is now predeclared with source revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:d2cf7ff87d1bc5fcefeca422913a37512ff1cee0d075e63af9eabd355baf0984`, functional1306-file digest `b7a13654ee3e477266baaa6882b2cec83816147afc66e7aef5f778ece19653fd`, and binary SHA256 `cce568600a0658ab8b24cf2597eacbc680d41d802b29e809e18881ee83147a76`. This combines admission-name optimization and fresh one-query epoch/SID validation at the same auth boundaries. SQLite/PostgreSQL race tests and independent reviews passed, including JWT expiry after a held SQL connection. SQL-error classification issueI249 remains separate.

The larger28003-row30ms diagnostic failed (HB366.012RPS/p95287.471ms,recall384.659ms,save393.245ms,all0errors). Its changed corpus is not the frozen target; keep this FAIL rather than using it to accept or reject an unmeasured fresh10000+6000 target. The fresh full protocol runs60sbaseline/600saudit,10000seed/6000mixed,drain,ACKreconciliation,native recovery/retention and resource checks in a new disposable database `levara_t29_pi_dispatch_20261007` and native root `dispatch-acceptance-20261007` on the existing ownedNVMe PostgreSQL container. Record current PostgreSQL settings again; migrate/delete no existing database. Expanded helpers passed independent read review, including CPU counters and authenticated `/api/v1/status` snapshots. All phase exit codes participate in acceptance; T29 remains open until actually measured.

## Final strict acceptance — 2026-10-08

The fresh strict run on the owned Raspberry Pi NVMe sandbox passed every predeclared gate with the original thresholds and provider delay. Binary SHA256 was `687b9c438dc7f736114b7fa21d45deaade57f4af3006167d76ea3f5b97f494d7`; evidence root is `/mnt/nvme/levara-t29-owned-20261007-1035/dispatch-acceptance-workers2-final-20261008`.

- Audit pair: baseline 498.891RPS/p95 1.809ms and audit-on 499.900RPS/p95 2.161ms, both 0 errors; overhead 0.352ms.
- Backlog mix with 30ms provider delay: heartbeat 398.981RPS/p95 46.406ms, recall 49.830RPS/p95 154.902ms, save 49.884RPS/p95 127.322ms; all 0 errors.
- Durability: exactly 16,000 acknowledged source values/scopes/job links reconciled; all 16,000 jobs drained in 67.510s; prior source rows and 370,000 audit IDs survived restart without duplicates.
- Failure/recovery: each of three isolated jobs observed a provider deadline while its SQL source remained committed and unpublished natively; the same job/memory identities recovered into three 768-dimensional native records after restart and healthy release.
- Resources: 432 valid samples, 55 during backlog, peak owned-server RSS 212,664,320 bytes, no owned PostgreSQL OOM.

The product root cause was a hard-coded pool of eight memory-index workers on a 2-CPU host. The worker pool now defaults to two and is explicitly configurable. HNSW search uses the measured 6/48 beam with deterministic top-10 recall 0.900 and coalesces only simultaneous identical traversals. No SLO, workload size, durability setting, or error allowance was weakened.

The primary audit JSONL is intentionally best-effort and dropped 223 projection lines under the 300,000-request audit load; durable SQL audit identity retention passed. Phase isolation therefore uses four stable line/byte/mtime samples rather than an invalid exact-JSONL-count requirement. Historical failed runs remain recorded above and in I244–I267.

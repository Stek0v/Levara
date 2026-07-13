# MCP load-test report — 2026-07-13

Host: local Mac, 8 CPU, 16 GiB RAM, PostgreSQL on loopback, Potion Code 16M
embedding sidecar (256 dimensions). Levara used one shard and a dedicated
database/data directory.

## Results

| Profile | Calls | Errors | Rate | p50 | p95 | p99 | Schedule lag p95 |
|---|---:|---:|---:|---:|---:|---:|---:|
| MCP baseline, audit JSONL disabled | 30,000 | 0 | 499.8 RPS | 0.58 ms | 2.65 ms | 9.81 ms | 0.29 ms |
| Audit + SQL projection, 10-minute soak | 300,000 | 0 | 499.9 RPS nominal | 2.39 ms | 228.93 ms | 631.59 ms | 25,147 ms |
| Outbox burst | 10,000 saves | 0 | 199.9 RPS | 1.33 ms | 7.17 ms | 69.61 ms | 0.66 ms |
| Mixed heartbeat | 8,400 | 0 | 70 RPS | 0.75 ms | 1.90 ms | 5.52 ms | 1.21 ms |
| Mixed recall | 2,400 | 0 | 20 RPS | 12.40 ms | 24.48 ms | 47.58 ms | 1.20 ms |
| Mixed save | 1,200 | 0 | 10 RPS | 1.15 ms | 2.43 ms | 5.92 ms | 1.21 ms |

The nominal soak rate is total calls divided by wall time. It is not a stable
arrival-rate pass: the generator's scheduling delay proves that requests
queued for tens of seconds near saturation.

## Durable audit findings

- JSONL retained all 300,000 events.
- The live SQL projection contained 212,143 events at the end of the second
  soak. Its bounded queue correctly kept MCP non-blocking, but dropped 87,857
  projection attempts while PostgreSQL could not sustain per-event inserts.
- A restart test on the first soak scanned 300,000 JSONL records, restored all
  missing events idempotently, and produced no duplicate IDs.
- Importing a 300,000-line tail delayed readiness by about 32 seconds.
- Average Levara RSS during the first soak was about 42 MiB; peak was 55 MiB.
  Average CPU was about 63% of one core, peak 123%.
- The audit p95 overhead target of 2 ms is **not met** at 500 RPS.

## Embedding outbox findings

- 10,000 SQL memories and 10,000 jobs were created.
- All jobs reached `completed`; there were no pending, failed, or dead-letter
  jobs after the burst.
- Eight workers sustained the sidecar and drained jobs while save p95 remained
  7.17 ms.
- The mixed workload added another 1,200 jobs without queue growth.

## Required optimizations before the 500 RPS audit gate can pass

1. Batch the audit projection (multi-row insert or PostgreSQL COPY), flushing
   by count and a short maximum delay. Per-event `Exec` is the primary limit.
2. Persist an importer checkpoint and import the tail asynchronously after the
   listener becomes ready. Expose replay progress and lag in health details.
3. Make HTTP access logging configurable or buffered. High-rate access logs
   can introduce output backpressure independent of the audit writer.
4. Report separate `accepted_rps`, `completed_rps`, concurrency, queue lag and
   scheduling compliance. Never treat total/wall-time alone as proof of a
   constant-arrival-rate pass.
5. Repeat the same test on the target Pi/Qwen hosts; local results do not
   establish their disk and PostgreSQL limits.

Release status: mixed agent traffic and embedding outbox **pass**. Durable
event retention and recovery **pass**. The 500 RPS / 10-minute audit latency
and live-projection-lag gates **fail** and require batching/checkpoint work.

## Optimized rerun

The failed gate was followed by three implementation changes: SQL projection
batches of up to 256 rows with a 25 ms maximum flush interval, a buffered and
asynchronous durable JSONL sink with graceful drain, and an optional
`LEVARA_HTTP_ACCESS_LOG=false` high-throughput profile. Replay now starts after
the HTTP listener becomes ready and its state is exposed by the analytics API.

| Profile | Calls | Errors | Rate | p50 | p95 | p99 | Schedule lag p95 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Optimized audit calibration | 30,000 | 0 | 499.9 RPS | 0.55 ms | 1.87 ms | 7.45 ms | 0.46 ms |
| Matching optimized baseline | 30,000 | 0 | 499.8 RPS | 0.55 ms | 2.00 ms | 7.17 ms | 0.46 ms |
| Optimized audit soak | 300,000 | 0 | 500.0 RPS | 0.54 ms | 1.06 ms | 5.07 ms | 0.46 ms |

At shutdown the combined calibration + soak contained 330,000 unique SQL
events and the projection reported queue depth 0, dropped 0, and errors 0.
JSONL was gracefully flushed. Restart readiness was observed in about 20 ms
while the 330,000-line replay proceeded asynchronously; replay completed
idempotently with 330,000 rows and 330,000 distinct IDs.

Final release status after optimization: the 500 RPS / 10-minute audit latency,
durability, live projection, and recovery gates **pass** on the local Mac.
Target-host Pi/Qwen canaries remain required before a fleet-wide rollout.

## Remote canary: Qwen 64 host

Target: `10.23.0.64`, service bound to `127.0.0.1:8080`, PostgreSQL container
on `127.0.0.1:5433`, Qwen embedding sidecar healthy. The production startup was
updated to disable synchronous HTTP access logging and enable MCP JSONL audit at
`./data/audit/mcp`.

| Profile | Calls | Errors | Rate | p50 | p95 | p99 | Schedule lag p95 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Qwen local calibration, 50 RPS | 1,500 | 0 | 49.9 RPS | 2.38 ms | 2.74 ms | 2.98 ms | 2.05 ms |
| Qwen local calibration, 200 RPS | 12,000 | 0 | 200.0 RPS | 2.12 ms | 2.61 ms | 2.79 ms | 1.03 ms |
| Qwen local calibration, 500 RPS | 30,000 | 0 | 499.7 RPS | 2.32 ms | 2.81 ms | 3.09 ms | 1.17 ms |
| Qwen local soak, 500 RPS / 10 min | 300,000 | 0 | 500.0 RPS | 2.35 ms | 2.85 ms | 3.14 ms | 1.17 ms |

During the 10-minute Qwen soak the analytics read-model stayed current:
`queue_depth=0`, `dropped=0`, `errors=0`. Observed server RSS grew from about
394 MiB to 408 MiB during the run; CPU reached about 84% of one core near the
end of the soak. The service remained healthy after the run.

The canary exposed a durability gap in the buffered MCP JSONL writer: after
the 300,000-call soak, the SQL projection had accepted 345,000 events from the
combined Qwen calibration/soak runs, but the active JSONL file had only 344,832
visible lines. The missing 168 events were still in the process buffer because
the writer flushed only every 256 lines, on rotation, or on close. A periodic
one-second flush was added to the MCP `FileLogger`, with a unit test covering
tail visibility before `Close`.

After deploying that fix and restarting Qwen, the JSONL file contained 345,000
lines. The asynchronous replay imported 345,000 JSONL events idempotently while
the persisted summary remained 346,500 total events, so restart did not create
duplicates. A post-fix one-event check increased the JSONL line count from
345,000 to 345,001 without closing the process, confirming that the buffered
tail now reaches disk promptly.

Pi canary status: blocked. `10.23.0.53` returned `No route to host` for both
ICMP and SSH, so no meaningful Pi load result was collected in this run.

# MCP load-test release gate

> Acceptance targets, not achieved performance results. Observed runs and
> their limitations belong in [testing](testing.md).

## Scope

A qualifying release load test should cover the MCP transport, durable JSONL audit writer,
asynchronous SQL audit projection, dashboard aggregation, PostgreSQL-backed
memory outbox, embedding worker, and owner-scoped reads. It does not benchmark
LLM provider throughput.

## Environment controls

- Dedicated PostgreSQL database and empty vector data directory.
- Loopback listener, one isolated Levara process and a separately configured
  embedding sidecar matching the intended model; no production endpoints.
- Fixed embedding model revision/digest and dimension recorded in the report.
- Explicit test URL, synthetic corpus and disposable user credentials; keep
  required auth and rate-limit settings representative of the intended deployment.
- No unrelated benchmark or backup process during the run.
- Record server CPU/RSS, disk free space, database size and queue depth before
  and after every phase.

## Phases and acceptance thresholds

| Phase | Workload | Duration | Release gate |
|---|---:|---:|---|
| MCP baseline | `heartbeat`, audit disabled, 500 RPS | 60 s | error rate <= 0.1% |
| Durable audit soak | `heartbeat`, audit + SQL projection, 500 RPS | 600 s | achieved >= 98%, errors <= 0.1%, p95 overhead <= 2 ms |
| Mixed agent traffic | heartbeat/recall/save, owner scoped | 120 s | MCP p95 < 200 ms, no ACL leak |
| Outbox burst | 10,000 saves | until drained | save p95 regression <= 15%, zero pending/stale vectors, no dead letters |
| Recovery | restart with audit tail/jobs present | until healthy | no lost/duplicate audit IDs; queues converge automatically |

The audit overhead is the enabled-soak p95 minus the baseline p95. Arrival
scheduling lag is reported separately from request latency so an overloaded
load generator cannot make the server look slow or fast accidentally.

## Required report

Every run stores target and achieved RPS, call count, error rate and samples,
p50/p95/p99 request latency, p50/p95/p99 scheduling lag, audit event count,
read-model lag, queue depth/age/retries/dead letters, process CPU/RSS and exact
build/configuration identifiers.

The command below drives MCP load only. It does not by itself execute the
full matrix, verify ACL, collect every required metric or perform recovery.
The runner accepts `--token`; supply a test credential for authenticated
phases. Omitting it measures the unauthenticated path.
Recovery requires a controlled restart of the disposable instance only.

Example against an already prepared isolated target:

```bash
export LEVARA_LOAD_TEST_URL=http://127.0.0.1:18081
python3 benchmark/mcp_load.py --url "$LEVARA_LOAD_TEST_URL" \
  --token "$LEVARA_LOAD_TEST_TOKEN" \
  --rate 500 --duration 600 --workers 64 \
  --output benchmark/results/mcp_audit_soak.json
```

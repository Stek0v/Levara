# Levara Agent Memory Evaluation

Harness mapping the Mem0/Zep/LangMem comparison framework onto Levara MCP tools.

## Quick start

```bash
# All 3 hosts (Mac :8081, qwen-64 :8080, Pi5 :8090)
bash benchmark/memory_eval/run_all_hosts.sh

# Or manually:
python3 benchmark/memory_eval/run_memory_eval.py \
  --targets benchmark/memory_eval/targets.json -v

# Single host
python3 benchmark/memory_eval/run_memory_eval.py \
  --url http://localhost:8081 --label local-mac -v

python3 benchmark/memory_eval/run_memory_eval.py \
  --url http://10.23.0.64:8080 --label qwen-64 --auth -v

python3 benchmark/memory_eval/run_memory_eval.py \
  --url http://10.23.0.53:8090 --label pi5 --auth -v
```

Artifacts land in `benchmark/memory_eval/results/`:
- `memory_eval_<label>_<timestamp>.json` — full structured report
- `memory_eval_<label>_<timestamp>.log` — step-by-step log

## Categories (12 × score 0–3, 36 pts max)

| # | Category | What it measures |
|---|----------|------------------|
| 1 | CRUD + memory types | semantic / episodic / procedural save, recall, upsert, delete |
| 2 | Retrieval quality | R@3, P@3, NDCG@3, MRR, Hit on `golden_dataset.json` v2 (~25 queries); **isolated collection per case** |
| 3 | Latency | save/recall p50/p95/p99, 10-way concurrent recall |
| 4 | Consolidation | `consolidate` dry_run, `wake_up` + pin |
| 5 | Integration | MCP tool surface, diary namespace, agent contract |
| 6 | Isolation | collection-scoped recall (no cross-tenant leakage) |
| 7 | Edge cases | invalid hall, empty query, unicode, contradiction upsert |
| 8 | Observability | doctor, runtime_stats, heartbeat |
| 9 | Cross-session | MCP reconnect → same JWT → recall persists |
| 10 | Owner isolation | JWT user A vs B (`--auth`) |
| 11 | Context efficiency | `wake_up` respects `max_tokens` budget |
| 12 | Scale smoke | N saves → recall latency (`--scale-memories`, default 50) |

See [`COVERAGE.md`](COVERAGE.md) for Mem0/Zep mapping and known gaps.

## Prerequisites

- Running Levara HTTP + MCP endpoint
- **PostgreSQL** + **embed sidecar** on all hosts (see `targets.json`):
  - **Mac:** `scripts/postgres-dev.sh` + `scripts/start-embed-local.sh` + `./start-levara.sh`
  - **qwen-64:** `scripts/postgres-remote-ensure.sh`
  - **Pi5:** prod systemd on `:8090` (potion embed `:9101`)
- Remote auth: `--auth` or `"auth": true` in targets
- Verify: `curl -s http://<host>/health/details | jq '{embed,postgres:.postgres}'`

**Auth rate limit (not PostgreSQL):** `/auth/login` + `/auth/register` share **10 req/min per IP** by default. The memory eval issues many logins — on Mac set `RATE_LIMIT_AUTH_MAX=10000` (already in `local.postgres.env.example` / `start-levara.sh`) and restart levara. Tests reuse fixed users (`memeval@bench.local`) and login-before-register.

## Host matrix

| Host | URL | Auth | Embed |
|------|-----|------|-------|
| Mac | `localhost:8081` | yes | potion `:9101` |
| qwen-64 | `10.23.0.64:8080` | yes | qwen3 `:9001` |
| Pi5 | `10.23.0.53:8090` | yes | potion `:9101` |

## Known deployment checks (2026-06-16)

| Host | Status |
|------|--------|
| `localhost:8081` | PG + potion embed via `start-levara.sh` |
| `10.23.0.64:8080` | PG + qwen3 embed |
| `10.23.0.53:8090` | Pi prod, auth required |

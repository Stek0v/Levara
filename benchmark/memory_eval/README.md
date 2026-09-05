# Levara Agent Memory Evaluation

A 12-category MCP evaluation harness for CRUD, retrieval, session continuity
and bounded isolation checks. This is a protocol and tool guide; current
verified results and historical limitations are in [testing](../../docs/testing.md).

## Run on an isolated target

Prepare a disposable Levara instance with its own SQL database and vector data
directory. Choose the server origin explicitly; the harness adds API paths.
Use only synthetic memories. The harness creates users, collections and records.

```bash
export LEVARA_EVAL_URL=http://127.0.0.1:18081
python3 benchmark/memory_eval/run_memory_eval.py \
  --url "$LEVARA_EVAL_URL" --label isolated-local --auth \
  --scale-memories 50 -v
```

For several isolated targets, create your own JSON file:

```json
[
  {"url": "http://127.0.0.1:18081", "label": "candidate-a", "auth": true},
  {"url": "http://127.0.0.1:18082", "label": "candidate-b", "auth": true}
]
```

Pass it with `--targets /path/to/eval-targets.json`. The repository's
`targets.json` and `run_all_hosts.sh` contain environment-specific host settings;
they are not portable setup instructions. Do not run them against live data.

Artifacts default to `benchmark/memory_eval/results/`; use `--output-dir` for a
separate run directory:

- `memory_eval_<label>_<timestamp>.json` — structured report;
- `memory_eval_<label>_<timestamp>.log` — execution log.

## Categories (12 × score 0–3, 36 points maximum)

| # | Category | What it measures |
|---|----------|------------------|
| 1 | CRUD + memory types | save, recall, upsert and delete on test records |
| 2 | Retrieval quality | R@3, P@3, NDCG@3, MRR and Hit; golden v2 has 15 cases and 19 queries, with an isolated collection per case |
| 3 | Latency | save/recall percentiles and 10-way concurrent recall |
| 4 | Consolidation | dry-run consolidation, pin and wake-up |
| 5 | Integration | MCP tool surface, diary namespace and agent contract |
| 6 | Collection isolation | cross-collection recall on the configured cases; not tenant authorization proof |
| 7 | Edge cases | invalid hall, empty query, Unicode and contradiction upsert |
| 8 | Observability | doctor, runtime_stats and heartbeat |
| 9 | Cross-session | reconnect with the same credentials and recall |
| 10 | Owner isolation | JWT user A versus B; requires `--auth` |
| 11 | Context efficiency | wake_up budget checks |
| 12 | Scale smoke | N saves and recall latency; `--scale-memories` defaults to 50 |

The displayed overall percentage normalizes category points. **36/36 does not
mean 100% retrieval accuracy**, complete code coverage or proof of every access
path. Read per-query metrics, skips, errors and authentication mode separately.
See [COVERAGE.md](COVERAGE.md) for test mapping and gaps.

## Preparation and interpretation

- Install dependencies needed by the harness and its imported MCP test client
  in a separate Python environment; check imports before a timed run.
- Record revision, dirty diff, SQL backend/version, model revision/dimension,
  corpus digest, settings and run command alongside the results.
- Use PostgreSQL if evaluating that backend; a SQLite run cannot establish
  PostgreSQL behavior. A configured embedding service is needed for semantic
  cases; skipped cases must stay visible in the report.
- `--auth` registers/logs in test accounts. `LEVARA_TEST_EMAIL` and
  `LEVARA_TEST_PASSWORD` override the main fixture account; the harness also
  uses test identities for owner checks. These accounts belong only on the
  disposable target. Review logs before sharing them: authentication metadata
  includes a token prefix.
- Keep normal authentication and rate-limit policy. If the run receives 429,
  report it and schedule a fresh run after the limit window; weakening a live
  service's auth limit changes the experiment and its security boundary.
- Remove only the disposable instance and its data after preserving the report.
  The harness is not a production health check or a backup mechanism.

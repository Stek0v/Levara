# Pi 3-embed-model honest calibration — design

**Date:** 2026-05-26
**Author:** stek0v (with Claude Opus 4.7)
**Status:** approved, ready for implementation plan

## 1. Goal

For each of three candidate embedding models, produce comparable
calibration and quality data by running them through the **full
cognify pipeline** (chunk → LLM entity extraction → dedup → embed →
write HNSW+BM25+graph+WAL) on the Pi 5, then exercising P4/P5 load
profiles via `/api/v1/search/text` (the canonical client path, post
PR #85 fix).

Candidates:

| Model | HF repo | Stack |
|---|---|---|
| `granite-97m` | `ibm-granite/granite-embedding-97m-multilingual-r2` | HuggingFace transformers / ONNX |
| `jina-omni-nano` | `jinaai/jina-embeddings-v5-omni-nano` | HuggingFace transformers (trust_remote_code) |
| `potion-code-16M` | `minishlab/potion-code-16M` | model2vec (static embeddings) |

## 2. Success criteria

Single deliverable: `docs/load-profile-analysis-pi-multimodel.md`
containing per-model and cross-model sections.

Per model, the analyzer must report:

- **Calibration:** score_gap_top_bottom percentiles (p25/p50/p75/p90),
  threshold sweep `T ∈ [0.02..0.20]` with `skip%` and `gate-wrong%`,
  and recommended `RERANK_SCORE_GAP_THRESHOLD`.
- **Quality** (light ground truth from `QUERIES[].expected_keywords`):
  mean recall@5, top1-keyword-hit-rate, mean rerank latency.

Cross-model: side-by-side comparison table, plus identification of
the model with the highest top1-keyword-hit-rate at lowest p50 latency.

## 3. Non-goals

- Not building a long-running benchmark service. Bench instance only
  exists for the duration of the test sweep, then is stopped.
- Not migrating production. The output is data and a recommendation;
  any production model swap is a follow-up project.
- Not constructing a rigorous IR ground-truth set. We use existing
  `expected_keywords` from `QUERIES[]` as a light proxy.

## 4. Architecture

Production stack stays untouched. A second Levara instance runs in
parallel for the duration of each model's sweep, with its own data
directory, port, and JWT secret.

```
Pi 10.23.0.53
┌───────────────────────────────────────────┐
│ PRODUCTION (unchanged)                    │
│  levara.service           :8090           │
│    -dim=768, prod sqlite, prod 71 colls   │
│  ollama.service           :11434          │
│  rerank-sidecar.service   :9100           │
├───────────────────────────────────────────┤
│ BENCH (lifecycle controlled per model)    │
│  embed-bench.service      :9101           │
│    FastAPI, OpenAI /v1/embeddings         │
│    loads ONE model from EMBED_BENCH_MODEL │
│    GET /health → {model, dim, ram_mb}     │
│  levara-bench.service     :8091           │
│    same Levara binary, separate data-dir  │
│    /home/stek0v/levara-bench/data         │
│    JWT_SECRET pinned (constant string)    │
│    EMBEDDING_ENDPOINT=http://127.0.0.1:9101│
│    EMBEDDING_MODEL=<current>              │
│    -dim=<current model dim>               │
│    -port=8091 -grpc-port=0                │
│    LLM_ENDPOINT=http://localhost:11434/v1 │
│    LLM_MODEL=qwen3:0.6b (shared w/ prod)  │
│    RERANK_ENDPOINT=http://127.0.0.1:9100  │
└───────────────────────────────────────────┘
```

The bench instance is plain `systemd start/stop`; no docker. The
embed-bench sidecar lives in `~/embed-bench/` on the Pi with its own
Python venv, and is also a plain systemd unit.

## 5. Components

### 5.1 embed-bench sidecar

- New directory `scripts/load-profiles/embed_bench/`
- `server.py` — FastAPI app, OpenAI-compatible `POST /v1/embeddings`
  + `GET /health` returning `{model, dim, ram_mb, backend}`
- `backends/transformers.py` — for granite-97m, jina-omni-nano
  - uses `transformers.AutoModel` with `trust_remote_code=True` for jina
  - mean-pooling + L2-normalize, returns `list[list[float]]`
- `backends/model2vec.py` — for potion-code-16M, uses `model2vec.StaticModel`
- Backend selection: env `EMBED_BENCH_MODEL` matches one of three
  hardcoded recipes; unknown name → exit 1.
- Single model loaded at process start. No hot-swap (sequential
  process restart instead — simpler, guaranteed RAM release).
- Batch size: 32. Returns 422 on dim mismatch with expected.

### 5.2 Bench Levara unit

`deploy/bench/levara-bench.service` (new), installed alongside but
**not** enabled at boot. Drop-in `EMBEDDING_MODEL` + `-dim` rewritten
by harness before each model run.

`JWT_SECRET` pinned via the unit file (constant per bench-stack
lifetime) so the harness can reuse tokens across restarts without
re-registering.

### 5.3 Harness changes

**`scripts/load-profiles/runner.py`**:
- New `--target bench` aliasing to `http://10.23.0.53:8091`.
- Removes `LEVARA_PRE_EMBED_URL` bypass code path entirely for bench
  target (canonical `/api/v1/search/text` after PR #85 fix).
- Adds JSONL fields: `embed_model`, `embed_dim`, `keyword_hits_top5`
  (int, case-insensitive substring count of expected keywords across
  the `metadata.text` field of top-5 hits), `top1_keyword_hit` (bool,
  true if any expected keyword appears in top1 `metadata.text`).
- `model_short` convention: `potion`, `granite`, `jina` — used as
  collection-name suffix and JSONL filename component.

**`scripts/load-profiles/seed/code_corpus.py`**:
- Replaces direct insert with `POST /api/v1/cognify` call against
  `loadprofile_<profile>_<model_short>`.
- Polls `/cognify/status/{run_id}` until terminal.
- Validates chunk count via `/api/v1/collections/<name>/info` post-run.

**`scripts/load-profiles/preflight_model.py`** (new):
- Single executable, exit code 0/1.
- Checks (all must pass):
  1. `GET 9101/health` 200, returns `{model, dim}` matching expected
  2. `POST 9101/v1/embeddings {"input":["ping"]}` 200, vector length == dim
  3. `GET 8091/health` 200
  4. `GET 11434/api/tags` includes `qwen3:0.6b`
  5. `GET 9100/health` 200
  6. Bench Levara `-dim` matches sidecar dim (read from process args
     via `ssh ps -ef`)
  7. Auth: register a fresh bench-only user, login, token returned
  8. Free disk on `/home/stek0v/levara-bench` ≥ 5GB
- On failure: prints the failing check + diagnostic, returns 1.

**`scripts/load-profiles/analyze.py`**:
- `--by-model` flag groups JSONL records by `embed_model`.
- Adds per-model recall@5 mean, top1-keyword-hit-rate.
- Renders cross-model markdown table to stdout (the runner pipes this
  into `docs/load-profile-analysis-pi-multimodel.md`).

**`scripts/load-profiles/run_all_models.sh`** (new):
- Orchestrates the per-model loop documented in §6.
- Reads model list from env or `MODELS=(potion-code-16M granite-97m jina-omni-nano)`.
- For each: writes drop-ins, restarts services, runs preflight, seeds,
  runs P4+P5, stops services. Smallest-first ordering.

## 6. Execution sequence (per model)

```
0. Prefetch HF cache (one-time, outside timed window)
   ssh Pi: huggingface-cli download <repo>

For each model in [potion-code-16M, granite-97m, jina-omni-nano]:

1. Tear down previous model (idempotent)
   ssh: sudo systemctl stop levara-bench embed-bench

2. Rewrite drop-ins
   ssh: write /etc/systemd/system/embed-bench.service.d/model.conf with
        Environment=EMBED_BENCH_MODEL=<model_id>
   ssh: write /etc/systemd/system/levara-bench.service.d/embed.conf with
        Environment=EMBEDDING_MODEL=<openai-name-for-model>
        ExecStart=... -dim=<model_dim> ...

3. Start sidecar (loads model, may take 10-60s)
   ssh: sudo systemctl start embed-bench
   wait until GET 9101/health responds with model+dim

4. Start bench Levara
   ssh: sudo systemctl start levara-bench
   wait until GET 8091/health 200

5. Preflight gate
   python3 preflight_model.py <model> || abort_with_logs

6. Seed corpus
   python3 seed/code_corpus.py --target bench --model <model>
     --collections loadprofile_p4_main_<short>,loadprofile_p5_main_<short>
   (uses /api/v1/cognify, blocks on terminal status, validates count)

7. Load profile runs
   python3 runner.py --target bench --profile p4 --model <model>
     --collection loadprofile_p4_main_<short>
     --out out/p4_<short>.jsonl
   python3 runner.py --target bench --profile p5 --model <model>
     --collection loadprofile_p5_main_<short>
     --out out/p5_<short>.jsonl

8. Tear down to free RAM
   ssh: sudo systemctl stop levara-bench embed-bench

End for.

9. Analysis (once, after all models complete)
   python3 analyze.py --by-model out/p[45]_*.jsonl
     > docs/load-profile-analysis-pi-multimodel.md
```

## 7. Data flow

For one query inside a profile run:

```
runner.py
  └─ POST :8091/api/v1/search/text {collection, query_text, top_k=5}
     └─ Levara-bench searchHandler → chunksSearch (post PR #85)
        ├─ embed query: POST 127.0.0.1:9101/v1/embeddings
        ├─ HNSW search in loadprofile_<profile>_<model>
        ├─ overfetch ×3, optional rerank via :9100
        └─ filter (RBAC, tags), return top_k
  └─ pair query: same call WITHOUT rerank (rerank:false)
  └─ compute: score_gap_top_bottom, top_changed, latencies
  └─ compute: keyword_hits_top5 = count(kw in top5_text), top1_keyword_hit
  └─ append JSONL with embed_model, embed_dim
```

For seeding one collection:

```
seed/code_corpus.py
  └─ load 576 chunks from seed/data/
  └─ POST :8091/api/v1/cognify {dataset, text, dataset_name, ...}
     └─ Levara-bench orchestrator:
        chunk → LLM extract (qwen3:0.6b @ 11434) → dedup
        → embed via 9101 → write HNSW+BM25+graph+WAL
  └─ poll :8091/api/v1/cognify/status/<run_id> until done|error
  └─ GET :8091/api/v1/collections/<name>/info → assert count >= 576
```

## 8. Error handling

| Failure | Behavior |
|---|---|
| Sidecar load fails (e.g. jina trust_remote_code blocked) | embed-bench exits non-zero; preflight detects, run skipped for that model, others continue |
| Cognify run errors mid-corpus | seed script captures `run_status=error`, prints last 200 lines of bench Levara journalctl, exits 1; harness skips that model's runner step |
| Dim mismatch between sidecar and bench Levara | preflight check 6 fails; both services stopped; harness aborts model |
| `/api/v1/search/text` returns 502 (PR #85 path) | runner logs error to JSONL with `error="search_502"`, continues with next query; analyzer reports error rate per model |
| OOM on Pi during model load | sidecar process killed; preflight check 1 times out → fail → skip model |
| HF Hub network failure | prefetch step in §6 step 0 catches this; if cache missing, sidecar exits with clear error |

## 9. Cost estimate

- Per-model cognify seeding (576 chunks, LLM extraction via qwen3:0.6b on Pi 5): ~10 min for P4+P5 collections combined
- Per-model load-profile run (720 queries P4 + P5): ~3-3.5 h (based on prior 6h12m observation, expected slightly faster post-fix)
- Per-model overhead (service restarts, preflight, teardown): ~5 min
- **3 models × ~3.5 h ≈ 10-11 h wall clock** — overnight prog run

## 10. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Jina v5 omni nano needs special preprocessing or HF auth | Order ensures potion (model2vec, simplest) and granite (well-documented ONNX path) succeed first; jina failure does not invalidate other data |
| Bench Levara `-dim` change requires restart between models | Already part of the sequence — sequential by design |
| Cognify LLM extraction is slow on Pi 5 (qwen3:0.6b on CPU) | Cost accepted; user explicitly chose full pipeline ("honest test") |
| Restarting bench Levara mid-run loses in-flight queries | Bench restarts happen only between models, not within a model's profile run |
| Production Levara restart accidentally triggered | Harness ssh commands target `levara-bench.service` only; explicit grep in restart wrapper rejects unit name `levara.service` |
| Collection namespace collision with prod 71 collections | Bench Levara has its own data-dir; physical isolation. Plus existing `loadprofile_` prefix assertion stays in place |
| Drop-in files left behind after run | Harness teardown step removes drop-ins on completion or on Ctrl-C trap |

## 11. Out of scope (deferred follow-ups)

- Adding cross-encoder rerank models alongside (only embedding swap)
- Migrating from sqlite to postgres for bench data
- Running on .64 host as well (current target Pi only, per user)
- Replacing `expected_keywords` proxy with curated IR ground truth
- Automated CI re-run on Levara binary changes

## 12. Open items (decided post-design)

None at design freeze. Implementation plan will pick concrete:
- Model dim values (read from HF config at prefetch time)
- Pinned JWT_SECRET value (generated once during initial setup)
- Per-model OpenAI-style "model name" strings (the values Levara
  sends as `model` in its embed request — sidecar can ignore or
  validate)

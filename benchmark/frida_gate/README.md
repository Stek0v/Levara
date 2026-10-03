# FRIDA-Decisions gate (2026-10-03)

Empirical gate for adopting `ai-forever/FRIDA-Decisions` as Levara's cheap
decision layer. Model: 823M T5-encoder, ONNX int8 CPU (threads=8), same
`Judge` API across choice/score/noul/ranking heads. All data is real prod
data dumped from the `memories` table (collection `levara` + cross-collection
corpus) with structural ground truth (superseded edges, hall labels,
source-of-query positives).

Run: `STAGE=1|2|3 python run_gate.py` (three runs fit the 10-min shell cap;
partials merge through `results_partial1.json` → `results.json`).

## Results (FRIDA int8 CPU / M2)

| Stage | Set | Headline | Separation | Calibrated thr |
|---|---|---|---|---|
| supersession gate | 148 (74 real edges) | AUC 0.929, P=1.0 | p 0.277 vs 0.027 | best-F1 0.844 @ 0.05 |
| dup (Jaccard pos) | 57 | AUC 0.977, P=1.0 | 0.415 vs 0.017 | best-F1 0.833 @ 0.125 |
| dup (semantic RU/EN) | 60 (30 cos≥0.86 pos / 30 cos 0.55–0.72 neg) | AUC 0.990 | 0.392 vs 0.021 | best-F1 0.966 @ 0.075 |
| hall classification | 113 stratified | acc 0.46 (majority 0.354), macro-F1 0.378 | conf≥0.7 → acc 0.769 @ cov 11.5% | — |
| relevance rerank | 24 q × 10 docs | pos@1 0.958, MRR 0.979, 4.4 s/query | cosine baseline 1.0 / 1.0 @ 9 ms | — |
| grounding (answerer feed) | 48 | **AUC 1.0, acc 1.0** | 0.908 vs 0.034 | thr 0.2 |
| routing | 24 | 0.5 on 5-way ad-hoc taxonomy | — | prod router uses a different route vocabulary (HYBRID / RAG_COMPLETION / TEMPORAL / COMMUNITY_GLOBAL) — not comparable |

Latency (M2, int8 CPU): noul single ~293 ms median, choice(6)
~470 ms, batch20 ~5.5 s; model load 3.4 s.

## Sidecar deployment test (2026-10-03, `deploy/decisions/app.py` + `sidecar_bench.py`)

The same gate re-run through the real deployment path — FastAPI sidecar on
:9200, HTTP `/judge`, OnnxJudge int8:

- **Parity**: every quality metric identical to the in-process gate to the
  third decimal (supersession AUC 0.929, dup 0.977/0.990, hall 0.46) — the
  sidecar contract is correct.
- **Full sets**: grounding n=80 → acc 0.975, AUC 0.999, best-F1 0.988 @ 0.25;
  relevance n=40 → FRIDA 0.925 pos@1 vs cosine 1.0 (rerank verdict unchanged);
  hall n=237 → acc 0.565, **below the majority baseline 0.616** (discovery
  P 0.871/R 0.74 carries it; conf ≥ 0.7 → 0.816 @ 16% coverage) — hall
  auto-labeling rejected even as a bulk relabeler.
- **Load** (noul pairs, threads=8): throughput flat ~0.7 RPS at concurrency
  1→8 (one request already saturates ~7 cores), p50 735 ms → 4.8 s at conc=8.
  One decision costs ~0.5–0.7 s wall but ~1.5–2 CPU-seconds.
- **Threads sweep** (noul pairs, HTTP): threads=4 beats threads=8 — p50 343 ms
  (vs 735), conc=2 → ~2.9 RPS at ~4.6 cores; threads=2 → p50 619 ms at ~2
  cores (Pi-like economy). Recommendation: **threads=4 in prod**, threads=8
  only for overnight consolidation batches.

Sidecar test artifacts: `sidecar_results_{parity,full,load}.json`.

## Pi 5 deployment (2026-10-03, live)

The gate shipped behind `-decisions-endpoint` (commit
`feat/frida-decisions-gate`) and was deployed to the Pi (berry8gb, 4×A76,
8 GB, levara.service on :8090):

- sidecar: `levara-decisions.service` (systemd), model at
  `~/models/frida-decisions` (ONNX int8 1.26 GB), `DECISIONS_THREADS=4`,
  RSS ≈ 1.8 GB;
- **noul latency on Pi: p50 ≈ 940 ms** (785–1050 ms) — matches the model
  card's ~0.9 s CPU figure; verdicts sane (commit-update pair 0.85, RU
  paraphrase 0.55, RU/EN 0.21, distinct fact 0.06 — note how close the
  distinct-fact margin sits to the 0.05 threshold: calibration matters);
- server flag wired via drop-in env `DECISIONS_ENDPOINT` (10-decisions.conf);
  Pi embed enabled for clustering via `90-embed.conf`
  (`-embed-endpoint=http://127.0.0.1:9101/v1/embeddings -embed-model=potion`);
- **end-to-end**: `consolidate {collection, dry_run}` over MCP →
  `decision gate: checked=1 rejected=0 errors=0`, sidecar journal shows the
  matching `POST /judge`. Background janitor sweeps now consult the gate on
  every collection with clusters.

Revert on Pi: `sudo rm /etc/systemd/system/levara.service.d/{10-decisions,90-embed}.conf
&& sudo systemctl disable --now levara-decisions && sudo systemctl daemon-reload
&& sudo systemctl restart levara`.

## Verdict

Adopt (behind a flag, CPU int8 sidecar per `deploy/rerank/app.py` pattern):
1. **Consolidation/supersession noul gate** — pre-filter clusters before LLM
   abstraction; zero false positives on all three noul sets at calibrated
   thresholds; ground truth free from `superseded_by` edges for ongoing eval.
2. **Answerer grounding gate** — noul "does this chunk answer the question"
   on top-3..5 recall chunks before the gemma prompt; perfect on this sample.

Reject for now: rerank (no gain over cosine on the easy proxy, too slow on
the hot path), routing (taxonomy mismatch, no gain), hall auto-labeling
(only usable as a high-confidence hint, coverage 11.5%).

## Caveats

- noul probabilities sit low for our phrasing — thresholds must be
  calibrated (0.05–0.2), never the naive 0.5.
- Relevance set is self-derived (query = first sentence of the source doc),
  which flatters the bi-encoder; a hard-query set is needed before any
  rerank decision (R2/R3/R4).
- MPS packed path was not cleanly measurable (torch MPS teardown crashed in
  this version stack; cached-state call 0.23 s in smoke). More importantly:
  **sharing MPS with the live embed server starves prod recall** (embed
  latency 14.7 s under contention vs 39–57 ms steady) — any FRIDA deployment
  must stay CPU-only while it co-hosts with `:9101`.

## Files

`build_datasets.py` (provenance-first sets), `run_gate.py` (staged runner),
`router_probe/main.go` (prod heuristic router over the same queries),
`embed_client.py` (prod embed client), `data/*.json|csv` (sets + dumps),
`results.json` (full metrics incl. per-class hall table and routing rows).

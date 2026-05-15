# Phase 2 — Rerank by Default

**Status:** design, 2026-05-14
**Owner:** stek0v
**Predecessor:** Phase 1.5 (mmini-L12 ONNX INT8 selected — see bench artifacts on Pi 5)

## Goal

Make cross-encoder rerank the **default** behavior of Levara search, not an opt-in flag (`req.Rerank=true`). Every `/api/v1/search` call should return reranked top-K when a reranker is reachable, without clients having to know the flag exists.

## Phase 1.5 result (input)

| Variant | rubq NDCG | rubq p50 | scidocs NDCG | scidocs p50 | RSS peak |
|---|---|---|---|---|---|
| PyTorch fp32 | 0.815 | 6.6 s | 0.696 | 1.00 s | ~1.5 GB |
| ONNX fp32 | 0.806 | 5.68 s | 0.715 | 0.88 s | — |
| **ONNX INT8** | **0.796** | **2.98 s** | **0.705** | **0.33 s** | **1.43 GB** |

Winner: `cross-encoder/mmarco-mMiniLMv2-L12-H384-v1` ONNX INT8 arm64, 118 MB, 0.33–3.0s p50 on Pi 5.

## Current state (opt-in)

`internal/http/api_search.go:386`:
```go
if req.Rerank && cfg.RerankEndpoint != "" {
    rerankClient = rerank.NewClient(cfg.RerankEndpoint, cfg.RerankModel, 0, cfg.RerankTimeoutMs)
}
```

Issues:
1. Clients who don't know about `req.Rerank` get worse quality silently.
2. No fallback path — if the reranker times out, the whole call slows.
3. No telemetry signal for "rerank skipped because too slow".

## Proposed change

### 1. Flip the default

`UnifiedSearchRequest.Rerank` becomes a tri-state via pointer:

```go
Rerank *bool `json:"rerank,omitempty"`  // nil = default-on, false = explicit opt-out, true = force
```

Resolution in handler:
```go
wantRerank := cfg.RerankEndpoint != ""
if req.Rerank != nil { wantRerank = *req.Rerank }
```

This preserves backward compat for the (rare) callers passing `"rerank": false` — they still get opt-out.

### 2. Latency budget + fallback

Add `RERANK_BUDGET_MS` env (default 1500ms). When the reranker call exceeds the budget, the handler returns the pre-rerank ranking and emits a metric.

```go
ctx, cancel := context.WithTimeout(ctx, time.Duration(budgetMs)*time.Millisecond)
results, reranked, err := sp.SearchByTextWithRerank(ctx, ...)
if errors.Is(err, context.DeadlineExceeded) {
    rerankSkipped.WithLabelValues("budget").Inc()
    results, err = sp.SearchByText(ctx0, ...)  // fall through
}
```

### 3. Per-result `reranked` flag stays

Already implemented in 20.04 (A.2 fix). No change needed.

### 4. Metrics

New Prometheus counters:
- `levara_rerank_invocations_total{outcome="ok|budget|error|disabled|no_text"}` (`no_text` added in Phase 2.1, 2026-05-15)
- `levara_rerank_latency_seconds{outcome}` (histogram)

### 5. Default deployment config

`docker-compose.yml` and deploy recipes ship with:
```
RERANK_ENDPOINT=http://rerank:9100/rerank
RERANK_MODEL=mmini-L12-int8
RERANK_BUDGET_MS=1500
```

A new sidecar service `rerank` runs ONNX Runtime + the INT8 model on whatever host runs Levara (Pi 5 included). For dev without the sidecar, `RERANK_ENDPOINT` stays empty → rerank silently disabled.

### 6. Rerank sidecar (new)

Minimal Python FastAPI service:
- Load `model_quantized.onnx` once at startup
- Endpoint `POST /rerank {query, documents[]}` → `{scores[]}`
- Health endpoint `/health`
- Default `OMP_NUM_THREADS = $(nproc)`
- Memory budget ~150 MB

Lives at `deploy/rerank/` (Dockerfile + app.py + model files mounted from a volume).

## Migration plan

1. **PR A** — sidecar service (`deploy/rerank/`) + docker-compose entry, behind `profiles: [rerank]`. No code change in Levara yet. Smoke: `curl rerank:9100/rerank`.
2. **PR B** — flip default in `api_search.go`, add budget/fallback, add metrics. Wire `RERANK_BUDGET_MS` config plumbing in `cmd/server/main.go`. Unit tests for: nil = on, false = off, true = force, budget exceeded → fallback path.
3. **PR C** — update `WEBUI_REQUIREMENTS.md` + WebUI search to drop the manual "Rerank" toggle from the default UI (move it under Advanced).
4. **PR D** — update docs: `api-reference.md`, `README.md`, getting-started — mention rerank is default-on.

## Risks

| Risk | Mitigation |
|---|---|
| Pi 5 with no rerank sidecar gets slower searches | Keep `RERANK_ENDPOINT=""` default in `.env.example`; opt-in by deploying sidecar |
| Reranker hangs → whole search hangs | `RERANK_BUDGET_MS` + context.WithTimeout fallback |
| Client sends `"rerank": false` expecting opt-in semantics (no effect) | Now means explicit opt-out; document migration |
| Tests pinned to `req.Rerank=true` start passing for the wrong reason | Update tests to assert via response `reranked` field, not request flag |

## Open questions

- Should rerank apply to `dualsearch.go` and graph search too, or stay chunks-only for Phase 2? Recommend: chunks-only, expand in Phase 2.5.
- ~~Should we expose the model behind a `/models/rerank` endpoint so clients can verify which variant is serving?~~ **Resolved (2026-05-15):** `GET /api/v1/models/rerank` returns `{enabled, endpoint, model, budget_ms}` — pure config echo, no sidecar round-trip. See `internal/http/rerank_info.go`.

## HYBRID rerank scope (Phase 2.5, 2026-05-15)

`hybridSearch` in `internal/http/api_search.go` overfetches `TopK*2`
fused candidates from the RRF pass, then calls `hybridApplyRerank`
which:

1. Extracts text from each row's metadata using `pipeline.ExtractText`
   (the same `text → name` rule as chunksSearch).
2. Calls the rerank sidecar under a `RerankBudgetMs` context.
3. Reorders `allResults` by sidecar score and stamps
   `reranked: true` + `rerank_score` on placed rows.
4. Increments `levara_rerank_invocations_total{outcome=...}` with the
   same scheme as chunksSearch (`ok|budget|error|no_text|disabled`).

Rows that lack a `text`/`name` payload are skipped from the rerank
input; if every row is skipped, outcome=`no_text` and the fused order
is preserved.

**Test that pins this contract:**
`TestHybridSearch_RerankApplied_Phase25` in
`internal/http/rerank_budget_test.go`. Asserts the sidecar IS hit,
the `ok` counter increments, and every returned row carries
`reranked: true`. A refactor that regresses HYBRID back to skip-rerank
fails loudly and must explicitly update this section.

## gRPC rerank (Phase 2.5, 2026-05-15)

gRPC v1 carries opt-in rerank fields on `SearchByTextReq` and
`HybridSearchReq` (`rerank_endpoint`, `rerank_model`, `rerank_budget_ms`,
`rerank_timeout_ms`). When `rerank_endpoint` is set, the handler:

1. Calls `pipeline.SearchByTextWithRerank` (for SearchByText) or runs
   the same fused → extract-text → sidecar → reorder pass as
   `hybridApplyRerank` (for HybridSearch).
2. Wraps the rerank call in `context.WithTimeout(budget)`.
3. Increments `levara_rerank_invocations_total{outcome=...}` with the
   same scheme as HTTP (`ok|budget|error|no_text|disabled`).

**Deviation from HTTP tri-state:** the gRPC `Service` holds no config
object, so there is no server-side default rerank endpoint to fall back
to. Rerank is therefore strict opt-in via the request — empty endpoint
= no rerank, no tri-state nil/true/false distinction needed.

`proto/levara_v2.proto` only exposes raw-vector `Search` (no SearchByText
or HybridSearch), so v2 has nothing to wire in this phase.

**Tests that pin this contract:** `TestGRPC_SearchByText_Rerank` and
`TestGRPC_HybridSearch_Rerank` in `internal/grpc/rerank_test.go`.

## ACL pre-rerank (Phase 2.5 fix, 2026-05-15)

`filterByAllowedDatasets` now runs **before** the rerank pass in both
`chunksSearch` and `hybridSearch`. The new sequence is:

```
vector search → overfetch candidates
              → filterByAllowedDatasets (drops forbidden datasets)
              → rerank sidecar (sees ONLY allowed candidates)
              → reorder
              → trim to TopK
```

Implementation:
- `chunksSearch` switched off `sp.SearchByTextWithRerank` (which had
  the ACL leak baked in). It now calls `sp.SearchByText` with an
  overfetch budget, applies `filterScoredByAllowedDatasets` against the
  raw metadata, then runs the shared `applyRerankToScored` helper on
  the filtered slice.
- `hybridSearch` moves `filterByAllowedDatasets` to *before* the
  `hybridApplyRerank` call. A second (idempotent) ACL pass after rerank
  is kept for defense-in-depth.

The Phase 3 plan to plumb `AllowedDatasetIDs` into `pipeline.SearchPipeline`
is no longer needed — the HTTP layer owns the filter and the pipeline
stays unaware of dataset semantics.

**Test that pins this contract:**
`TestChunksSearch_RerankPreFiltersForbiddenDocs` in
`internal/http/rerank_budget_test.go` — taps the sidecar HTTP path and
fails the test if any forbidden document text reaches it.

**Operational implication:** `RERANK_ENDPOINT` may now safely point at
a third-party reranker (Cohere/Voyage/etc.) without leaking chunks
across the trust boundary. The handler will only ship documents from
datasets the JWT-resolved user owns or has been shared.

## Phase 2.5 follow-ups (2026-05-15)

Three known-debt items spotted during the soak validation and folded
into the same PR.

### 1. `pipeline.SearchByTextWithRerank` deprecation — DONE 2026-05-15

The legacy method has been **deleted**. All callers now go through
the shared helper `pipeline.ApplyRerankToScored` (in
`pipeline/rerank_apply.go`), which centralises the
overfetch → ACL pre-filter → score-gap gate → cross-encoder →
graceful-degradation logic.

**Migrations completed:**

| Surface | File | New flow |
|---|---|---|
| HTTP `chunksSearch` / hybrid | `internal/http/api_search.go` | already on the new path since Phase 2.5; `applyRerankToScored` now a thin wrapper over `pipeline.ApplyRerankToScored` |
| gRPC `SearchByText` | `internal/grpc/service.go` | overfetch via `sp.SearchByText` → `pipeline.FilterScoredByAllowedDatasets` → `pipeline.ApplyRerankToScored`. Two new proto fields: `rerank_score_gap_threshold` (10) and `allowed_dataset_ids` (11) |
| MCP `search` tool | `pkg/mcp/tool_search.go` | same shape; `Deps.AllowedDatasetIDs(ctx)` plumbs the JWT scope into `runSearchStrategy`. Interface change: `SearchPipeline.SearchByTextWithRerank` → `SearchPipeline.ApplyRerank(ctx, query, in, topK)` |

**Shared helper location:** `pipeline/rerank_apply.go` —
`ApplyRerankToScored(ctx, ApplyRerankConfig, *rerank.Client, query, []ScoredResult, topK) (bool, []ScoredResult)`
plus `FilterScoredByAllowedDatasets([]ScoredResult, []string) []ScoredResult`.

**ACL guarantee:** `RERANK_ENDPOINT` can now safely point at
third-party rerankers (Cohere/Voyage) from any surface — forbidden
chunks are filtered out before the sidecar sees them, on every
code path.

### 2. Adaptive rerank score-gap gate

New env knob `RERANK_SCORE_GAP_THRESHOLD` (`float`, default 0 = off)
plumbed through `cmd/server/rerank_config.go` →
`APIConfig.RerankScoreGapThreshold`. When set and the spread between
the top and bottom candidate exceeds it, the cross-encoder call is
skipped:

- `applyRerankToScored` (chunksSearch path): gap measured on
  `ScoredResult.Score` (raw vector similarity).
- `hybridApplyRerank` (HYBRID path): gap measured on the
  `fused_score` produced by RRF.

Skipped calls record `levara_rerank_invocations_total{outcome="skipped_gap"}`,
distinct from `disabled` (config-off) and `ok` (sidecar reordered).
The label is eager-initialised in `metrics.telemetry.init()` so
dashboards see it from process start.

**Operational guidance:** start at 0 (gate off), then tune by
inspecting `histogram_quantile(0.5, rate(...))` of `rerank_score - vector_score`
and pick a threshold that catches the "already confident" cases.
A practical starting point on SciDocs-shaped traffic is in the 0.15–0.25
range for cosine vectors; HYBRID `fused_score` runs in a different
range and needs its own measurement.

### 3. Sub-query fan-out histogram

New metric `levara_search_chunks_subquery_fanout` (histogram with
buckets `1,2,3,5,8,13,21`) records `len(subQueries) * len(colls)`
per `chunksSearch` request. Driven by `graph.DecomposeQuery` —
multi-clause queries fan out, and that fan-out multiplies the
rerank outcome counter relative to request count.

**Why it matters:** `levara_rerank_invocations_total` increments
per inner iteration, not per HTTP request. Dashboards translating
outcome-delta back into request count must divide by the mean of
this histogram. Capacity planning for the rerank sidecar should
size against `outcome-ok-rate × P95(fanout)` rather than against
the request rate alone.

The soak test (`deploy/rerank/test_soak.py`) already asserts the
counter-delta lower bound (≥ search count) precisely because of
this fan-out; strict equality would over-fit.

## Done-when

- Default search returns reranked results without any flag from the client.
- A failing/slow reranker never degrades search latency below the budget.
- Prometheus shows rerank invocation/outcome breakdown.
- Docs and WebUI no longer treat rerank as advanced/experimental.
- Phase 2.5 follow-ups: legacy method deprecated, adaptive gate
  available behind env, fan-out histogram exposed for capacity
  planning.

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

## HYBRID rerank scope (known limitation, 2026-05-15)

`hybridSearch` in `internal/http/api_search.go` constructs a
`rerank.Client` when `rerankWanted` returns true, but then calls
`sp.SearchByText` instead of `sp.SearchByTextWithRerank`. The client
is dead allocation; the sidecar is never hit.

This matches the Phase 2 scope decision (rerank = chunks-only) but
is a foot-gun: a future contributor reading `hybridSearch` will see
the client creation and assume rerank is wired. Phase 2.5 work should
either remove the construction or call the rerank-aware path.

**Test that pins this contract:**
`TestHybridSearch_RerankIsIgnored_Phase2Limitation` in
`internal/http/rerank_budget_test.go`. Fails loudly if a refactor
wires rerank through HYBRID without updating this section.

## gRPC scope (Phase 2, 2026-05-15)

Phase 2 rerank is HTTP-only. Neither `proto/levara.proto` (v1) nor
`proto/levara_v2.proto` (v2) carries a `rerank` field on any `Search*`
request, and `internal/grpc/` contains no rerank references. gRPC
clients fundamentally cannot opt into rerank in this phase — they
always get the pre-rerank vector order. Adding rerank to gRPC is a
Phase 2.5+ task and requires:

1. A `rerank` field on the request messages that need it (Search,
   SearchByText, HybridSearch, ...).
2. The same tri-state semantics as the HTTP `*bool` flag.
3. Mirrored budget/outcome counter handling in the gRPC service
   methods.

The proto file is the contract — `grep -rn rerank proto/` returning
zero lines is the assertion. If you add the field, update this
section.

## ACL (known limitation, 2026-05-15)

`filterByAllowedDatasets` runs **after** the rerank pass in `chunksSearch`
(and in every other handler that supports rerank). The sequence is:

```
vector search → candidates
              → rerank sidecar (sees ALL candidates, incl. forbidden datasets)
              → reorder
              → filterByAllowedDatasets (drops forbidden from response)
```

The user-visible response is correctly ACL-filtered, but the sidecar
receives document text from datasets the requester is not authorized to
read.

**Impact:** low while `RERANK_ENDPOINT` points to a localhost or
same-LAN sidecar (Pi 5 default). **High** the moment it points to a
third-party service (Cohere/Voyage/etc.) — that path would exfiltrate
forbidden chunks even though the response hides them.

**Mitigation today:** treat the sidecar as a trust-equivalent peer of
Levara itself. Never set `RERANK_ENDPOINT` to a service outside the
trust boundary without first moving the ACL filter pre-rerank.

**Test that pins this contract:**
`TestChunksSearch_RerankSeesForbiddenDocs_KnownLimitation` in
`internal/http/rerank_budget_test.go`. It deliberately asserts the
current (leaky) behavior so that any refactor moving the ACL filter
pre-rerank fails loudly and forces an explicit update to this doc.

**Phase 3 fix:** plumb `AllowedDatasetIDs` into `pipeline.SearchPipeline`
so vector candidates are filtered before they ever leave the host.

## Done-when

- Default search returns reranked results without any flag from the client.
- A failing/slow reranker never degrades search latency below the budget.
- Prometheus shows rerank invocation/outcome breakdown.
- Docs and WebUI no longer treat rerank as advanced/experimental.

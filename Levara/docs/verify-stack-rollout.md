# Verify-stack rollout playbook

The verify-stack adds three orthogonal quality gates to RAG completions:

1. **Confidence** (`confidence.go`) — `retrieval × 0.7 + routing × 0.3`. Retrieval is `top × 0.6 + gap × 0.25 + coverage × 0.15`; routing pulls from the smart-router decision. Combined value lands in `[0,1]` and is the input to abstain.
2. **Verification** (`verify.go`) — drops result rows whose score is below `min_score` or whose metadata fails JSON validation. Runs **before** the LLM is asked to ground.
3. **Evidence + abstain** (`evidence.go`, `api_search.go`) — collects up-to-10 unique chunk IDs as `evidence_ids`. With `strict_grounded=true`, an empty list short-circuits to `abstain_reason=strict_grounded_no_evidence`. With non-zero confidence threshold, low-confidence completions abstain with `abstain_reason=low_confidence`.

**All three default to off** (threshold `0`, no `verify_results`, no `strict_grounded`). The rollout in this repo is *opt-in via env or per-request flags*; this doc explains how to flip them on safely.

## Phase 1 — Observe-only (production-safe, do this first)

Goal: ship the feature without changing user-visible behaviour. Watch real traffic, learn the shape of `levara_rag_confidence`, then choose thresholds.

No code or env change is needed — the metrics are emitted on every RAG completion regardless of threshold setting. Add the Prometheus recording rules from [`prometheus-verify.rules.yml`](./prometheus-verify.rules.yml) and the dashboard panel below.

Check the percentile distribution per search type:

```promql
histogram_quantile(0.50, sum by (search_type, le) (rate(levara_rag_confidence_bucket[1h])))
histogram_quantile(0.10, sum by (search_type, le) (rate(levara_rag_confidence_bucket[1h])))
```

Pick `LEVARA_RAG_ABSTAIN_THRESHOLD_<TYPE>` slightly below the 10th percentile of *good* completions for that type. Per-type tuning matters: `RAG_COMPLETION` and `GRAPH_COMPLETION` have very different score scales.

## Phase 2 — Per-request opt-in (canary)

Let downstream callers (agent, dashboard, API consumers) flip the gates on a per-request basis without changing the server config. The Levara MCP/HTTP API already accepts these fields on RAG endpoints:

```json
{
  "query": "...",
  "verify_results": true,
  "min_score": 0.5,
  "strict_grounded": true,
  "include_debug": true
}
```

`include_debug=true` switches list responses (CHUNKS/HYBRID/BM25) from a bare array to the envelope `{ results, debug: { confidence, verification, evidence_ids } }` so callers can see what the gates did without changing logging.

## Phase 3 — Server-wide thresholds

Once Phase 1 has produced 1–2 weeks of live confidence data and Phase 2 has shaken out client-visible regressions, set thresholds in `docker-compose.yml`:

```yaml
environment:
  # Conservative starting points — adjust per-type from the histogram.
  LEVARA_RAG_ABSTAIN_THRESHOLD: "0.30"                       # global floor
  LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION: "0.35"        # tighter for prose
  LEVARA_RAG_ABSTAIN_THRESHOLD_GRAPH_COMPLETION: "0.25"      # graph paths score lower
```

Per-type keys override the global; an unset/invalid key falls back. Anything outside `[0, 1]` is silently ignored — see `parseThreshold` in `internal/http/confidence.go`.

## Alerts

Suggested rules ship in `prometheus-verify.rules.yml`. Two are load-bearing:

- **HighRAGAbstainRate** — fires when `>20%` of completions abstain over 10 min. Indicates either a real quality drop, a bad threshold, or a missing collection.
- **VerifyDropAnomaly** — fires when `levara_rag_verify_dropped_total{reason="bad_metadata"}` rate spikes. This is almost always a write-path regression; the embed-side or upstream chunker started emitting non-JSON metadata.

## Rollback

The kill-switch is environment-only — unset the threshold variables and restart Levara. The verify-stack code itself stays compiled in but is inert at threshold `0`.

Per-request flags (`verify_results`, `strict_grounded`) are caller-controlled, so individual clients can disable them without a server change.

## Decisions log

### 2026-05-09 — current state

- **Phase 1 still active.** Not enough Prometheus history to pick thresholds
  empirically. Continue observe-only; revisit when ≥1–2 weeks of
  `levara_rag_confidence_bucket` data is in.
- **Threshold candidates frozen at the playbook defaults** (`0.30` global,
  `0.35` RAG, `0.25` GRAPH) as the *starting point* for Phase 3 once data is
  in. No env change yet — these are documentation, not configuration.
- **Phase 2 trigger = client-side query parameter.** Server will not flip
  defaults per request type. Callers (PicoClaw, WebUI, mem0, ad-hoc curl)
  must explicitly pass `verify_results=true` / `min_score=…` /
  `strict_grounded=true`. This keeps the rollout reversible at the caller
  level without touching server config.
- **Envelope shape (legacy array vs `{results, debug}`)** — undecided.
  `include_debug=true` already toggles it per-request; default stays the
  legacy array until we know what WebUI + MCP clients can parse without a
  release. Tracking as an open question, not a blocker.
- **Phase 3 rollback = test endpoint, not a flag flip.** Until thresholds
  are enabled globally, the per-request flags are the *test handle*: QA can
  exercise the gates against any `/search` call and observe metrics.
  See "Test handle (QA cookbook)" below.

## Test handle (QA cookbook)

The same `/api/v1/search` endpoint **is** the verify test handle. Pass the
flags in the request body — no separate route, no server config required.

```bash
# Vanilla search (no gates)
curl -sf -X POST http://127.0.0.1:8080/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"collection":"mem0","query":"hello","limit":5}' | jq .

# Verify ON: drop low-score + bad-metadata rows; envelope the response.
curl -sf -X POST http://127.0.0.1:8080/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{
        "collection":"mem0",
        "query":"hello",
        "limit":5,
        "verify_results": true,
        "min_score": 0.30,
        "strict_grounded": true,
        "include_debug": true
      }' | jq .
```

What to look at in the envelope:

- `debug.confidence.value` — should land in `[0,1]`; spot-check against
  `histogram_quantile(0.5, sum by (search_type, le) (rate(levara_rag_confidence_bucket[1h])))`.
- `debug.verification` — `{total, kept, dropped_score, dropped_meta}`.
  `dropped_meta > 0` means an upstream chunker is emitting non-JSON metadata
  (write-path regression, file an issue).
- `debug.evidence_ids` — should be ≤10 unique IDs; empty + `strict_grounded=true`
  → `abstain_reason=strict_grounded_no_evidence`.

Metrics to watch while exercising the handle:

```promql
rate(levara_rag_verify_dropped_total[5m])  by (reason)
rate(levara_rag_abstain_total[5m])         by (reason, search_type)
histogram_quantile(0.5, sum by (search_type, le)
   (rate(levara_rag_confidence_bucket[5m])))
```

## Related code

- `Levara/internal/http/confidence.go` — score blend, env parsing.
- `Levara/internal/http/verify.go` — `min_score` + metadata filter, metrics emit.
- `Levara/internal/http/evidence.go` — top-10 unique evidence IDs.
- `Levara/internal/http/response_meta.go` — debug envelope toggle.
- `Levara/internal/metrics/telemetry.go` (search for `RAGConfidence`) — series + eager init.

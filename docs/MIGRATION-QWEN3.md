# Migration runbook — Qwen3-Embedding-0.6B + Qwen3-Reranker-0.6B (Q8_0)

**Target host:** 10.23.0.64 (RTX 3090 24GB, 62GB RAM, Linux Mint 22.3)
**Coexists with:** Gemopus-4-26B-A4B-it-Preview-Q4_K_M on :11434 (LLM), existing Postgres, existing Levara HTTP+gRPC.

This runbook walks through upgrading Levara's embed + rerank stack to
the Qwen3 pair. It's designed as a **blue-green migration**: the old
collections stay on nomic-embed-text-v2 (or whatever's current) until
you've validated that the new model + reranker produce strictly better
results, then you cut over.

Rollback at every step is explicit. Nothing below modifies existing
data irreversibly until step 6.

---

## Phase 0 — Inventory + sanity

Before touching anything:

```bash
# 1. What's currently deployed?
curl -s http://10.23.0.64:8080/health/details | jq .services

# Expected output sections: postgres, embed, llm, neo4j, collections.
# Note: `embed.model` + `embed.endpoint` — these are what we're
# replacing.

# 2. What collections exist and how big are they?
TOKEN=$(curl -s -X POST http://10.23.0.64:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"'"$ADMIN_EMAIL"'","password":"'"$ADMIN_PW"'"}' | jq -r .token)

curl -s -H "Authorization: Bearer $TOKEN" \
  http://10.23.0.64:8080/api/v1/collections | jq '.[] | {name, record_count, embedding_model, embedding_dim}'

# 3. Baseline search quality — run a handful of canonical queries and
# record the top-5 results + their scores. You'll compare these later.
#
# Use whatever queries are representative for your corpus — "what's X"
# type factual recall is the easiest A/B.
for q in "company revenue 2026" "how does auth work" "deployment procedure"; do
  echo "=== $q ==="
  curl -s -X POST http://10.23.0.64:8080/api/v1/search/text \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"query_text\":\"$q\",\"top_k\":5}" | jq '.[] | {id, score}'
done > /tmp/baseline_scores.txt
```

**Checklist:**
- [ ] `/health/details` shows current `embed.model` — write it down.
- [ ] `collections` output lists every prod collection with `record_count > 0`.
- [ ] Baseline queries saved to `/tmp/baseline_scores.txt`.

**Rollback:** none needed — this phase is read-only.

---

## Phase 1 — Stand up the new services (no traffic yet)

Both llama-server instances run on the GPU host. If you're on the
same box as Levara, use the compose overlay; otherwise start the two
services standalone.

### Option A: compose overlay (Levara on same host)

```bash
# Put the GGUFs next to docker-compose.yml
mkdir -p models
cp /path/to/qwen3-embedding-0.6b-q8_0.gguf models/
cp /path/to/qwen3-reranker-0.6b-q8_0.gguf models/

# Bring up embed + rerank services WITHOUT touching cognevra yet:
docker compose -f docker-compose.qwen3.yml up -d qwen3-embed qwen3-rerank-llm qwen3-rerank-front

# Watch them load (first run is slow — GPU layer allocation):
docker compose logs -f qwen3-embed qwen3-rerank-llm
```

### Option B: standalone llama-server (Levara on a different box)

```bash
# Embedding on :9001
./llama-server \
  -m qwen3-embedding-0.6b-q8_0.gguf \
  --embedding --port 9001 --host 0.0.0.0 \
  --n-gpu-layers 999 -c 8192 \
  --batch-size 64 --parallel 4 --cont-batching &

# Reranker raw on :9002 (chat completions)
./llama-server \
  -m qwen3-reranker-0.6b-q8_0.gguf \
  --port 9002 --host 0.0.0.0 \
  --n-gpu-layers 999 -c 4096 \
  --batch-size 32 --parallel 4 --cont-batching &

# Cohere-compat adapter on :9003 (from Levara repo)
cd Levara && go build -o /tmp/qwen3rerank ./cmd/qwen3rerank
QWEN3_UPSTREAM=http://127.0.0.1:9002 \
QWEN3_MODEL=qwen3-reranker-0.6b \
  /tmp/qwen3rerank &
```

### Verify all three are alive

```bash
# Embedding (OpenAI-compat)
curl -sf http://10.23.0.64:9001/v1/models | jq
# → {"data":[{"id":"qwen3-embedding-0.6b","object":"model",...}]}

curl -s http://10.23.0.64:9001/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"input":["hello world"],"model":"qwen3-embedding-0.6b"}' | jq '.data[0].embedding | length'
# → 1024 (dimension — record this)

# Reranker (Cohere-compat front)
curl -s http://10.23.0.64:9003/health | jq
# → {"status":"ok","upstream":"http://...","model":"..."}

curl -s -X POST http://10.23.0.64:9003/rerank \
  -H "Content-Type: application/json" \
  -d '{"query":"what is levara?","documents":["Levara is a vector db","unrelated noise"],"top_n":2}' | jq
# → {"results":[{"index":0,"relevance_score":0.87},{"index":1,"relevance_score":0.03}]}
```

**Checklist:**
- [ ] `/v1/models` on :9001 returns qwen3-embedding-0.6b.
- [ ] Single embedding has length **1024** (or adjust `EMBEDDING_DIMENSIONS` in phase 3).
- [ ] Reranker front on :9003 returns sorted results on the smoke query.
- [ ] `nvidia-smi` shows VRAM usage ~15.3 GB (Gemopus) + 0.65 GB × 2 (new models).

**Rollback:** `docker compose -f docker-compose.qwen3.yml down` (option A) or `kill %1 %2 %3` (option B). Levara still points at the old embed service — no impact.

---

## Phase 2 — Create a shadow collection (no old data touched)

Create a **test collection** and populate it with a small sample via
the new embed model. This proves the pipeline works end-to-end before
you spend time re-embedding production data.

```bash
# Create a fresh collection named "qwen3_smoke"
curl -s -X POST http://10.23.0.64:8080/api/v1/collections \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"qwen3_smoke","embedding_model":"qwen3-embedding-0.6b","embedding_dim":1024,"distance_metric":"cosine"}'

# TEMPORARY env override on the Levara process — point it at new embed
# for this one request. If compose: edit docker-compose.qwen3.yml's
# cognevra block, or use docker compose exec for a one-off:
docker compose exec cognevra sh -c '
  EMBEDDING_ENDPOINT=http://qwen3-embed:9001/v1/embeddings \
  EMBEDDING_MODEL=qwen3-embedding-0.6b \
  EMBEDDING_DIMENSIONS=1024 \
  ./cognevra ...'

# Alternative: stand up a second Levara instance on a different port
# (8090) with new env, leave the main one untouched. This is the
# safest pattern for blue-green and the rest of this runbook assumes
# you're using it.

# Feed a handful of representative documents to the new setup.
# (Use your own dataset here — the exact content doesn't matter, just
# make it resemble production.)
curl -s -X POST http://10.23.0.64:8090/api/v1/cognify \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"texts":["Levara is a Go HNSW vector DB.","It supports hybrid search."],"collection":"qwen3_smoke","mode":"rag"}' | jq
# → {"status":"PipelineRunStarted","pipeline_run_id":"..."}

# Watch the run finish
curl -s http://10.23.0.64:8090/api/v1/cognify/<run_id>/status \
  -H "Authorization: Bearer $TOKEN" | jq
# → {"status":"COMPLETED","chunks":N,...}

# Search to prove end-to-end
curl -s -X POST http://10.23.0.64:8090/api/v1/search/text \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query_text":"what is levara?","collection":"qwen3_smoke","rerank":true,"top_k":3}' | jq
```

**Checklist:**
- [ ] `cognify` run transitions to COMPLETED.
- [ ] `search` returns hits with non-zero scores.
- [ ] When `rerank:true` — hits have `"reranked": true` flag in metadata.
- [ ] Latency looks reasonable (<100ms end-to-end for `HYBRID+rerank` top_k=10).

**Rollback:** delete the test collection:
```bash
curl -X DELETE http://10.23.0.64:8080/api/v1/collections/qwen3_smoke \
  -H "Authorization: Bearer $TOKEN"
```

---

## Phase 3 — Shadow evaluation (A/B against real queries)

Before re-embedding production, compare **new vs old** on your baseline queries from Phase 0. Keep the old system running at :8080, new at :8090.

```bash
# Re-cognify the SAME corpus into qwen3_smoke (use the same dataset_id
# the old system used, but via the new Levara instance).
curl -s -X POST http://10.23.0.64:8090/api/v1/cognify \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"dataset_id":"<existing_dataset_id>","collection":"qwen3_smoke","mode":"full"}'

# Wait for COMPLETED, then run the same baseline queries and diff:
for q in "company revenue 2026" "how does auth work" "deployment procedure"; do
  echo "=== $q ==="
  echo "--- OLD ---"
  curl -s -X POST http://10.23.0.64:8080/api/v1/search/text \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"query_text\":\"$q\",\"top_k\":5}" | jq '.[] | {id, score}'
  echo "--- NEW ---"
  curl -s -X POST http://10.23.0.64:8090/api/v1/search/text \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"query_text\":\"$q\",\"top_k\":5,\"rerank\":true}" | jq '.[] | {id, score, reranked}'
done > /tmp/ab_comparison.txt
```

**Go/no-go criteria:**

- [ ] For queries in your native language (e.g. Russian), new recall should be ≥ old (reminder: Qwen3 is native multilingual, nomic is English-focused — if old had issues with non-English queries, new will almost always win).
- [ ] Top-1 result on obvious queries is sensible in both systems.
- [ ] New scores with `rerank:true` are strictly >= new without rerank (on average).
- [ ] No regression on straightforward English factual recall.

If **any** criterion fails with a clear pattern (e.g. new system can't find Term-X that old does), **stop here** and investigate before the re-embed. Re-embedding is ~10 minutes for 100K chunks but the ordering is permanent.

**Feedback capture during shadow phase:** have real users rate results via the `/feedback` endpoint. After 50-100 submissions:
```bash
curl -s http://10.23.0.64:8090/api/v1/feedback/stats \
  -H "Authorization: Bearer $TOKEN" | jq
# Compare avg_rating between the two systems.
```

---

## Phase 4 — Switch the main Levara over

Once shadow validates:

### With compose overlay

```bash
# Pull the overlay — the cognevra env gets patched automatically.
docker compose -f docker-compose.yml -f docker-compose.qwen3.yml up -d --build cognevra
```

### Standalone

Update the env on the main Levara process:

```bash
export EMBEDDING_ENDPOINT=http://10.23.0.64:9001/v1/embeddings
export EMBEDDING_MODEL=qwen3-embedding-0.6b
export EMBEDDING_DIMENSIONS=1024
export RERANK_ENDPOINT=http://10.23.0.64:9003/rerank
export RERANK_MODEL=qwen3-reranker-0.6b
export RERANK_TIMEOUT_MS=5000

# Graceful restart (SIGTERM → WAL flush → exit)
kill -TERM $(pgrep -f cognevra)
# systemd/supervisor brings it back up with new env.
```

### Verify

```bash
curl -s http://10.23.0.64:8080/health/details | jq .services.embed
# → {"status":"connected","endpoint":"http://10.23.0.64:9001/v1/embeddings","model":"qwen3-embedding-0.6b"}

# doctor MCP tool now checks reranker + drift (post-Qwen3 migration
# doctor extension):
curl -s -X POST http://10.23.0.64:8080/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"doctor"},"id":1}' | jq
# Expect:
#   - embed_service: ok (qwen3-embedding-0.6b)
#   - rerank_service: ok (qwen3-reranker-0.6b)
#   - embedding_drift: WARN (N collections on stale model: ...)
```

**The `embedding_drift` warn is expected** after the switch — existing
collections are on the OLD model. That's what Phase 5 fixes.

---

## Phase 5 — Re-embed production collections (the destructive step)

`POST /api/v1/reembed` kicks off a background worker that streams
existing chunks through the new embed model and writes them into a
new collection. **Old collection is NOT dropped** — you have to do it
explicitly in Phase 6 after the new one is validated.

```bash
# For every collection that doctor flagged as drifted:
for COLL in docs knowledge_base research_notes; do
  echo "=== Re-embedding $COLL → ${COLL}_v2 ==="
  curl -s -X POST http://10.23.0.64:8080/api/v1/reembed \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{
      \"source_collection\": \"$COLL\",
      \"target_collection\": \"${COLL}_v2\",
      \"embedding_endpoint\": \"http://10.23.0.64:9001/v1/embeddings\",
      \"embedding_model\": \"qwen3-embedding-0.6b\",
      \"embedding_dim\": 1024,
      \"batch_size\": 32
    }" | jq -r '.run_id'
done

# Each returns a run_id — watch progress:
curl -s http://10.23.0.64:8080/api/v1/reembed/<run_id>/status \
  -H "Authorization: Bearer $TOKEN" | jq
# → {"status":"RUNNING","completed":450,"total":1200,...}
```

**Estimates:**
- ~1000 chunks/minute per GPU worker at batch_size=32 on RTX 3090.
- 10K chunks = 10 minutes.
- 100K chunks = ~100 minutes.

**During re-embed:**
- Old collection keeps serving searches — no downtime.
- New `_v2` collection is being built in parallel.
- You can do intermediate A/B queries against both (`collection: "docs"` vs `collection: "docs_v2"`) to watch quality mature.

**Checklist per collection:**
- [ ] Reembed `status: COMPLETED`.
- [ ] `record_count` on `${COLL}_v2` matches or exceeds old collection.
- [ ] Sample queries against `_v2` return sensible top-k.

**Rollback:** delete the `_v2` collections, re-run with different batch_size / chunk shaping:
```bash
curl -X DELETE http://10.23.0.64:8080/api/v1/collections/docs_v2 \
  -H "Authorization: Bearer $TOKEN"
```

---

## Phase 6 — Cutover + cleanup (the point of no return)

After Phase 5 completes for every production collection:

```bash
# 1. For each collection, rename the old one to _v1 (archived) and
# promote _v2 to the primary name. Collection renaming isn't a native
# Levara op — easiest is via a DB rename + restart:

for COLL in docs knowledge_base research_notes; do
  docker compose exec postgres psql -U cognee -d cognee_db <<EOF
  UPDATE collections SET name = '${COLL}_v1_nomic' WHERE name = '$COLL';
  UPDATE collections SET name = '$COLL' WHERE name = '${COLL}_v2';
EOF
done

# 2. Restart Levara so CollectionManager rebuilds its index from DB:
docker compose restart cognevra

# 3. Verify:
curl -s http://10.23.0.64:8080/api/v1/collections \
  -H "Authorization: Bearer $TOKEN" | jq '.[] | select(.name | startswith("docs")) | {name, embedding_model}'
# Expect: docs_v1_nomic + docs (both present; docs on qwen3).

# 4. doctor no longer warns about drift:
curl -s -X POST http://10.23.0.64:8080/mcp \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"doctor"},"id":1}' | jq '.. | .["embedding_drift"]? // empty'
# → {"status":"ok","message":"All collections on qwen3-embedding-0.6b"}
```

**Soak period:** leave `_v1_nomic` collections in place for at least
1 week. If anything goes wrong, you can reverse the DB rename and the
old embeddings are still there.

**Final cleanup** (only after soak):

```bash
# Delete archived collections.
for COLL in docs_v1_nomic knowledge_base_v1_nomic research_notes_v1_nomic; do
  curl -X DELETE http://10.23.0.64:8080/api/v1/collections/$COLL \
    -H "Authorization: Bearer $TOKEN"
done
```

---

## Rollback (any phase)

| Phase | How to undo |
|---|---|
| 0 (inventory) | Nothing to undo |
| 1 (new services up) | `docker compose -f docker-compose.qwen3.yml down` or kill standalone processes |
| 2 (shadow collection) | `DELETE /collections/qwen3_smoke` |
| 3 (A/B) | Drop shadow collection; no prod touched |
| 4 (switch main) | Revert env vars, restart Levara |
| 5 (re-embed) | Drop `_v2` collections |
| 6 (cutover) | Swap the DB names back + restart |
| Post-cleanup | **Irreversible** — `_v1_nomic` is gone |

The only **irreversible** step is Phase 6 cleanup. Plan for the 1-week
soak before you get there.

---

## Tuning after cutover

1. **BM25 index** doesn't need re-embed but may benefit from re-indexing
   if chunk boundaries changed. Trigger via `POST /api/v1/bm25/reindex`
   if BM25 recall on new chunks feels degraded.

2. **Parent-child collections** (if you use `parent_child: true`) get
   re-embedded as a pair — re-embed `docs` AND `docs_child` together.

3. **Adaptive router weights** (pkg/router/adaptive) reset their
   learning when a strategy starts seeing different score
   distributions. Expect `AUTO` to behave conservatively for the first
   few hundred queries post-cutover.

4. **Community summaries** (`_community_summaries`) from memify need a
   re-memify pass if you want them aligned with the new graph:
   ```bash
   POST /api/v1/memify
   {"dataset_id":"...","collection":"docs"}
   ```

5. **Monitor `levara_rate_limit_rejected_total`** during re-embed — the
   batch worker doesn't go through the per-user bucket but embed /
   rerank round-trip volume is up.

---

## Post-migration observability

After cutover, these Prometheus series tell you the setup is healthy:

```promql
# Rerank call duration — should be <50ms p99 on 10-doc batches.
histogram_quantile(0.99, rate(levara_http_duration_seconds_bucket{operation="search"}[5m]))

# Rerank hit rate — how often clients ask for it.
rate(levara_search_requests_total[5m])

# Embed latency per request — Qwen3-0.6B Q8_0 should land <15ms.
rate(levara_embed_duration_seconds_sum[5m]) / rate(levara_embed_duration_seconds_count[5m])

# Drift alert (doctor heartbeat payload)
max_over_time(levara_cognify_panics_total[1h])  # sanity: zero
```

Set a Grafana alert on:
- `embedding_drift` from `/health/details` returning `warn` for > 1 hour after you think migration is done → a collection got missed.
- Search p99 latency > 200ms → reranker likely starved for GPU.

---

**Updated:** 2026-04-21, release 20.04.

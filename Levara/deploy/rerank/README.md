# Levara rerank sidecar

ONNX INT8 cross-encoder (`mmarco-mMiniLMv2-L12-H384-v1`) served over HTTP.
Phase 1.5 winner — see `docs/phase2-rerank-default-design.md`.

## Layout

```
deploy/rerank/
  app.py            FastAPI service
  requirements.txt  pinned deps (optimum/onnxruntime/transformers)
  Dockerfile        python:3.11-slim image
  README.md         this file
```

The model itself is NOT in this repo (118 MB INT8 / 471 MB fp32). Mount
it as a volume at `/models/mmini-L12-int8/`. On the Pi 5 the artifact
already lives at `/home/stek0v/rerank-bench/onnx/mmini-L12-int8/`.

## Run locally

```
docker build -t levara-rerank deploy/rerank/
docker run --rm -p 9100:9100 \
  -v /path/to/mmini-L12-int8:/models/mmini-L12-int8:ro \
  levara-rerank
```

Health check:
```
curl localhost:9100/health
```

Score:
```
curl -X POST localhost:9100/rerank \
  -H 'content-type: application/json' \
  -d '{"query":"what is HNSW","documents":["hierarchical navigable small world","unrelated text"]}'
```

## Config

| Env | Default | Notes |
|---|---|---|
| `RERANK_MODEL_DIR` | `/models/mmini-L12-int8` | Tokenizer + ONNX dir |
| `RERANK_MODEL_FILE` | `model_quantized.onnx` | ONNX file inside the dir |
| `RERANK_MAX_DOC_LEN` | `384` | Truncation length |
| `RERANK_BATCH_SIZE` | `16` | Inference batch |
| `OMP_NUM_THREADS` | `4` | Pi 5 cores |

## Chaos sidecar (testing)

`chaos_sidecar.py` is a fault-injecting drop-in replacement for `app.py`.
It speaks the same Cohere-compatible `/rerank` contract but adds bounded
latency (uniform `0..CHAOS_LATENCY_MS_MAX` ms) and random HTTP 500s
(`CHAOS_5XX_PROB`). Used to validate Levara's rerank budget / error
fallback paths and the `levara_rerank_invocations_total{outcome=...}`
distribution.

Run it directly:

```
pip install fastapi uvicorn pydantic
CHAOS_SEED=1337 CHAOS_LATENCY_MS_MAX=500 CHAOS_5XX_PROB=0.20 \
  python deploy/rerank/chaos_sidecar.py
# health
curl localhost:9101/health
# score
curl -X POST localhost:9101/rerank \
  -H 'content-type: application/json' \
  -d '{"query":"q","documents":["a","b","c"]}'
```

Or via uvicorn from the deploy dir:

```
cd deploy/rerank && \
  CHAOS_SEED=1337 uvicorn chaos_sidecar:app --host 127.0.0.1 --port 9101
```

Then point Levara at it:

```
RERANK_ENDPOINT=http://127.0.0.1:9101/rerank \
RERANK_MODEL=chaos \
RERANK_BUDGET_MS=5000 \
  ./server
```

The end-to-end test lives in `test_chaos_integration.py` and is gated on
`LEVARA_INTEGRATION=1` so it never runs in default CI:

```
LEVARA_INTEGRATION=1 pytest deploy/rerank/test_chaos_integration.py -v
```

## Pi 5 deploy

Sidecar runs alongside Levara on `berry8gb` (10.23.0.53). Compose profile
`rerank` enables it; see `deploy/docker/docker-compose.yml`.

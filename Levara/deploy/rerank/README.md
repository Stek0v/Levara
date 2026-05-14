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

## Pi 5 deploy

Sidecar runs alongside Levara on `berry8gb` (10.23.0.53). Compose profile
`rerank` enables it; see `deploy/docker/docker-compose.yml`.

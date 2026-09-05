# Levara rerank sidecar

ONNX INT8 cross-encoder (`mmarco-mMiniLMv2-L12-H384-v1`) served over HTTP.
This guide describes configuration, not a current quality or latency ranking.
Use [evaluation fixtures](eval/README.md) and
[testing evidence](../../docs/testing.md) when comparing models.

## Layout

```
deploy/rerank/
  app.py            FastAPI service
  requirements.txt  pinned deps (optimum/onnxruntime/transformers)
  Dockerfile        python:3.11-slim image
  README.md         this file
```

The model is not supplied by this repository. Prepare an ONNX model and
tokenizer compatible with the loader, record their immutable digests and
mount them read-only at `/models/mmini-L12-int8/`. Download size and runtime
RAM must be measured for the chosen artifact and hardware.

## Run locally

```
docker build -t levara-rerank deploy/rerank/
docker run --rm -p 127.0.0.1:9100:9100 \
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
| `OMP_NUM_THREADS` | `4` in Dockerfile | CPU thread setting; tune on the test hardware |

## Search integration

Set Levara's `RERANK_ENDPOINT` to the selected sidecar's `/rerank` URL and
choose `RERANK_MODEL` and `RERANK_BUDGET_MS` for that configuration. Follow the
[search strategies guide](../../docs/search-strategies-guide.md) for the
request's tri-state rerank setting, score-gap skips, fallback behavior and
the distinction between request metrics and Qwen pair-level fan-out metrics.
A configured endpoint does not mean every search invokes the sidecar.

## Chaos sidecar (testing)

`chaos_sidecar.py` is a fault-injecting drop-in replacement for `app.py`.
It speaks the same Cohere-compatible `/rerank` contract but adds bounded
latency (uniform `0..CHAOS_LATENCY_MS_MAX` ms) and random HTTP 500s
(`CHAOS_5XX_PROB`). Used to validate Levara's rerank budget / error
fallback paths and the `levara_rerank_invocations_total{outcome=...}`
distribution.

Use a separate Python environment with FastAPI, Uvicorn and Pydantic.
Start the fault injector only for an isolated Levara target:

```bash
cd deploy/rerank
CHAOS_SEED=1337 CHAOS_LATENCY_MS_MAX=500 CHAOS_5XX_PROB=0.20 \
  uvicorn chaos_sidecar:app --host 127.0.0.1 --port 9101
# health
curl localhost:9101/health
# score
curl -X POST localhost:9101/rerank \
  -H 'content-type: application/json' \
  -d '{"query":"q","documents":["a","b","c"]}'
```

From the repository root, start a separate Levara process with its own
database and data directory. Set these variables for that process:

```
export RERANK_ENDPOINT=http://127.0.0.1:9101/rerank
export RERANK_MODEL=chaos
export RERANK_BUDGET_MS=5000
```

The integration test is gated on `LEVARA_INTEGRATION=1`. Review its target
configuration and the [seeding limitations](eval/README.md#ingestion-helper-current-limitation)
before running it; an enabled test is not evidence of a successful current run:

```
LEVARA_INTEGRATION=1 pytest deploy/rerank/test_chaos_integration.py -v
```

## Qwen3 reranker adapter

`cmd/qwen3rerank` translates `/rerank` requests into one upstream chat-completion
request per query/document pair and sorts the resulting scores. It requires
an upstream that supports the model's yes/no log probabilities through
`/v1/chat/completions`. The native Cohere-compatible sidecar above does not
need this extra adapter.

| Variable | Default | Meaning |
|----------|---------|---------|
| `QWEN3_UPSTREAM` | required | upstream origin; adapter appends `/v1/chat/completions` |
| `QWEN3_MODEL` | `qwen3-reranker-0.6b` | model identifier sent upstream |
| `QWEN3_TIMEOUT_MS` | `5000` | per-pair request timeout |
| `QWEN3_CONCURRENCY` | `4` | maximum simultaneous pairs |
| `PORT` | `9003` | adapter listen port |

Build from the repository root:

```bash
go build -mod=readonly -o ./qwen3rerank ./cmd/qwen3rerank
```

Run it inside the isolated test network with the upstream URL you selected:

```bash
QWEN3_UPSTREAM="$LEVARA_TEST_RERANK_UPSTREAM" \
QWEN3_MODEL=qwen3-reranker-0.6b PORT=9003 ./qwen3rerank
```

The adapter listens on all interfaces and has no built-in authentication or
bind-address option. Restrict it through container networking or the host's
network policy; do not expose it as a public API. `/health` is liveness only
and does not probe the upstream. Verify an actual `/rerank` response before
pointing a test Levara process at its URL through `RERANK_ENDPOINT`.

Compare reranked ordering against labelled queries; scores from different
models are not comparable quality measurements. This setup does not require
renaming SQL collections, restarting a working instance or migrating its data.

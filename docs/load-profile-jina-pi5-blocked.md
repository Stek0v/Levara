# Jina on Pi 5 — calibration blocked

**Status:** Pi 5 8GB cannot stably host jina embedding sidecar alongside ollama (qwen3:0.6b) under the embed-bench cognify+search workload. Calibration ships with nomic + potion as the two baselines; jina deferred.

## Attempts

| # | Variant | Backend | Result |
|---|---|---|---|
| 1 | `jina-embeddings-v5-text-nano-retrieval` `model.onnx` (fp32) | ONNX | OOM at 4.28 GB anon RSS, kernel killed sidecar |
| 2 | same repo, `model_quantized.onnx` (int8) | ONNX | Stock onnxruntime rejected `com.microsoft.GatherBlockQuantized` with `bits` attribute (INVALID_GRAPH) |
| 3 | same repo, `model_fp16.onnx` | ONNX | Mac smoke green, Pi sidecar OOM during cognify burst (RSS ~4 GB) |
| 4 | `jina-embeddings-v2-small-en` (33M params, dim 512) | transformers (trust_remote_code) | Mac smoke green; Pi sidecar still grew to 4.27 GB RSS, OOM under contention with ollama (2.3 GB) |

## Anomaly

A 33M-parameter model holding 4.27 GB anon RSS is the unexplained part. fp32 weights alone are ~130 MB. Suspected causes (not investigated):

- `jina-bert-implementation` (loaded via `trust_remote_code`) materializes ALiBi or attention-bias buffers across layers under load.
- HF transformers cross-input shape caches accumulating in long-running uvicorn process.
- Possible per-request tensor retention path in the sidecar (not confirmed).

A focused investigation (py-spy / tracemalloc on a hot sidecar) is a follow-up; not gating this calibration cycle.

## Decision

- v2-small-en recipe retained in `scripts/load-profiles/embed_bench/recipes.py` for off-Pi use.
- `run_all_models.sh` retains the `jina` branch so the harness can target a larger host later.
- Stale `out/p{3,4,5}_jina.jsonl` removed (data was mixed-recipe across attempts and not comparable).

## Off-Pi follow-up

When jina calibration is resumed, run the same harness on a host with ≥16 GB RAM and no ollama contention, or split the harness so the sidecar lives on a separate machine from the LLM-extraction host.

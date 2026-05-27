# Jina on Pi 5 bench — design

**Date:** 2026-05-27
**Author:** Claude (brainstormed with stek0v)
**Status:** Draft — pending plan + implementation

## Goal

Unblock the third calibration model in the Pi 5 embed-bench harness so that P3/P4/P5 load profiles can be executed against a Jina embedding model alongside `nomic` and `potion`. Current blocker: `jinaai/jina-embeddings-v5-omni-nano` fails to load via HF transformers on Pi venv with `Tokenizer class TokenizersBackend does not exist or is not currently imported` — the omni variant ships custom multimodal code that the installed transformers version cannot resolve.

## Decision

Replace the broken omni recipe with the text-only retrieval-tuned sibling and load it through ONNX Runtime via `optimum.onnxruntime`:

- **Model:** `jinaai/jina-embeddings-v5-text-nano-retrieval` (~239M params, text-only, retrieval-tuned, dim 768).
- **Backend:** new `ONNXBackend` in `embed_bench.backends` using `ORTModelForFeatureExtraction` with `CPUExecutionProvider`.
- **Pre-built weights:** the model repo ships `onnx/model.onnx` directly — no on-host export step.
- **Pooling:** last-token (NOT mean-pool); this is required by Jina v5.

Rejected alternatives:

- **Stay on transformers with `…-text-nano`** — risks the same TokenizersBackend regression from custom tokenizer code; even if it works it duplicates the transformer load cost.
- **`alikia2x/jina-embedding-v3-m2v-1024` via model2vec** — zero new code but it is a community distill of v3, not jina-official v5, and weakens the "third independent calibration model" intent of the benchmark.

## Architecture

Single new backend class slots into the existing dispatcher:

```
scripts/load-profiles/embed_bench/
  backends.py
    TransformersBackend       (existing — nomic, granite)
    Model2VecBackend          (existing — potion)
    ONNXBackend               (NEW — jina)
  recipes.py                  (jina recipe updated: repo, backend=onnx, dim 768, openai_name)
  requirements.txt            (+ optimum[onnxruntime])
  server.py                   (UNCHANGED — make_backend dispatch is already backend-agnostic)
```

Downstream surfaces are unchanged:

- `levara-bench.service` continues to talk OpenAI-compatible `/v1/embeddings` on the sidecar.
- `preflight_model.py` already validates `/health` (model name, dim) and `/v1/embeddings` (returned vector dim) — same contract.
- `run_all_models.sh` needs a one-line update: jina now resolves to `DIM=768` and `OPENAI="jina-v5-text-nano-retrieval"`.

The port-9201 move (separate from this design, already in flight) remains in place so the bench sidecar does not collide with prod `embed-potion.service` on 9101.

## Components

### `ONNXBackend` (new)

```python
class ONNXBackend:
    def __init__(self, recipe: Recipe):
        from optimum.onnxruntime import ORTModelForFeatureExtraction
        from transformers import AutoTokenizer
        import torch

        self._torch = torch
        self.tokenizer = AutoTokenizer.from_pretrained(
            recipe.repo, trust_remote_code=recipe.trust_remote_code,
        )
        self.model = ORTModelForFeatureExtraction.from_pretrained(
            recipe.repo,
            subfolder="onnx",
            file_name="model.onnx",
            provider="CPUExecutionProvider",
            trust_remote_code=recipe.trust_remote_code,
        )

        with torch.no_grad():
            inputs = self.tokenizer(["dim probe"], padding=True, truncation=True, return_tensors="pt")
            out = self.model(**inputs)
            pooled = self._last_token_pool(out.last_hidden_state, inputs["attention_mask"])
            self.dim = int(pooled.shape[-1])
        if self.dim != recipe.dim:
            raise ValueError(
                f"recipe dim mismatch: {recipe.repo} produced {self.dim}-d, "
                f"recipe said {recipe.dim}"
            )

    def _last_token_pool(self, last_hidden_state, attention_mask):
        seq_lens = attention_mask.sum(dim=1) - 1
        seq_lens = seq_lens.clamp(min=0)
        batch_idx = self._torch.arange(last_hidden_state.size(0))
        return last_hidden_state[batch_idx, seq_lens]

    def embed(self, texts: list[str]) -> list[list[float]]:
        with self._torch.no_grad():
            inputs = self.tokenizer(
                texts, padding=True, truncation=True, max_length=512, return_tensors="pt",
            )
            out = self.model(**inputs)
            pooled = self._last_token_pool(out.last_hidden_state, inputs["attention_mask"])
            normed = self._torch.nn.functional.normalize(pooled, p=2, dim=1)
            return normed.cpu().tolist()
```

Mirrors `TransformersBackend` for tokenize → forward → pool → normalize, but uses ORT for forward and last-token pooling instead of mean-pool.

### `recipes.py` change

```python
"jina": Recipe(
    short="jina",
    repo="jinaai/jina-embeddings-v5-text-nano-retrieval",
    backend="onnx",
    dim=768,
    openai_name="jina-v5-text-nano-retrieval",
    trust_remote_code=True,
),
```

### `make_backend` dispatch

```python
def make_backend(recipe: Recipe) -> Backend:
    if recipe.backend == "transformers":
        return TransformersBackend(recipe)
    if recipe.backend == "model2vec":
        return Model2VecBackend(recipe)
    if recipe.backend == "onnx":
        return ONNXBackend(recipe)
    raise ValueError(f"unknown backend {recipe.backend!r}")
```

### `requirements.txt`

Add `optimum[onnxruntime]`. `transformers` is already pinned; `onnxruntime` will resolve to the ARM wheel automatically (`onnxruntime` ≥ 1.17 has linux/aarch64 wheels on PyPI).

### `run_all_models.sh`

```sh
jina) OPENAI="jina-v5-text-nano-retrieval"; DIM=768 ;;
```

(replaces the `jina-omni-nano` / `DIM=512` line).

## Data flow

1. **Cold start** — sidecar boots with `EMBED_BENCH_MODEL=jina`, `make_backend` instantiates `ONNXBackend`, HF downloads ~950 MB ONNX + tokenizer once into `/home/stek0v/embed-bench/hf-cache`, ORT session opens with CPUExecutionProvider.
2. **Probe** — backend embeds `["dim probe"]`, validates dim==768, exposes `self.dim` to the FastAPI layer.
3. **Per request** — `/v1/embeddings` receives `{"model": "jina-v5-text-nano-retrieval", "input": [...]}`, sidecar batches them, runs tokenizer → ORT forward → last-token pool → L2 normalize → returns OpenAI-shaped JSON.
4. **Levara-bench** — reads `EMBEDDING_ENDPOINT=http://127.0.0.1:9201/v1/embeddings` and calls the sidecar exactly as it does for nomic/potion.
5. **Mac harness** — `run_all_models.sh jina` pre-embeds the query against the Pi sidecar at `http://10.23.0.53:9201/v1/embeddings` (`LEVARA_PRE_EMBED_URL`) and posts the resulting 768-d vector to `/api/v1/search` on Pi bench Levara, bypassing the `/search/text` null-result regression (see `discovery_pi_search_text_null.md`).

## Error handling

- **Missing optimum**: `ImportError` propagates — systemd unit goes `failed` with a clear trace in `journalctl -u embed-bench`. No fallback.
- **HF download failure**: exception propagates; sidecar does not start. We do not want a "running but empty" sidecar.
- **Dim mismatch**: `ValueError` at construction, sidecar fails to start. Same shape as the other backends.
- **OOM at inference time**: uvicorn worker crashes, systemd restarts (`Restart=on-failure`, `RestartSec=3`). We do not add a graceful 503 — bench is not prod.
- **Empty attention mask edge case**: `seq_lens.clamp(min=0)` guards last-token indexing; defensive against pathological inputs even though tokenizer should never produce them.

What we explicitly do NOT do:

- No int8 / fp16 quantization step. The repo's prebuilt `model.onnx` is fp32; if RAM headroom on Pi 5 turns out to be a problem we revisit with `model_int8.onnx` (if Jina ships one) as a follow-up. fp32 first.
- No on-host ONNX export. The repo ships the onnx file.
- No custom OpenAI adapter changes — Levara already speaks OpenAI to the sidecar.

## Testing

Three layers:

### Unit — `scripts/load-profiles/tests/test_onnx_backend.py` (new)

Mock `optimum.onnxruntime.ORTModelForFeatureExtraction` and `transformers.AutoTokenizer`. Cover:

- `_last_token_pool` picks the correct index when sequence lengths in a batch differ.
- The returned vectors are L2-normalized to ‖v‖≈1.
- Dim mismatch (probe returns ≠ `recipe.dim`) raises `ValueError`.
- Empty attention mask path does not crash (`seq_lens.clamp(min=0)` invariant).

Runs in the existing pytest harness on Mac — no Pi required.

### Local smoke — Mac arm64

`python -m embed_bench.smoke --model jina` (script added) instantiates `ONNXBackend` against the real repo on Mac, embeds three short phrases, prints shape and L2 norm. If it dies on Mac it will die on Pi — catch transformers/optimum/onnxruntime issues before deploy.

### Preflight on Pi

Existing `preflight_model.py` is sufficient: it checks `/health` returns `{"model": "jina-v5-text-nano-retrieval", "dim": 768, …}` and that a probe POST to `/v1/embeddings` returns a 768-element vector. No new preflight code.

End-to-end success criterion: `bash scripts/load-profiles/run_all_models.sh jina` completes with three non-empty JSONL outputs (`p3_jina.jsonl`, `p4_jina.jsonl`, `p5_jina.jsonl`) in `scripts/load-profiles/out/`.

## Out of scope

- Mac potion-sidecar deployment (separate spec, step 2 of the four-step embed-rollout plan).
- Migrating prod `embed-potion.service` to also serve jina — bench-only here.
- Quality analysis of jina vs potion vs nomic — that's the deliverable of P3/P4/P5 runs after this spec is implemented.
- ONNX quantization research.
- Replacing the omni recipe entry name — we keep `"jina"` as the short name for shell continuity; only `repo`, `dim`, `openai_name`, `backend` change inside the recipe value.

## Risks and mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| `trust_remote_code=True` still triggers the same `TokenizersBackend` bug on the text-only repo | Medium | Local smoke on Mac before deploying; if it reproduces, fall back to plain `AutoTokenizer.from_pretrained(repo)` without subfolder and reload the tokenizer from the repo root (not the `onnx/` subfolder) — this is the common workaround for Jina ONNX repos. |
| `optimum[onnxruntime]` ARM wheel missing or broken on Pi 5 (Debian bookworm, Python 3.11) | Low | onnxruntime 1.17+ ships aarch64 wheels; we pin a known-good version in `requirements.txt`. Fallback: build from source (slow but supported). |
| Pi 5 8 GB RAM headroom too thin (Ollama + Levara-bench + ORT session ≈ 2.5 GB) | Low | Stop other bench processes before run (already done by `stop_bench` in `run_all_models.sh`); if still OOM, switch to int8 ONNX variant if available. |
| Pre-embed flow on Mac side sends 512-d vectors (cached from old recipe) instead of 768-d | Low | Recipe change is atomic; no caching of embeddings between models — each run starts from texts. |
| HF cache cold pull takes >10 min on Pi link | Medium | Warm the cache by running smoke locally then `rsync` `hf-cache/` to Pi, or just budget time and let it run once. |

## Acceptance

The design is accepted when:

1. `make_backend("jina")` returns an instance that embeds text on Pi without errors.
2. `preflight_model.py --model jina --host 127.0.0.1` reports `"ok": true`.
3. `run_all_models.sh jina` produces three non-empty JSONL files.
4. `analyze.py --by-model` includes `jina` columns in the comparison output.

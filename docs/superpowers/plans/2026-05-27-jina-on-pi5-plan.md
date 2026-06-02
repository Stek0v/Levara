# Jina on Pi 5 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unblock the third calibration model in the Pi 5 embed-bench harness by replacing the broken `jina-omni-nano` transformers loader with `jina-embeddings-v5-text-nano-retrieval` via ONNX Runtime, then run P3/P4/P5 against jina.

**Architecture:** Add a third backend (`ONNXBackend`) alongside `TransformersBackend` and `Model2VecBackend` in `scripts/load-profiles/embed_bench/backends.py`. Update the `jina` recipe to point at the text-only retrieval-tuned repo with ONNX weights and last-token pooling. No changes to `server.py`, `levara-bench`, or Levara — the sidecar contract is identical.

**Tech Stack:** Python 3.11, `optimum[onnxruntime]`, `transformers`, `torch`, `fastapi` (existing).

**Spec:** `docs/superpowers/specs/2026-05-27-jina-on-pi5-design.md`

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `scripts/load-profiles/embed_bench/backends.py` | Modify | Add `ONNXBackend` class + extend `make_backend` dispatch |
| `scripts/load-profiles/embed_bench/recipes.py` | Modify | Update `jina` recipe: repo, backend=onnx, dim=768, openai_name |
| `scripts/load-profiles/embed_bench/requirements.txt` | Modify | Add `optimum[onnxruntime]` pin |
| `scripts/load-profiles/tests/test_onnx_backend.py` | Create | Unit tests for `_last_token_pool`, L2-norm, dim-mismatch |
| `scripts/load-profiles/embed_bench/smoke.py` | Create | `python -m embed_bench.smoke --model jina` runner |
| `scripts/load-profiles/run_all_models.sh` | Modify | `jina) OPENAI=…; DIM=768` |

All Pi-side deployment uses the existing `run_all_models.sh` flow — no separate deploy script.

---

### Task 1: Add ONNXBackend skeleton with last-token pool helper

**Files:**
- Modify: `scripts/load-profiles/embed_bench/backends.py`
- Test: `scripts/load-profiles/tests/test_onnx_backend.py` (create)

- [ ] **Step 1: Write the failing test for `_last_token_pool`**

Create `scripts/load-profiles/tests/test_onnx_backend.py`:

```python
"""Unit tests for ONNXBackend last-token pool + L2-norm + dim guard.

We don't load the real jina ONNX model — we mock optimum.onnxruntime and
transformers.AutoTokenizer so the test runs in milliseconds on any host.
"""
from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest
import torch

from embed_bench.backends import ONNXBackend
from embed_bench.recipes import Recipe


def _make_recipe(dim: int = 4) -> Recipe:
    return Recipe(
        short="jina",
        repo="fake/jina-onnx",
        backend="onnx",
        dim=dim,
        openai_name="fake-jina",
        trust_remote_code=True,
    )


def _patched_backend(probe_hidden, probe_mask, recipe):
    """Construct an ONNXBackend with mocked ORT + tokenizer so we can hit
    real pooling code without downloading weights."""
    tok = MagicMock()
    tok.return_value = {"input_ids": torch.tensor([[1, 1]]), "attention_mask": probe_mask}
    model = MagicMock()
    model.return_value = MagicMock(last_hidden_state=probe_hidden)
    with patch("optimum.onnxruntime.ORTModelForFeatureExtraction.from_pretrained", return_value=model), \
         patch("transformers.AutoTokenizer.from_pretrained", return_value=tok):
        return ONNXBackend(recipe), tok, model


def test_last_token_pool_picks_last_real_token_per_row():
    # Two-row batch, row 0 has 3 real tokens, row 1 has 2.
    hidden = torch.tensor([
        [[1.0, 0.0, 0.0, 0.0],
         [0.0, 1.0, 0.0, 0.0],
         [0.0, 0.0, 1.0, 0.0],  # row 0 last real
         [9.9, 9.9, 9.9, 9.9]], # row 0 padding (must be ignored)
        [[0.0, 0.0, 0.0, 1.0],
         [1.0, 1.0, 0.0, 0.0],  # row 1 last real
         [9.9, 9.9, 9.9, 9.9],
         [9.9, 9.9, 9.9, 9.9]],
    ])
    mask = torch.tensor([[1, 1, 1, 0], [1, 1, 0, 0]])

    # Probe path uses a different hidden state (single token), set up so
    # the dim guard passes.
    backend, _, model = _patched_backend(
        probe_hidden=torch.tensor([[[0.1, 0.2, 0.3, 0.4]]]),
        probe_mask=torch.tensor([[1]]),
        recipe=_make_recipe(dim=4),
    )

    pooled = backend._last_token_pool(hidden, mask)
    # row 0 → index 2, row 1 → index 1
    expected = torch.tensor([
        [0.0, 0.0, 1.0, 0.0],
        [1.0, 1.0, 0.0, 0.0],
    ])
    assert torch.equal(pooled, expected)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py::test_last_token_pool_picks_last_real_token_per_row -v
```

Expected: FAIL with `ImportError: cannot import name 'ONNXBackend' from 'embed_bench.backends'`.

- [ ] **Step 3: Implement ONNXBackend (minimal — pooling + L2-norm only, no dim guard yet)**

Append to `scripts/load-profiles/embed_bench/backends.py` after `Model2VecBackend`:

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

- [ ] **Step 4: Run test to verify it passes**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py::test_last_token_pool_picks_last_real_token_per_row -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/backends.py scripts/load-profiles/tests/test_onnx_backend.py
git commit -m "feat(bench): add ONNXBackend with last-token pool"
```

---

### Task 2: Add L2-normalization unit test

**Files:**
- Test: `scripts/load-profiles/tests/test_onnx_backend.py:append`

- [ ] **Step 1: Write the failing test (will actually pass — verifying behavior)**

Append to `test_onnx_backend.py`:

```python
def test_embed_returns_l2_normalized_vectors():
    # Probe + embed both return non-unit vectors; expect ‖v‖ ≈ 1 in output.
    hidden_probe = torch.tensor([[[3.0, 4.0, 0.0, 0.0]]])  # ‖·‖=5
    hidden_run = torch.tensor([[[6.0, 8.0, 0.0, 0.0]]])    # ‖·‖=10
    tok = MagicMock()
    tok.return_value = {"input_ids": torch.tensor([[1]]), "attention_mask": torch.tensor([[1]])}
    model = MagicMock()
    # First call: probe (during __init__). Second call: embed().
    model.side_effect = [
        MagicMock(last_hidden_state=hidden_probe),
        MagicMock(last_hidden_state=hidden_run),
    ]
    with patch("optimum.onnxruntime.ORTModelForFeatureExtraction.from_pretrained", return_value=model), \
         patch("transformers.AutoTokenizer.from_pretrained", return_value=tok):
        backend = ONNXBackend(_make_recipe(dim=4))

    out = backend.embed(["hello"])
    norm = sum(x * x for x in out[0]) ** 0.5
    assert abs(norm - 1.0) < 1e-5
```

- [ ] **Step 2: Run test**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py::test_embed_returns_l2_normalized_vectors -v
```

Expected: PASS (L2-norm is in the implementation already).

- [ ] **Step 3: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/tests/test_onnx_backend.py
git commit -m "test(bench): assert ONNXBackend L2-normalizes outputs"
```

---

### Task 3: Add dim-mismatch guard + test

**Files:**
- Modify: `scripts/load-profiles/embed_bench/backends.py` (extend `ONNXBackend.__init__`)
- Test: `scripts/load-profiles/tests/test_onnx_backend.py:append`

- [ ] **Step 1: Write the failing test**

Append to `test_onnx_backend.py`:

```python
def test_dim_mismatch_raises_value_error():
    # Recipe says dim=4, probe returns dim=7 → must raise.
    hidden = torch.tensor([[[0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7]]])
    tok = MagicMock()
    tok.return_value = {"input_ids": torch.tensor([[1]]), "attention_mask": torch.tensor([[1]])}
    model = MagicMock()
    model.return_value = MagicMock(last_hidden_state=hidden)
    with patch("optimum.onnxruntime.ORTModelForFeatureExtraction.from_pretrained", return_value=model), \
         patch("transformers.AutoTokenizer.from_pretrained", return_value=tok):
        with pytest.raises(ValueError, match="recipe dim mismatch"):
            ONNXBackend(_make_recipe(dim=4))
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py::test_dim_mismatch_raises_value_error -v
```

Expected: FAIL — no guard yet, constructor silently records `self.dim=7`.

- [ ] **Step 3: Add the guard**

In `scripts/load-profiles/embed_bench/backends.py`, in `ONNXBackend.__init__`, immediately after `self.dim = int(pooled.shape[-1])`, add:

```python
        if self.dim != recipe.dim:
            raise ValueError(
                f"recipe dim mismatch: {recipe.repo} produced {self.dim}-d, "
                f"recipe said {recipe.dim}"
            )
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/backends.py scripts/load-profiles/tests/test_onnx_backend.py
git commit -m "feat(bench): ONNXBackend dim-mismatch guard"
```

---

### Task 4: Wire `onnx` backend into `make_backend` dispatch

**Files:**
- Modify: `scripts/load-profiles/embed_bench/backends.py:make_backend`
- Test: `scripts/load-profiles/tests/test_onnx_backend.py:append`

- [ ] **Step 1: Write the failing test**

Append to `test_onnx_backend.py`:

```python
from embed_bench.backends import make_backend


def test_make_backend_dispatches_onnx_recipe():
    recipe = _make_recipe(dim=4)
    hidden = torch.tensor([[[0.1, 0.2, 0.3, 0.4]]])
    tok = MagicMock()
    tok.return_value = {"input_ids": torch.tensor([[1]]), "attention_mask": torch.tensor([[1]])}
    model = MagicMock()
    model.return_value = MagicMock(last_hidden_state=hidden)
    with patch("optimum.onnxruntime.ORTModelForFeatureExtraction.from_pretrained", return_value=model), \
         patch("transformers.AutoTokenizer.from_pretrained", return_value=tok):
        backend = make_backend(recipe)
    assert isinstance(backend, ONNXBackend)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py::test_make_backend_dispatches_onnx_recipe -v
```

Expected: FAIL with `ValueError: unknown backend 'onnx'`.

- [ ] **Step 3: Extend `make_backend`**

In `scripts/load-profiles/embed_bench/backends.py`, modify the `make_backend` function:

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

- [ ] **Step 4: Run test to verify it passes**

```bash
cd ~/src/levara/scripts/load-profiles && pytest tests/test_onnx_backend.py -v
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/backends.py scripts/load-profiles/tests/test_onnx_backend.py
git commit -m "feat(bench): make_backend dispatches onnx recipes"
```

---

### Task 5: Update the `jina` recipe

**Files:**
- Modify: `scripts/load-profiles/embed_bench/recipes.py:44-51`

- [ ] **Step 1: Read the current recipe block**

```bash
sed -n '44,51p' ~/src/levara/scripts/load-profiles/embed_bench/recipes.py
```

Expected output: the old `jina-embeddings-v5-omni-nano` recipe with `backend="transformers"`, `dim=512`.

- [ ] **Step 2: Replace the recipe**

In `scripts/load-profiles/embed_bench/recipes.py`, change the `"jina"` entry from:

```python
    "jina": Recipe(
        short="jina",
        repo="jinaai/jina-embeddings-v5-omni-nano",
        backend="transformers",
        dim=512,
        openai_name="jina-omni-nano",
        trust_remote_code=True,
    ),
```

to:

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

- [ ] **Step 3: Verify import still works**

```bash
cd ~/src/levara/scripts/load-profiles && python -c "from embed_bench.recipes import get_recipe; r = get_recipe('jina'); print(r.repo, r.backend, r.dim, r.openai_name)"
```

Expected output: `jinaai/jina-embeddings-v5-text-nano-retrieval onnx 768 jina-v5-text-nano-retrieval`

- [ ] **Step 4: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/recipes.py
git commit -m "feat(bench): jina recipe -> v5-text-nano-retrieval via onnx, dim 768"
```

---

### Task 6: Pin `optimum[onnxruntime]` in requirements

**Files:**
- Modify: `scripts/load-profiles/embed_bench/requirements.txt`

- [ ] **Step 1: Read current requirements**

```bash
cat ~/src/levara/scripts/load-profiles/embed_bench/requirements.txt
```

Expected: `fastapi`, `uvicorn[standard]`, `transformers==4.49.0`, `pillow`, `torch==2.4.1`, `model2vec`, `einops`, `numpy`, `pydantic`.

- [ ] **Step 2: Append the optimum + onnxruntime pin**

Edit `scripts/load-profiles/embed_bench/requirements.txt` and append at end of file:

```
optimum[onnxruntime]==1.23.3
```

- [ ] **Step 3: Verify pip can resolve on Mac (smoke for the resolver)**

```bash
cd ~/src/levara/scripts/load-profiles && python -m pip install --dry-run optimum[onnxruntime]==1.23.3
```

Expected: pip prints "Would install …" without errors.

- [ ] **Step 4: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/requirements.txt
git commit -m "feat(bench): pin optimum[onnxruntime]==1.23.3 for ONNX backend"
```

---

### Task 7: Add local smoke runner

**Files:**
- Create: `scripts/load-profiles/embed_bench/smoke.py`

- [ ] **Step 1: Create the smoke script**

Create `scripts/load-profiles/embed_bench/smoke.py`:

```python
"""Local smoke for the embed-bench backends.

Usage:
    python -m embed_bench.smoke --model jina

Instantiates the real backend (downloads weights to HF cache on first run),
embeds three short phrases, prints shape and L2 norm of each vector.

Run this on Mac before deploying to Pi — if it dies here it will die there.
"""
from __future__ import annotations

import argparse
import math

from embed_bench.backends import make_backend
from embed_bench.recipes import get_recipe


PHRASES = [
    "the cat sat on the mat",
    "import numpy as np",
    "embeddings are vectors that encode semantic meaning",
]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True, help="short name from recipes.RECIPES")
    args = parser.parse_args()

    recipe = get_recipe(args.model)
    print(f"[smoke] model={recipe.openai_name} backend={recipe.backend} expected_dim={recipe.dim}")
    backend = make_backend(recipe)
    print(f"[smoke] backend ready, dim={backend.dim}")

    vectors = backend.embed(PHRASES)
    for phrase, vec in zip(PHRASES, vectors):
        norm = math.sqrt(sum(x * x for x in vec))
        print(f"  len={len(vec)} norm={norm:.4f}  {phrase!r}")
    print("[smoke] ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

- [ ] **Step 2: Install optimum locally if not already**

```bash
cd ~/src/levara/scripts/load-profiles && python -m pip install -q "optimum[onnxruntime]==1.23.3"
```

Expected: pip succeeds.

- [ ] **Step 3: Run smoke against jina**

```bash
cd ~/src/levara/scripts/load-profiles && python -m embed_bench.smoke --model jina
```

Expected output:
```
[smoke] model=jina-v5-text-nano-retrieval backend=onnx expected_dim=768
[smoke] backend ready, dim=768
  len=768 norm=1.0000  'the cat sat on the mat'
  len=768 norm=1.0000  'import numpy as np'
  len=768 norm=1.0000  'embeddings are vectors that encode semantic meaning'
[smoke] ok
```

First run downloads ~1 GB to `~/.cache/huggingface/`. If it fails with `TokenizersBackend does not exist` or similar custom-code error, see Task 7a (fallback).

- [ ] **Step 4: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/smoke.py
git commit -m "feat(bench): local smoke runner for embed_bench backends"
```

---

### Task 7a: Tokenizer fallback (run ONLY if Task 7 step 3 fails with TokenizersBackend error)

**Files:**
- Modify: `scripts/load-profiles/embed_bench/backends.py:ONNXBackend.__init__`

- [ ] **Step 1: Add fallback tokenizer load**

If `AutoTokenizer.from_pretrained(repo, trust_remote_code=True)` fails with `TokenizersBackend does not exist or is not currently imported`, replace the tokenizer line in `ONNXBackend.__init__` with:

```python
        try:
            self.tokenizer = AutoTokenizer.from_pretrained(
                recipe.repo, trust_remote_code=recipe.trust_remote_code,
            )
        except ValueError as e:
            if "TokenizersBackend" not in str(e):
                raise
            # Known jina custom-tokenizer regression: load without remote code.
            # The plain tokenizer config still produces valid input_ids for the
            # ONNX model — only the custom Python wrapper is unavailable.
            self.tokenizer = AutoTokenizer.from_pretrained(recipe.repo)
```

- [ ] **Step 2: Re-run the smoke**

```bash
cd ~/src/levara/scripts/load-profiles && python -m embed_bench.smoke --model jina
```

Expected: smoke now succeeds with the fallback. If still fails, surface the new traceback to the human — the spec's Risks section flags this as the catch-all escalation.

- [ ] **Step 3: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/embed_bench/backends.py
git commit -m "feat(bench): jina tokenizer fallback when TokenizersBackend missing"
```

If Task 7 step 3 succeeded without this fallback, skip Task 7a entirely.

---

### Task 8: Update `run_all_models.sh` jina branch

**Files:**
- Modify: `scripts/load-profiles/run_all_models.sh:51`

- [ ] **Step 1: Verify current line**

```bash
grep -n "jina)" ~/src/levara/scripts/load-profiles/run_all_models.sh
```

Expected: line 51 with `jina) OPENAI="jina-omni-nano"; DIM=512 ;;`.

- [ ] **Step 2: Edit the case branch**

Change line 51 of `scripts/load-profiles/run_all_models.sh` from:

```sh
    jina)    OPENAI="jina-omni-nano";               DIM=512 ;;
```

to:

```sh
    jina)    OPENAI="jina-v5-text-nano-retrieval";  DIM=768 ;;
```

- [ ] **Step 3: Verify**

```bash
grep -n "jina)" ~/src/levara/scripts/load-profiles/run_all_models.sh
```

Expected: line 51 with the new values.

- [ ] **Step 4: Commit**

```bash
cd ~/src/levara && git add scripts/load-profiles/run_all_models.sh
git commit -m "feat(bench): run_all_models jina branch -> dim 768, v5-text-nano-retrieval"
```

---

### Task 9: Deploy + run jina on Pi via the harness

**Files:** none (operational — the harness deploys itself via rsync inside `run_all_models.sh`).

- [ ] **Step 1: Ensure Pi venv has the new dependency**

```bash
ssh stek0v@10.23.0.53 "/home/stek0v/embed-bench/venv/bin/python -m pip install 'optimum[onnxruntime]==1.23.3'"
```

Expected: pip succeeds; aarch64 wheels for onnxruntime download and install.

- [ ] **Step 2: Run the jina-only harness pass**

```bash
cd ~/src/levara && bash scripts/load-profiles/run_all_models.sh jina 2>&1 | tee /tmp/jina_run.log
```

This runs (in order): stop bench services, write `EMBED_BENCH_MODEL=jina` drop-in, rsync `scripts/`, restart `embed-bench` + `levara-bench`, preflight, seed three `loadprofile_*_jina` collections, run P3, P4, P5 against the bench Levara on port 8091.

Expected final lines:
```
[run] p5 / jina
=== analyze ===
OK. Output: docs/load-profile-analysis-pi-multimodel.md
```

If preflight fails: read `journalctl -u embed-bench.service -n 60 --no-pager` over ssh to diagnose. The first-run download of the jina ONNX model adds ~5-10 minutes wall time.

- [ ] **Step 3: Verify outputs exist and are non-empty**

```bash
ls -la ~/src/levara/scripts/load-profiles/out/p{3,4,5}_jina.jsonl
wc -l ~/src/levara/scripts/load-profiles/out/p{3,4,5}_jina.jsonl
```

Expected: all three files exist with >0 lines each.

- [ ] **Step 4: Commit the result JSONLs + analysis markdown**

```bash
cd ~/src/levara && git add scripts/load-profiles/out/p3_jina.jsonl scripts/load-profiles/out/p4_jina.jsonl scripts/load-profiles/out/p5_jina.jsonl docs/load-profile-analysis-pi-multimodel.md
git commit -m "chore(bench): record jina P3/P4/P5 run results on Pi 5"
```

---

## Self-review

**Spec coverage:**
- ONNXBackend class → Tasks 1, 3, 4.
- Recipe update → Task 5.
- `make_backend` dispatch → Task 4.
- `optimum[onnxruntime]` dependency → Task 6.
- `run_all_models.sh` jina branch → Task 8.
- Last-token pool + L2-norm + dim guard → Tasks 1, 2, 3.
- Local smoke runner → Task 7.
- TokenizersBackend fallback (Risk #1 mitigation) → Task 7a (conditional).
- Pi deploy + run + recorded outputs → Task 9.

No spec section is uncovered. Quantization, Mac-side potion sidecar, and prod migration are explicitly out-of-scope in the spec and stay out of the plan.

**Placeholder scan:** no TBD / TODO / "appropriate" / "handle edge cases" without code.

**Type consistency:** `ONNXBackend.__init__(self, recipe: Recipe)`, `_last_token_pool(self, last_hidden_state, attention_mask)`, `embed(self, texts: list[str]) -> list[list[float]]` — same names across Tasks 1, 2, 3, 4. Recipe field names (`short`, `repo`, `backend`, `dim`, `openai_name`, `trust_remote_code`) match the existing dataclass in `recipes.py`.

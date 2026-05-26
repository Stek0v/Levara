# Pi 3-Embed-Model Honest Calibration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run granite-97m, jina-omni-nano, and potion-code-16M through the full Levara cognify pipeline on an isolated Pi bench instance, sequentially, to produce comparable calibration + retrieval-quality data.

**Architecture:** New `embed-bench` FastAPI sidecar on Pi:9101 (one model loaded at a time). New `levara-bench` systemd unit on Pi:8091 (separate data-dir, separate JWT, drop-in `EMBEDDING_*` + `-dim` per model). Existing load-profile harness (`runner.py`, `seed/`, `analyze.py`) gets a `--model` axis. A `seed_one.py` helper seeds one collection via the full cognify pipeline. Orchestrator shell script sequences: stop → drop-in → start → preflight → seed → P4 + P5 runs → stop. After all three models complete, `analyze.py --by-model` writes the comparison doc.

**Tech Stack:** Python 3.11 (FastAPI, transformers, model2vec, pytest), Bash (orchestration), systemd (services), Levara Go binary (unchanged from PR #85), ssh/scp (deployment), Ollama (LLM for cognify), qwen3:0.6b (entity extraction).

---

## File map

**Create:**
- `scripts/load-profiles/embed_bench/__init__.py`
- `scripts/load-profiles/embed_bench/recipes.py`
- `scripts/load-profiles/embed_bench/backends.py`
- `scripts/load-profiles/embed_bench/server.py`
- `scripts/load-profiles/embed_bench/requirements.txt`
- `scripts/load-profiles/embed_bench/tests/__init__.py`
- `scripts/load-profiles/embed_bench/tests/test_recipes.py`
- `scripts/load-profiles/embed_bench/tests/test_backends.py`
- `scripts/load-profiles/embed_bench/tests/test_server.py`
- `scripts/load-profiles/preflight_model.py`
- `scripts/load-profiles/seed_one.py`
- `scripts/load-profiles/run_all_models.sh`
- `scripts/load-profiles/tests/__init__.py`
- `scripts/load-profiles/tests/test_preflight.py`
- `scripts/load-profiles/tests/test_keyword_metrics.py`
- `scripts/load-profiles/tests/test_analyze_by_model.py`
- `deploy/bench/embed-bench.service`
- `deploy/bench/levara-bench.service`
- `deploy/bench/setup_pi.sh`
- `deploy/bench/README.md`

**Modify:**
- `scripts/load-profiles/runner.py` — keyword_metrics() helper
- `scripts/load-profiles/p4_memory_palace.py` — `--model`, `--embed-dim`, `--collection-override`
- `scripts/load-profiles/p5_filtered_search.py` — same flags
- `scripts/load-profiles/seed/code_corpus.py` — `seed_via_cognify()` pathway
- `scripts/load-profiles/analyze.py` — `--by-model` group

---

## Task 1: Recipes registry (TDD)

The recipe maps a short model name → `{repo, backend, dim, openai_name}`. Drives the sidecar and orchestrator.

**Files:**
- Create: `scripts/load-profiles/embed_bench/__init__.py`
- Create: `scripts/load-profiles/embed_bench/recipes.py`
- Create: `scripts/load-profiles/embed_bench/tests/__init__.py`
- Create: `scripts/load-profiles/embed_bench/tests/test_recipes.py`

- [ ] **Step 1: Create empty package markers**

```bash
mkdir -p scripts/load-profiles/embed_bench/tests
touch scripts/load-profiles/embed_bench/__init__.py
touch scripts/load-profiles/embed_bench/tests/__init__.py
```

- [ ] **Step 2: Write failing test**

Create `scripts/load-profiles/embed_bench/tests/test_recipes.py`:

```python
import pytest

from scripts.load_profiles.embed_bench.recipes import RECIPES, get_recipe


def test_three_recipes_present():
    assert set(RECIPES.keys()) == {"potion", "granite", "jina"}


def test_potion_recipe_shape():
    r = get_recipe("potion")
    assert r.repo == "minishlab/potion-code-16M"
    assert r.backend == "model2vec"
    assert r.dim == 256
    assert r.openai_name == "potion-code-16M"


def test_granite_recipe_shape():
    r = get_recipe("granite")
    assert r.repo == "ibm-granite/granite-embedding-97m-multilingual-r2"
    assert r.backend == "transformers"
    assert r.dim == 384
    assert r.openai_name == "granite-97m-multilingual-r2"


def test_jina_recipe_shape():
    r = get_recipe("jina")
    assert r.repo == "jinaai/jina-embeddings-v5-omni-nano"
    assert r.backend == "transformers"
    assert r.dim == 512
    assert r.openai_name == "jina-omni-nano"
    assert r.trust_remote_code is True


def test_unknown_recipe_raises():
    with pytest.raises(KeyError):
        get_recipe("nope")
```

- [ ] **Step 3: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_recipes.py -v`
Expected: FAIL with ModuleNotFoundError.

- [ ] **Step 4: Implement**

Create `scripts/load-profiles/embed_bench/recipes.py`:

```python
"""Model recipes for the embed-bench sidecar.

Each recipe records the HuggingFace repo, the backend name (consumed by
backends.py), the expected output dimension (consumed by preflight and
collection creation), and the model name to advertise in OpenAI-style
embedding responses (so cognify accepts our /v1/embeddings).
"""
from dataclasses import dataclass


@dataclass(frozen=True)
class Recipe:
    short: str
    repo: str
    backend: str  # "transformers" | "model2vec"
    dim: int
    openai_name: str
    trust_remote_code: bool = False


RECIPES: dict[str, Recipe] = {
    "potion": Recipe(
        short="potion",
        repo="minishlab/potion-code-16M",
        backend="model2vec",
        dim=256,
        openai_name="potion-code-16M",
    ),
    "granite": Recipe(
        short="granite",
        repo="ibm-granite/granite-embedding-97m-multilingual-r2",
        backend="transformers",
        dim=384,
        openai_name="granite-97m-multilingual-r2",
    ),
    "jina": Recipe(
        short="jina",
        repo="jinaai/jina-embeddings-v5-omni-nano",
        backend="transformers",
        dim=512,
        openai_name="jina-omni-nano",
        trust_remote_code=True,
    ),
}


def get_recipe(short: str) -> Recipe:
    if short not in RECIPES:
        raise KeyError(f"unknown model short name: {short!r}; known: {sorted(RECIPES)}")
    return RECIPES[short]
```

Note on dims: dims are best-effort from model cards. The dim probe in `backends.py` (Task 2) raises if reality disagrees — fix the recipe and re-run.

- [ ] **Step 5: Run test, verify PASS**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_recipes.py -v`
Expected: 5 passed.

- [ ] **Step 6: Commit**

```bash
git add scripts/load-profiles/embed_bench/__init__.py \
        scripts/load-profiles/embed_bench/tests/__init__.py \
        scripts/load-profiles/embed_bench/recipes.py \
        scripts/load-profiles/embed_bench/tests/test_recipes.py
git commit -m "feat(embed-bench): add model recipe registry"
```

---

## Task 2: Backend protocol (Transformers + Model2Vec) (TDD)

Two backends behind a shared protocol exposing `dim` + `embed(list[str]) -> list[list[float]]`.

**Files:**
- Create: `scripts/load-profiles/embed_bench/backends.py`
- Create: `scripts/load-profiles/embed_bench/requirements.txt`
- Create: `scripts/load-profiles/embed_bench/tests/test_backends.py`

- [ ] **Step 1: Write failing test**

Create `scripts/load-profiles/embed_bench/tests/test_backends.py`:

```python
"""Backend tests. Skipped when the HF cache lacks the model files."""
import os
from pathlib import Path

import pytest

from scripts.load_profiles.embed_bench.backends import make_backend
from scripts.load_profiles.embed_bench.recipes import get_recipe


def _cache_has(repo: str) -> bool:
    hf_home = Path(os.environ.get("HF_HOME", Path.home() / ".cache" / "huggingface"))
    safe = repo.replace("/", "--")
    return (hf_home / "hub" / f"models--{safe}").exists()


@pytest.mark.parametrize("short", ["potion", "granite", "jina"])
def test_backend_dim_matches_recipe(short):
    recipe = get_recipe(short)
    if not _cache_has(recipe.repo):
        pytest.skip(f"HF cache missing for {recipe.repo}")
    backend = make_backend(recipe)
    assert backend.dim == recipe.dim


@pytest.mark.parametrize("short", ["potion", "granite", "jina"])
def test_backend_embed_returns_correct_shape(short):
    recipe = get_recipe(short)
    if not _cache_has(recipe.repo):
        pytest.skip(f"HF cache missing for {recipe.repo}")
    backend = make_backend(recipe)
    out = backend.embed(["hello world", "another query"])
    assert len(out) == 2
    assert len(out[0]) == recipe.dim
    assert all(isinstance(x, float) for x in out[0])
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_backends.py -v`
Expected: ModuleNotFoundError.

- [ ] **Step 3: Implement**

Create `scripts/load-profiles/embed_bench/backends.py`:

```python
"""Embedding backends.

TransformersBackend: HuggingFace AutoModel + AutoTokenizer; mean-pools the
last hidden state with attention mask, then L2-normalizes.

Model2VecBackend: minishlab/model2vec StaticModel (single matmul, sub-ms).
"""
from __future__ import annotations

from typing import Protocol

import numpy as np

from .recipes import Recipe


class Backend(Protocol):
    dim: int

    def embed(self, texts: list[str]) -> list[list[float]]: ...


class TransformersBackend:
    def __init__(self, recipe: Recipe):
        from transformers import AutoModel, AutoTokenizer
        import torch

        self._torch = torch
        self.tokenizer = AutoTokenizer.from_pretrained(
            recipe.repo, trust_remote_code=recipe.trust_remote_code
        )
        self.model = AutoModel.from_pretrained(
            recipe.repo, trust_remote_code=recipe.trust_remote_code
        )
        self.model.eval()
        with torch.no_grad():
            inputs = self.tokenizer(["dim probe"], padding=True, truncation=True, return_tensors="pt")
            out = self.model(**inputs)
            pooled = self._mean_pool(out.last_hidden_state, inputs["attention_mask"])
            self.dim = pooled.shape[-1]
        if self.dim != recipe.dim:
            raise ValueError(
                f"recipe dim mismatch: {recipe.repo} produced {self.dim}-d, "
                f"recipe said {recipe.dim}"
            )

    def _mean_pool(self, last_hidden_state, attention_mask):
        mask = attention_mask.unsqueeze(-1).float()
        summed = (last_hidden_state * mask).sum(dim=1)
        counts = mask.sum(dim=1).clamp(min=1e-9)
        return summed / counts

    def embed(self, texts: list[str]) -> list[list[float]]:
        with self._torch.no_grad():
            inputs = self.tokenizer(
                texts, padding=True, truncation=True, max_length=512, return_tensors="pt"
            )
            out = self.model(**inputs)
            pooled = self._mean_pool(out.last_hidden_state, inputs["attention_mask"])
            normed = self._torch.nn.functional.normalize(pooled, p=2, dim=1)
            return normed.cpu().tolist()


class Model2VecBackend:
    def __init__(self, recipe: Recipe):
        from model2vec import StaticModel

        self.model = StaticModel.from_pretrained(recipe.repo)
        probe = self.model.encode(["dim probe"])
        self.dim = int(np.asarray(probe).shape[-1])
        if self.dim != recipe.dim:
            raise ValueError(
                f"recipe dim mismatch: {recipe.repo} produced {self.dim}-d, "
                f"recipe said {recipe.dim}"
            )

    def embed(self, texts: list[str]) -> list[list[float]]:
        arr = self.model.encode(texts)
        return np.asarray(arr).astype(float).tolist()


def make_backend(recipe: Recipe) -> Backend:
    if recipe.backend == "transformers":
        return TransformersBackend(recipe)
    if recipe.backend == "model2vec":
        return Model2VecBackend(recipe)
    raise ValueError(f"unknown backend {recipe.backend!r}")
```

- [ ] **Step 4: Create requirements.txt**

Create `scripts/load-profiles/embed_bench/requirements.txt`:

```
fastapi==0.115.0
uvicorn[standard]==0.32.0
transformers==4.46.0
torch==2.4.1
model2vec==0.4.0
numpy==1.26.4
pydantic==2.9.2
```

- [ ] **Step 5: Run test, verify PASS (or skip cleanly)**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_backends.py -v`
Expected (dev box without HF cache): 6 SKIPPED. On Pi after prefetch: PASS.

- [ ] **Step 6: Commit**

```bash
git add scripts/load-profiles/embed_bench/backends.py \
        scripts/load-profiles/embed_bench/tests/test_backends.py \
        scripts/load-profiles/embed_bench/requirements.txt
git commit -m "feat(embed-bench): transformers + model2vec backends with dim probe"
```

---

## Task 3: FastAPI sidecar server (TDD)

OpenAI-compatible `POST /v1/embeddings` + `GET /health`. Loads ONE recipe from `EMBED_BENCH_MODEL` at startup.

**Files:**
- Create: `scripts/load-profiles/embed_bench/server.py`
- Create: `scripts/load-profiles/embed_bench/tests/test_server.py`

- [ ] **Step 1: Write failing test**

Create `scripts/load-profiles/embed_bench/tests/test_server.py`:

```python
"""Server tests use a fake backend; no model files needed."""
import os

import pytest
from fastapi.testclient import TestClient

os.environ["EMBED_BENCH_FAKE_DIM"] = "8"
os.environ["EMBED_BENCH_MODEL"] = "_fake"

from scripts.load_profiles.embed_bench.server import build_app  # noqa: E402


@pytest.fixture
def client():
    return TestClient(build_app())


def test_health_returns_model_dim_backend(client):
    r = client.get("/health")
    assert r.status_code == 200
    body = r.json()
    assert body["model"] == "_fake"
    assert body["dim"] == 8
    assert body["backend"] == "fake"
    assert isinstance(body["ram_mb"], int)


def test_embeddings_returns_openai_shape(client):
    r = client.post("/v1/embeddings", json={"input": ["a", "b"], "model": "_fake"})
    assert r.status_code == 200
    body = r.json()
    assert body["model"] == "_fake"
    assert len(body["data"]) == 2
    assert len(body["data"][0]["embedding"]) == 8
    assert body["data"][0]["index"] == 0


def test_embeddings_accepts_single_string(client):
    r = client.post("/v1/embeddings", json={"input": "single", "model": "_fake"})
    assert r.status_code == 200
    assert len(r.json()["data"]) == 1


def test_embeddings_empty_input_returns_400(client):
    r = client.post("/v1/embeddings", json={"input": [], "model": "_fake"})
    assert r.status_code == 400
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_server.py -v`

- [ ] **Step 3: Implement**

Create `scripts/load-profiles/embed_bench/server.py`:

```python
"""Embed-bench FastAPI sidecar.

Reads EMBED_BENCH_MODEL at startup (e.g. "potion" | "granite" | "jina"),
resolves the recipe, instantiates the backend, exposes:

  GET  /health           -> {model, dim, ram_mb, backend}
  POST /v1/embeddings    -> OpenAI-compatible

Fake mode (unit tests only): EMBED_BENCH_MODEL=_fake + EMBED_BENCH_FAKE_DIM=N.
"""
from __future__ import annotations

import os
import resource
from typing import Union

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from .backends import make_backend
from .recipes import get_recipe


class EmbedRequest(BaseModel):
    input: Union[str, list[str]]
    model: str | None = None


def _ram_mb() -> int:
    return int(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024)


class _FakeBackend:
    def __init__(self, dim: int):
        self.dim = dim

    def embed(self, texts: list[str]) -> list[list[float]]:
        return [[float((hash(t) + i) % 7) / 7.0 for i in range(self.dim)] for t in texts]


def build_app() -> FastAPI:
    model_short = os.environ.get("EMBED_BENCH_MODEL", "")
    if not model_short:
        raise RuntimeError("EMBED_BENCH_MODEL env required")

    if model_short == "_fake":
        backend = _FakeBackend(int(os.environ.get("EMBED_BENCH_FAKE_DIM", "8")))
        openai_name = "_fake"
        backend_name = "fake"
    else:
        recipe = get_recipe(model_short)
        backend = make_backend(recipe)
        openai_name = recipe.openai_name
        backend_name = recipe.backend

    app = FastAPI(title="embed-bench", version="1")

    @app.get("/health")
    def health() -> dict:
        return {
            "model": openai_name,
            "dim": backend.dim,
            "ram_mb": _ram_mb(),
            "backend": backend_name,
        }

    @app.post("/v1/embeddings")
    def embeddings(req: EmbedRequest) -> dict:
        texts = [req.input] if isinstance(req.input, str) else req.input
        if not texts:
            raise HTTPException(status_code=400, detail="input must not be empty")
        vectors = backend.embed(texts)
        return {
            "model": openai_name,
            "data": [{"embedding": v, "index": i} for i, v in enumerate(vectors)],
            "object": "list",
        }

    return app


app = build_app() if os.environ.get("EMBED_BENCH_MODEL") else None
```

- [ ] **Step 4: Run test, verify PASS**

Run: `python -m pytest scripts/load-profiles/embed_bench/tests/test_server.py -v`
Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/load-profiles/embed_bench/server.py \
        scripts/load-profiles/embed_bench/tests/test_server.py
git commit -m "feat(embed-bench): FastAPI sidecar with OpenAI /v1/embeddings"
```

---

## Task 4: Pi deployment artifacts

Two systemd units + setup script. No unit tests — validated by Task 11 smoke.

**Files:**
- Create: `deploy/bench/embed-bench.service`
- Create: `deploy/bench/levara-bench.service`
- Create: `deploy/bench/setup_pi.sh`
- Create: `deploy/bench/README.md`

- [ ] **Step 1: Create embed-bench systemd unit**

Create `deploy/bench/embed-bench.service`:

```ini
[Unit]
Description=Embed-bench sidecar (Levara load-profile model swap)
After=network-online.target

[Service]
Type=simple
User=stek0v
WorkingDirectory=/home/stek0v/embed-bench
Environment=PYTHONUNBUFFERED=1
Environment=HF_HOME=/home/stek0v/embed-bench/hf-cache
ExecStart=/home/stek0v/embed-bench/venv/bin/uvicorn \
  scripts.load_profiles.embed_bench.server:app \
  --host 127.0.0.1 --port 9101 --workers 1
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

`EMBED_BENCH_MODEL` is provided via drop-in `/etc/systemd/system/embed-bench.service.d/model.conf` written by the orchestrator.

- [ ] **Step 2: Create levara-bench systemd unit**

Create `deploy/bench/levara-bench.service`:

```ini
[Unit]
Description=Levara bench instance (model calibration)
After=network-online.target embed-bench.service ollama.service
Wants=network-online.target

[Service]
Type=simple
User=stek0v
WorkingDirectory=/home/stek0v/levara-bench
Environment=DB_PROVIDER=sqlite
Environment=DB_PATH=/home/stek0v/levara-bench/data/levara.db
Environment=EMBEDDING_ENDPOINT=http://127.0.0.1:9101/v1/embeddings
Environment=LLM_PROVIDER=openai
Environment=LLM_ENDPOINT=http://127.0.0.1:11434/v1
Environment=LLM_MODEL=qwen3:0.6b
Environment=RERANK_ENDPOINT=http://127.0.0.1:9100/rerank
Environment=RERANK_MODEL=mmini-L12-int8
Environment=RERANK_BUDGET_MS=5000
Environment=LOG_LEVEL=INFO
ExecStart=/home/stek0v/levara-bench/levara -standalone=true -port=8091 -grpc-port=0 \
  -data-dir=/home/stek0v/levara-bench/data -node-id=pi-bench
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

`JWT_SECRET`, `EMBEDDING_MODEL`, and `-dim` are provided via drop-ins.

- [ ] **Step 3: Create setup script**

Create `deploy/bench/setup_pi.sh`:

```bash
#!/usr/bin/env bash
# One-time Pi setup. Idempotent.
# Usage: ssh stek0v@10.23.0.53 bash -s < deploy/bench/setup_pi.sh
set -euo pipefail

EMBED_DIR=/home/stek0v/embed-bench
BENCH_DIR=/home/stek0v/levara-bench
REPO_DIR=/home/stek0v/levara-source

mkdir -p "$EMBED_DIR/hf-cache" "$BENCH_DIR/data"

if [ ! -d "$EMBED_DIR/venv" ]; then
  python3 -m venv "$EMBED_DIR/venv"
fi
"$EMBED_DIR/venv/bin/pip" install --upgrade pip
"$EMBED_DIR/venv/bin/pip" install -r "$REPO_DIR/scripts/load-profiles/embed_bench/requirements.txt"

rsync -a --delete "$REPO_DIR/scripts/" "$EMBED_DIR/scripts/"

JWT_FILE=/etc/systemd/system/levara-bench.service.d/jwt.conf
if [ ! -f "$JWT_FILE" ]; then
  SECRET=$(head -c 32 /dev/urandom | xxd -p -c 32)
  sudo mkdir -p "$(dirname "$JWT_FILE")"
  sudo tee "$JWT_FILE" >/dev/null <<EOF
[Service]
Environment=JWT_SECRET=$SECRET
EOF
fi

sudo install -m 0644 "$REPO_DIR/deploy/bench/embed-bench.service"  /etc/systemd/system/
sudo install -m 0644 "$REPO_DIR/deploy/bench/levara-bench.service" /etc/systemd/system/
sudo systemctl daemon-reload

echo "[setup_pi.sh] OK. embed-bench + levara-bench installed (not enabled)."
```

- [ ] **Step 4: Create README**

Create `deploy/bench/README.md`:

```markdown
# Bench stack on Pi (10.23.0.53)

Isolated from production levara.service:8090. Lifecycle controlled by
scripts/load-profiles/run_all_models.sh.

## One-time setup

1. Sync repo to Pi at /home/stek0v/levara-source
2. Place levara binary at /home/stek0v/levara-bench/levara (cross-compiled
   from cmd/server, GOOS=linux GOARCH=arm64)
3. Run setup: `ssh stek0v@10.23.0.53 'bash /home/stek0v/levara-source/deploy/bench/setup_pi.sh'`

## Per-model run

Drop-ins written by orchestrator:
- /etc/systemd/system/embed-bench.service.d/model.conf
    Environment=EMBED_BENCH_MODEL=<short>
- /etc/systemd/system/levara-bench.service.d/embed.conf
    Environment=EMBEDDING_MODEL=<openai-name>
- /etc/systemd/system/levara-bench.service.d/dim.conf
    ExecStart= override with -dim=<N>

After writing drop-ins: `systemctl daemon-reload && systemctl restart`.

## Cleanup

`systemctl stop levara-bench embed-bench` frees memory. Data persists in
/home/stek0v/levara-bench/data; remove manually for a fresh start.
```

- [ ] **Step 5: Mark executable, commit**

```bash
chmod +x deploy/bench/setup_pi.sh
git add deploy/bench/
git commit -m "feat(bench): systemd units + Pi setup script for bench stack"
```

---

## Task 5: preflight_model.py (TDD)

Gate before any seed or run. HTTP injected for tests.

**Files:**
- Create: `scripts/load-profiles/preflight_model.py`
- Create: `scripts/load-profiles/tests/__init__.py`
- Create: `scripts/load-profiles/tests/test_preflight.py`

- [ ] **Step 1: Create package marker**

```bash
mkdir -p scripts/load-profiles/tests
touch scripts/load-profiles/tests/__init__.py
```

- [ ] **Step 2: Write failing test**

Create `scripts/load-profiles/tests/test_preflight.py`:

```python
from scripts.load_profiles.preflight_model import Checks, run_checks


class FakeHTTP:
    def __init__(self, routes):
        self.routes = routes

    def __call__(self, method, url, headers=None, body=None, timeout=10.0):
        key = (method, url)
        if key not in self.routes:
            raise RuntimeError(f"unmocked HTTP {method} {url}")
        return self.routes[key]


GOOD = FakeHTTP({
    ("GET",  "http://10.23.0.53:9101/health"):   {"model": "potion-code-16M", "dim": 256, "backend": "model2vec", "ram_mb": 120},
    ("POST", "http://10.23.0.53:9101/v1/embeddings"): {"data": [{"embedding": [0.0] * 256, "index": 0}], "model": "potion-code-16M"},
    ("GET",  "http://10.23.0.53:8091/health"):   {"status": "ok"},
    ("GET",  "http://10.23.0.53:11434/api/tags"): {"models": [{"name": "qwen3:0.6b"}]},
    ("GET",  "http://10.23.0.53:9100/health"):    {"status": "ok"},
    ("POST", "http://10.23.0.53:8091/api/v1/auth/register"): {"access_token": "tok"},
})


def test_all_checks_pass_for_good_stack():
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=GOOD)
    assert result.ok is True
    assert result.failed == []


def test_fails_on_sidecar_dim_mismatch():
    bad = FakeHTTP({
        **GOOD.routes,
        ("GET", "http://10.23.0.53:9101/health"): {"model": "potion-code-16M", "dim": 999, "backend": "model2vec", "ram_mb": 120},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("sidecar_dim" in f for f in result.failed)


def test_fails_on_missing_llm():
    bad = FakeHTTP({
        **GOOD.routes,
        ("GET", "http://10.23.0.53:11434/api/tags"): {"models": [{"name": "other:1b"}]},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("ollama_model" in f for f in result.failed)


def test_fails_on_embed_vector_length_mismatch():
    bad = FakeHTTP({
        **GOOD.routes,
        ("POST", "http://10.23.0.53:9101/v1/embeddings"): {"data": [{"embedding": [0.0] * 7, "index": 0}], "model": "potion-code-16M"},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("embed_ping_dim" in f for f in result.failed)
```

- [ ] **Step 3: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/tests/test_preflight.py -v`

- [ ] **Step 4: Implement**

Create `scripts/load-profiles/preflight_model.py`:

```python
#!/usr/bin/env python3
"""Preflight gate for one model run on the Pi bench stack.

Usage: python preflight_model.py --model <short> --host 10.23.0.53
Exits 0 if every check passes, 1 otherwise. Prints structured JSON report.
"""
from __future__ import annotations

import argparse
import json
import sys
import urllib.request
from dataclasses import dataclass, field
from typing import Callable

from scripts.load_profiles.embed_bench.recipes import get_recipe


@dataclass
class Checks:
    model_short: str
    expected_dim: int
    expected_openai_name: str
    host: str = "10.23.0.53"
    sidecar_port: int = 9101
    bench_port: int = 8091
    rerank_port: int = 9100
    ollama_port: int = 11434
    ollama_model_required: str = "qwen3:0.6b"


@dataclass
class Result:
    ok: bool
    passed: list[str] = field(default_factory=list)
    failed: list[str] = field(default_factory=list)


def default_http(method: str, url: str, headers=None, body=None, timeout: float = 10.0) -> dict:
    payload = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=payload, headers=headers or {}, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = resp.read()
        if not data:
            return {}
        return json.loads(data)


HttpFn = Callable[..., dict]


def run_checks(c: Checks, *, http: HttpFn = default_http) -> Result:
    passed: list[str] = []
    failed: list[str] = []

    def _try(label: str, fn):
        try:
            fn()
            passed.append(label)
        except Exception as e:
            failed.append(f"{label}: {e}")

    sidecar = f"http://{c.host}:{c.sidecar_port}"
    bench = f"http://{c.host}:{c.bench_port}"
    rerank = f"http://{c.host}:{c.rerank_port}"
    ollama = f"http://{c.host}:{c.ollama_port}"

    def check_sidecar_health():
        body = http("GET", f"{sidecar}/health")
        if body.get("model") != c.expected_openai_name:
            raise RuntimeError(f"model name mismatch: {body.get('model')!r} != {c.expected_openai_name!r}")
        if body.get("dim") != c.expected_dim:
            raise RuntimeError(f"sidecar_dim mismatch: {body.get('dim')} != {c.expected_dim}")

    def check_embed_ping():
        body = http("POST", f"{sidecar}/v1/embeddings",
                    headers={"Content-Type": "application/json"},
                    body={"input": ["ping"], "model": c.expected_openai_name})
        data = body.get("data", [])
        if not data:
            raise RuntimeError("empty data array")
        v = data[0].get("embedding", [])
        if len(v) != c.expected_dim:
            raise RuntimeError(f"embed_ping_dim {len(v)} != {c.expected_dim}")

    def check_bench_health():
        http("GET", f"{bench}/health")

    def check_ollama_model():
        body = http("GET", f"{ollama}/api/tags")
        names = {m.get("name") for m in body.get("models", [])}
        if c.ollama_model_required not in names:
            raise RuntimeError(f"ollama_model {c.ollama_model_required!r} not in {names}")

    def check_rerank_health():
        http("GET", f"{rerank}/health")

    def check_auth():
        import secrets
        email = f"preflight_{secrets.token_hex(4)}@local"
        passwd = secrets.token_hex(8)
        body = http("POST", f"{bench}/api/v1/auth/register",
                    headers={"Content-Type": "application/json"},
                    body={"email": email, "password": passwd})
        if not body.get("access_token"):
            raise RuntimeError("no access_token returned")

    _try("sidecar_health", check_sidecar_health)
    _try("embed_ping",     check_embed_ping)
    _try("bench_health",   check_bench_health)
    _try("ollama_model",   check_ollama_model)
    _try("rerank_health",  check_rerank_health)
    _try("auth_register",  check_auth)

    return Result(ok=not failed, passed=passed, failed=failed)


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--model", required=True)
    p.add_argument("--host", default="10.23.0.53")
    args = p.parse_args()

    recipe = get_recipe(args.model)
    checks = Checks(
        model_short=recipe.short,
        expected_dim=recipe.dim,
        expected_openai_name=recipe.openai_name,
        host=args.host,
    )
    result = run_checks(checks)
    print(json.dumps({"ok": result.ok, "passed": result.passed, "failed": result.failed}, indent=2))
    return 0 if result.ok else 1


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 5: Run test, verify PASS**

Run: `python -m pytest scripts/load-profiles/tests/test_preflight.py -v`
Expected: 4 passed.

- [ ] **Step 6: Commit**

```bash
git add scripts/load-profiles/preflight_model.py \
        scripts/load-profiles/tests/__init__.py \
        scripts/load-profiles/tests/test_preflight.py
git commit -m "feat(load-profiles): preflight gate for per-model bench runs"
```

---

## Task 6: keyword_metrics helper in runner.py (TDD)

Pure function over the no-rerank response + expected keywords.

**Files:**
- Modify: `scripts/load-profiles/runner.py`
- Create: `scripts/load-profiles/tests/test_keyword_metrics.py`

- [ ] **Step 1: Write failing test**

Create `scripts/load-profiles/tests/test_keyword_metrics.py`:

```python
from scripts.load_profiles.runner import keyword_metrics


def _resp(*texts):
    return [{"id": str(i), "score": 1.0 - i*0.1, "metadata": {"text": t}}
            for i, t in enumerate(texts)]


def test_no_keywords_returns_zero():
    metrics = keyword_metrics(_resp("foo bar"), expected_keywords=[])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}


def test_top1_match_case_insensitive():
    metrics = keyword_metrics(_resp("Redis Cache invalidation logic"),
                              expected_keywords=["redis", "cache"])
    assert metrics["top1_keyword_hit"] is True
    assert metrics["keyword_hits_top5"] == 2


def test_only_top5_window_counted():
    docs = _resp("a", "b", "c", "d", "e", "redis-server", "cache-key")
    metrics = keyword_metrics(docs, expected_keywords=["redis", "cache"])
    assert metrics["keyword_hits_top5"] == 0
    assert metrics["top1_keyword_hit"] is False


def test_each_keyword_counted_at_most_once_per_query():
    docs = _resp("redis redis redis", "redis again", "x", "y", "z")
    metrics = keyword_metrics(docs, expected_keywords=["redis"])
    assert metrics["keyword_hits_top5"] == 1


def test_empty_response_safe():
    metrics = keyword_metrics(None, expected_keywords=["x"])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}
    metrics = keyword_metrics([], expected_keywords=["x"])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/tests/test_keyword_metrics.py -v`
Expected: ImportError.

- [ ] **Step 3: Add implementation**

Open `scripts/load-profiles/runner.py`. Find the function `top_changed` (around line 546). Add this new function immediately after it (before `build_query_record`):

```python
def keyword_metrics(resp: Any, *, expected_keywords: list[str]) -> dict[str, Any]:
    """Light-weight retrieval-quality metric.

    keyword_hits_top5: distinct expected_keywords (case-insensitive substring)
        appearing in any top-5 hit text. Each keyword counts at most once.

    top1_keyword_hit: any expected_keyword in top-1 text.
    """
    hits = _hits_from(resp)[:5]
    if not hits or not expected_keywords:
        return {"keyword_hits_top5": 0, "top1_keyword_hit": False}
    needles = [k.lower() for k in expected_keywords]

    def _text_of(h):
        m = h.get("metadata")
        if isinstance(m, dict):
            return str(m.get("text", "")).lower()
        if isinstance(m, str):
            try:
                inner = json.loads(m)
                if isinstance(inner, dict):
                    return str(inner.get("text", "")).lower()
            except Exception:
                return ""
        return ""

    top1_text = _text_of(hits[0])
    top1_hit = any(n in top1_text for n in needles)

    found: set[str] = set()
    for h in hits:
        text = _text_of(h)
        for n in needles:
            if n in text:
                found.add(n)

    return {"keyword_hits_top5": len(found), "top1_keyword_hit": top1_hit}
```

- [ ] **Step 4: Run test, verify PASS**

Run: `python -m pytest scripts/load-profiles/tests/test_keyword_metrics.py -v`
Expected: 5 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/load-profiles/runner.py \
        scripts/load-profiles/tests/test_keyword_metrics.py
git commit -m "feat(load-profiles): keyword_metrics helper"
```

---

## Task 7: Profile scripts — `--model`, `--embed-dim`, `--collection-override`

p4 and p5 get three new CLI flags and pass keyword metrics into `build_query_record` via the `extra` dict.

**Files:**
- Modify: `scripts/load-profiles/p4_memory_palace.py`
- Modify: `scripts/load-profiles/p5_filtered_search.py`

- [ ] **Step 1: Inspect existing argparse blocks**

Run: `grep -n "argparse\|add_argument\|build_query_record" scripts/load-profiles/p4_memory_palace.py scripts/load-profiles/p5_filtered_search.py`

- [ ] **Step 2: Add CLI flags to p4_memory_palace.py**

In the argparse block (around line 135), before `args = p.parse_args()`, add:

```python
    p.add_argument("--model", default="", help="embed model short (potion/granite/jina); used for JSONL fields")
    p.add_argument("--embed-dim", type=int, default=0, help="expected embedding dim (JSONL only)")
    p.add_argument("--collection-override", default="", help="if set, use this collection name instead of the default")
```

- [ ] **Step 3: Use the override and attach metrics in p4**

Find `collection = runner.profile_collection(PROFILE_ID)` (around line 82) and replace with:

```python
    collection = args.collection_override or runner.profile_collection(PROFILE_ID)
```

Find the `runner.build_query_record(` call (around line 104) and replace it with:

```python
                rec = runner.build_query_record(
                    profile_id=PROFILE_ID,
                    target=target,
                    query_id=q["id"],
                    query_text=q["text"],
                    collection=collection,
                    query_type=q.get("query_type", "CHUNKS"),
                    pair=pair,
                    extra={
                        "embed_model": args.model or target.embed_model,
                        "embed_dim": args.embed_dim,
                        "expected_keywords": q.get("expected_keywords", []),
                        **runner.keyword_metrics(
                            pair["no_rerank"]["response"],
                            expected_keywords=q.get("expected_keywords", []),
                        ),
                    },
                )
```

If `extra=` is already used, merge fields rather than replace.

- [ ] **Step 4: Mirror in p5_filtered_search.py**

Apply identical patches in `scripts/load-profiles/p5_filtered_search.py`: three new argparse calls, `collection` override line, `build_query_record(...)` with new `extra` dict.

- [ ] **Step 5: Verify help output**

```bash
python scripts/load-profiles/p4_memory_palace.py --help 2>&1 | grep -E "model|embed-dim|collection-override"
python scripts/load-profiles/p5_filtered_search.py --help 2>&1 | grep -E "model|embed-dim|collection-override"
```

Expected: each prints the three new flags.

- [ ] **Step 6: Verify keyword_metrics test still green**

Run: `python -m pytest scripts/load-profiles/tests/test_keyword_metrics.py -v`

- [ ] **Step 7: Commit**

```bash
git add scripts/load-profiles/p4_memory_palace.py scripts/load-profiles/p5_filtered_search.py
git commit -m "feat(load-profiles): per-model flags + keyword metrics in p4/p5"
```

---

## Task 8: seed_via_cognify in seed/code_corpus.py

Function that submits the corpus to `/api/v1/cognify` and polls for completion. Keeps existing direct-insert path for backward-compat.

**Files:**
- Modify: `scripts/load-profiles/seed/code_corpus.py`

- [ ] **Step 1: Append seed_via_cognify**

Open `scripts/load-profiles/seed/code_corpus.py`. At the bottom of the file, before any `if __name__ == "__main__":` block, add:

```python
def seed_via_cognify(
    target,
    collection: str,
    *,
    chunks: list[dict],
    poll_seconds: float = 2.0,
    timeout_seconds: float = 1800.0,
) -> dict:
    """Seed `collection` by submitting the joined corpus to /api/v1/cognify.

    Runs the FULL Levara pipeline (chunk -> LLM entity extraction -> dedup
    -> embed -> write HNSW+BM25+graph+WAL) so the bench is honest.
    """
    import time
    import sys
    from scripts.load_profiles import runner

    text = "\n\n=====\n\n".join(c["text"] for c in chunks)

    resp = runner.http_request(
        "POST",
        target.url("/api/v1/cognify"),
        headers=target.headers(),
        body={"dataset": collection, "dataset_name": collection, "text": text},
        timeout=60.0,
    )
    run_id = resp.get("run_id") or resp.get("id")
    if not run_id:
        raise RuntimeError(f"cognify did not return run_id: {resp!r}")

    print(f"[cognify] run_id={run_id} polling...", file=sys.stderr)
    start = time.time()
    while True:
        if time.time() - start > timeout_seconds:
            raise TimeoutError(f"cognify {run_id} did not terminate in {timeout_seconds}s")
        status = runner.http_request(
            "GET",
            target.url(f"/api/v1/cognify/status/{run_id}"),
            headers=target.headers(),
        )
        state = (status.get("status") or status.get("state") or "").lower()
        if state in ("done", "completed", "success"):
            print(f"[cognify] done in {time.time()-start:.1f}s", file=sys.stderr)
            return status
        if state in ("error", "failed"):
            raise RuntimeError(f"cognify failed: {status!r}")
        time.sleep(poll_seconds)
```

- [ ] **Step 2: Commit (smoke-tested in Task 11)**

```bash
git add scripts/load-profiles/seed/code_corpus.py
git commit -m "feat(load-profiles): cognify-pathway seeding"
```

---

## Task 9: seed_one.py orchestrator helper

Bridges the shell orchestrator and the Python seeding code without inline `python3 -c`.

**Files:**
- Create: `scripts/load-profiles/seed_one.py`

- [ ] **Step 1: Implement**

Create `scripts/load-profiles/seed_one.py`:

```python
#!/usr/bin/env python3
"""Seed ONE collection via the full cognify pipeline against bench Levara.

Called by run_all_models.sh. Reads target URL + token from args/env,
loads the code corpus, calls seed_via_cognify.

Usage:
    python seed_one.py --target-url http://10.23.0.53:8091 \
        --collection loadprofile_p4_main_potion
"""
from __future__ import annotations

import argparse
import sys

from scripts.load_profiles import runner
from scripts.load_profiles.seed import code_corpus


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--target-url", required=True)
    p.add_argument("--target-name", default="bench")
    p.add_argument("--collection", required=True)
    p.add_argument("--token-env", default="LEVARA_BENCH_TOKEN")
    args = p.parse_args()

    token = runner.load_token(args.token_env, args.target_name)
    target = runner.Target(
        name=args.target_name,
        base_url=args.target_url,
        token=token,
        embed_model="",
        rerank_endpoint="",
        rerank_enabled=None,
    )

    chunks = code_corpus.load_corpus()
    print(f"[seed_one] loaded {len(chunks)} chunks", file=sys.stderr)
    code_corpus.seed_via_cognify(target, args.collection, chunks=chunks)
    print(f"[seed_one] seeded collection={args.collection}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
```

Note: `runner.load_token` and `runner.Target` and `code_corpus.load_corpus` exist in the codebase already. If `load_token` does not exist, replace with the existing equivalent (e.g. `runner.load_token_from_env_or_file`); same for `load_corpus` (it may be `iter_corpus` or `build_corpus`). Grep first:

```bash
grep -n "^def load_corpus\|^def iter_corpus\|^def build_corpus" scripts/load-profiles/seed/code_corpus.py
grep -n "^def load_token\|^def load_token_from" scripts/load-profiles/runner.py
```

- [ ] **Step 2: Commit**

```bash
git add scripts/load-profiles/seed_one.py
git commit -m "feat(load-profiles): seed_one.py helper for orchestrator"
```

---

## Task 10: analyze.py — `--by-model` + cross-model table (TDD)

**Files:**
- Modify: `scripts/load-profiles/analyze.py`
- Create: `scripts/load-profiles/tests/test_analyze_by_model.py`

- [ ] **Step 1: Write failing test**

Create `scripts/load-profiles/tests/test_analyze_by_model.py`:

```python
from scripts.load_profiles.analyze import (
    group_by_model,
    summarize_quality,
    render_cross_model_markdown,
)


def _rec(model, gap, recall, top1):
    return {
        "embed_model": model,
        "score_gap_top_bottom": gap,
        "keyword_hits_top5": recall,
        "top1_keyword_hit": top1,
        "latency_no_rerank_ms": 10.0,
        "latency_with_rerank_ms": 50.0,
        "rerank_outcome": "reranked",
    }


def test_group_by_model_splits_correctly():
    recs = [_rec("potion", 0.1, 2, True), _rec("granite", 0.2, 3, True), _rec("potion", 0.15, 1, False)]
    g = group_by_model(recs)
    assert set(g) == {"potion", "granite"}
    assert len(g["potion"]) == 2
    assert len(g["granite"]) == 1


def test_summarize_quality_means():
    recs = [_rec("p", 0.1, 2, True), _rec("p", 0.2, 4, False), _rec("p", 0.0, 0, False)]
    s = summarize_quality(recs)
    assert abs(s["mean_recall_top5"] - 2.0) < 1e-9
    assert abs(s["top1_keyword_hit_rate"] - (1/3)) < 1e-9
    assert s["n"] == 3


def test_cross_model_markdown_has_all_models():
    recs = [_rec("potion", 0.1, 2, True), _rec("granite", 0.2, 3, True), _rec("jina", 0.05, 1, False)]
    md = render_cross_model_markdown(group_by_model(recs))
    assert "potion" in md and "granite" in md and "jina" in md
    assert "mean_recall_top5" in md
    assert "top1_keyword_hit_rate" in md
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `python -m pytest scripts/load-profiles/tests/test_analyze_by_model.py -v`

- [ ] **Step 3: Implement**

Open `scripts/load-profiles/analyze.py`. After existing `pct` helper, before `load`, add:

```python
def group_by_model(recs: list[dict]) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {}
    for r in recs:
        m = r.get("embed_model") or "_unknown"
        out.setdefault(m, []).append(r)
    return out


def summarize_quality(recs: list[dict]) -> dict:
    if not recs:
        return {"n": 0, "mean_recall_top5": 0.0, "top1_keyword_hit_rate": 0.0}
    hits = [int(r.get("keyword_hits_top5", 0)) for r in recs]
    top1 = [bool(r.get("top1_keyword_hit", False)) for r in recs]
    return {
        "n": len(recs),
        "mean_recall_top5": sum(hits) / len(hits),
        "top1_keyword_hit_rate": sum(1 for x in top1 if x) / len(top1),
    }


def render_cross_model_markdown(by_model: dict[str, list[dict]]) -> str:
    lines = ["## Cross-model comparison", ""]
    lines.append("| model | n | mean_recall_top5 | top1_keyword_hit_rate | p50 gap | p50 lat_no_rerank_ms | p50 lat_with_rerank_ms |")
    lines.append("|---|---|---|---|---|---|---|")
    for model in sorted(by_model):
        recs = by_model[model]
        q = summarize_quality(recs)
        gaps = [float(r.get("score_gap_top_bottom", 0.0)) for r in recs]
        lat_no = [float(r.get("latency_no_rerank_ms", 0.0)) for r in recs]
        lat_w = [float(r.get("latency_with_rerank_ms", 0.0)) for r in recs]
        lines.append(
            f"| {model} | {q['n']} | {q['mean_recall_top5']:.3f} | {q['top1_keyword_hit_rate']:.3f} "
            f"| {pct(gaps, 50):.4f} | {pct(lat_no, 50):.1f} | {pct(lat_w, 50):.1f} |"
        )
    return "\n".join(lines)
```

In the argparse block, add (before `parse_args()`):

```python
    p.add_argument("--by-model", action="store_true", help="group records by embed_model and emit per-model + cross-model report")
```

In `main()`, before final return, add:

```python
    if args.by_model:
        all_recs: list[dict] = []
        for path in args.files:
            all_recs.extend(load(path))
        by_model = group_by_model(all_recs)
        for model, recs in sorted(by_model.items()):
            print(f"\n=== model: {model} (n={len(recs)}) ===")
            profiles = per_profile(recs)
            for label, prof in profiles.items():
                print_profile(f"{model}/{label}", prof)
            threshold_sweep(profiles)
        print()
        print(render_cross_model_markdown(by_model))
```

- [ ] **Step 4: Run test, verify PASS**

Run: `python -m pytest scripts/load-profiles/tests/test_analyze_by_model.py -v`
Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/load-profiles/analyze.py \
        scripts/load-profiles/tests/test_analyze_by_model.py
git commit -m "feat(load-profiles): --by-model grouping + cross-model table"
```

---

## Task 11: run_all_models.sh — orchestrator

Wraps the per-model sequence. Idempotent.

**Files:**
- Create: `scripts/load-profiles/run_all_models.sh`

- [ ] **Step 1: Create**

Create `scripts/load-profiles/run_all_models.sh`:

```bash
#!/usr/bin/env bash
# Sequentially run 3 embed models through the full Levara cognify
# pipeline on Pi bench. Refuses to touch production levara.service.
#
# Usage:
#   bash run_all_models.sh              # all 3 models
#   bash run_all_models.sh potion       # just one
set -euo pipefail

PI_HOST="${PI_HOST:-10.23.0.53}"
PI_USER="${PI_USER:-stek0v}"
OUT_DIR="${OUT_DIR:-scripts/load-profiles/out}"
DEFAULT_MODELS=(potion granite jina)
MODELS_ARR=(${MODELS:-${DEFAULT_MODELS[@]}})
if [ $# -gt 0 ]; then
  MODELS_ARR=("$@")
fi

mkdir -p "$OUT_DIR"

safe_unit() {
  case "$1" in
    levara.service) echo "REFUSING to touch prod unit $1" >&2; exit 2 ;;
  esac
}

write_dropin() {
  local unit="$1" name="$2" content="$3"
  safe_unit "$unit"
  ssh "$PI_USER@$PI_HOST" "sudo mkdir -p /etc/systemd/system/${unit}.d && \
    printf '%s\n' '$content' | sudo tee /etc/systemd/system/${unit}.d/${name}.conf >/dev/null"
}

restart_bench() {
  ssh "$PI_USER@$PI_HOST" "sudo systemctl daemon-reload && \
    sudo systemctl restart embed-bench.service levara-bench.service"
}

stop_bench() {
  ssh "$PI_USER@$PI_HOST" "sudo systemctl stop levara-bench.service embed-bench.service || true"
}

run_one_model() {
  local short="$1"
  echo "=== model: $short ==="

  case "$short" in
    potion)  OPENAI="potion-code-16M";              DIM=256 ;;
    granite) OPENAI="granite-97m-multilingual-r2";  DIM=384 ;;
    jina)    OPENAI="jina-omni-nano";               DIM=512 ;;
    *) echo "unknown model: $short" >&2; exit 2 ;;
  esac

  stop_bench

  write_dropin embed-bench.service model "[Service]
Environment=EMBED_BENCH_MODEL=$short"
  write_dropin levara-bench.service embed "[Service]
Environment=EMBEDDING_MODEL=$OPENAI"
  write_dropin levara-bench.service dim "[Service]
ExecStart=
ExecStart=/home/stek0v/levara-bench/levara -standalone=true -port=8091 -grpc-port=0 -dim=$DIM -data-dir=/home/stek0v/levara-bench/data -node-id=pi-bench"

  restart_bench
  sleep 10

  python3 scripts/load-profiles/preflight_model.py --model "$short" --host "$PI_HOST" \
    || { echo "preflight failed for $short, skipping" >&2; stop_bench; return 1; }

  local TARGET_URL="http://$PI_HOST:8091"
  local COLL_P4="loadprofile_p4_main_$short"
  local COLL_P5="loadprofile_p5_main_$short"

  echo "[seed] $COLL_P4 + $COLL_P5"
  python3 scripts/load-profiles/seed_one.py --target-url "$TARGET_URL" --collection "$COLL_P4"
  python3 scripts/load-profiles/seed_one.py --target-url "$TARGET_URL" --collection "$COLL_P5"

  echo "[run] p4 / $short"
  python3 scripts/load-profiles/p4_memory_palace.py \
    --target-name bench --target-url "$TARGET_URL" \
    --model "$short" --embed-dim "$DIM" \
    --collection-override "$COLL_P4" \
    --out "$OUT_DIR/p4_$short.jsonl"

  echo "[run] p5 / $short"
  python3 scripts/load-profiles/p5_filtered_search.py \
    --target-name bench --target-url "$TARGET_URL" \
    --model "$short" --embed-dim "$DIM" \
    --collection-override "$COLL_P5" \
    --out "$OUT_DIR/p5_$short.jsonl"

  stop_bench
}

for m in "${MODELS_ARR[@]}"; do
  run_one_model "$m" || echo "model $m skipped" >&2
done

echo "=== analyze ==="
python3 scripts/load-profiles/analyze.py --by-model "$OUT_DIR"/p?_*.jsonl \
  > docs/load-profile-analysis-pi-multimodel.md

echo "OK. Output: docs/load-profile-analysis-pi-multimodel.md"
```

- [ ] **Step 2: Check syntax**

```bash
chmod +x scripts/load-profiles/run_all_models.sh
bash -n scripts/load-profiles/run_all_models.sh && echo "syntax OK"
```

- [ ] **Step 3: Commit**

```bash
git add scripts/load-profiles/run_all_models.sh
git commit -m "feat(load-profiles): orchestrator for 3-model sequential bench"
```

---

## Task 12: End-to-end smoke with potion (smallest model first)

Manual validation. Prove the full chain works before the overnight run.

- [ ] **Step 1: Cross-compile Levara binary**

```bash
cd Levara && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/levara-pi ./cmd/server
file /tmp/levara-pi
```

Expected: ELF 64-bit LSB ARM aarch64.

- [ ] **Step 2: Deploy binary + source + run setup**

```bash
ssh stek0v@10.23.0.53 'mkdir -p /home/stek0v/levara-bench/data /home/stek0v/levara-source'
rsync -a --delete --exclude='.git' --exclude='Levara/data' --exclude='out' \
  ./ stek0v@10.23.0.53:/home/stek0v/levara-source/
scp /tmp/levara-pi stek0v@10.23.0.53:/home/stek0v/levara-bench/levara
ssh stek0v@10.23.0.53 'chmod +x /home/stek0v/levara-bench/levara && \
  bash /home/stek0v/levara-source/deploy/bench/setup_pi.sh'
```

Expected: `[setup_pi.sh] OK.`

- [ ] **Step 3: Prefetch potion model**

```bash
ssh stek0v@10.23.0.53 '/home/stek0v/embed-bench/venv/bin/huggingface-cli download minishlab/potion-code-16M'
```

- [ ] **Step 4: Run orchestrator for ONLY potion**

```bash
bash scripts/load-profiles/run_all_models.sh potion
```

Expected: completes under 30 min; produces `scripts/load-profiles/out/p{4,5}_potion.jsonl` and `docs/load-profile-analysis-pi-multimodel.md` containing a `potion` row in the cross-model table.

- [ ] **Step 5: Validate JSONL fields**

```bash
head -1 scripts/load-profiles/out/p4_potion.jsonl > /tmp/probe.json
python3 -m json.tool < /tmp/probe.json | grep -E "embed_model|embed_dim|keyword_hits_top5|top1_keyword_hit"
```

Expected: all four fields present with sensible values.

- [ ] **Step 6: Verify production untouched**

```bash
ssh stek0v@10.23.0.53 'systemctl is-active levara.service'
ssh stek0v@10.23.0.53 'curl -s http://localhost:8090/health'
```

Expected: `active` and valid health response.

- [ ] **Step 7: If dims/recipe were wrong, fix and re-run**

If `make_backend` raised a `ValueError`, the true dim was logged. Update `scripts/load-profiles/embed_bench/recipes.py`, re-run Task 1 tests, redeploy source, re-run smoke. Commit fix as a separate commit.

---

## Task 13: Full overnight 3-model run + writeup

- [ ] **Step 1: Prefetch remaining models**

```bash
ssh stek0v@10.23.0.53 '/home/stek0v/embed-bench/venv/bin/huggingface-cli download ibm-granite/granite-embedding-97m-multilingual-r2'
ssh stek0v@10.23.0.53 '/home/stek0v/embed-bench/venv/bin/huggingface-cli download jinaai/jina-embeddings-v5-omni-nano'
```

- [ ] **Step 2: Kick off full sweep in tmux on Pi**

```bash
ssh stek0v@10.23.0.53 'cd /home/stek0v/levara-source && \
  tmux new-session -d -s bench "bash scripts/load-profiles/run_all_models.sh 2>&1 | tee /tmp/bench.log"'
```

Check progress: `ssh stek0v@10.23.0.53 'tmux capture-pane -p -t bench | tail -30'`

- [ ] **Step 3: Wait + pull results (~10-11h)**

```bash
scp -r stek0v@10.23.0.53:/home/stek0v/levara-source/scripts/load-profiles/out/ scripts/load-profiles/out/
scp stek0v@10.23.0.53:/home/stek0v/levara-source/docs/load-profile-analysis-pi-multimodel.md docs/
```

- [ ] **Step 4: Hand-review the report**

Open `docs/load-profile-analysis-pi-multimodel.md`. Confirm three model rows, sane recall@5 values, p50 latency within an order of magnitude of nomic baseline.

- [ ] **Step 5: Commit results + open PR**

```bash
git add scripts/load-profiles/out/p?_potion.jsonl \
        scripts/load-profiles/out/p?_granite.jsonl \
        scripts/load-profiles/out/p?_jina.jsonl \
        docs/load-profile-analysis-pi-multimodel.md
git commit -m "data: 3-embed-model Pi calibration results"

git push -u origin feat/load-profiles
gh pr create --base main \
  --title "feat(bench): 3-embed-model honest calibration on Pi" \
  --body "Spec: docs/superpowers/specs/2026-05-26-pi-embed-model-calibration-design.md
Plan: docs/superpowers/plans/2026-05-26-pi-embed-model-calibration-plan.md
Report: docs/load-profile-analysis-pi-multimodel.md"
```

---

## Notes for the executor

- **Order matters for Tasks 1-3.** recipes drives backends drives server.
- **Tasks 4 + 11 (infra) have no unit tests** — validated end-to-end by Task 12.
- **Task 12 is a gate.** Do not start Task 13 until Task 12 produces a complete potion run.
- **Production safety.** `safe_unit()` in `run_all_models.sh` refuses `levara.service`. Do NOT add unguarded `systemctl restart levara` anywhere.
- **HF Hub access.** Granite/Jina may require accepting a license. Prefetch will fail visibly — accept license and retry.
- **Model dims.** Hardcoded dims in `recipes.py` are best-effort. Dim probe in `backends.py` raises if reality disagrees.
- **gitignore.** `scripts/load-profiles/out/` is already gitignored. The Task 13 `git add` deliberately overrides per-file.

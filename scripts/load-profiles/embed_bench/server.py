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

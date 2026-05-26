"""Backend tests. Skipped when the HF cache lacks the model files."""
import os
from pathlib import Path

import pytest

from embed_bench.backends import make_backend
from embed_bench.recipes import get_recipe


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

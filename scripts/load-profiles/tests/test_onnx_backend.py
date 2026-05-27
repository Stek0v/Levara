"""Unit tests for ONNXBackend last-token pool + L2-norm + dim guard.

We don't load the real jina ONNX model — we mock optimum.onnxruntime and
transformers.AutoTokenizer so the test runs in milliseconds on any host.
"""
from __future__ import annotations

import sys
import types
from unittest.mock import MagicMock, patch

import pytest
import torch

# Inject stub modules so patch() can resolve 'optimum.onnxruntime.*' even
# when the real optimum package is not installed.
_optimum_stub = types.ModuleType("optimum")
_optimum_ort_stub = types.ModuleType("optimum.onnxruntime")
_optimum_ort_stub.ORTModelForFeatureExtraction = MagicMock()
_optimum_stub.onnxruntime = _optimum_ort_stub
sys.modules.setdefault("optimum", _optimum_stub)
sys.modules.setdefault("optimum.onnxruntime", _optimum_ort_stub)

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

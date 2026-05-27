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

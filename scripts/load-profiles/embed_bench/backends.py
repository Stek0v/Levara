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
        self.model.train(False)
        # Apple Silicon: MPS is several times faster than CPU for these
        # encoder sizes; harmless no-op elsewhere.
        try:
            import torch as _t
            if _t.backends.mps.is_available():
                self.model = self.model.to("mps")
        except Exception:
            pass
        # Some mirror repos (e.g. unsloth/embeddinggemma-300m) ship
        # add_bos_token=False; Gemma-style encoders silently produce
        # garbage embeddings without BOS. Force it on.
        if getattr(self.tokenizer, "add_bos_token", None) is False:
            self.tokenizer.add_bos_token = True
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

    def embed(self, texts: list[str], kind: str = "document") -> list[list[float]]:
        if kind == "query":
            texts = ["task: search result | query: " + t for t in texts]
        else:
            texts = ["title: none | text: " + t for t in texts]
        return self._embed_raw(texts)

    def _mean_pool(self, last_hidden_state, attention_mask):
        mask = attention_mask.unsqueeze(-1).float()
        summed = (last_hidden_state * mask).sum(dim=1)
        counts = mask.sum(dim=1).clamp(min=1e-9)
        return summed / counts

    def _embed_raw(self, texts: list[str]) -> list[list[float]]:
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
            file_name=recipe.onnx_file_name,
            provider="CPUExecutionProvider",
            trust_remote_code=recipe.trust_remote_code,
        )
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
        inputs = self.tokenizer(
            texts, padding=True, truncation=True, max_length=512, return_tensors="pt",
        )
        out = self.model(**inputs)
        pooled = self._last_token_pool(out.last_hidden_state, inputs["attention_mask"])
        normed = self._torch.nn.functional.normalize(pooled, p=2, dim=1)
        return normed.cpu().tolist()


def make_backend(recipe: Recipe) -> Backend:
    if recipe.backend == "transformers":
        return TransformersBackend(recipe)
    if recipe.backend == "model2vec":
        return Model2VecBackend(recipe)
    if recipe.backend == "onnx":
        return ONNXBackend(recipe)
    raise ValueError(f"unknown backend {recipe.backend!r}")

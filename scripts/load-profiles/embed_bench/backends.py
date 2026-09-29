"""Embedding backends.

TransformersBackend: HuggingFace AutoModel + AutoTokenizer; mean-pools the
last hidden state with attention mask, then L2-normalizes.

Model2VecBackend: minishlab/model2vec StaticModel (single matmul, sub-ms).
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Protocol

import numpy as np

from .recipes import Recipe


class Backend(Protocol):
    dim: int

    def embed(self, texts: list[str], kind: str = "document") -> list[list[float]]: ...


class TransformersBackend:
    def __init__(self, recipe: Recipe):
        from transformers import AutoModel, AutoTokenizer
        import torch

        self._torch = torch
        self._max_length = 512
        self._projection_weights = ()
        load_options = {"trust_remote_code": recipe.trust_remote_code}
        if recipe.short == "gemma-full":
            load_options.update(revision=recipe.revision, local_files_only=True)
        self.tokenizer = AutoTokenizer.from_pretrained(
            recipe.repo, **load_options
        )
        self.model = AutoModel.from_pretrained(
            recipe.repo, **load_options
        )
        self.model.train(False)
        # Some mirror repos (e.g. unsloth/embeddinggemma-300m) ship
        # add_bos_token=False; Gemma-style encoders silently produce
        # garbage embeddings without BOS. Force it on.
        if recipe.short == "gemma-full":
            self._load_gemma_projection(recipe)
        elif getattr(self.tokenizer, "add_bos_token", None) is False:
            self.tokenizer.add_bos_token = True
        with torch.no_grad():
            inputs = self.tokenizer(["dim probe"], padding=True, truncation=True, return_tensors="pt")
            out = self.model(**inputs)
            pooled = self._project(self._mean_pool(out.last_hidden_state, inputs["attention_mask"]))
            self.dim = pooled.shape[-1]
        if self.dim != recipe.dim:
            raise ValueError(
                f"recipe dim mismatch: {recipe.repo} produced {self.dim}-d, "
                f"recipe said {recipe.dim}"
            )
        # Apple Silicon: MPS is several times faster than CPU for these
        # encoder sizes; harmless no-op elsewhere.
        try:
            import torch as _t
            if _t.backends.mps.is_available():
                self.model = self.model.to("mps")
        except Exception:
            pass
        parameter = next(self.model.parameters())
        self._projection_weights = tuple(w.to(parameter) for w in self._projection_weights)

    def _load_gemma_projection(self, recipe: Recipe) -> None:
        from huggingface_hub import hf_hub_download
        from safetensors.torch import load_file

        def cached(name: str) -> str:
            return hf_hub_download(
                recipe.repo, name, revision=recipe.revision, local_files_only=True
            )

        config = json.loads(Path(cached("tokenizer_config.json")).read_text())
        if config.get("add_bos_token") is not True or config.get("add_eos_token") is not True:
            raise ValueError("gemma-full requires the cached BOS/EOS tokenizer contract")
        # Transformers 5.17 flags can be false while the cached post-processor adds both IDs.
        raw_ids = self.tokenizer("tokenizer probe", add_special_tokens=False)["input_ids"]
        actual_ids = self.tokenizer("tokenizer probe", add_special_tokens=True)["input_ids"]
        if actual_ids != [self.tokenizer.bos_token_id, *raw_ids, self.tokenizer.eos_token_id]:
            raise ValueError("gemma-full tokenizer must emit exactly BOS + text + EOS")
        config = json.loads(Path(cached("sentence_bert_config.json")).read_text())
        if config.get("max_seq_length") != 2048:
            raise ValueError("gemma-full requires max_seq_length=2048")
        self._max_length = 2048
        pooling = json.loads(Path(cached("1_Pooling/config.json")).read_text())
        if (pooling.get("word_embedding_dimension") != 768
                or pooling.get("pooling_mode_mean_tokens") is not True
                or pooling.get("include_prompt") is not True
                or any(value for key, value in pooling.items()
                       if key.startswith("pooling_mode_") and key != "pooling_mode_mean_tokens")):
            raise ValueError("gemma-full requires mean pooling including the prompt")
        weights = []
        # ponytail: this is the pinned Gemma head, not a generic module loader.
        for folder, input_dim, output_dim in (("2_Dense", 768, 3072), ("3_Dense", 3072, 768)):
            config = json.loads(Path(cached(f"{folder}/config.json")).read_text())
            expected = {"in_features": input_dim, "out_features": output_dim,
                        "bias": False, "activation_function": "torch.nn.modules.linear.Identity"}
            if any(config.get(key) != value for key, value in expected.items()):
                raise ValueError(f"unsupported gemma-full projection config: {folder}")
            state = load_file(cached(f"{folder}/model.safetensors"))
            if set(state) != {"linear.weight"} or tuple(state["linear.weight"].shape) != (output_dim, input_dim):
                raise ValueError(f"invalid gemma-full projection weights: {folder}")
            weights.append(state["linear.weight"])
        self._projection_weights = tuple(weights)

    def _project(self, pooled):
        for weight in self._projection_weights:
            pooled = self._torch.nn.functional.linear(pooled, weight)
        return pooled

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
                texts, padding=True, truncation=True, max_length=self._max_length, return_tensors="pt"
            )
            device = next(self.model.parameters()).device
            inputs = {k: v.to(device) for k, v in inputs.items()}
            out = self.model(**inputs)
            pooled = self._project(self._mean_pool(out.last_hidden_state, inputs["attention_mask"]))
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

    def embed(self, texts: list[str], kind: str = "document") -> list[list[float]]:
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

    def embed(self, texts: list[str], kind: str = "document") -> list[list[float]]:
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

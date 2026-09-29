"""CPU-only encoder contract tests; no downloads or real backbone inference."""
import json
import sys
from types import SimpleNamespace

import pytest
import torch

from embed_bench.backends import TransformersBackend
from embed_bench.recipes import get_recipe


@pytest.fixture
def encoder_files(tmp_path, monkeypatch):
    configs = {
        "tokenizer_config.json": {"add_bos_token": True, "add_eos_token": True},
        "sentence_bert_config.json": {"max_seq_length": 2048},
        "1_Pooling/config.json": {"word_embedding_dimension": 768,
                                  "pooling_mode_mean_tokens": True, "include_prompt": True},
        "2_Dense/config.json": {"in_features": 768, "out_features": 3072, "bias": False,
                                "activation_function": "torch.nn.modules.linear.Identity"},
        "3_Dense/config.json": {"in_features": 3072, "out_features": 768, "bias": False,
                                "activation_function": "torch.nn.modules.linear.Identity"},
    }
    weights = {"2_Dense": torch.zeros(3072, 768), "3_Dense": torch.zeros(768, 3072)}
    weights["2_Dense"][:2, :2] = torch.tensor([[1., 1.], [1., -1.]])
    weights["3_Dense"][:2, :2] = torch.tensor([[2., 0.], [1., 1.]])
    loads, tokenizations = [], []

    def cached(repo, name, **kwargs):
        assert repo == get_recipe("gemma-full").repo
        assert kwargs == {"revision": get_recipe("gemma-full").revision, "local_files_only": True}
        path = tmp_path / name
        if name in configs:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(configs[name]))
        return str(path)

    class Tokenizer:
        # Transformers 5.17 can expose false flags while TemplateProcessing adds both IDs.
        add_bos_token = False
        add_eos_token = False
        bos_token_id = 2
        eos_token_id = 1
        emit_eos = True

        def __call__(self, texts, **kwargs):
            if isinstance(texts, str):
                ids = [99]
                if kwargs.get("add_special_tokens", True):
                    ids = [2] + ids + ([1] if self.emit_eos else [])
                return {"input_ids": ids}
            tokenizations.append((texts, kwargs))
            return {"input_ids": torch.ones(len(texts), 2, dtype=torch.long),
                    "attention_mask": torch.tensor([[1, 0]] * len(texts))}

        @classmethod
        def from_pretrained(cls, repo, **kwargs):
            loads.append((repo, kwargs))
            return cls()

    class Backbone(torch.nn.Module):
        def __init__(self, dim):
            super().__init__()
            self.parameter = torch.nn.Parameter(torch.zeros(1))
            self.dim = dim

        def forward(self, input_ids, attention_mask):
            hidden = torch.zeros(len(input_ids), 2, self.dim)
            hidden[:, 0, :2] = torch.tensor([1., 2.])
            hidden[:, 1, :2] = torch.tensor([500., 500.])  # padding must not affect mean
            return SimpleNamespace(last_hidden_state=hidden)

        @classmethod
        def from_pretrained(cls, repo, **kwargs):
            loads.append((repo, kwargs))
            return cls(384 if "granite" in repo else 768)

    monkeypatch.setitem(sys.modules, "transformers", SimpleNamespace(
        AutoModel=Backbone, AutoTokenizer=Tokenizer))
    monkeypatch.setitem(sys.modules, "huggingface_hub", SimpleNamespace(hf_hub_download=cached))
    monkeypatch.setitem(sys.modules, "safetensors.torch", SimpleNamespace(
        load_file=lambda path: {"linear.weight": weights[path.split("/")[-2]]}))
    monkeypatch.setattr(torch.backends.mps, "is_available", lambda: False)
    return SimpleNamespace(configs=configs, weights=weights, loads=loads, tokenizations=tokenizations,
                           tokenizer=Tokenizer)


def test_full_gemma_projects_before_normalizing_and_preserves_prompts(encoder_files):
    backend = TransformersBackend(get_recipe("gemma-full"))
    actual = torch.tensor(backend.embed(["Что хранит журнал?"], kind="query"))
    expected = torch.zeros(1, 768)
    expected[0, :2] = torch.tensor([6., 2.])
    expected = torch.nn.functional.normalize(expected, dim=1)
    torch.testing.assert_close(actual, expected)
    assert actual[0, 0] > actual[0, 1]  # pooling-only gives the opposite order
    torch.testing.assert_close(actual.norm(dim=1), torch.ones(1))
    texts, options = encoder_files.tokenizations[-1]
    assert texts == ["task: search result | query: Что хранит журнал?"]
    assert options["max_length"] == 2048
    assert options["truncation"] is True
    backend.embed(["Журнал хранит шаги."], kind="document")
    assert encoder_files.tokenizations[-1][0] == ["title: none | text: Журнал хранит шаги."]
    assert all(options["local_files_only"] for _, options in encoder_files.loads)
    assert all(options["revision"] == get_recipe("gemma-full").revision
               for _, options in encoder_files.loads)


@pytest.mark.parametrize("invalid", ["bias", "activation", "shape", "pooling", "length", "eos", "token_ids"])
def test_full_gemma_rejects_incompatible_cached_contract(encoder_files, invalid):
    if invalid == "bias":
        encoder_files.configs["2_Dense/config.json"]["bias"] = True
    elif invalid == "activation":
        encoder_files.configs["3_Dense/config.json"]["activation_function"] = "torch.nn.ReLU"
    elif invalid == "shape":
        encoder_files.weights["3_Dense"] = torch.zeros(767, 3072)
    elif invalid == "pooling":
        encoder_files.configs["1_Pooling/config.json"]["include_prompt"] = False
    elif invalid == "length":
        encoder_files.configs["sentence_bert_config.json"]["max_seq_length"] = 512
    elif invalid == "eos":
        encoder_files.configs["tokenizer_config.json"]["add_eos_token"] = False
    else:
        encoder_files.tokenizer.emit_eos = False
    with pytest.raises(ValueError, match="gemma-full"):
        TransformersBackend(get_recipe("gemma-full"))


def test_legacy_gemma_keeps_pooling_only_and_512_limit(encoder_files):
    backend = TransformersBackend(get_recipe("gemma"))
    actual = torch.tensor(backend.embed(["original"], kind="query"))
    assert actual[0, 1] > actual[0, 0]
    assert backend._projection_weights == ()
    assert encoder_files.tokenizations[-1][0] == ["task: search result | query: original"]
    assert encoder_files.tokenizations[-1][1]["max_length"] == 512
    assert all("revision" not in options for _, options in encoder_files.loads)

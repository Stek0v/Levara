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
    backend: str  # "transformers" | "model2vec" | "onnx"
    dim: int
    openai_name: str
    trust_remote_code: bool = False
    onnx_file_name: str = "model.onnx"


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
    "nomic": Recipe(
        short="nomic",
        repo="nomic-ai/nomic-embed-text-v2-moe",
        backend="transformers",
        dim=768,
        openai_name="nomic-embed-text-v2-moe",
        trust_remote_code=True,
    ),
    "jina": Recipe(
        short="jina",
        repo="jinaai/jina-embeddings-v5-text-nano-retrieval",
        backend="onnx",
        dim=768,
        openai_name="jina-v5-text-nano-retrieval",
        trust_remote_code=True,
        onnx_file_name="model_quantized.onnx",
    ),
}


def get_recipe(short: str) -> Recipe:
    if short not in RECIPES:
        raise KeyError(f"unknown model short name: {short!r}; known: {sorted(RECIPES)}")
    return RECIPES[short]

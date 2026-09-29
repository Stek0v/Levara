import pytest

from embed_bench.recipes import RECIPES, get_recipe


def test_recipes_present():
    assert set(RECIPES.keys()) == {"potion", "gemma", "gemma-full", "granite", "nomic", "jina"}


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
    assert r.repo == "jinaai/jina-embeddings-v2-small-en"
    assert r.backend == "transformers"
    assert r.dim == 512
    assert r.openai_name == "jina-v2-small-en"
    assert r.trust_remote_code is True


def test_full_gemma_is_pinned_and_has_a_distinct_identity():
    legacy, full = get_recipe("gemma"), get_recipe("gemma-full")
    assert legacy.openai_name == "embeddinggemma-300m"
    assert legacy.revision is None
    assert full.openai_name == "embeddinggemma-300m-full-v1"
    assert full.revision == "bfa3c846ac738e62aa61806ef9112d34acb1dc5a"
    assert full.repo == legacy.repo
    assert full.dim == legacy.dim == 768


def test_unknown_recipe_raises():
    with pytest.raises(KeyError):
        get_recipe("nope")

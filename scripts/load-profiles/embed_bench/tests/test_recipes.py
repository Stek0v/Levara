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

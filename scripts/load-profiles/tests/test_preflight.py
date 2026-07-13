from preflight_model import Checks, run_checks


class FakeHTTP:
    def __init__(self, routes):
        self.routes = routes

    def __call__(self, method, url, headers=None, body=None, timeout=10.0):
        key = (method, url)
        if key not in self.routes:
            raise RuntimeError(f"unmocked HTTP {method} {url}")
        return self.routes[key]


GOOD = FakeHTTP({
    ("GET",  "http://10.23.0.53:9201/health"):   {"model": "potion-code-16M", "dim": 256, "backend": "model2vec", "ram_mb": 120},
    ("POST", "http://10.23.0.53:9201/v1/embeddings"): {"data": [{"embedding": [0.0] * 256, "index": 0}], "model": "potion-code-16M"},
    ("GET",  "http://10.23.0.53:8091/health"):   {"status": "ok"},
    ("GET",  "http://10.23.0.53:11434/api/tags"): {"models": [{"name": "qwen3:0.6b"}]},
    ("GET",  "http://10.23.0.53:9100/health"):    {"status": "ok"},
    ("POST", "http://10.23.0.53:8091/api/v1/auth/register"): {"access_token": "tok"},
})


def test_all_checks_pass_for_good_stack():
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=GOOD)
    assert result.ok is True
    assert result.failed == []


def test_fails_on_sidecar_dim_mismatch():
    bad = FakeHTTP({
        **GOOD.routes,
        ("GET", "http://10.23.0.53:9201/health"): {"model": "potion-code-16M", "dim": 999, "backend": "model2vec", "ram_mb": 120},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("sidecar_dim" in f for f in result.failed)


def test_fails_on_missing_llm():
    bad = FakeHTTP({
        **GOOD.routes,
        ("GET", "http://10.23.0.53:11434/api/tags"): {"models": [{"name": "other:1b"}]},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("ollama_model" in f for f in result.failed)


def test_fails_on_embed_vector_length_mismatch():
    bad = FakeHTTP({
        **GOOD.routes,
        ("POST", "http://10.23.0.53:9201/v1/embeddings"): {"data": [{"embedding": [0.0] * 7, "index": 0}], "model": "potion-code-16M"},
    })
    checks = Checks(model_short="potion", expected_dim=256, expected_openai_name="potion-code-16M")
    result = run_checks(checks, http=bad)
    assert result.ok is False
    assert any("embed_ping_dim" in f for f in result.failed)

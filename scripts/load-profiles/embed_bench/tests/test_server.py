"""Server tests use a fake backend; no model files needed."""
import os

import pytest
from fastapi.testclient import TestClient

os.environ["EMBED_BENCH_FAKE_DIM"] = "8"
os.environ["EMBED_BENCH_MODEL"] = "_fake"

from embed_bench.server import build_app  # noqa: E402


@pytest.fixture
def client():
    return TestClient(build_app())


def test_health_returns_model_dim_backend(client):
    r = client.get("/health")
    assert r.status_code == 200
    body = r.json()
    assert body["model"] == "_fake"
    assert body["dim"] == 8
    assert body["backend"] == "fake"
    assert isinstance(body["ram_mb"], int)


def test_embeddings_returns_openai_shape(client):
    r = client.post("/v1/embeddings", json={"input": ["a", "b"], "model": "_fake"})
    assert r.status_code == 200
    body = r.json()
    assert body["model"] == "_fake"
    assert len(body["data"]) == 2
    assert len(body["data"][0]["embedding"]) == 8
    assert body["data"][0]["index"] == 0


def test_embeddings_accepts_single_string(client):
    r = client.post("/v1/embeddings", json={"input": "single", "model": "_fake"})
    assert r.status_code == 200
    assert len(r.json()["data"]) == 1


def test_embeddings_empty_input_returns_400(client):
    r = client.post("/v1/embeddings", json={"input": [], "model": "_fake"})
    assert r.status_code == 400


@pytest.fixture
def full_gemma_client(monkeypatch):
    from embed_bench import server

    calls = []

    class Backend:
        dim = 768

        def embed(self, texts, kind="document"):
            calls.append((texts, kind))
            return [[0.] * self.dim for _ in texts]

    monkeypatch.setenv("EMBED_BENCH_MODEL", "gemma-full")
    monkeypatch.setattr(server, "make_backend", lambda recipe: Backend())
    return TestClient(server.build_app()), calls


@pytest.mark.parametrize("suffix,kind", [("", "document"), (":document", "document"), (":query", "query")])
def test_full_gemma_model_identity_and_kind(full_gemma_client, suffix, kind):
    client, calls = full_gemma_client
    name = "embeddinggemma-300m-full-v1"
    response = client.post("/v1/embeddings", json={"input": "text", "model": name + suffix})
    assert response.status_code == 200
    assert response.json()["model"] == name
    assert calls == [(["text"], kind)]
    assert client.get("/health").json()["model"] == name


@pytest.mark.parametrize("name", [None, "", "gemma-full", "embeddinggemma-300m",
                                 "embeddinggemma-300m:query", "embeddinggemma-300m-full-v1:other"])
def test_full_gemma_rejects_missing_or_wrong_encoder_identity(full_gemma_client, name):
    client, calls = full_gemma_client
    response = client.post("/v1/embeddings", json={"input": "text", "model": name})
    assert response.status_code == 400
    assert calls == []

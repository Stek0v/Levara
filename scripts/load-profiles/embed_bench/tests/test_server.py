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

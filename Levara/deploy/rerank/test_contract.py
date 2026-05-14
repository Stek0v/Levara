"""Black-box contract tests for the rerank sidecar.

Targets a live sidecar (default: Pi 5 at 10.23.0.53:9100). Override via
RERANK_URL env. These are NOT unit tests of app.py — they verify the wire
contract Levara depends on (Cohere-compat shape, edge inputs, /health).

Run:
    RERANK_URL=http://10.23.0.53:9100 pytest deploy/rerank/test_contract.py -v
"""
from __future__ import annotations
import os

import pytest
import requests

URL = os.environ.get("RERANK_URL", "http://10.23.0.53:9100").rstrip("/")
TIMEOUT = float(os.environ.get("RERANK_TIMEOUT", "30"))


def _post_rerank(payload: dict) -> requests.Response:
    return requests.post(f"{URL}/rerank", json=payload, timeout=TIMEOUT)


def test_health_ok():
    r = requests.get(f"{URL}/health", timeout=5)
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["ok"] is True
    assert isinstance(body.get("model_dir"), str)


def test_rerank_basic_shape():
    """Cohere shape: results[].index + relevance_score, plus model + latency_ms."""
    r = _post_rerank({
        "query": "what is the capital of france",
        "documents": ["Paris is the capital of France.", "Bananas are yellow.", "France is in Europe."],
    })
    assert r.status_code == 200, r.text
    body = r.json()
    assert {"results", "model", "latency_ms"} <= set(body.keys())
    assert isinstance(body["latency_ms"], (int, float))
    assert len(body["results"]) == 3
    for item in body["results"]:
        assert set(item.keys()) == {"index", "relevance_score"}
        assert isinstance(item["index"], int)
        assert isinstance(item["relevance_score"], (int, float))


def test_rerank_orders_relevant_first():
    """Sanity that the model is actually scoring — the obviously relevant
    doc should rank first. If this flips, either the wrong model is loaded
    or the input pairing is broken."""
    r = _post_rerank({
        "query": "what is the capital of france",
        "documents": [
            "Bananas grow in tropical climates.",
            "Paris is the capital of France.",
            "Cats are mammals.",
        ],
    })
    assert r.status_code == 200, r.text
    top = r.json()["results"][0]
    assert top["index"] == 1, f"expected paris-doc index 1 first, got {r.json()['results']}"


def test_rerank_empty_documents():
    r = _post_rerank({"query": "anything", "documents": []})
    assert r.status_code == 200, r.text
    assert r.json()["results"] == []


def test_rerank_single_document():
    r = _post_rerank({"query": "q", "documents": ["only one"]})
    assert r.status_code == 200, r.text
    out = r.json()["results"]
    assert len(out) == 1 and out[0]["index"] == 0


def test_rerank_top_n_truncates():
    r = _post_rerank({
        "query": "europe capitals",
        "documents": ["Paris", "Berlin", "Rome", "Madrid", "Lisbon"],
        "top_n": 2,
    })
    assert r.status_code == 200, r.text
    assert len(r.json()["results"]) == 2


def test_rerank_long_document_does_not_crash():
    """Tokenizer must truncate at MAX_DOC_LEN; a 50k-char doc must not OOM
    or 500 the sidecar."""
    long_doc = "lorem ipsum dolor sit amet " * 2000  # ~54k chars
    r = _post_rerank({"query": "test", "documents": ["short doc", long_doc]})
    assert r.status_code == 200, r.text
    assert len(r.json()["results"]) == 2


def test_rerank_non_ascii_query_and_docs():
    """Cyrillic + emoji must not break encoding paths."""
    r = _post_rerank({
        "query": "столица Франции",
        "documents": ["Париж — столица Франции.", "Бананы жёлтые 🍌"],
    })
    assert r.status_code == 200, r.text
    top = r.json()["results"][0]
    assert top["index"] == 0


def test_rerank_indices_cover_input_range():
    """Returned indices must be a permutation of 0..N-1 — no duplicates,
    no out-of-range. Levara uses index to look up candidates by position."""
    docs = [f"doc number {i}" for i in range(8)]
    r = _post_rerank({"query": "q", "documents": docs})
    assert r.status_code == 200, r.text
    idxs = sorted(item["index"] for item in r.json()["results"])
    assert idxs == list(range(8))

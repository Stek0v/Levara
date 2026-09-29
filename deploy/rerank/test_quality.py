"""Quality regression against an explicitly selected rerank sidecar.

Uses the MTEB `mteb/scidocs-reranking` test split — the same dataset
that produced the Phase 1.5 ONNX INT8 baseline (NDCG@10 ≈ 0.705). The
dataset is cached locally at `eval/mteb_scidocs_reranking.jsonl` so the
test does not depend on Hugging Face availability at run time.

Each row is `{query, positive[], negative[]}`. For NDCG@10 we treat
positives as label 1, negatives as label 0. Deterministically shuffle
documents with their labels, then require the reranker to beat that
same input order as well as the historical absolute quality floor.

Offline checks (no sidecar):
    python3 -m pytest deploy/rerank/test_quality.py -k offline -v

Live quality evaluation (explicit test endpoint required):
    RERANK_URL=http://127.0.0.1:19100 \
    python3 -m pytest deploy/rerank/test_quality.py -v -s

Knobs (env):
    RERANK_EVAL_N          — number of queries to sample (default 100).
                             Full 3978 takes ~30 min on Pi 5.
    RERANK_NDCG10_FLOOR    — fail threshold (default 0.61 — ~8 pp under
                             the live N=100 mean of 0.690 measured on
                             2026-05-15; see comment by the constant).
    RERANK_EVAL_SEED       — RNG seed for sample and candidate order (default 0).
"""
from __future__ import annotations
import json
import math
import os
import pathlib
import random
from types import SimpleNamespace

import pytest
import requests

URL = os.environ.get("RERANK_URL", "").rstrip("/")
TIMEOUT = float(os.environ.get("RERANK_TIMEOUT", "60"))
N_QUERIES = int(os.environ.get("RERANK_EVAL_N", "100"))
SEED = int(os.environ.get("RERANK_EVAL_SEED", "0"))

# Observed on the live Pi 5 INT8 sidecar (2026-05-15): NDCG@10 = 0.690
# on N=100 of mteb/scidocs-reranking. The Phase 1.5 bench report quoted
# 0.705 against BEIR-scidocs (different dataset, graded qrels) — the
# MTEB reranking split uses binary pos/neg pairs so the absolute
# number is a few points lower for the same model. Floor below is
# ~8 pp under the observed mean: tolerates subsample/seed noise on
# N=100 while still failing on a real quality regression.
NDCG10_FLOOR = float(os.environ.get("RERANK_NDCG10_FLOOR", "0.61"))

EVAL_FILE = pathlib.Path(__file__).parent / "eval" / "mteb_scidocs_reranking.jsonl"


def _dcg(gains: list[float]) -> float:
    return sum(g / math.log2(i + 2) for i, g in enumerate(gains))


def _ndcg_at_k(ranked_labels: list[int], ideal_labels: list[int], k: int) -> float:
    dcg = _dcg(ranked_labels[:k])
    idcg = _dcg(sorted(ideal_labels, reverse=True)[:k])
    return dcg / idcg if idcg > 0 else 0.0


@pytest.fixture(scope="module")
def eval_rows():
    if not URL:
        pytest.skip("set RERANK_URL to a test sidecar for live quality evaluation")
    if not EVAL_FILE.exists():
        pytest.skip(
            f"missing {EVAL_FILE.name} — run the cache step in the test docstring"
        )
    with EVAL_FILE.open() as f:
        rows = [json.loads(line) for line in f if line.strip()]
    rng = random.Random(SEED)
    rng.shuffle(rows)
    return rows[:N_QUERIES]


def test_ndcg10_regression(eval_rows):
    ndcgs: list[float] = []
    baselines: list[float] = []
    rng = random.Random(SEED)
    for row in eval_rows:
        pos = list(row["positive"])
        neg = list(row["negative"])
        if not pos:
            continue
        candidates = [(doc, 1) for doc in pos] + [(doc, 0) for doc in neg]
        rng.shuffle(candidates)
        docs, labels = map(list, zip(*candidates))
        baselines.append(_ndcg_at_k(labels, labels, 10))

        r = requests.post(
            f"{URL}/rerank",
            json={"query": row["query"], "documents": docs},
            timeout=TIMEOUT,
        )
        assert r.status_code == 200, r.text
        ranked = r.json()["results"]
        indices = [item["index"] for item in ranked]
        assert all(type(i) is int for i in indices) and sorted(indices) == list(range(len(docs))), (
            "reranker indices must be a full permutation of the input documents"
        )
        ranked_labels = [labels[i] for i in indices]
        ndcgs.append(_ndcg_at_k(ranked_labels, labels, 10))

    assert ndcgs, "no scorable rows in subsample"
    mean_ndcg = sum(ndcgs) / len(ndcgs)
    mean_baseline = sum(baselines) / len(baselines)
    print(
        f"\nNDCG@10 mean over {len(ndcgs)} queries: {mean_ndcg:.4f} "
        f"(input order={mean_baseline:.4f}, delta={mean_ndcg - mean_baseline:+.4f}, "
        f"floor={NDCG10_FLOOR})"
    )
    assert mean_ndcg >= NDCG10_FLOOR, (
        f"NDCG@10 regression: {mean_ndcg:.4f} < {NDCG10_FLOOR} — "
        "sidecar quality dropped below the historical floor"
    )
    assert mean_ndcg > mean_baseline, (
        f"No rerank improvement: {mean_ndcg:.4f} <= input order {mean_baseline:.4f}"
    )


def test_offline_quality_rejects_noop_and_accepts_improvement(monkeypatch):
    row = {"query": "test", "positive": [f"positive-{i}" for i in range(8)],
           "negative": ["negative-0", "negative-1"]}
    payloads = []

    def fake_post(*args, json, **kwargs):
        payloads.append(json)
        indices = list(range(len(json["documents"])))
        if improve:
            indices.sort(key=lambda i: json["documents"][i].startswith("positive"), reverse=True)
        return SimpleNamespace(status_code=200, text="", json=lambda: {
            "results": [{"index": i} for i in indices]})

    monkeypatch.setattr(requests, "post", fake_post)
    monkeypatch.setitem(globals(), "SEED", 0)
    monkeypatch.setitem(globals(), "NDCG10_FLOOR", 0.61)
    improve = False
    with pytest.raises(AssertionError, match="No rerank improvement"):
        test_ndcg10_regression([row])
    improve = True
    monkeypatch.setitem(globals(), "NDCG10_FLOOR", 1.0)
    test_ndcg10_regression([row])
    assert payloads[0] == payloads[1], "candidate order must be reproducible"
    assert payloads[0]["documents"] != row["positive"] + row["negative"]


@pytest.mark.parametrize("indices", [[0, 0, 2], [-1, 1, 2], [0, 1, 3], [0, 1], [False, 1, 2]])
def test_offline_quality_rejects_invalid_indices(monkeypatch, indices):
    monkeypatch.setattr(requests, "post", lambda *args, **kwargs: SimpleNamespace(
        status_code=200, text="", json=lambda: {"results": [{"index": i} for i in indices]}))
    with pytest.raises(AssertionError, match="full permutation"):
        test_ndcg10_regression([{"query": "test", "positive": ["a", "b"], "negative": ["c"]}])

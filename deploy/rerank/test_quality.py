"""Offline quality regression against the live rerank sidecar.

Uses the MTEB `mteb/scidocs-reranking` test split — the same dataset
that produced the Phase 1.5 ONNX INT8 baseline (NDCG@10 ≈ 0.705). The
dataset is cached locally at `eval/mteb_scidocs_reranking.jsonl` so the
test does not depend on Hugging Face availability at run time.

Each row is `{query, positive[], negative[]}`. For NDCG@10 we treat
positives as label 1, negatives as label 0, then rerank the
concatenation and check that the produced order pushes positives up.

Run:
    RERANK_URL=http://10.23.0.53:9100 \
    pytest deploy/rerank/test_quality.py -v -s

Knobs (env):
    RERANK_EVAL_N          — number of queries to sample (default 100).
                             Full 3978 takes ~30 min on Pi 5.
    RERANK_NDCG10_FLOOR    — fail threshold (default 0.61 — ~8 pp under
                             the live N=100 mean of 0.690 measured on
                             2026-05-15; see comment by the constant).
    RERANK_EVAL_SEED       — RNG seed for the subsample (default 0).
"""
from __future__ import annotations
import json
import math
import os
import pathlib
import random

import pytest
import requests

URL = os.environ.get("RERANK_URL", "http://10.23.0.53:9100").rstrip("/")
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
    for row in eval_rows:
        pos = list(row["positive"])
        neg = list(row["negative"])
        if not pos:
            continue
        docs = pos + neg
        # Labels are positional in `docs`: first len(pos) are 1, rest 0.
        labels = [1] * len(pos) + [0] * len(neg)

        r = requests.post(
            f"{URL}/rerank",
            json={"query": row["query"], "documents": docs},
            timeout=TIMEOUT,
        )
        assert r.status_code == 200, r.text
        ranked = r.json()["results"]
        ranked_labels = [labels[item["index"]] for item in ranked]
        ndcgs.append(_ndcg_at_k(ranked_labels, labels, 10))

    assert ndcgs, "no scorable rows in subsample"
    mean_ndcg = sum(ndcgs) / len(ndcgs)
    print(
        f"\nNDCG@10 mean over {len(ndcgs)} queries: {mean_ndcg:.4f} "
        f"(floor={NDCG10_FLOOR}, Phase 1.5 INT8 baseline=0.705)"
    )
    assert mean_ndcg >= NDCG10_FLOOR, (
        f"NDCG@10 regression: {mean_ndcg:.4f} < {NDCG10_FLOOR} — "
        f"sidecar quality dropped vs Phase 1.5 baseline (0.705)"
    )

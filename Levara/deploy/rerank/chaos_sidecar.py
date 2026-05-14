"""Chaos sidecar for Levara rerank.

A drop-in replacement for `deploy/rerank/app.py` that injects bounded
latency and random 5xx errors. Used to validate the Levara chunk-search
handler's rerank budget / error fallback paths and the
`levara_rerank_invocations_total{outcome=...}` distribution.

Request/response shape mirrors the Cohere-compatible contract that
`pkg/rerank/reranker.go` speaks:

    POST /rerank
    {"query": "...", "documents": ["...", ...], "model": "...", "top_n": N}
    -> 200 {"results": [{"index": i, "relevance_score": f}, ...]}
    -> 5xx  (random injection)

Config (env):
    CHAOS_LATENCY_MS_MAX   max uniform latency, ms (default 3000)
    CHAOS_5XX_PROB         probability of HTTP 500 (default 0.10)
    CHAOS_PORT             listen port when run as __main__ (default 9101)
    CHAOS_SEED             optional int seed for reproducibility
"""
from __future__ import annotations

import os
import random
import time
from typing import List, Optional

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel


LATENCY_MS_MAX = int(os.environ.get("CHAOS_LATENCY_MS_MAX", "3000"))
FIVEXX_PROB = float(os.environ.get("CHAOS_5XX_PROB", "0.10"))
PORT = int(os.environ.get("CHAOS_PORT", "9101"))
SEED_ENV = os.environ.get("CHAOS_SEED")

_rng = random.Random()
if SEED_ENV is not None and SEED_ENV != "":
    _rng.seed(int(SEED_ENV))


class RerankRequest(BaseModel):
    query: str
    documents: List[str]
    model: Optional[str] = None
    top_n: Optional[int] = None


class RerankResultItem(BaseModel):
    index: int
    relevance_score: float


class RerankResponse(BaseModel):
    results: List[RerankResultItem]


app = FastAPI(title="levara-rerank-chaos")


@app.get("/health")
def health() -> dict:
    return {
        "status": "ok",
        "mode": "chaos",
        "latency_ms_max": LATENCY_MS_MAX,
        "p5xx": FIVEXX_PROB,
    }


@app.post("/rerank", response_model=RerankResponse)
def rerank(req: RerankRequest) -> RerankResponse:
    # 1. Inject latency.
    if LATENCY_MS_MAX > 0:
        delay_ms = _rng.uniform(0.0, float(LATENCY_MS_MAX))
        time.sleep(delay_ms / 1000.0)

    # 2. Inject 5xx (after the latency, so the handler observes
    #    "slow then failure" the same way a real broken upstream would).
    if FIVEXX_PROB > 0.0 and _rng.random() < FIVEXX_PROB:
        raise HTTPException(status_code=500, detail="chaos: injected 5xx")

    # 3. Happy path — random scores per doc, original index order.
    results = [
        RerankResultItem(index=i, relevance_score=_rng.random())
        for i in range(len(req.documents))
    ]
    return RerankResponse(results=results)


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="127.0.0.1", port=PORT, log_level="warning")

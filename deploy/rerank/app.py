"""Levara rerank sidecar — ONNX INT8 cross-encoder, Cohere-compatible API.

Loads mmarco-mMiniLMv2-L12-H384-v1 (ONNX INT8 arm64) once at startup and
exposes a single scoring endpoint. Designed to run next to Levara on the
same host (Pi 5 included).
"""
from __future__ import annotations
import logging
import os
import time
from contextlib import asynccontextmanager
from typing import List

import numpy as np
from fastapi import FastAPI, HTTPException
from optimum.onnxruntime import ORTModelForSequenceClassification
from pydantic import BaseModel, Field
from transformers import AutoTokenizer


MODEL_DIR = os.environ.get("RERANK_MODEL_DIR", "/models/mmini-L12-int8")
MODEL_FILE = os.environ.get("RERANK_MODEL_FILE", "model_quantized.onnx")
MAX_DOC_LEN = int(os.environ.get("RERANK_MAX_DOC_LEN", "384"))
BATCH_SIZE = int(os.environ.get("RERANK_BATCH_SIZE", "16"))

log = logging.getLogger("rerank")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

state: dict = {}


@asynccontextmanager
async def lifespan(_: FastAPI):
    t = time.perf_counter()
    state["tok"] = AutoTokenizer.from_pretrained(MODEL_DIR)
    state["model"] = ORTModelForSequenceClassification.from_pretrained(
        MODEL_DIR, file_name=MODEL_FILE, provider="CPUExecutionProvider"
    )
    log.info("loaded %s/%s in %.1fs", MODEL_DIR, MODEL_FILE, time.perf_counter() - t)
    yield
    state.clear()


app = FastAPI(title="Levara Rerank", lifespan=lifespan)


class RerankRequest(BaseModel):
    query: str
    documents: List[str] = Field(default_factory=list)
    top_n: int | None = None
    model: str | None = None


class RerankResult(BaseModel):
    index: int
    relevance_score: float


class RerankResponse(BaseModel):
    results: List[RerankResult]
    model: str
    latency_ms: float


@app.get("/health")
def health():
    return {"ok": "model" in state, "model_dir": MODEL_DIR}


@app.post("/rerank", response_model=RerankResponse)
def rerank(req: RerankRequest):
    if "model" not in state:
        raise HTTPException(503, "model not loaded")
    if not req.documents:
        return RerankResponse(results=[], model=MODEL_DIR, latency_ms=0.0)

    t = time.perf_counter()
    tok = state["tok"]
    model = state["model"]
    scores: list[float] = []
    for i in range(0, len(req.documents), BATCH_SIZE):
        batch = req.documents[i : i + BATCH_SIZE]
        inputs = tok(
            [req.query] * len(batch),
            batch,
            padding=True,
            truncation=True,
            max_length=MAX_DOC_LEN,
            return_tensors="np",
        )
        logits = model(**inputs).logits
        arr = logits.reshape(-1) if logits.shape[-1] == 1 else logits[:, -1]
        scores.extend(arr.tolist())

    paired = sorted(enumerate(scores), key=lambda x: -x[1])
    if req.top_n is not None:
        paired = paired[: req.top_n]
    results = [RerankResult(index=i, relevance_score=float(s)) for i, s in paired]
    return RerankResponse(
        results=results,
        model=MODEL_DIR,
        latency_ms=(time.perf_counter() - t) * 1000.0,
    )

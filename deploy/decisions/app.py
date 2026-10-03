"""Levara decisions sidecar — FRIDA-Decisions ONNX INT8, CPU-only.

Same shape as deploy/rerank/app.py: load once at startup, expose thin
endpoints. CPU only by policy — sharing MPS with the live embed server
(:9101) starves prod recall (see benchmark/frida_gate/README.md).
"""
from __future__ import annotations

import logging
import os
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException

MODEL_DIR = os.environ.get("DECISIONS_MODEL_DIR", "ai-forever/FRIDA-Decisions")
THREADS = int(os.environ.get("DECISIONS_THREADS", "8"))
STATE_MAX = int(os.environ.get("DECISIONS_STATE_MAX", "384"))

log = logging.getLogger("decisions")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

state: dict = {}


@asynccontextmanager
async def lifespan(_: FastAPI):
    t = time.perf_counter()
    from frida_decisions import OnnxJudge

    state["judge"] = OnnxJudge.from_pretrained(MODEL_DIR, threads=THREADS,
                                               state_max=STATE_MAX)
    log.info("loaded %s in %.1fs (threads=%d, state_max=%d)",
             MODEL_DIR, time.perf_counter() - t, THREADS, STATE_MAX)
    yield
    state.clear()


app = FastAPI(title="Levara Decisions", lifespan=lifespan)


@app.get("/health")
def health():
    judge = state.get("judge")
    return {"ok": judge is not None, "model": MODEL_DIR,
            "threads": THREADS, "state_max": STATE_MAX}


@app.post("/judge")
def judge(req: dict):
    """Raw FRIDA-Decisions request passthrough: state + questions -> answers."""
    j = state.get("judge")
    if j is None:
        raise HTTPException(503, "model not loaded")
    t = time.perf_counter()
    try:
        body = j.judge(req)
    except ValueError as e:  # RequestError
        raise HTTPException(400, str(e))
    body["server_ms"] = round((time.perf_counter() - t) * 1000, 1)
    return body

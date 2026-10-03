#!/usr/bin/env python3
"""Shared helpers: prod embed client (OpenAI-compatible /v1/embeddings on :9101)."""
import json
import urllib.request
import numpy as np

EMBED_URL = "http://127.0.0.1:9101/v1/embeddings"
MODEL = "embeddinggemma-300m"


def embed(texts, batch=128, timeout=120):
    out = []
    for i in range(0, len(texts), batch):
        chunk = [t[:2000] for t in texts[i : i + batch]]
        body = json.dumps({"model": MODEL, "input": chunk}).encode()
        req = urllib.request.Request(EMBED_URL, data=body,
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read())
        vecs = [d["embedding"] for d in sorted(data["data"], key=lambda d: d["index"])]
        out.extend(np.asarray(v, dtype=np.float32) for v in vecs)
    m = np.stack(out)
    m /= np.linalg.norm(m, axis=1, keepdims=True) + 1e-9
    return m


def cosine_matrix(m):
    return m @ m.T

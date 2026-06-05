"""Minimal Levara HTTP client for reembed operational scripts.

Kept separate from scripts/load-profiles/runner.py because that module
is bench-specific (preflight, namespace guard, profile collection
naming) — reembed scripts run against prod and don't want any of
that machinery. This file: just http_request, search_text, list_meta.
"""
from __future__ import annotations

import json
import random
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any

RETRYABLE = {429, 500, 502, 503, 504}


class HttpError(RuntimeError):
    def __init__(self, status: int, body: str):
        super().__init__(f"HTTP {status}: {body[:300]}")
        self.status = status
        self.body = body


@dataclass
class Target:
    base_url: str
    token: str

    def url(self, path: str) -> str:
        return self.base_url.rstrip("/") + path

    def headers(self) -> dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self.token:
            h["Authorization"] = f"Bearer {self.token}"
        return h


def http_request(
    method: str,
    url: str,
    headers: dict[str, str] | None = None,
    body: dict[str, Any] | None = None,
    timeout: float = 30.0,
    max_retries: int = 3,
) -> Any:
    payload = json.dumps(body).encode("utf-8") if body is not None else None
    last: Exception | None = None
    for attempt in range(max_retries + 1):
        try:
            req = urllib.request.Request(url, data=payload, headers=headers or {}, method=method)
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                data = resp.read()
                return json.loads(data) if data else {}
        except urllib.error.HTTPError as e:
            text = ""
            try:
                text = e.read().decode("utf-8", "replace")
            except Exception:
                pass
            if e.code in RETRYABLE and attempt < max_retries:
                last = HttpError(e.code, text)
            else:
                raise HttpError(e.code, text) from None
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            if attempt >= max_retries:
                raise
            last = e
        time.sleep(min(2.0**attempt * 0.5, 8.0) + random.random() * 0.25)
    raise last if last else RuntimeError("retry loop exhausted")


def get_meta(target: Target, collection: str) -> dict[str, Any]:
    return http_request("GET", target.url(f"/api/v1/collections/{collection}/meta"), headers=target.headers())


def search_text(
    target: Target,
    collection: str,
    query: str,
    *,
    top_k: int = 10,
    rerank: bool = False,
) -> tuple[list[dict[str, Any]], int]:
    """Return (hits, latency_ms). hits is the raw `results` array.

    rerank=False is the right choice for parity: we want to compare
    pure embedding-model behaviour, not embedding+rerank. Rerank stays
    constant across the swap (same cross-encoder), so reranked output
    differences would conflate the two effects.
    """
    body = {
        "collection": collection,
        "query_text": query,
        "top_k": top_k,
        "query_type": "CHUNKS",
        "rerank": rerank,
    }
    t0 = time.monotonic()
    resp = http_request(
        "POST",
        target.url("/api/v1/search/text"),
        headers=target.headers(),
        body=body,
    )
    latency_ms = int((time.monotonic() - t0) * 1000)
    hits = resp.get("results") if isinstance(resp, dict) else None
    if not isinstance(hits, list):
        hits = []
    return hits, latency_ms


def eprint(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)

"""Shared infrastructure for load-profile scripts.

Profiles are not stress tests. Their job is to produce realistic
production-shaped traffic against a Levara instance so that
RERANK_SCORE_GAP_THRESHOLD (Phase 2.5) can be calibrated from real
score distributions.

Each profile imports this module to get:
- A thin HTTP client with retries and per-host timeouts.
- An atomic JSONL writer with periodic fsync.
- A double-traffic search wrapper that calls /api/v1/search twice
  per query (rerank=true and rerank=false) so the analyzer can
  derive ground-truth `top1_changed` and full vector-score gaps
  from the rerank=false response.
- A namespace guard so profiles physically cannot touch existing
  production collections — all writes go through prefixes like
  `loadprofile_p1_*`.
"""

from __future__ import annotations

import json
import os
import random
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass, field
from typing import Any, Iterable

SCHEMA_VERSION = 1
NAMESPACE_PREFIX = "loadprofile_"
DEFAULT_TIMEOUT_S = 30.0
RETRYABLE_STATUS = {429, 500, 502, 503, 504}


# ── Target description ──────────────────────────────────────────────


@dataclass
class Target:
    """A Levara instance under test."""

    name: str
    base_url: str  # e.g. http://10.23.0.64:8080
    token: str
    embed_model: str = ""  # filled in by preflight
    rerank_endpoint: str = ""  # filled in by preflight
    rerank_enabled: bool = False

    def url(self, path: str) -> str:
        return self.base_url.rstrip("/") + path

    def headers(self) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {self.token}",
            "Content-Type": "application/json",
        }


# ── HTTP client ─────────────────────────────────────────────────────


class HttpError(RuntimeError):
    def __init__(self, status: int, body: str):
        super().__init__(f"HTTP {status}: {body[:300]}")
        self.status = status
        self.body = body


def http_request(
    method: str,
    url: str,
    headers: dict[str, str] | None = None,
    body: dict[str, Any] | None = None,
    timeout: float = DEFAULT_TIMEOUT_S,
    max_retries: int = 3,
) -> dict[str, Any]:
    """Single HTTP call with retry-on-5xx / 429. Returns parsed JSON.

    Body is JSON-encoded if not None. 4xx (other than 429) raise
    immediately so caller can distinguish auth/validation problems.
    """
    payload = None
    if body is not None:
        payload = json.dumps(body).encode("utf-8")

    last_err: Exception | None = None
    for attempt in range(max_retries + 1):
        try:
            req = urllib.request.Request(
                url, data=payload, headers=headers or {}, method=method
            )
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                data = resp.read()
                if not data:
                    return {}
                return json.loads(data)
        except urllib.error.HTTPError as e:
            text = ""
            try:
                text = e.read().decode("utf-8", "replace")
            except Exception:
                pass
            if e.code in RETRYABLE_STATUS and attempt < max_retries:
                last_err = HttpError(e.code, text)
            else:
                raise HttpError(e.code, text) from None
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            if attempt >= max_retries:
                raise
            last_err = e
        sleep_s = min(2.0**attempt * 0.5, 8.0) + random.random() * 0.25
        time.sleep(sleep_s)

    raise last_err if last_err else RuntimeError("retry loop exhausted")


# ── JSONL writer ────────────────────────────────────────────────────


class JsonlWriter:
    """Append-only JSONL writer with periodic fsync.

    Atomicity: each line is written under a lock with a single write()
    call. Lines are bounded — no embeddings or full chunk text are
    ever emitted, only ids, scores, and short snippets.
    """

    def __init__(self, path: str, fsync_every_lines: int = 100, fsync_every_s: float = 5.0):
        os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
        self.path = path
        self._fh = open(path, "a", encoding="utf-8")
        self._lock = threading.Lock()
        self._fsync_every_lines = fsync_every_lines
        self._fsync_every_s = fsync_every_s
        self._lines_since_fsync = 0
        self._last_fsync = time.monotonic()
        self.dropped = 0

    def write(self, record: dict[str, Any]) -> None:
        record.setdefault("schema_version", SCHEMA_VERSION)
        line = json.dumps(record, separators=(",", ":"), ensure_ascii=False) + "\n"
        with self._lock:
            try:
                self._fh.write(line)
            except OSError:
                self.dropped += 1
                return
            self._lines_since_fsync += 1
            now = time.monotonic()
            if (
                self._lines_since_fsync >= self._fsync_every_lines
                or now - self._last_fsync >= self._fsync_every_s
            ):
                self._fh.flush()
                os.fsync(self._fh.fileno())
                self._lines_since_fsync = 0
                self._last_fsync = now

    def close(self) -> None:
        with self._lock:
            try:
                self._fh.flush()
                os.fsync(self._fh.fileno())
            except OSError:
                pass
            self._fh.close()


# ── Preflight & namespace guard ─────────────────────────────────────


def preflight(target: Target) -> dict[str, Any]:
    """Verify auth + collect server-side capabilities.

    Populates target.embed_model / rerank_endpoint / rerank_enabled
    from /api/v1/rerank/info and /api/v1/info. Raises if rerank isn't
    configured — every load profile needs it (without it there's
    nothing to calibrate).
    """
    info = http_request("GET", target.url("/api/v1/info"), headers=target.headers())
    rerank_info: dict[str, Any] = {}
    try:
        rerank_info = http_request(
            "GET", target.url("/api/v1/rerank/info"), headers=target.headers()
        )
    except HttpError as e:
        if e.status != 404:
            raise
    target.embed_model = (info.get("embedding") or {}).get("model", "") or info.get(
        "embed_model", ""
    )
    target.rerank_endpoint = rerank_info.get("endpoint", "")
    target.rerank_enabled = bool(target.rerank_endpoint)
    if not target.rerank_enabled:
        raise RuntimeError(
            f"target {target.name}: RERANK_ENDPOINT not configured — "
            f"profile output would be useless for gate calibration"
        )
    return {"info": info, "rerank_info": rerank_info}


def assert_namespace(target: Target, profile_id: str) -> None:
    """Sanity check: profile-owned collection names must start with
    `loadprofile_<profile_id>_`. Also confirms we are not accidentally
    pointed at a host where production datasets share that prefix
    (very unlikely, but a single misconfigured target would otherwise
    silently corrupt prod data)."""
    expected = f"{NAMESPACE_PREFIX}{profile_id}_"
    # We don't list collections directly — datasets endpoint is what
    # admins use to see "human" data. Cross-check there's no overlap.
    try:
        datasets = http_request(
            "GET", target.url("/api/v1/datasets"), headers=target.headers()
        )
    except HttpError:
        datasets = {}
    items = datasets.get("datasets") if isinstance(datasets, dict) else datasets
    if isinstance(items, list):
        for d in items:
            name = (d.get("name") if isinstance(d, dict) else str(d)) or ""
            if name.startswith(expected) and not name.startswith(NAMESPACE_PREFIX):
                raise RuntimeError(
                    f"namespace collision on {target.name}: {name}"
                )
    return None


def profile_collection(profile_id: str, shard: str = "main") -> str:
    return f"{NAMESPACE_PREFIX}{profile_id}_{shard}"


# ── Add / search wrappers ───────────────────────────────────────────


def add_texts(
    target: Target,
    collection: str,
    docs: list[dict[str, Any]],
    *,
    room: str | None = None,
    tags: list[str] | None = None,
) -> dict[str, Any]:
    """Push a batch through /api/v1/add (cognify=false to keep ingest
    deterministic — we want raw chunks indexed, not LLM-extracted
    entities). Each doc must carry id + text; metadata is optional."""
    body: dict[str, Any] = {
        "collection": collection,
        "documents": docs,
        "cognify": False,
    }
    if room:
        body["room"] = room
    if tags:
        body["tags"] = tags
    return http_request(
        "POST", target.url("/api/v1/add"), headers=target.headers(), body=body
    )


def search_pair(
    target: Target,
    collection: str,
    query: str,
    *,
    top_k: int = 10,
    query_type: str = "CHUNKS",
    room: str | None = None,
    tags: list[str] | None = None,
) -> dict[str, Any]:
    """Issue the same query twice — rerank=true and rerank=false —
    so the analyzer has both the production (post-rerank) order and
    the vector-only order to compute score_gap and top1_changed.

    Returns a dict shaped:
        {
          "with_rerank": {<server response>, latency_ms: ...},
          "no_rerank":   {<server response>, latency_ms: ...},
        }
    """
    out: dict[str, Any] = {}
    for label, rerank in (("with_rerank", True), ("no_rerank", False)):
        body = {
            "collection": collection,
            "query_text": query,
            "top_k": top_k,
            "query_type": query_type,
            "rerank": rerank,
        }
        if room:
            body["room"] = room
        if tags:
            body["tags"] = tags
        t0 = time.monotonic()
        resp = http_request(
            "POST", target.url("/api/v1/search"), headers=target.headers(), body=body
        )
        out[label] = {
            "latency_ms": int((time.monotonic() - t0) * 1000),
            "response": resp,
        }
    return out


# ── Score extraction (analyzer-side helpers used inline) ────────────


def extract_score_vector(no_rerank_resp: dict[str, Any]) -> list[float]:
    """Pull the ordered vector-only score list from a `rerank=false`
    /search response. Server returns hits under one of two keys
    (`results` or `chunks`) depending on strategy — we accept both."""
    items = no_rerank_resp.get("results") or no_rerank_resp.get("chunks") or []
    return [float(item.get("score", 0.0)) for item in items if isinstance(item, dict)]


def gap_features(scores: list[float]) -> dict[str, float]:
    """Compute the gap features the Phase 2.5 design specifies:

    - score_gap_top_bottom: scores[0] - scores[-1]
    - score_gap_top1_top2:  scores[0] - scores[1]
    - score_gap_top1_top5:  scores[0] - scores[4] (if available)
    """
    if not scores:
        return {}
    n = len(scores)
    f: dict[str, float] = {
        "score_gap_top_bottom": scores[0] - scores[-1],
        "score_top1": scores[0],
    }
    if n >= 2:
        f["score_gap_top1_top2"] = scores[0] - scores[1]
    if n >= 5:
        f["score_gap_top1_top5"] = scores[0] - scores[4]
    return f


def top_changed(
    with_rerank_resp: dict[str, Any], no_rerank_resp: dict[str, Any]
) -> bool | None:
    """True iff the top-1 id differs between rerank=on and rerank=off.
    None if either response is empty (signal absent → can't compare)."""
    a = with_rerank_resp.get("results") or with_rerank_resp.get("chunks") or []
    b = no_rerank_resp.get("results") or no_rerank_resp.get("chunks") or []
    if not a or not b:
        return None
    ida = (a[0] or {}).get("id")
    idb = (b[0] or {}).get("id")
    if ida is None or idb is None:
        return None
    return ida != idb


# ── Convenience: build a record ready for JSONL ────────────────────


def build_query_record(
    *,
    profile_id: str,
    target: Target,
    query_id: str,
    query_text: str,
    collection: str,
    query_type: str,
    pair: dict[str, Any],
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    no_r = pair["no_rerank"]["response"]
    with_r = pair["with_rerank"]["response"]
    scores_vec = extract_score_vector(no_r)
    rec: dict[str, Any] = {
        "ts": time.time(),
        "profile_id": profile_id,
        "target": target.name,
        "embedding_model": target.embed_model,
        "rerank_endpoint": target.rerank_endpoint,
        "query_id": query_id,
        "query_text_len": len(query_text),
        "query_type": query_type,
        "collection": collection,
        "latency_no_rerank_ms": pair["no_rerank"]["latency_ms"],
        "latency_with_rerank_ms": pair["with_rerank"]["latency_ms"],
        "scores_vector": scores_vec,
        "n_hits_no_rerank": len(no_r.get("results") or no_r.get("chunks") or []),
        "n_hits_with_rerank": len(
            with_r.get("results") or with_r.get("chunks") or []
        ),
        "top_changed_by_rerank": top_changed(with_r, no_r),
        "rerank_outcome": _outcome_from_response(with_r),
    }
    rec.update(gap_features(scores_vec))
    if extra:
        rec.update(extra)
    return rec


def _outcome_from_response(resp: dict[str, Any]) -> str:
    """Infer rerank outcome from response shape. Server doesn't echo
    the metric outcome name directly, but the per-hit `reranked` flag
    plus the rerank_skip_reason hint (when present) is enough."""
    if "rerank_skip_reason" in resp:
        return str(resp["rerank_skip_reason"])
    hits = resp.get("results") or resp.get("chunks") or []
    if not hits:
        return "no_results"
    any_reranked = any((h or {}).get("reranked") for h in hits if isinstance(h, dict))
    return "reranked" if any_reranked else "fallback"


# ── CLI plumbing ────────────────────────────────────────────────────


def load_token_from_env_or_file(target_name: str) -> str:
    """Token resolution order:
      1. $LEVARA_TOKEN_<TARGETNAME>
      2. $LEVARA_TOKEN
      3. ~/.config/levara-service-keys/<target_name>.jwt
    """
    env_key = f"LEVARA_TOKEN_{target_name.upper()}"
    for k in (env_key, "LEVARA_TOKEN"):
        v = os.environ.get(k)
        if v:
            return v.strip()
    path = os.path.expanduser(f"~/.config/levara-service-keys/{target_name}.jwt")
    if os.path.exists(path):
        with open(path) as f:
            return f.read().strip()
    raise SystemExit(
        f"no JWT for target {target_name}: set {env_key} or place "
        f"~/.config/levara-service-keys/{target_name}.jwt"
    )


def make_target_from_argv(default_url: str, target_name: str) -> Target:
    base_url = os.environ.get("LEVARA_URL", default_url)
    token = load_token_from_env_or_file(target_name)
    t = Target(name=target_name, base_url=base_url, token=token)
    preflight(t)
    return t


def stderr(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)

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


def preflight(target: Target, *, require_rerank: bool = True) -> dict[str, Any]:
    """Verify auth + collect server-side capabilities.

    Populates target.embed_model / rerank_endpoint / rerank_enabled
    from /api/v1/models/rerank and /api/v1/info. Raises if rerank
    isn't configured and require_rerank is True — every calibration
    profile needs it. Pass require_rerank=False for smoke tests
    against dev environments without a configured reranker.
    """
    info = http_request("GET", target.url("/api/v1/info"), headers=target.headers())
    rerank_info: dict[str, Any] = {}
    try:
        rerank_info = http_request(
            "GET", target.url("/api/v1/models/rerank"), headers=target.headers()
        )
    except HttpError as e:
        if e.status != 404:
            raise
    target.embed_model = (info.get("embedding") or {}).get("model", "") or info.get(
        "embed_model", ""
    )
    target.rerank_endpoint = rerank_info.get("endpoint", "")
    target.rerank_enabled = bool(rerank_info.get("enabled")) or bool(
        target.rerank_endpoint
    )
    if not target.rerank_enabled and require_rerank:
        raise RuntimeError(
            f"target {target.name}: rerank not configured — calibration "
            f"output would be useless (pass --no-rerank for smoke runs)"
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
    poll_timeout_s: float = 600.0,
    poll_interval_s: float = 2.0,
) -> dict[str, Any]:
    """Ingest a batch of pre-chunked text into a named HNSW collection
    via /api/v1/cognify with skip_graph=true (RAG mode).

    This is the only canonical write path on b4fface: /api/v1/add
    routes into dataset ingest and never touches the HNSW collection
    store. Cognify with skip_graph runs only chunk → embed → HNSW
    insert (+ BM25), with no LLM entity extraction or graph writes —
    deterministic and clean for calibration corpora.

    Note: server-side chunking may further split each `text` if it
    exceeds MaxChunkChars (~2KB). For the load-profile corpora we keep
    chunks under that boundary so the index size matches doc count.

    The room/tags params are kept for API symmetry but cognify HTTP
    doesn't propagate them per-doc; per-record metadata in `docs[i]`
    is also unused by cognify. Filtered-search profiles (P5) rely on
    the corpus-side room embedding in the text itself, not on
    metadata tags. We log a warning if room/tags are provided here.
    """
    if room or tags:
        stderr(
            "[add_texts] note: room/tags not propagated to cognify; "
            "rely on per-text content for filter semantics"
        )
    texts = [d["text"] for d in docs]
    body: dict[str, Any] = {
        "texts": texts,
        "collection": collection,
        "skip_graph": True,
        "runInBackground": True,
    }
    resp = http_request(
        "POST", target.url("/api/v1/cognify"), headers=target.headers(), body=body
    )
    run_id = resp.get("pipeline_run_id") or resp.get("run_id")
    if not run_id:
        # already_processed or other terminal-on-arrival case
        return resp
    deadline = time.monotonic() + poll_timeout_s
    last_stage = ""
    while time.monotonic() < deadline:
        status = http_request(
            "GET",
            target.url(f"/api/v1/cognify/{run_id}/status"),
            headers=target.headers(),
        )
        state = (status.get("status") or "").upper()
        stage = status.get("stage") or ""
        if stage and stage != last_stage:
            stderr(f"[cognify] {collection} run={run_id[:8]} stage={stage}")
            last_stage = stage
        if state in ("COMPLETED", "SUCCESS", "DONE"):
            return {"status": "ok", "run_id": run_id, "chunks": len(texts), "result": status}
        if state in ("FAILED", "ERROR"):
            raise RuntimeError(f"cognify run {run_id} failed: {status}")
        time.sleep(poll_interval_s)
    raise TimeoutError(f"cognify run {run_id} did not complete in {poll_timeout_s}s")


def _embed_local(query: str) -> list[float]:
    """Embed `query` via the URL in LEVARA_PRE_EMBED_URL (Ollama or
    OpenAI-compatible POST /v1/embeddings). The model used is
    LEVARA_PRE_EMBED_MODEL (default 'nomic-embed-text-v2-moe').

    Bypass for the Pi de809b71 binary regression where server-side
    /api/v1/search/text serialises a nil result slice as literal JSON
    `null` when the in-process embed step silently errors. See
    docs/reviews/pi-search-text-null.md (memory).
    """
    url = os.environ["LEVARA_PRE_EMBED_URL"]
    model = os.environ.get("LEVARA_PRE_EMBED_MODEL", "nomic-embed-text-v2-moe")
    resp = http_request(
        "POST",
        url,
        headers={"Content-Type": "application/json"},
        body={"input": [query], "model": model},
    )
    data = resp.get("data")
    if isinstance(data, list) and data and isinstance(data[0], dict):
        return [float(x) for x in data[0]["embedding"]]
    embs = resp.get("embeddings")
    if isinstance(embs, list) and embs:
        return [float(x) for x in embs[0]]
    raise RuntimeError(f"embed response shape unrecognised: {list(resp.keys())[:5]}")


def _rerank_local(query: str, documents: list[str], target: Target) -> list[dict[str, Any]]:
    """Score (query, document) pairs against the rerank sidecar.
    Returns the sidecar response shape: [{index, relevance_score}, ...]
    sorted by score descending."""
    resp = http_request(
        "POST",
        target.rerank_endpoint,
        headers={"Content-Type": "application/json"},
        body={"query": query, "documents": documents},
    )
    results = resp.get("results") or []
    return sorted(results, key=lambda r: r.get("relevance_score", 0.0), reverse=True)


def _result_text(hit: dict[str, Any]) -> str:
    meta = hit.get("metadata")
    if isinstance(meta, dict):
        t = meta.get("text")
        if isinstance(t, str):
            return t
    return ""


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

    Two transport modes:
    - Default: server-side /api/v1/search/text with rerank flag. Works
      on .64 (main branch binary) — Levara embeds query, runs vector
      search, optionally calls rerank sidecar, returns sorted hits.
    - LEVARA_PRE_EMBED_URL set: client-side. Embed locally via Ollama,
      POST /api/v1/search with the vector (legacy handler, never
      reranks), then call the rerank sidecar from this process for
      the with_rerank pair. Needed on Pi (de809b71) where server-side
      /search/text returns literal JSON `null`.

    Returns a dict shaped:
        {
          "with_rerank": {<server response>, latency_ms: ...},
          "no_rerank":   {<server response>, latency_ms: ...},
        }
    """
    if os.environ.get("LEVARA_PRE_EMBED_URL"):
        return _search_pair_pre_embed(
            target, collection, query, top_k=top_k, room=room, tags=tags
        )
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
        # /search/text is the canonical text-search route on b4fface;
        # /search is shadowed by the legacy vector-only handler that
        # demands a pre-computed embedding vector.
        resp = http_request(
            "POST",
            target.url("/api/v1/search/text"),
            headers=target.headers(),
            body=body,
        )
        out[label] = {
            "latency_ms": int((time.monotonic() - t0) * 1000),
            "response": resp,
        }
    return out


def _search_pair_pre_embed(
    target: Target,
    collection: str,
    query: str,
    *,
    top_k: int,
    room: str | None,
    tags: list[str] | None,
) -> dict[str, Any]:
    """Client-side equivalent of search_pair for the Pi /search/text
    bypass. Overfetches 3× top_k for vector candidates, reranks
    locally via the sidecar, then projects both views back into the
    {results: [...]} envelope the analyzer expects."""
    overfetch = max(top_k * 3, 30)
    t_no_r0 = time.monotonic()
    vector = _embed_local(query)
    body = {"collection": collection, "vector": vector, "top_k": overfetch}
    raw = http_request(
        "POST", target.url("/api/v1/search"), headers=target.headers(), body=body
    )
    no_r_hits = raw.get("results") if isinstance(raw, dict) else None
    if not isinstance(no_r_hits, list):
        no_r_hits = []
    no_r_top = no_r_hits[:top_k]
    no_r_latency = int((time.monotonic() - t_no_r0) * 1000)

    t_r0 = time.monotonic()
    with_r_hits: list[dict[str, Any]] = no_r_top
    if no_r_hits and target.rerank_endpoint:
        docs = [_result_text(h) for h in no_r_hits]
        # Drop empty-text candidates — rerank sidecar would score them
        # against the query meaninglessly. The original chunksSearch
        # path passes the text from metadata.text, and skips silently
        # when extractText returns "" (see ExtractText in pipeline).
        idx_keep = [i for i, t in enumerate(docs) if t]
        if idx_keep:
            kept_docs = [docs[i] for i in idx_keep]
            scored = _rerank_local(query, kept_docs, target)
            ordered: list[dict[str, Any]] = []
            for s in scored:
                local_idx = s.get("index")
                if not isinstance(local_idx, int) or local_idx >= len(idx_keep):
                    continue
                hit = dict(no_r_hits[idx_keep[local_idx]])
                hit["score"] = float(s.get("relevance_score", 0.0))
                hit["reranked"] = True
                ordered.append(hit)
            with_r_hits = ordered[:top_k]
    with_r_latency = int((time.monotonic() - t_r0) * 1000)

    return {
        "no_rerank": {
            "latency_ms": no_r_latency,
            "response": {"results": no_r_top},
        },
        "with_rerank": {
            "latency_ms": no_r_latency + with_r_latency,
            "response": {"results": with_r_hits},
        },
    }


# ── Score extraction (analyzer-side helpers used inline) ────────────


def extract_score_vector(no_rerank_resp: Any) -> list[float]:
    """Pull the ordered vector-only score list from a `rerank=false`
    /search response.

    Three response shapes seen in the wild:
    - bare JSON array (legacy contract, current default for /search/text
      when include_debug=false; can be literal null on zero hits)
    - {results: [...]} envelope (some strategies)
    - {chunks: [...]} envelope (older alias)
    """
    if no_rerank_resp is None:
        return []
    if isinstance(no_rerank_resp, list):
        items = no_rerank_resp
    elif isinstance(no_rerank_resp, dict):
        items = no_rerank_resp.get("results") or no_rerank_resp.get("chunks") or no_rerank_resp.get("items") or []
    else:
        return []
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


def _hits_from(resp: Any) -> list[dict[str, Any]]:
    if resp is None:
        return []
    if isinstance(resp, list):
        return [h for h in resp if isinstance(h, dict)]
    if isinstance(resp, dict):
        for key in ("results", "chunks", "items"):
            v = resp.get(key)
            if isinstance(v, list):
                return [h for h in v if isinstance(h, dict)]
    return []


def top_changed(with_rerank_resp: Any, no_rerank_resp: Any) -> bool | None:
    """True iff the top-1 id differs between rerank=on and rerank=off.
    None if either response is empty (signal absent → can't compare)."""
    a = _hits_from(with_rerank_resp)
    b = _hits_from(no_rerank_resp)
    if not a or not b:
        return None
    ida = a[0].get("id")
    idb = b[0].get("id")
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
        "n_hits_no_rerank": len(_hits_from(no_r)),
        "n_hits_with_rerank": len(_hits_from(with_r)),
        "top_changed_by_rerank": top_changed(with_r, no_r),
        "rerank_outcome": _outcome_from_response(with_r),
    }
    rec.update(gap_features(scores_vec))
    if extra:
        rec.update(extra)
    return rec


def _outcome_from_response(resp: Any) -> str:
    """Infer rerank outcome from response shape. Server doesn't echo
    the metric outcome name directly, but the per-hit `reranked` flag
    plus the rerank_skip_reason hint (when present) is enough."""
    if isinstance(resp, dict) and "rerank_skip_reason" in resp:
        return str(resp["rerank_skip_reason"])
    hits = _hits_from(resp)
    if not hits:
        return "no_results"
    any_reranked = any(h.get("reranked") for h in hits)
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

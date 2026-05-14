#!/usr/bin/env python3
"""Real-hardware perf gate against the Pi 5 production sidecar.

Drives a Levara instance running on a Pi 5 (default 10.23.0.53) end-to-end
through the ONNX INT8 mmini-L12 rerank sidecar, then asserts latency,
NDCG@10, and outcome distribution against Phase 2 targets.

Gated on LEVARA_PERF_GATE=1 so it never runs by accident in CI; the parent
caller is expected to set this explicitly after a deploy.

Usage:
    LEVARA_PERF_GATE=1 python3 deploy/rerank/eval/perf_gate_pi.py

Knobs (env):
    PI_LEVARA_URL          (default http://10.23.0.53:8080)
    PI_SIDECAR_URL         (default http://10.23.0.53:9100/rerank)
    PERF_N_QUERIES         (default 100)
    PERF_MAX_DOCS          (default 500)
    PERF_P50_MS            (default 1500)
    PERF_P95_MS            (default 3000)
    PERF_NDCG_MIN          (default 0.65)
    PERF_OK_RATIO_MIN      (default 0.95)
    PERF_TOKEN             (optional bearer token for Levara HTTP)
    PERF_COLLECTION        (override; default perf_gate_<unix_ts>)
    PERF_KEEP_COLLECTION   (set to "1" to skip cleanup)

Exit codes:
    0 — all thresholds met
    1 — at least one threshold failed (remediation hints printed)
    2 — gate not enabled (LEVARA_PERF_GATE != "1")
    3 — health check / preflight failed
    4 — usage / fixture / network error before measurement
"""
from __future__ import annotations

import json
import math
import os
import sys
import time
from pathlib import Path
from typing import Any, Iterable, Optional

# Make the corpus_fixture import work both when invoked from repo root and
# when run directly from the eval/ directory.
HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

try:
    import requests  # type: ignore
except ImportError:
    print("[perf_gate] FATAL: `requests` not installed", file=sys.stderr)
    sys.exit(4)

from corpus_fixture import (  # noqa: E402
    flatten_docs,
    load_scidocs,
    make_queries,
    seed_collection,
)


# ---------------------------- helpers ---------------------------- #

def _env(name: str, default: str) -> str:
    v = os.environ.get(name)
    return v if v is not None and v != "" else default


def _env_int(name: str, default: int) -> int:
    try:
        return int(_env(name, str(default)))
    except ValueError:
        return default


def _env_float(name: str, default: float) -> float:
    try:
        return float(_env(name, str(default)))
    except ValueError:
        return default


def _percentile(sorted_vals: list[float], p: float) -> float:
    if not sorted_vals:
        return 0.0
    if len(sorted_vals) == 1:
        return sorted_vals[0]
    k = (len(sorted_vals) - 1) * p
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return sorted_vals[int(k)]
    return sorted_vals[f] + (sorted_vals[c] - sorted_vals[f]) * (k - f)


def _dcg(rels: list[int]) -> float:
    return sum(
        (2 ** r - 1) / math.log2(i + 2) for i, r in enumerate(rels)
    )


def _ndcg_at_k(returned_rels: list[int], n_relevant: int, k: int = 10) -> float:
    """returned_rels: 0/1 relevance for each returned doc in returned order.
    n_relevant: total number of relevant docs available for this query.
    """
    rels_k = returned_rels[:k]
    dcg = _dcg(rels_k)
    ideal_hits = min(n_relevant, k)
    if ideal_hits == 0:
        return 0.0
    idcg = _dcg([1] * ideal_hits)
    if idcg == 0:
        return 0.0
    return dcg / idcg


def _extract_text(item: dict) -> str:
    """Best-effort extraction of the chunk text from a search hit."""
    for key in ("text", "chunk_text", "content"):
        v = item.get(key)
        if isinstance(v, str) and v:
            return v
    meta = item.get("metadata")
    if isinstance(meta, str):
        try:
            meta = json.loads(meta)
        except Exception:
            meta = None
    if isinstance(meta, dict):
        for key in ("text", "chunk_text", "content", "raw_text"):
            v = meta.get(key)
            if isinstance(v, str) and v:
                return v
    return ""


def _is_relevant(chunk_text: str, relevant_texts: list[str]) -> int:
    """1 iff chunk_text overlaps with any relevant doc.

    Chunking means an exact ID match is not possible — we rely on the
    chunk text being a substring of (or containing) a positive doc.
    Short chunks (<32 chars) are too noisy to substring-match safely.
    """
    if not chunk_text or len(chunk_text) < 32:
        return 0
    ct = chunk_text.strip().lower()
    for doc in relevant_texts:
        if not doc:
            continue
        dt = doc.strip().lower()
        # Either direction: chunk inside doc (chunked positive) or doc
        # inside chunk (very short positive embedded in larger chunk).
        if ct in dt or dt in ct:
            return 1
        # Cheap substring intersection on a long shared run (>=64 chars).
        # MTEB scidocs positives are typically full abstracts; chunks
        # will be 200-400 char windows from those. The containment check
        # above usually fires; this is a fallback.
        head = ct[:128]
        if len(head) >= 64 and head in dt:
            return 1
    return 0


# ---------------------------- metrics scrape ---------------------------- #

OUTCOME_LABELS = ("ok", "budget", "error", "disabled", "no_text")


def _scrape_outcomes(base_url: str, token: Optional[str]) -> dict[str, float]:
    """Parse levara_rerank_invocations_total{outcome=...} from /metrics."""
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    try:
        r = requests.get(f"{base_url.rstrip('/')}/metrics",
                         headers=headers, timeout=10)
        r.raise_for_status()
    except Exception as e:
        print(f"[perf_gate] WARN: /metrics scrape failed: {e}", file=sys.stderr)
        return {lbl: 0.0 for lbl in OUTCOME_LABELS}

    out = {lbl: 0.0 for lbl in OUTCOME_LABELS}
    for line in r.text.splitlines():
        if not line or line.startswith("#"):
            continue
        if not line.startswith("levara_rerank_invocations_total"):
            continue
        # levara_rerank_invocations_total{outcome="ok"} 123
        try:
            label_part, val = line.rsplit(" ", 1)
            for lbl in OUTCOME_LABELS:
                if f'outcome="{lbl}"' in label_part:
                    out[lbl] = float(val)
                    break
        except ValueError:
            continue
    return out


def _outcome_delta(before: dict[str, float], after: dict[str, float]) -> dict[str, float]:
    return {lbl: max(0.0, after.get(lbl, 0.0) - before.get(lbl, 0.0))
            for lbl in OUTCOME_LABELS}


# ---------------------------- preflight ---------------------------- #

def _check_levara(base_url: str, token: Optional[str]) -> tuple[bool, str]:
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    try:
        r = requests.get(f"{base_url.rstrip('/')}/health",
                         headers=headers, timeout=10)
        if r.status_code == 200:
            return True, r.text[:200]
        return False, f"status {r.status_code}: {r.text[:200]}"
    except Exception as e:
        return False, f"exception: {e}"


def _check_sidecar(sidecar_url: str) -> tuple[bool, str]:
    """Sidecar exposes GET /health (see deploy/rerank/app.py)."""
    base = sidecar_url
    if base.endswith("/rerank"):
        base = base[: -len("/rerank")]
    base = base.rstrip("/")
    try:
        r = requests.get(f"{base}/health", timeout=10)
        if r.status_code == 200:
            return True, r.text[:200]
    except Exception as e:
        return False, f"/health exception: {e}"
    # Fallback to root (in case future versions move the endpoint)
    try:
        r = requests.get(f"{base}/", timeout=10)
        if r.status_code < 500:
            return True, f"GET / -> {r.status_code}"
        return False, f"GET / -> {r.status_code}: {r.text[:200]}"
    except Exception as e:
        return False, f"sidecar unreachable: {e}"


# ---------------------------- main flow ---------------------------- #

def _print_remediation(failures: list[str], outcomes: dict[str, float]) -> None:
    print("\n[perf_gate] remediation hints:")
    total = sum(outcomes.values()) or 1.0
    if any("p50" in f or "p95" in f for f in failures):
        print(
            "  - latency over budget: check Pi 5 CPU load (top), check "
            "RERANK_BUDGET_MS/RERANK_TIMEOUT_MS on the server, and confirm "
            "sidecar isn't paging (free -m on Pi)."
        )
    if any("ndcg" in f for f in failures):
        print(
            "  - NDCG regressed: run the local Phase 1.5 baseline harness "
            "to see if the model artifact drifted; verify the sidecar's "
            "/health reports the expected MODEL_DIR (mmini-L12-int8)."
        )
    if any("ok_ratio" in f for f in failures):
        no_text = outcomes.get("no_text", 0.0) / total
        budget = outcomes.get("budget", 0.0) / total
        err = outcomes.get("error", 0.0) / total
        if no_text > 0.2:
            print(
                f"  - outcome=no_text is {no_text:.0%}: the search path is "
                "handing rerank chunks without text. Check that cognify "
                "wrote metadata.text and that the rerank caller passes "
                "documents through (api_search.go -> SearchByTextWithRerank)."
            )
        if budget > 0.2:
            print(
                f"  - outcome=budget is {budget:.0%}: the rerank pass is "
                "exceeding RERANK_BUDGET_MS — raise budget or lower "
                "rerank top_n. Check sidecar latency on /health."
            )
        if err > 0.05:
            print(
                f"  - outcome=error is {err:.0%}: inspect Levara logs for "
                "rerank HTTP failures; confirm PI_SIDECAR_URL is reachable "
                "from the Levara process (not just from the Mac)."
            )


def main() -> int:
    if os.environ.get("LEVARA_PERF_GATE") != "1":
        print("[perf_gate] LEVARA_PERF_GATE != 1; skipping (exit 2).")
        return 2

    levara_url = _env("PI_LEVARA_URL", "http://10.23.0.53:8080")
    sidecar_url = _env("PI_SIDECAR_URL", "http://10.23.0.53:9100/rerank")
    n_queries = _env_int("PERF_N_QUERIES", 100)
    max_docs = _env_int("PERF_MAX_DOCS", 500)
    p50_max = _env_float("PERF_P50_MS", 1500.0)
    p95_max = _env_float("PERF_P95_MS", 3000.0)
    ndcg_min = _env_float("PERF_NDCG_MIN", 0.65)
    ok_min = _env_float("PERF_OK_RATIO_MIN", 0.95)
    token = os.environ.get("PERF_TOKEN") or None
    collection = _env("PERF_COLLECTION", f"perf_gate_{int(time.time())}")
    keep = os.environ.get("PERF_KEEP_COLLECTION") == "1"

    print(f"[perf_gate] target Levara : {levara_url}")
    print(f"[perf_gate] target sidecar: {sidecar_url}")
    print(f"[perf_gate] collection   : {collection}")
    print(f"[perf_gate] N queries    : {n_queries}")
    print(f"[perf_gate] max docs     : {max_docs}")
    print(
        f"[perf_gate] thresholds   : p50<{p50_max}ms p95<{p95_max}ms "
        f"NDCG@10>{ndcg_min} ok_ratio>{ok_min}"
    )

    # 1. Preflight health.
    ok_l, info_l = _check_levara(levara_url, token)
    print(f"[perf_gate] Levara /health : {'OK' if ok_l else 'FAIL'} ({info_l})")
    ok_s, info_s = _check_sidecar(sidecar_url)
    print(f"[perf_gate] sidecar /health: {'OK' if ok_s else 'FAIL'} ({info_s})")
    if not (ok_l and ok_s):
        print("[perf_gate] preflight failed; aborting before any traffic.")
        return 3

    # 2. Load fixture and pick queries.
    try:
        rows = load_scidocs()
    except FileNotFoundError as e:
        print(f"[perf_gate] FATAL: {e}", file=sys.stderr)
        return 4
    queries = make_queries(rows, n=n_queries)
    if not queries:
        print("[perf_gate] FATAL: no queries with positives in fixture",
              file=sys.stderr)
        return 4
    n_queries = len(queries)
    print(f"[perf_gate] loaded {n_queries} queries from fixture")

    # Build the doc set: positives for chosen queries + a slice of
    # negatives from the same rows, capped at max_docs.
    chosen_qs = {q["query"] for q in queries}
    sub_rows = [r for r in rows if r.get("query") in chosen_qs]
    docs = flatten_docs(sub_rows, max_total=max_docs)
    print(f"[perf_gate] seeding {len(docs)} docs into '{collection}'")

    # Pre-index relevant texts per query for the matching step. We use
    # the raw positive doc texts (not sci-* IDs) since cognify chunks
    # and reassigns IDs.
    relevant_texts_by_query: dict[str, list[str]] = {}
    for r in sub_rows:
        q = r.get("query", "")
        if q in chosen_qs:
            pos = [t for t in (r.get("positive") or []) if t]
            if q not in relevant_texts_by_query:
                relevant_texts_by_query[q] = []
            relevant_texts_by_query[q].extend(pos)

    # 3+4. Seed and wait for cognify.
    try:
        seed_info = seed_collection(
            levara_url, collection, docs, token=token,
        )
    except Exception as e:
        print(f"[perf_gate] FATAL: seed_collection failed: {e}",
              file=sys.stderr)
        return 4
    print(
        f"[perf_gate] seed done: added={seed_info.get('added')} "
        f"run_id={seed_info.get('run_id')} status={seed_info.get('status')}"
    )

    # 5. Record /metrics baseline, then run searches.
    outcomes_before = _scrape_outcomes(levara_url, token)
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    search_url = f"{levara_url.rstrip('/')}/api/v1/search"

    latencies_ms: list[float] = []
    ndcgs: list[float] = []
    http_errors = 0

    wall_t0 = time.perf_counter()
    cleanup_status: str = "skipped"
    try:
        for i, q in enumerate(queries):
            payload = {
                "query_text": q["query"],
                "query_type": "CHUNKS",
                "top_k": 10,
                "collection": collection,
            }
            t0 = time.perf_counter()
            try:
                r = requests.post(search_url, headers=headers,
                                  json=payload, timeout=30)
                dt_ms = (time.perf_counter() - t0) * 1000.0
                latencies_ms.append(dt_ms)
                if r.status_code != 200:
                    http_errors += 1
                    ndcgs.append(0.0)
                    continue
                body = r.json()
            except Exception as e:
                http_errors += 1
                ndcgs.append(0.0)
                if (time.perf_counter() - t0) * 1000.0 > 0:
                    latencies_ms.append((time.perf_counter() - t0) * 1000.0)
                print(f"[perf_gate] WARN: query {i} failed: {e}",
                      file=sys.stderr)
                continue

            items = body if isinstance(body, list) else body.get("items", [])
            rels = []
            rel_texts = relevant_texts_by_query.get(q["query"], [])
            for it in items[:10]:
                if not isinstance(it, dict):
                    rels.append(0)
                    continue
                rels.append(_is_relevant(_extract_text(it), rel_texts))
            ndcgs.append(_ndcg_at_k(rels, len(rel_texts), k=10))

            if (i + 1) % 25 == 0:
                so_far = sorted(latencies_ms)
                print(
                    f"[perf_gate] progress {i+1}/{n_queries} "
                    f"p50={_percentile(so_far, 0.5):.1f}ms "
                    f"NDCG@10={sum(ndcgs)/len(ndcgs):.3f}"
                )
    finally:
        # Best-effort cleanup. Don't fail the gate on cleanup errors.
        if keep:
            cleanup_status = "kept (PERF_KEEP_COLLECTION=1)"
        else:
            try:
                cr = requests.delete(
                    f"{levara_url.rstrip('/')}/api/v1/collections/{collection}",
                    headers=headers, timeout=30,
                )
                cleanup_status = f"DELETE -> {cr.status_code}"
            except Exception as e:
                cleanup_status = f"cleanup error (ignored): {e}"

    wall_ms = (time.perf_counter() - wall_t0) * 1000.0
    outcomes_after = _scrape_outcomes(levara_url, token)
    outcomes = _outcome_delta(outcomes_before, outcomes_after)

    # 6+7+8. Summary.
    sorted_lat = sorted(latencies_ms)
    p50 = _percentile(sorted_lat, 0.5)
    p95 = _percentile(sorted_lat, 0.95)
    p99 = _percentile(sorted_lat, 0.99)
    ndcg_mean = sum(ndcgs) / len(ndcgs) if ndcgs else 0.0
    total_outcomes = sum(outcomes.values())
    ok_ratio = (outcomes["ok"] / total_outcomes) if total_outcomes > 0 else 0.0

    print("\n========== PERF GATE SUMMARY ==========")
    print(f"queries          : {n_queries}")
    print(f"http errors      : {http_errors}")
    print(f"wallclock        : {wall_ms:.0f} ms")
    print(f"p50 latency      : {p50:.1f} ms (threshold < {p50_max})")
    print(f"p95 latency      : {p95:.1f} ms (threshold < {p95_max})")
    print(f"p99 latency      : {p99:.1f} ms")
    print(f"NDCG@10 (mean)   : {ndcg_mean:.4f} (threshold > {ndcg_min})")
    print(f"rerank outcomes  : {outcomes}")
    print(f"ok_ratio         : {ok_ratio:.4f} (threshold > {ok_min})")
    print(f"cleanup          : {cleanup_status}")
    print("======================================\n")

    # 9. Assert thresholds.
    failures: list[str] = []
    if p50 >= p50_max:
        failures.append(f"p50 {p50:.1f}ms >= {p50_max}ms")
    if p95 >= p95_max:
        failures.append(f"p95 {p95:.1f}ms >= {p95_max}ms")
    if ndcg_mean <= ndcg_min:
        failures.append(f"ndcg {ndcg_mean:.4f} <= {ndcg_min}")
    # ok_ratio only meaningful when we actually observed rerank invocations.
    if total_outcomes > 0 and ok_ratio <= ok_min:
        failures.append(f"ok_ratio {ok_ratio:.4f} <= {ok_min}")
    elif total_outcomes == 0:
        print("[perf_gate] WARN: no rerank invocations recorded — "
              "either rerank is disabled on the server or /metrics scrape "
              "missed the counter. Not failing the gate on this alone.")

    if failures:
        print("[perf_gate] FAIL:")
        for f in failures:
            print(f"  - {f}")
        _print_remediation(failures, outcomes)
        return 1

    print("[perf_gate] PASS: all thresholds met.")
    return 0


if __name__ == "__main__":
    sys.exit(main())

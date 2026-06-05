#!/usr/bin/env python3
"""Capture a parity baseline for a Levara collection.

For each query (provided via file, or sampled from the collection's own
chunks), records top_10 IDs + top_1 score + latency. Output JSON is
later fed to parity_check.py against the shadow collection.

Read-only against the target — never writes.

Usage:
    snapshot_baseline.py --target-url http://localhost:8090 \
        --token-file ~/.levara/prod-token --collection foo \
        [--queries-file queries.txt] [--sample-from-chunks 20] \
        --out baselines/foo.json
"""
from __future__ import annotations

import argparse
import json
import os
import random
import re
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from _levara_http import Target, get_meta, http_request, search_text, eprint


def _read_token(token_file: str | None, token_inline: str | None) -> str:
    if token_inline:
        return token_inline
    if token_file:
        p = Path(token_file).expanduser()
        if not p.exists():
            raise SystemExit(f"token file not found: {p}")
        return p.read_text().strip()
    env = os.environ.get("LEVARA_TOKEN")
    if env:
        return env
    return ""


def _sample_queries_from_chunks(target: Target, collection: str, n: int) -> list[str]:
    """Pull N random chunk texts via /api/v1/collections/<c>/data and
    use the first sentence of each as a query. This is a pragmatic
    fallback when the caller didn't supply real production queries —
    it covers the collection's own vocabulary distribution so parity
    measurements aren't dominated by out-of-distribution noise.
    """
    resp = http_request(
        "GET",
        target.url(f"/api/v1/collections/{collection}/data?limit={n * 5}"),
        headers=target.headers(),
    )
    records = resp.get("data") if isinstance(resp, dict) else None
    if not isinstance(records, list) or not records:
        raise SystemExit(
            f"can't sample: /collections/{collection}/data returned no records"
        )
    random.shuffle(records)
    out: list[str] = []
    sent_re = re.compile(r"[^.!?]+[.!?]")
    for rec in records:
        meta = rec.get("metadata") if isinstance(rec, dict) else None
        text = ""
        if isinstance(meta, dict):
            for k in ("text", "content", "name", "description"):
                v = meta.get(k)
                if isinstance(v, str) and v.strip():
                    text = v.strip()
                    break
        if not text:
            continue
        m = sent_re.search(text)
        first = (m.group(0) if m else text)[:200].strip()
        if len(first) >= 10:
            out.append(first)
        if len(out) >= n:
            break
    if not out:
        raise SystemExit("could not extract any usable sentences from chunks")
    return out


def main() -> int:
    p = argparse.ArgumentParser(description="Capture parity baseline for a collection")
    p.add_argument("--target-url", required=True)
    p.add_argument("--token-file", default=None)
    p.add_argument("--token", default=None)
    p.add_argument("--collection", required=True)
    p.add_argument(
        "--queries-file",
        default=None,
        help="one query per line; mutually exclusive with --sample-from-chunks",
    )
    p.add_argument(
        "--sample-from-chunks",
        type=int,
        default=20,
        help="if --queries-file absent, sample this many synthetic queries from chunk text",
    )
    p.add_argument("--top-k", type=int, default=10)
    p.add_argument("--out", required=True)
    args = p.parse_args()

    token = _read_token(args.token_file, args.token)
    target = Target(base_url=args.target_url, token=token)

    meta = get_meta(target, args.collection)
    if not meta or not meta.get("name"):
        raise SystemExit(f"collection {args.collection!r} not found")

    if args.queries_file:
        with open(args.queries_file, encoding="utf-8") as f:
            queries = [ln.strip() for ln in f if ln.strip()]
        if not queries:
            raise SystemExit("queries file is empty")
    else:
        queries = _sample_queries_from_chunks(
            target, args.collection, args.sample_from_chunks
        )
    eprint(f"[baseline] {len(queries)} queries against {args.collection}")

    records = []
    for i, q in enumerate(queries):
        hits, latency_ms = search_text(target, args.collection, q, top_k=args.top_k, rerank=False)
        ids = [h.get("id") for h in hits if isinstance(h, dict) and h.get("id")]
        top1 = hits[0] if hits else {}
        records.append(
            {
                "id": f"q{i}",
                "text": q,
                "top_ids": ids[: args.top_k],
                "top_1_id": top1.get("id"),
                "top_1_score": top1.get("score"),
                "n_hits": len(hits),
                "latency_ms": latency_ms,
                "empty": len(hits) == 0,
            }
        )
        eprint(f"[baseline] q{i}: {len(hits)} hits, top1={top1.get('id')}, {latency_ms}ms")

    out = {
        "collection": args.collection,
        "embedding_model": meta.get("embedding_model"),
        "embedding_dim": meta.get("embedding_dim"),
        "record_count": meta.get("record_count"),
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "top_k": args.top_k,
        "queries": records,
    }
    os.makedirs(os.path.dirname(os.path.abspath(args.out)) or ".", exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2, ensure_ascii=False)
    eprint(f"[baseline] wrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Profile P2 — technical docs with ambiguity.

Target: 10.23.0.64 (heavy host with GPU rerank).

Corpus: ~600 chunks across 12 Kubernetes + Postgres doc pages with
deliberate semantic overlap (pods/replicasets/deployments share
language; WAL/checkpoint/backup all touch durability). The overlap
is the whole point — ambiguous queries with multiple plausible
targets are where the rerank pass actually changes results, and
that's the regime the gate threshold must distinguish from the
clear-winner regime where rerank is wasted budget.

Output: JSONL at $LOADPROFILE_OUT or ./out/p2.jsonl.
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import runner  # noqa: E402
from seed import docs_corpus  # noqa: E402

PROFILE_ID = "p2"
DEFAULT_TARGET_URL = "http://10.23.0.64:8080"
DEFAULT_TARGET_NAME = "rpi64"


def seed_if_needed(target: runner.Target, collection: str) -> dict:
    expected = len(docs_corpus.load_corpus())
    info = {}
    try:
        info = runner.http_request(
            "GET",
            target.url(f"/api/v1/collections/{collection}/meta"),
            headers=target.headers(),
        )
    except runner.HttpError:
        info = {}
    current = int(info.get("record_count", info.get("count", 0))) if isinstance(info, dict) else 0
    if current >= int(expected * 0.95):
        runner.stderr(
            f"[seed] {collection} has {current} chunks (expected {expected}), skipping"
        )
        return {"reused": True, "count": current}

    runner.stderr(f"[seed] ingesting {expected} chunks into {collection}")
    corpus = docs_corpus.load_corpus()
    batch_size = 64
    written = 0
    for i in range(0, len(corpus), batch_size):
        batch = corpus[i : i + batch_size]
        runner.add_texts(
            target,
            collection,
            batch,
            room="docs",
            tags=["loadprofile", "p2", "k8s", "postgres"],
        )
        written += len(batch)
        if written % (batch_size * 4) == 0:
            runner.stderr(f"[seed] {written}/{len(corpus)}")
    return {"reused": False, "count": written}


def run(
    target: runner.Target,
    out_path: str,
    rounds: int,
    sleep_ms: int,
    top_k: int,
) -> None:
    collection = runner.profile_collection(PROFILE_ID)
    runner.assert_namespace(target, PROFILE_ID)
    seed_info = seed_if_needed(target, collection)

    writer = runner.JsonlWriter(out_path)
    sent = 0
    errors = 0
    try:
        for round_idx in range(rounds):
            for q in docs_corpus.QUERIES:
                try:
                    pair = runner.search_pair(
                        target,
                        collection,
                        q["text"],
                        top_k=top_k,
                        query_type="CHUNKS",
                        room="docs",
                    )
                except runner.HttpError as e:
                    errors += 1
                    runner.stderr(f"[search] {q['id']}: {e}")
                    continue
                rec = runner.build_query_record(
                    profile_id=PROFILE_ID,
                    target=target,
                    query_id=f"{q['id']}_r{round_idx}",
                    query_text=q["text"],
                    collection=collection,
                    query_type="CHUNKS",
                    pair=pair,
                    extra={
                        "query_kind": q["kind"],
                        "round": round_idx,
                        "seed_reused": seed_info.get("reused", False),
                        "corpus_fingerprint": docs_corpus.corpus_fingerprint(),
                    },
                )
                writer.write(rec)
                sent += 1
                if sleep_ms > 0:
                    time.sleep(sleep_ms / 1000.0)
            runner.stderr(f"[run] round {round_idx + 1}/{rounds} done ({sent} queries)")
    finally:
        writer.close()
    runner.stderr(
        f"[done] queries={sent} errors={errors} out={out_path} "
        f"dropped={writer.dropped}"
    )


def main() -> int:
    p = argparse.ArgumentParser(description="P2 docs+ambiguous load profile")
    p.add_argument("--rounds", type=int, default=10)
    p.add_argument("--top-k", type=int, default=10)
    p.add_argument("--sleep-ms", type=int, default=250)
    p.add_argument(
        "--out",
        default=os.environ.get(
            "LOADPROFILE_OUT", str(Path("out") / f"{PROFILE_ID}.jsonl")
        ),
    )
    p.add_argument("--target-name", default=DEFAULT_TARGET_NAME)
    p.add_argument("--target-url", default=DEFAULT_TARGET_URL)
    args = p.parse_args()

    target = runner.Target(
        name=args.target_name,
        base_url=args.target_url,
        token=runner.load_token_from_env_or_file(args.target_name),
    )
    runner.preflight(target)
    runner.stderr(
        f"[preflight] target={target.name} embed={target.embed_model} "
        f"rerank={target.rerank_endpoint}"
    )
    run(target, args.out, args.rounds, args.sleep_ms, args.top_k)
    return 0


if __name__ == "__main__":
    sys.exit(main())

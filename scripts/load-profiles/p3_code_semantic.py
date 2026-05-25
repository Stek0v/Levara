#!/usr/bin/env python3
"""Profile P3 — code + prose, semantic split.

Target: 10.23.0.64 (heavy host with GPU rerank).

Corpus: ~400 chunks of real Go/Python/JS source from pinned commits
plus 8 hand-written prose paragraphs describing the same concepts.
The split is intentional — code and prose embed into different
regions of the vector space even when they describe the same idea,
and the gate has to make different cost/benefit calls for each.

Query mix probes that split deliberately:
  - code-precise: token-shaped, should match code via lexical/
    embedding overlap (small gap from neighbours expected — rerank
    has work to do).
  - concept-paraphrase: natural language, prose should win after
    rerank but vector top-1 will often be code.
  - mixed: either kind is a valid answer.
  - adversarial: code-shaped queries for code NOT in corpus —
    expect deceptively high vector top-1 with no real signal.
  - ooc: nothing related — gap should be tiny and rerank is
    wasted budget.

Output: JSONL at $LOADPROFILE_OUT or ./out/p3.jsonl, with
`query_kind` AND `target_kind` so analyzer can cross-tabulate.
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import runner  # noqa: E402
from seed import code_corpus  # noqa: E402

PROFILE_ID = "p3"
DEFAULT_TARGET_URL = "http://10.23.0.64:8080"
DEFAULT_TARGET_NAME = "rpi64"


def seed_if_needed(target: runner.Target, collection: str) -> dict:
    expected = len(code_corpus.load_corpus())
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
    corpus = code_corpus.load_corpus()
    batch_size = 64
    written = 0
    for i in range(0, len(corpus), batch_size):
        batch = corpus[i : i + batch_size]
        runner.add_texts(
            target,
            collection,
            batch,
            room="code",
            tags=["loadprofile", "p3", "code+prose"],
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
            for q in code_corpus.QUERIES:
                try:
                    pair = runner.search_pair(
                        target,
                        collection,
                        q["text"],
                        top_k=top_k,
                        query_type="CHUNKS",
                        room="code",
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
                        "target_kind": q.get("target_kind"),
                        "round": round_idx,
                        "seed_reused": seed_info.get("reused", False),
                        "corpus_fingerprint": code_corpus.corpus_fingerprint(),
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
    p = argparse.ArgumentParser(description="P3 code+prose load profile")
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

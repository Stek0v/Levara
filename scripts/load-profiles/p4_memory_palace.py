#!/usr/bin/env python3
"""Profile P4 — memory palace recall pattern.

Target: 10.23.0.53 (Pi, light host).

Models an agent-style memory workload: ~600 short records bucketed
by (room, hall), queried with a mix of broad/sharp/ambiguous shapes
similar to what real agents send to the mfs MCP. The records are
generated deterministically — no external download, which matters
because the Pi runs the lighter embedding stack (nomic-768) and we
want the test runtime dominated by inference, not by network IO.

Calibration value: short records produce different score
distributions than the long doc/code chunks in P1-P3. The gate
threshold must work across this regime too, and P4 surfaces
exactly that — the JSONL output is sliced by query_kind so the
analyzer can compare gap distributions against P1-P3 on the same
axis.

Output: JSONL at $LOADPROFILE_OUT or ./out/p4.jsonl.
"""

from __future__ import annotations

import argparse
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import runner  # noqa: E402
from seed import memory_palace  # noqa: E402

PROFILE_ID = "p4"
DEFAULT_TARGET_URL = "http://10.23.0.53:8080"
DEFAULT_TARGET_NAME = "pi"


def seed_if_needed(target: runner.Target, collection: str) -> dict:
    expected = len(memory_palace.load_corpus())
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
    corpus = memory_palace.load_corpus()
    # Single-batch ingest. Chunk IDs are uuid5("doc-{i}-{chunkIndex}")
    # where i is the text index within the /cognify call; submitting
    # everything in one call keeps i unique and avoids cross-batch
    # collisions on Levara builds that don't carry the per-run-prefix
    # fix (Pi b4fface).
    runner.add_texts(
        target,
        collection,
        corpus,
        room="memory",
        tags=["loadprofile", "p4", "palace"],
    )
    return {"reused": False, "count": len(corpus)}


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
            for q in memory_palace.QUERIES:
                try:
                    pair = runner.search_pair(
                        target,
                        collection,
                        q["text"],
                        top_k=top_k,
                        query_type="CHUNKS",
                        room="memory",
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
                        "expected_hall": q.get("expected_hall"),
                        "expected_room": q.get("expected_room"),
                        "round": round_idx,
                        "seed_reused": seed_info.get("reused", False),
                        "corpus_fingerprint": memory_palace.corpus_fingerprint(),
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
    p = argparse.ArgumentParser(description="P4 memory-palace load profile")
    p.add_argument("--rounds", type=int, default=15)
    p.add_argument("--top-k", type=int, default=10)
    p.add_argument("--sleep-ms", type=int, default=400)
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

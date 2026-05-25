#!/usr/bin/env python3
"""Profile P5 — filtered search (room / tag pre-filter).

Target: 10.23.0.53 (Pi, light host).

Reuses the deterministic memory-palace corpus from P4 but every
query is paired with a `room` filter that exercises Levara's
overfetch-and-filter HNSW path. Calibration value:

  - The filter shrinks the candidate set the bi-encoder sees, which
    changes the shape of the score distribution feeding the rerank
    gate. The gate threshold tuned on unfiltered traffic may behave
    differently under filters; P5 is what surfaces that.
  - Some queries pair a `room` with mismatched semantic content
    ("auth-room query about deploy") so the bi-encoder will return
    weak matches inside the filter — exactly the case where rerank
    has the most or least to offer.

Output: JSONL at $LOADPROFILE_OUT or ./out/p5.jsonl. Each record
carries `filter_room` so the analyzer can compare gap distributions
filter-on vs filter-off (cross-referenced against P4's same-corpus
output).
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

PROFILE_ID = "p5"
DEFAULT_TARGET_URL = "http://10.23.0.53:8080"
DEFAULT_TARGET_NAME = "pi"


# Filter-aware queries. Each is a (room, query, kind) triple. The
# kinds are:
#   on-topic     — query matches the filter room semantically
#   off-topic    — query is about a different room (filter forces a
#                  weak set; rerank has little to work with)
#   broad-room   — query is general and applies to most records
#                  within the room (close-call rerank regime)
FILTERED_QUERIES: list[dict[str, str]] = [
    {"id": "q01", "room": "auth", "text": "JWT secret rotation", "kind": "on-topic"},
    {"id": "q02", "room": "auth", "text": "service account login flow", "kind": "on-topic"},
    {"id": "q03", "room": "auth", "text": "p99 latency spike", "kind": "off-topic"},
    {"id": "q04", "room": "auth", "text": "recent changes", "kind": "broad-room"},
    {"id": "q05", "room": "deploy", "text": "host migration", "kind": "on-topic"},
    {"id": "q06", "room": "deploy", "text": "config rollback", "kind": "on-topic"},
    {"id": "q07", "room": "deploy", "text": "JWT secret rotation", "kind": "off-topic"},
    {"id": "q08", "room": "deploy", "text": "what happened recently", "kind": "broad-room"},
    {"id": "q09", "room": "rerank", "text": "score gap threshold", "kind": "on-topic"},
    {"id": "q10", "room": "rerank", "text": "GPU memory fragmentation", "kind": "on-topic"},
    {"id": "q11", "room": "rerank", "text": "user preferences for UI", "kind": "off-topic"},
    {"id": "q12", "room": "rerank", "text": "decisions made", "kind": "broad-room"},
    {"id": "q13", "room": "ingest", "text": "WAL fsync batching", "kind": "on-topic"},
    {"id": "q14", "room": "ingest", "text": "embedding dim mismatch", "kind": "on-topic"},
    {"id": "q15", "room": "ingest", "text": "auth tokens", "kind": "off-topic"},
    {"id": "q16", "room": "ingest", "text": "facts about this subsystem", "kind": "broad-room"},
    {"id": "q17", "room": "mcp", "text": "tool group classification", "kind": "on-topic"},
    {"id": "q18", "room": "mcp", "text": "audit log rotation", "kind": "on-topic"},
    {"id": "q19", "room": "graph", "text": "neo4j edge supersession", "kind": "on-topic"},
    {"id": "q20", "room": "graph", "text": "JWT secret", "kind": "off-topic"},
    {"id": "q21", "room": "ui", "text": "dashboard layout", "kind": "on-topic"},
    {"id": "q22", "room": "observability", "text": "p99 grafana board", "kind": "on-topic"},
    {"id": "q23", "room": "observability", "text": "lasagne recipe", "kind": "off-topic"},
    {"id": "q24", "room": "observability", "text": "preferences", "kind": "broad-room"},
]


def seed_if_needed(target: runner.Target, collection: str) -> dict:
    expected = len(memory_palace.load_corpus())
    info = {}
    try:
        info = runner.http_request(
            "GET",
            target.url(f"/api/v1/collections/{collection}/info"),
            headers=target.headers(),
        )
    except runner.HttpError:
        info = {}
    current = int(info.get("count", 0)) if isinstance(info, dict) else 0
    if current >= int(expected * 0.95):
        runner.stderr(
            f"[seed] {collection} has {current} chunks (expected {expected}), skipping"
        )
        return {"reused": True, "count": current}
    runner.stderr(f"[seed] ingesting {expected} chunks into {collection}")
    corpus = memory_palace.load_corpus()
    # Re-tag each record's metadata room into the per-record room so
    # /search room-filter actually selects on per-chunk metadata
    # (not just on the request's room tag). The seeder already
    # encodes room into metadata; we forward it as the chunk's
    # add-time room so it's indexed for filter overfetch.
    batch_size = 64
    written = 0
    # Group corpus by room to send one batch per room, attaching
    # the room tag at request level. This is what makes the
    # downstream `room` filter on /search meaningful for P5.
    by_room: dict[str, list[dict[str, str]]] = {}
    import json as _json
    for rec in corpus:
        meta = _json.loads(rec["metadata"])
        by_room.setdefault(meta["room"], []).append(rec)
    for room_name, recs in by_room.items():
        for i in range(0, len(recs), batch_size):
            batch = recs[i : i + batch_size]
            runner.add_texts(
                target,
                collection,
                batch,
                room=room_name,
                tags=["loadprofile", "p5", "palace", room_name],
            )
            written += len(batch)
        runner.stderr(f"[seed] room={room_name}: {len(recs)} records")
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
            for q in FILTERED_QUERIES:
                try:
                    pair = runner.search_pair(
                        target,
                        collection,
                        q["text"],
                        top_k=top_k,
                        query_type="CHUNKS",
                        room=q["room"],
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
                        "filter_room": q["room"],
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
    p = argparse.ArgumentParser(description="P5 filtered-search load profile")
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

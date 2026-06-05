#!/usr/bin/env python3
"""Rebuild missing memory vectors from the SQL `memories` table.

Repairs the SQL-vs-vector divergence (findings P1.4 Cause B): memory rows that
exist in the `memories` table but have no vector in their `_memories_<ctx>`
sidecar — so recall's vector pass can never surface them. This re-embeds each
missing row exactly as the live save path does and inserts it under its
canonical SQL id, so the vector overwrites/lands in place (replace-by-id).

Indexing recipe mirrors indexMemoryAsync (tool_save_recall_memory.go):
  vector  = embed(key + " " + value)
  id      = the memory row id (canonical, P1.4 stable id)
  metadata= {key, value, type, collection, memory_id}

The embed sidecar is loopback on the Pi, so RUN THIS ON THE PI:
  ssh pi 'python3 /tmp/rebuild_memory_vectors.py --collection local-net'        # dry-run
  ssh pi 'python3 /tmp/rebuild_memory_vectors.py --collection local-net --apply'

REST surface (all loopback on the server host):
  GET  /sync/export/memories            → SQL truth (id, key, value, type, collection_name)
  GET  /sync/export/collection/<name>   → vector ids already present
  POST {embed}/v1/embeddings            → vector for key+" "+value
  POST /insert                          → upsert {collection, id, vector, metadata}

By default only rows MISSING a vector are rebuilt; pass --all to re-embed every
row (overwrites existing vectors too).
"""
import argparse
import json
import subprocess
import sys


def curl_json(url, method="GET", body=None, max_time=30):
    args = ["curl", "-s", "--max-time", str(max_time), "-X", method, url]
    if body is not None:
        args += ["-H", "Content-Type: application/json", "-d", json.dumps(body)]
    out = subprocess.run(args, capture_output=True, text=True, timeout=max_time + 5)
    if out.returncode != 0:
        raise RuntimeError(f"{method} {url} failed: {out.stderr.strip()}")
    txt = out.stdout.strip()
    return json.loads(txt) if txt else None


def memory_collection_name(coll):
    # mirrors memoryCollectionName(): '' -> _memories, else _memories_<coll>
    return "_memories" if coll == "" else f"_memories_{coll}"


def embed_one(embed_url, model, text, max_time=20):
    resp = curl_json(embed_url, method="POST",
                     body={"model": model, "input": text}, max_time=max_time)
    return resp["data"][0]["embedding"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8090/api/v1",
                    help="Levara REST base (loopback on the server host)")
    ap.add_argument("--embed", default="http://127.0.0.1:9101/v1/embeddings",
                    help="embedding sidecar endpoint (loopback)")
    ap.add_argument("--model", default="potion-code-16M")
    ap.add_argument("--collection", required=True,
                    help="logical memory context (collection_name), e.g. local-net")
    ap.add_argument("--all", action="store_true",
                    help="re-embed every row, not just rows missing a vector")
    ap.add_argument("--apply", action="store_true",
                    help="actually insert (default: dry-run)")
    args = ap.parse_args()
    base = args.base.rstrip("/")
    sidecar = memory_collection_name(args.collection)

    rows = curl_json(f"{base}/sync/export/memories") or []
    sql = [r for r in rows if isinstance(r, dict)
           and r.get("collection_name") == args.collection]

    coll = curl_json(f"{base}/sync/export/collection/{sidecar}") or {}
    have = {rec.get("id") for rec in (coll.get("records") or []) if isinstance(rec, dict)}

    missing = [r for r in sql if args.all or r.get("id") not in have]
    print(f"context={args.collection!r} sidecar={sidecar}")
    print(f"  SQL rows: {len(sql)}  vectors present: {len(have)}  "
          f"to rebuild: {len(missing)}{' (--all)' if args.all else ''}")
    if not missing:
        print("  nothing to do.")
        return 0

    done = failed = 0
    for r in missing:
        mid = r.get("id")
        key = r.get("key", "") or ""
        value = r.get("value", "") or ""
        mtype = r.get("type", "") or ""
        text = f"{key} {value}".strip()
        preview = value[:60].replace("\n", " ")
        if not args.apply:
            tag = "rebuild" if mid not in have else "overwrite"
            print(f"  [{tag}] {mid}  {preview}")
            continue
        try:
            vec = embed_one(args.embed, args.model, text)
        except Exception as e:
            print(f"  embed FAIL {mid}: {e}")
            failed += 1
            continue
        meta = {"key": key, "value": value, "type": mtype,
                "collection": args.collection, "memory_id": mid}
        try:
            curl_json(f"{base}/insert", method="POST",
                      body={"collection": sidecar, "id": mid,
                            "vector": vec, "metadata": meta})
            done += 1
        except Exception as e:
            print(f"  insert FAIL {mid}: {e}")
            failed += 1

    if args.apply:
        print(f"  inserted: {done}  failed: {failed}")
    else:
        print("  dry-run — re-run with --apply to insert")
    return 0


if __name__ == "__main__":
    sys.exit(main())

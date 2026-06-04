#!/usr/bin/env python3
"""P1.4 orphan-vector GC: delete vectors whose id is not a live memories row.

Root cause (fixed in pkg/mcp/tool_save_recall_memory.go): ToolSaveMemory used
to mint a fresh uuid per save and index the vector under it, so every re-save
of an existing key left a stale vector in HNSW (vectors > SQL rows). The code
fix makes future saves overwrite in place; this script cleans up the orphans
that already accumulated.

Read-only by default — prints the orphan table and exits. Pass --apply to issue
the deletes (destructive, prod data: take a snapshot first). Uses the owner-blind
REST surface, so no JWT/owner filtering hides rows:

  GET  /sync/export/memories                 → SQL truth (id, collection_name)
  GET  /collections                          → physical _memories_* collections
  GET  /sync/export/collection/<name>        → vector ids per collection
  DELETE /collections/<name>/records/<id>    → per-vector delete (the GC primitive)

Usage:
  python3 scripts/orphan_gc.py                       # dry-run, local :8081
  python3 scripts/orphan_gc.py --base http://10.23.0.53:8090/api/v1
  python3 scripts/orphan_gc.py --apply               # actually delete
"""
import argparse
import json
import subprocess
import sys


def curl_json(url, method="GET", max_time=30):
    args = ["curl", "-s", "--max-time", str(max_time), "-X", method, url]
    out = subprocess.run(args, capture_output=True, text=True, timeout=max_time + 5)
    if out.returncode != 0:
        raise RuntimeError(f"{method} {url} failed: {out.stderr.strip()}")
    body = out.stdout.strip()
    if not body:
        return None
    return json.loads(body)


def curl_status(url, method="DELETE", max_time=30):
    args = ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
            "--max-time", str(max_time), "-X", method, url]
    out = subprocess.run(args, capture_output=True, text=True, timeout=max_time + 5)
    return out.stdout.strip()


def vector_collection(collection_name):
    """Mirror memoryCollectionName: '' → _memories, x → _memories_x."""
    return "_memories" if collection_name == "" else "_memories_" + collection_name


def logical_name(vec_collection):
    """Inverse: _memories → '', _memories_x → x."""
    if vec_collection == "_memories":
        return ""
    return vec_collection[len("_memories_"):]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8081/api/v1",
                    help="Levara REST base (default local :8081)")
    ap.add_argument("--apply", action="store_true",
                    help="actually issue DELETEs (default: dry-run)")
    args = ap.parse_args()
    base = args.base.rstrip("/")

    # 1. SQL truth: collection_name -> set(ids), plus a global id set.
    mems = curl_json(f"{base}/sync/export/memories") or []
    if isinstance(mems, dict):  # some builds wrap in {"memories": [...]}
        mems = mems.get("memories") or mems.get("records") or []
    sql_ids_by_coll = {}
    sql_ids_global = set()
    for m in mems:
        cid = m.get("id", "")
        coll = m.get("collection_name", "")
        sql_ids_by_coll.setdefault(coll, set()).add(cid)
        sql_ids_global.add(cid)
    print(f"SQL memories rows: {len(sql_ids_global)} across {len(sql_ids_by_coll)} collections")

    # 2. Physical memory vector collections.
    colls = curl_json(f"{base}/collections") or []
    names = []
    for c in colls:
        n = c.get("name") if isinstance(c, dict) else c
        if n and (n == "_memories" or n.startswith("_memories_")):
            names.append(n)
    names.sort()

    total_orphans = 0
    total_deleted = 0
    print(f"\n{'collection':40} {'vectors':>8} {'orphans':>8}")
    print("-" * 60)
    for vc in names:
        coll = logical_name(vc)
        live = sql_ids_by_coll.get(coll, set())
        try:
            export = curl_json(f"{base}/sync/export/collection/{vc}")
        except Exception as e:
            print(f"{vc:40} {'ERR':>8}  {e}")
            continue
        records = (export or {}).get("records", []) if isinstance(export, dict) else []
        orphans = []
        for r in records:
            vid = r.get("id", "")
            # Orphan: vector id is not a live memories row for this collection.
            # Fall back to the global set so a misattributed collection_name
            # never causes a false-positive delete.
            if vid not in live and vid not in sql_ids_global:
                orphans.append(vid)
        total_orphans += len(orphans)
        print(f"{vc:40} {len(records):>8} {len(orphans):>8}")
        if args.apply:
            for vid in orphans:
                status = curl_status(f"{base}/collections/{vc}/records/{vid}")
                if status == "204":
                    total_deleted += 1
                else:
                    print(f"    delete {vid} -> HTTP {status}")

    print("-" * 60)
    print(f"total orphans: {total_orphans}")
    if args.apply:
        print(f"deleted: {total_deleted}")
    else:
        print("dry-run — re-run with --apply to delete (snapshot prod first)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""P3.1 junk test-collection GC: drop leftover `test_*` collections.

Test runs (pytest, BEIR, ad-hoc smoke checks) create throwaway collections that
are never cleaned up — prod accumulated ~51 `test_*` collections. This applies a
simple TTL/name cleanup policy:

  KEEP unless ALL of:
    * name matches the junk pattern (default ^test[_-], case-insensitive), AND
    * the collection has not been updated within --ttl-days (default 7), AND
    * it is NOT an internal memory sidecar (_memories*) — never touched.

Read-only by default — prints the candidate table and exits. Pass --apply to
issue the deletes (destructive, prod data: take a snapshot first). Uses the REST
surface:

  GET    /collections           → name, record_count, created_at, updated_at
  DELETE /collections/<name>     → drop one collection (204)

Usage:
  python3 scripts/test_collections_gc.py                          # dry-run, local :8081
  python3 scripts/test_collections_gc.py --base http://10.23.0.53:8090/api/v1
  python3 scripts/test_collections_gc.py --ttl-days 0             # ignore age, pure name match
  python3 scripts/test_collections_gc.py --pattern '^(test|tmp)[_-]'
  python3 scripts/test_collections_gc.py --apply                  # actually delete
"""
import argparse
import json
import re
import subprocess
import sys
from datetime import datetime, timezone


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


def parse_ts(value):
    """Parse an RFC3339 timestamp; return None when absent/unparseable."""
    if not value:
        return None
    try:
        # Python's fromisoformat handles the "...Z" suffix from Go 3.11+, but be
        # defensive for older runtimes by normalising the trailing Z.
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


def age_days(ts, now):
    if ts is None:
        return None
    return (now - ts).total_seconds() / 86400.0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://localhost:8081/api/v1",
                    help="Levara REST base (default local :8081)")
    ap.add_argument("--pattern", default=r"^test[_-]",
                    help="junk-name regex, case-insensitive (default ^test[_-])")
    ap.add_argument("--ttl-days", type=float, default=7.0,
                    help="only drop collections idle at least this long; 0 = ignore age")
    ap.add_argument("--apply", action="store_true",
                    help="actually issue DELETEs (default: dry-run)")
    args = ap.parse_args()
    base = args.base.rstrip("/")
    junk = re.compile(args.pattern, re.IGNORECASE)
    now = datetime.now(timezone.utc)

    colls = curl_json(f"{base}/collections") or []
    candidates, kept_recent, kept_nonmatch = [], 0, 0
    for c in colls:
        if not isinstance(c, dict):
            c = {"name": c}
        name = c.get("name", "")
        if not name or name == "_memories" or name.startswith("_memories"):
            continue  # internal sidecars are never GC targets
        if not junk.search(name):
            kept_nonmatch += 1
            continue
        updated = c.get("updated_at") or c.get("created_at")
        age = age_days(parse_ts(updated), now)
        # Age guard: skip collections updated within the TTL window. Unknown age
        # (no timestamp) is treated as "old enough" only when ttl is disabled.
        if args.ttl_days > 0:
            if age is None or age < args.ttl_days:
                kept_recent += 1
                continue
        candidates.append({
            "name": name,
            "records": c.get("record_count", 0),
            "updated": updated or "?",
            "age_days": age,
        })

    candidates.sort(key=lambda x: x["name"])
    print(f"scanned {len(colls)} collections; "
          f"{len(candidates)} junk candidate(s), "
          f"{kept_recent} kept (too recent), {kept_nonmatch} kept (name not junk)")
    print(f"\n{'collection':45} {'records':>8} {'age_days':>9}  updated")
    print("-" * 80)
    deleted = 0
    for cand in candidates:
        age = cand["age_days"]
        age_s = f"{age:.1f}" if age is not None else "?"
        print(f"{cand['name']:45} {cand['records']:>8} {age_s:>9}  {cand['updated']}")
        if args.apply:
            status = curl_status(f"{base}/collections/{cand['name']}")
            if status == "204":
                deleted += 1
            else:
                print(f"    delete {cand['name']} -> HTTP {status}")

    print("-" * 80)
    print(f"total junk candidates: {len(candidates)}")
    if args.apply:
        print(f"deleted: {deleted}")
    else:
        print("dry-run — re-run with --apply to delete (snapshot prod first)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

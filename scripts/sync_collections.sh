#!/bin/bash
# sync_collections.sh — sync Levara vector collections between two instances.
#
# Collections re-embed on the receiver, so import is ASYNCHRONOUS: the script
# POSTs each collection's export, gets a run_id, then polls
#   GET /sync/import/collection/<run_id>/status
# until the run reports COMPLETED or FAILED.
#
# Usage:
#   bash scripts/sync_collections.sh [pull|push|both]
#     push  — Mac → Pi
#     pull  — Pi → Mac
#     both  — push then pull (default)
#
# By default it syncs only collections MISSING on the target and skips system
# collections (names starting with "_", which the receiver rebuilds from the
# memories sync). Override via the env vars below.
#
# Env overrides:
#   PI_URL=http://10.23.0.53:8090/api/v1     Pi HTTP API base
#   MAC_URL=http://localhost:8081/api/v1     Mac HTTP API base
#   LEVARA_MAC_KEY=...   X-API-Key for Mac (default: macOS keychain item
#                        `levara-local-api-key`, the same the MCP bridge uses)
#   LEVARA_PI_KEY=...    X-API-Key for Pi (default: empty / no auth)
#   FORCE=1              re-sync collections that already exist on the target
#   INCLUDE_SYSTEM=1     include "_"-prefixed system collections
#   ONLY="a b c"         sync only these named collections (space-separated)
#   BATCH_RECORDS=200    records per import request (guards the body-size limit;
#                        lower it if a document-heavy collection trips HTTP 413)
#   POLL_TIMEOUT=600     seconds to wait for each import run to finish
#
set -uo pipefail

DIR="${1:-both}"
PI_URL="${PI_URL:-http://10.23.0.53:8090/api/v1}"
MAC_URL="${MAC_URL:-http://localhost:8081/api/v1}"
LEVARA_PI_KEY="${LEVARA_PI_KEY:-}"
# Mac key: explicit env wins, otherwise read the macOS keychain item the MCP
# stdio bridge uses. Never echoed — only passed to curl/urllib as a header.
LEVARA_MAC_KEY="${LEVARA_MAC_KEY:-$(security find-generic-password -s levara-local-api-key -w 2>/dev/null || true)}"

FORCE="${FORCE:-0}"
INCLUDE_SYSTEM="${INCLUDE_SYSTEM:-0}"
ONLY="${ONLY:-}"
BATCH_RECORDS="${BATCH_RECORDS:-200}"
POLL_TIMEOUT="${POLL_TIMEOUT:-600}"

sync_dir() {
    local FROM=$1 TO=$2 FROM_KEY=$3 TO_KEY=$4 LABEL=$5
    echo "$LABEL"
    FROM="$FROM" TO="$TO" FROM_KEY="$FROM_KEY" TO_KEY="$TO_KEY" \
    FORCE="$FORCE" INCLUDE_SYSTEM="$INCLUDE_SYSTEM" ONLY="$ONLY" \
    BATCH_RECORDS="$BATCH_RECORDS" POLL_TIMEOUT="$POLL_TIMEOUT" \
    python3 - <<'PYEOF'
import os, sys, json, time, urllib.request, urllib.error, urllib.parse

FROM, TO = os.environ["FROM"], os.environ["TO"]
FROM_KEY, TO_KEY = os.environ.get("FROM_KEY", ""), os.environ.get("TO_KEY", "")
FORCE = os.environ.get("FORCE", "0") == "1"
INCLUDE_SYSTEM = os.environ.get("INCLUDE_SYSTEM", "0") == "1"
ONLY = [s for s in os.environ.get("ONLY", "").split() if s]
BATCH = max(1, int(os.environ.get("BATCH_RECORDS", "200")))
POLL_TIMEOUT = int(os.environ.get("POLL_TIMEOUT", "600"))


def req(method, url, key, body=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"} if data is not None else {}
    if key:
        headers["X-API-Key"] = key
    r = urllib.request.Request(url, data=data, headers=headers, method=method)
    with urllib.request.urlopen(r, timeout=180) as resp:
        return json.load(resp)


# 1. Source + target collection inventories from the manifests.
try:
    src = req("GET", f"{FROM}/sync/manifest", FROM_KEY)
    dst = req("GET", f"{TO}/sync/manifest", TO_KEY)
except urllib.error.HTTPError as ex:
    print(f"  manifest failed: HTTP {ex.code} {ex.reason} (auth?)")
    sys.exit(0)
except Exception as ex:
    print(f"  manifest failed: {ex}")
    sys.exit(0)

src_cols = {c["name"]: c.get("records", 0) for c in (src.get("collections") or [])}
dst_cols = {c["name"] for c in (dst.get("collections") or [])}


def wanted(name):
    if ONLY:
        return name in ONLY
    if not INCLUDE_SYSTEM and name.startswith("_"):
        return False
    if not FORCE and name in dst_cols:
        return False
    return True


names = sorted(n for n in src_cols if wanted(n))
if not names:
    print(f"  nothing to sync ({len(src_cols)} on source, all present/filtered)")
    sys.exit(0)
print(f"  {len(names)} collection(s) to sync (of {len(src_cols)} on source)")

ok = empty = failed = 0
for name in names:
    enc = urllib.parse.quote(name, safe="")
    try:
        exp = req("GET", f"{FROM}/sync/export/collection/{enc}", FROM_KEY)
    except Exception as ex:
        print(f"  x {name}: export failed: {ex}")
        failed += 1
        continue
    records = exp.get("records") or []
    if not records:
        print(f"  . {name}: empty, skipped")
        empty += 1
        continue

    processed = failed_recs = 0
    err = None
    # Re-embedding happens on the receiver; chunk records so a big collection
    # never exceeds the import body limit (each chunk is its own async run).
    for i in range(0, len(records), BATCH):
        chunk = dict(exp)
        chunk["records"] = records[i:i + BATCH]
        try:
            started = req("POST", f"{TO}/sync/import/collection", TO_KEY, chunk)
        except urllib.error.HTTPError as ex:
            err = f"HTTP {ex.code} on chunk@{i} (lower BATCH_RECORDS?)"
            break
        except Exception as ex:
            err = str(ex)
            break
        if started.get("status") == "empty":
            continue
        run_id = started.get("run_id")
        if not run_id:
            err = f"no run_id in response: {started}"
            break

        deadline = time.time() + POLL_TIMEOUT
        st = None
        while time.time() < deadline:
            time.sleep(2)
            try:
                st = req("GET", f"{TO}/sync/import/collection/{run_id}/status", TO_KEY)
            except Exception:
                continue
            if st.get("status") in ("COMPLETED", "FAILED"):
                break
        if not st or st.get("status") != "COMPLETED":
            state = st.get("status") if st else "timeout"
            msg = st.get("message", "") if st else ""
            err = f"run {run_id} {state}: {msg}"
            break
        processed += st.get("processed", 0)
        failed_recs += st.get("failed", 0)

    if err:
        print(f"  x {name}: {err}")
        failed += 1
    else:
        extra = f", {failed_recs} failed" if failed_recs else ""
        print(f"  + {name}: {processed} embedded{extra} ({len(records)} src)")
        ok += 1

print(f"  -> {ok} ok, {empty} empty, {failed} failed")
PYEOF
}

case "$DIR" in
    push)
        sync_dir "$MAC_URL" "$PI_URL" "$LEVARA_MAC_KEY" "$LEVARA_PI_KEY" "==> Mac -> Pi"
        ;;
    pull)
        sync_dir "$PI_URL" "$MAC_URL" "$LEVARA_PI_KEY" "$LEVARA_MAC_KEY" "<== Pi -> Mac"
        ;;
    both | *)
        sync_dir "$MAC_URL" "$PI_URL" "$LEVARA_MAC_KEY" "$LEVARA_PI_KEY" "==> Mac -> Pi"
        echo ""
        sync_dir "$PI_URL" "$MAC_URL" "$LEVARA_PI_KEY" "$LEVARA_MAC_KEY" "<== Pi -> Mac"
        ;;
esac

echo ""
echo "Done."

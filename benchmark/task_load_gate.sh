#!/usr/bin/env bash
# B3 load gate: S2 (lease contention, zero double-claims) and S3 (dual-process
# outbox) from benchmark/multi_user.py against a real Postgres-backed server.
# Invoked by the CI job "task runtime load gate (S2/S3)"; also runnable locally.
# Env: LEVARA_TASK_LOAD_DSN, LEVARA_TASK_LOAD_PGUSER (CI provides both).
set -euo pipefail

DIR=$(mktemp -d /tmp/levara-taskload.XXXXXX)
export DIR
PORT=18094
DSN="${LEVARA_TASK_LOAD_DSN:?LEVARA_TASK_LOAD_DSN required}"
PGUSER="${LEVARA_TASK_LOAD_PGUSER:-$(whoami)}"
# psql needs the password out-of-band when the service requires auth.
if [ -z "${PGPASSWORD:-}" ]; then
  PGPASSWORD=$(printf '%s' "$DSN" | sed -nE 's|.*://[^:/@]+:([^@]+)@.*|\1|p')
  export PGPASSWORD
fi

cleanup() {
  # Both load-gate servers — the primary and the S3 dual-process child that
  # multi_user.py spawns from the same $DIR binary — must be dead BEFORE
  # rm runs: a dying server keeps writing logs/WAL into $DIR, and rm -rf
  # loses that race with "Directory not empty" (the 2026-09-27 CI flake
  # where the gate itself passed but the job failed on cleanup).
  pkill -f "$DIR/levara-server" 2>/dev/null || true
  for _ in $(seq 1 50); do
    pgrep -f "$DIR/levara-server" >/dev/null 2>&1 || break
    sleep 0.1
  done
  pkill -9 -f "$DIR/levara-server" 2>/dev/null || true
  rm -rf "$DIR"
}
trap cleanup EXIT

echo "== build =="
go build -o "$DIR/levara-server" ./cmd/server/

DB_NAME=$(printf '%s' "$DSN" | sed -E 's|.*/([^/?]+)\?.*|\1|')
psql "postgres://$PGUSER@localhost:5432/${DB_NAME%%\?*}" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null

echo "== boot server on :$PORT =="
LEVARA_LONG_HORIZON_RUNTIME=1 /"$DIR"/levara-server \
  -profile=standalone-embed -port=$PORT -grpc-port=0 \
  -data-dir="$DIR/data" -node-id=taskload -dim=256 \
  -pg-url="$DSN" > "$DIR/server.log" 2>&1 &
for i in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://127.0.0.1:$PORT/health" >/dev/null || { echo "server never became healthy"; tail -20 "$DIR/server.log"; exit 1; }
echo "-- server boot lines:"
grep -iE "postgres|sql schema|schema migration|pool" "$DIR/server.log" || echo "(no sql lines)"

echo "== S2 lease contention =="
python3 benchmark/multi_user.py --scenario s2 --url "http://127.0.0.1:$PORT" --output "$DIR/s2.json"

echo "== S3 dual-process outbox =="
python3 benchmark/multi_user.py --scenario s3 --url "http://127.0.0.1:$PORT" \
  --server-binary "$DIR/levara-server" --data-dir-b "$DIR/data-b" \
  --pg-dsn "$DSN" --output "$DIR/s3.json"

python3 - "$DIR/s2.json" "$DIR/s3.json" <<'EOF'
import json, sys
ok = True
for p in sys.argv[1:]:
    r = json.load(open(p))
    for s in r["scenarios"]:
        name = s.get("scenario", "?")
        print(f"{name}: pass={s.get('pass')} {s.get('error','')[:200]}")
        ok = ok and s.get("pass", False)
if not ok:
    import os
    import pathlib
    log = pathlib.Path(os.environ.get("DIR", "/tmp")) / "server.log"
    print("--- server.log tail ---")
    print(log.read_text(errors="replace")[-1500:])
sys.exit(0 if ok else 1)
EOF
echo "TASK_LOAD_GATE: PASS"

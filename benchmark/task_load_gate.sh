#!/usr/bin/env bash
# B3 load gate: S2 (lease contention, zero double-claims) and S3 (dual-process
# outbox) from benchmark/multi_user.py against a real Postgres-backed server.
# Invoked by the CI job "task runtime load gate (S2/S3)"; also runnable locally.
# Env: LEVARA_TASK_LOAD_DSN, LEVARA_TASK_LOAD_PGUSER (CI provides both).
set -euo pipefail

DIR=$(mktemp -d /tmp/levara-taskload.XXXXXX)
PORT=18094
DSN="${LEVARA_TASK_LOAD_DSN:?LEVARA_TASK_LOAD_DSN required}"
PGUSER="${LEVARA_TASK_LOAD_PGUSER:-$(whoami)}"

cleanup() {
  kill "${SRV:-0}" 2>/dev/null || true
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
SRV=$!
for i in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://127.0.0.1:$PORT/health" >/dev/null || { echo "server never became healthy"; tail -20 "$DIR/server.log"; exit 1; }

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
sys.exit(0 if ok else 1)
EOF
echo "TASK_LOAD_GATE: PASS"

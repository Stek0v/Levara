#!/usr/bin/env bash
# Full memory eval on all configured hosts (Mac + qwen-64 + Pi5).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

echo "==> Local prerequisites: PostgreSQL + embed sidecar"
bash scripts/postgres-dev.sh ensure
bash scripts/start-embed-local.sh ensure

if ! curl -sf http://localhost:8081/health/details | python3 -c \
  "import json,sys; d=json.load(sys.stdin); assert d['services']['embed']['status']=='connected'" 2>/dev/null; then
  echo "==> Restarting local Levara with PG + embed env ..."
  pkill -f "levara-server.*-port=8081" 2>/dev/null || true
  sleep 2
  set -a
  # shellcheck disable=SC1091
  source deploy/profiles/local.postgres.env.example
  # shellcheck disable=SC1091
  source deploy/profiles/local.embed.env.example
  set +a
  nohup "$ROOT/levara-server" -standalone=true -dim=256 -port=8081 -grpc-port=0 \
    -data-dir="$ROOT/data" -node-id=mac1 -require-auth=false \
    >>"$ROOT/data/logs/levara-local.log" 2>&1 &
  sleep 5
fi
curl -sf http://localhost:8081/health/details | python3 -c \
  "import json,sys; s=json.load(sys.stdin)['services']; print('local embed:', s['embed']['status'], 'pg:', s['postgres']['status'])"

echo "==> Remote PostgreSQL check (qwen-64)"
bash scripts/postgres-remote-ensure.sh

echo "==> Running 3-host memory eval"
python3 benchmark/memory_eval/run_memory_eval.py \
  --targets benchmark/memory_eval/targets.json \
  -v

#!/usr/bin/env bash
# Local potion-code-16M embed sidecar on :9101 (matches Pi prod embed-potion).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BENCH_DIR="$ROOT/scripts/load-profiles"
VENV="$BENCH_DIR/.venv-embed"
PORT="${EMBED_PORT:-9101}"
HOST="${EMBED_HOST:-127.0.0.1}"
MODEL="${EMBED_BENCH_MODEL:-potion}"

cmd="${1:-ensure}"

health_ok() {
  curl -sf "http://${HOST}:${PORT}/health" >/dev/null 2>&1
}

install_venv() {
  if [[ -d "$VENV" ]]; then
    return 0
  fi
  echo "Creating embed venv at $VENV ..."
  python3 -m venv "$VENV"
  # Potion uses model2vec only — avoid full torch stack on Mac.
  "$VENV/bin/pip" install -q --upgrade pip
  "$VENV/bin/pip" install -q \
    "model2vec==0.4.0" \
    "fastapi==0.115.0" \
    "uvicorn[standard]==0.32.0" \
    "pydantic==2.9.2" \
    "numpy==1.26.4"
}

start_sidecar() {
  install_venv
  mkdir -p "$ROOT/data/logs"
  cd "$BENCH_DIR"
  nohup env EMBED_BENCH_MODEL="$MODEL" \
    "$VENV/bin/uvicorn" embed_bench.server:app \
    --host "$HOST" --port "$PORT" --workers 1 \
    >>"$ROOT/data/logs/embed-local.log" 2>&1 &
  echo $! >"$ROOT/data/logs/embed-local.pid"
  echo "Started embed sidecar pid=$(cat "$ROOT/data/logs/embed-local.pid")"
}

wait_ready() {
  for _ in $(seq 1 90); do
    if health_ok; then
      curl -sf "http://${HOST}:${PORT}/health"
      echo
      return 0
    fi
    sleep 1
  done
  echo "ERROR: embed sidecar not ready on ${HOST}:${PORT}" >&2
  tail -20 "$ROOT/data/logs/embed-local.log" 2>/dev/null || true
  return 1
}

case "$cmd" in
  ensure|start)
    if health_ok; then
      echo "Embed sidecar already up on ${HOST}:${PORT}"
      curl -sf "http://${HOST}:${PORT}/health"
      echo
      exit 0
    fi
    start_sidecar
    wait_ready
    ;;
  status)
    if health_ok; then
      curl -sf "http://${HOST}:${PORT}/health"
      echo
    else
      echo "Embed sidecar down on ${HOST}:${PORT}"
      exit 1
    fi
    ;;
  stop)
    if [[ -f "$ROOT/data/logs/embed-local.pid" ]]; then
      kill "$(cat "$ROOT/data/logs/embed-local.pid")" 2>/dev/null || true
      rm -f "$ROOT/data/logs/embed-local.pid"
    fi
    pkill -f "uvicorn embed_bench.server:app.*--port ${PORT}" 2>/dev/null || true
    ;;
  *)
    echo "Usage: $0 {ensure|start|status|stop}" >&2
    exit 1
    ;;
esac

#!/usr/bin/env bash
# Start Levara on Mac with PostgreSQL metadata (port 8081).
# Requires: docker, scripts/postgres-dev.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.postgres.local" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/.env.postgres.local"
elif [[ -f "$ROOT/deploy/profiles/local.postgres.env.example" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/deploy/profiles/local.postgres.env.example"
fi

export DB_PROVIDER="${DB_PROVIDER:-postgres}"
export DB_HOST="${DB_HOST:-localhost}"
export DB_PORT="${DB_PORT:-5433}"
export DB_USERNAME="${DB_USERNAME:-levara}"
export DB_PASSWORD="${DB_PASSWORD:-levara}"
export DB_NAME="${DB_NAME:-levara}"

"$ROOT/scripts/postgres-dev.sh" ensure
"$ROOT/scripts/start-embed-local.sh" ensure

if [[ -f "$ROOT/deploy/profiles/local.embed.env.example" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/deploy/profiles/local.embed.env.example"
fi

export EMBEDDING_ENDPOINT="${EMBEDDING_ENDPOINT:-http://127.0.0.1:9101/v1/embeddings}"
export EMBEDDING_MODEL="${EMBEDDING_MODEL:-potion-code-16M}"
export EMBEDDING_DIMENSIONS="${EMBEDDING_DIMENSIONS:-256}"

# Bench/eval hammers /auth/login — default server cap is 10/min/IP (not a DB limit).
export RATE_LIMIT_AUTH_MAX="${RATE_LIMIT_AUTH_MAX:-10000}"
export RATE_LIMIT_AUTH_WINDOW_SECONDS="${RATE_LIMIT_AUTH_WINDOW_SECONDS:-60}"

# Stop previous instance on :8081 if any
if pgrep -f "levara-server.*-port=8081" >/dev/null 2>&1; then
  pkill -f "levara-server.*-port=8081" || true
  sleep 2
fi

exec "$ROOT/levara-server" \
  -standalone=true \
  -dim="${LEVARA_DIM:-256}" \
  -port=8081 \
  -grpc-port=0 \
  -data-dir="$ROOT/data" \
  -node-id=mac1 \
  -require-auth=false

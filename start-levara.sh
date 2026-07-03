#!/usr/bin/env bash
# Start Levara on Mac with PostgreSQL metadata (port 8081).
# Uses native macOS/Homebrew PostgreSQL by default; no Docker required.
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
export DB_HOST="${DB_HOST:-${POSTGRES_HOST:-localhost}}"
export DB_PORT="${DB_PORT:-${POSTGRES_PORT:-5432}}"
export DB_USERNAME="${DB_USERNAME:-${POSTGRES_USER:-$(id -un)}}"
export DB_PASSWORD="${DB_PASSWORD:-${POSTGRES_PASSWORD:-}}"
export DB_NAME="${DB_NAME:-levara}"

if [[ -z "${DATABASE_URL:-}" ]]; then
  if [[ -n "${POSTGRES_DSN:-}" ]]; then
    export DATABASE_URL="$POSTGRES_DSN"
  elif [[ -n "$DB_PASSWORD" ]]; then
    export DATABASE_URL="postgres://${DB_USERNAME}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
  else
    export DATABASE_URL="postgres://${DB_USERNAME}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
  fi
fi

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
  -profile=standalone-embed \
  -dim="${LEVARA_DIM:-256}" \
  -port=8081 \
  -grpc-port=0 \
  -data-dir="$ROOT/data" \
  -node-id=mac1 \
  -require-auth=false \
  -pg-url="$DATABASE_URL" \
  -embed-keepalive-interval="${EMBED_KEEPALIVE_INTERVAL:-5m}"

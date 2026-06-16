#!/usr/bin/env bash
# Ensure local PostgreSQL for Levara dev (container levara-pg on :5433).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.postgres.local" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/.env.postgres.local"
fi

CONTAINER="${LEVARA_PG_CONTAINER:-levara-pg}"
PORT="${POSTGRES_PORT:-5433}"
USER="${POSTGRES_USER:-levara}"
PASS="${POSTGRES_PASSWORD:-levara}"
DB="${POSTGRES_DB:-levara}"

cmd="${1:-ensure}"

wait_ready() {
  echo "Waiting for PostgreSQL on localhost:${PORT}..."
  for _ in $(seq 1 30); do
    if PGPASSWORD="$PASS" pg_isready -h localhost -p "$PORT" -U "$USER" -q 2>/dev/null \
      && PGPASSWORD="$PASS" psql -h localhost -p "$PORT" -U "$USER" -d "$DB" -c 'SELECT 1' -q >/dev/null 2>&1; then
      echo "PostgreSQL ready (db=${DB}, user=${USER}, port=${PORT})"
      return 0
    fi
    sleep 1
  done
  echo "ERROR: PostgreSQL not ready after 30s" >&2
  return 1
}

start_container() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    docker start "$CONTAINER" >/dev/null
    echo "Started existing container: $CONTAINER"
  else
    docker compose -f docker-compose.postgres.yml up -d
    echo "Created container via docker-compose.postgres.yml"
  fi
}

case "$cmd" in
  ensure|start)
    if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
      start_container
    else
      echo "Container $CONTAINER already running"
    fi
    wait_ready
    ;;
  status)
    if docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
      docker ps --filter "name=^${CONTAINER}$" --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
    else
      echo "Container $CONTAINER is not running"
      docker ps -a --filter "name=^${CONTAINER}$" --format 'table {{.Names}}\t{{.Status}}' 2>/dev/null || true
    fi
    pg_isready -h localhost -p "$PORT" -U "$USER" 2>&1 || true
    ;;
  stop)
    docker stop "$CONTAINER" 2>/dev/null || true
    ;;
  *)
    echo "Usage: $0 {ensure|start|status|stop}" >&2
    exit 1
    ;;
esac

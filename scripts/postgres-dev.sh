#!/usr/bin/env bash
# Ensure local PostgreSQL for Levara dev.
#
# Default mode is native macOS/Homebrew PostgreSQL on localhost:5432.
# Docker is still available explicitly:
#
#   LEVARA_POSTGRES_MODE=docker scripts/postgres-dev.sh ensure
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.postgres.local" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/.env.postgres.local"
fi

cmd="${1:-ensure}"
mode="${LEVARA_POSTGRES_MODE:-${POSTGRES_MODE:-native}}"
brew_service="${LEVARA_POSTGRES_BREW_SERVICE:-postgresql@16}"

default_port="5432"
default_user="$(id -un)"
default_pass=""
if [[ "$mode" == "docker" ]]; then
  default_port="5433"
  default_user="levara"
  default_pass="levara"
fi

host="${POSTGRES_HOST:-${DB_HOST:-localhost}}"
port="${POSTGRES_PORT:-${DB_PORT:-$default_port}}"
pg_user="${POSTGRES_USER:-${DB_USERNAME:-$default_user}}"
pg_pass="${POSTGRES_PASSWORD:-${DB_PASSWORD:-$default_pass}}"
db="${POSTGRES_DB:-${DB_NAME:-levara}}"
container="${LEVARA_PG_CONTAINER:-levara-pg}"

require_cmd() {
  local name="$1"
  if ! command -v "$name" >/dev/null 2>&1; then
    echo "ERROR: required command not found: $name" >&2
    return 1
  fi
}

pg_env() {
  PGPASSWORD="$pg_pass" "$@"
}

wait_native_ready() {
  echo "Waiting for native PostgreSQL on ${host}:${port}..."
  for _ in $(seq 1 30); do
    if pg_env pg_isready -h "$host" -p "$port" -U "$pg_user" -q 2>/dev/null; then
      echo "PostgreSQL server ready (host=${host}, port=${port}, user=${pg_user})"
      return 0
    fi
    sleep 1
  done
  echo "ERROR: PostgreSQL not ready on ${host}:${port}" >&2
  return 1
}

ensure_native_db() {
  require_cmd pg_isready
  require_cmd psql
  require_cmd createdb

  if ! pg_env pg_isready -h "$host" -p "$port" -U "$pg_user" -q 2>/dev/null; then
    if command -v brew >/dev/null 2>&1; then
      echo "Starting Homebrew service: ${brew_service}"
      brew services start "$brew_service" >/dev/null
    else
      echo "ERROR: PostgreSQL is down and Homebrew is unavailable." >&2
      return 1
    fi
  fi

  wait_native_ready

  if pg_env psql -h "$host" -p "$port" -U "$pg_user" -d "$db" -c 'SELECT 1' -q >/dev/null 2>&1; then
    echo "Database ready (db=${db})"
    return 0
  fi

  echo "Creating database: ${db}"
  pg_env createdb -h "$host" -p "$port" -U "$pg_user" "$db"
  pg_env psql -h "$host" -p "$port" -U "$pg_user" -d "$db" -c 'SELECT 1' -q >/dev/null
  echo "Database ready (db=${db})"
}

status_native() {
  if command -v brew >/dev/null 2>&1; then
    brew services list 2>/dev/null | awk 'NR == 1 || /postgresql/'
  fi
  pg_env pg_isready -h "$host" -p "$port" -U "$pg_user" 2>&1 || true
  pg_env psql -h "$host" -p "$port" -U "$pg_user" -d "$db" \
    -Atc "select current_database(), current_user, count(*) from information_schema.tables where table_schema='public';" \
    2>/dev/null || true
}

stop_native() {
  if command -v brew >/dev/null 2>&1; then
    brew services stop "$brew_service" >/dev/null
    echo "Stopped Homebrew service: ${brew_service}"
  else
    echo "ERROR: Homebrew is unavailable; cannot stop native PostgreSQL service." >&2
    return 1
  fi
}

wait_docker_ready() {
  echo "Waiting for Docker PostgreSQL on localhost:${port}..."
  for _ in $(seq 1 30); do
    if pg_env pg_isready -h localhost -p "$port" -U "$pg_user" -q 2>/dev/null \
      && pg_env psql -h localhost -p "$port" -U "$pg_user" -d "$db" -c 'SELECT 1' -q >/dev/null 2>&1; then
      echo "PostgreSQL ready (db=${db}, user=${pg_user}, port=${port})"
      return 0
    fi
    sleep 1
  done
  echo "ERROR: Docker PostgreSQL not ready after 30s" >&2
  return 1
}

start_docker_container() {
  require_cmd docker
  if docker ps -a --format '{{.Names}}' | grep -qx "$container"; then
    docker start "$container" >/dev/null
    echo "Started existing container: $container"
  else
    docker compose -f docker-compose.postgres.yml up -d
    echo "Created container via docker-compose.postgres.yml"
  fi
}

ensure_docker() {
  if ! docker ps --format '{{.Names}}' | grep -qx "$container"; then
    start_docker_container
  else
    echo "Container $container already running"
  fi
  wait_docker_ready
}

status_docker() {
  require_cmd docker
  if docker ps --format '{{.Names}}' | grep -qx "$container"; then
    docker ps --filter "name=^${container}$" --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
  else
    echo "Container $container is not running"
    docker ps -a --filter "name=^${container}$" --format 'table {{.Names}}\t{{.Status}}' 2>/dev/null || true
  fi
  pg_env pg_isready -h localhost -p "$port" -U "$pg_user" 2>&1 || true
}

stop_docker() {
  require_cmd docker
  docker stop "$container" 2>/dev/null || true
}

case "$mode:$cmd" in
  native:ensure|native:start)
    ensure_native_db
    ;;
  native:status)
    status_native
    ;;
  native:stop)
    stop_native
    ;;
  docker:ensure|docker:start)
    ensure_docker
    ;;
  docker:status)
    status_docker
    ;;
  docker:stop)
    stop_docker
    ;;
  *)
    echo "Usage: $0 {ensure|start|status|stop}" >&2
    echo "Set LEVARA_POSTGRES_MODE=native|docker (default: native)." >&2
    exit 1
    ;;
esac

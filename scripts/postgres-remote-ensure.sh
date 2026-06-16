#!/usr/bin/env bash
# Ensure PostgreSQL on the Qwen/GPU host (10.23.0.64 by default).
set -euo pipefail

HOST="${LEVARA_DEPLOY_HOST:-10.23.0.64}"
SSH_USER="${LEVARA_SSH_USER:-stek0v}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Remote creds — never inherit Mac local POSTGRES_* from the shell.
if [[ -f "$ROOT/deploy/profiles/remote-qwen.postgres.env.example" ]]; then
  # shellcheck disable=SC1091
  source "$ROOT/deploy/profiles/remote-qwen.postgres.env.example"
fi
CONTAINER="${LEVARA_PG_CONTAINER:-levara-postgres}"
PORT="${POSTGRES_PORT:-5433}"
PGUSER="${POSTGRES_USER:-levara}"
PASS="${POSTGRES_PASSWORD:-testpass}"
DB="${POSTGRES_DB:-levara_test}"

ssh "${SSH_USER}@${HOST}" \
  "CONTAINER=$(printf %q "$CONTAINER") PORT=$(printf %q "$PORT") PGUSER=$(printf %q "$PGUSER") PASS=$(printf %q "$PASS") DB=$(printf %q "$DB") bash -s" <<'REMOTE'
set -euo pipefail

if docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  echo "Container $CONTAINER already running"
elif docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  docker start "$CONTAINER"
  echo "Started $CONTAINER"
else
  echo "Creating $CONTAINER (first-time setup)..."
  docker run -d --name "$CONTAINER" --restart unless-stopped \
    -e "POSTGRES_USER=$PGUSER" \
    -e "POSTGRES_PASSWORD=$PASS" \
    -e "POSTGRES_DB=$DB" \
    -p "${PORT}:5432" \
    -v levara-postgres-data:/var/lib/postgresql/data \
    postgres:16-alpine
fi

for _ in $(seq 1 30); do
  if PGPASSWORD="$PASS" pg_isready -h 127.0.0.1 -p "$PORT" -U "$PGUSER" -q 2>/dev/null \
    && PGPASSWORD="$PASS" psql -h 127.0.0.1 -p "$PORT" -U "$PGUSER" -d "$DB" -c 'SELECT 1' -q >/dev/null 2>&1; then
    echo "PostgreSQL ready on 127.0.0.1:${PORT} (db=${DB})"
    PGPASSWORD="$PASS" psql -h 127.0.0.1 -p "$PORT" -U "$PGUSER" -d "$DB" \
      -c "SELECT count(*) AS memories FROM memories;" 2>/dev/null \
      || echo "(memories table not migrated yet — start levara once)"
    exit 0
  fi
  sleep 1
done
echo "ERROR: PostgreSQL not ready on remote" >&2
exit 1
REMOTE

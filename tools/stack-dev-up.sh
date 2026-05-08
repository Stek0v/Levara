#!/usr/bin/env bash
# stack-dev-up.sh — one-command LevaraOS dev bootstrap.
#
# Idempotent: ensures .env exists, brings up docker-compose.levaraos.yml,
# waits for Levara to report healthy, pulls the embedding model into Ollama
# (if missing), and prints the endpoint summary.
#
# Re-run safely — `docker compose up -d` is a no-op when services already run.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILE="docker-compose.levaraos.yml"
LEVARA_URL="http://localhost:8080"
OLLAMA_URL="http://localhost:11434"
EMBED_MODEL="${EMBEDDING_MODEL:-nomic-embed-text}"
HEALTH_TIMEOUT_SECONDS="${STACK_DEV_HEALTH_TIMEOUT:-180}"

log()  { printf '\033[1;36m[stack-dev]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[stack-dev]\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[1;31m[stack-dev]\033[0m %s\n' "$*" >&2; exit 1; }

# 1. Ensure .env exists. Copy from template on first run.
if [[ ! -f .env ]]; then
  if [[ -f .env.template ]]; then
    log ".env not found — seeding from .env.template (edit later for real keys)"
    cp .env.template .env
  else
    fail ".env and .env.template both missing — cannot configure stack"
  fi
fi

# 2. Bring up the stack.
log "starting compose stack ($COMPOSE_FILE)"
docker compose -f "$COMPOSE_FILE" up -d --build

# 3. Wait for Levara /metrics to respond. /metrics is unauthenticated and
#    matches the compose healthcheck — using it here keeps the wait condition
#    aligned with the container-level definition of "healthy".
log "waiting for Levara at $LEVARA_URL/metrics (timeout ${HEALTH_TIMEOUT_SECONDS}s)"
deadline=$(( $(date +%s) + HEALTH_TIMEOUT_SECONDS ))
until curl -sf "$LEVARA_URL/metrics" > /dev/null 2>&1; do
  if (( $(date +%s) >= deadline )); then
    fail "Levara did not become healthy within ${HEALTH_TIMEOUT_SECONDS}s — check 'docker compose -f $COMPOSE_FILE logs levara'"
  fi
  sleep 2
done
log "Levara healthy"

# 4. Pull embed model into Ollama if missing. Cheap when already present.
if curl -sf "$OLLAMA_URL/api/tags" > /dev/null 2>&1; then
  if ! curl -sf "$OLLAMA_URL/api/tags" | grep -q "\"$EMBED_MODEL"; then
    log "pulling embed model '$EMBED_MODEL' into Ollama"
    curl -sS -X POST "$OLLAMA_URL/api/pull" \
      -H 'Content-Type: application/json' \
      -d "{\"name\":\"$EMBED_MODEL\"}" > /dev/null \
      || warn "embed model pull failed — first cognify request will retry"
  else
    log "embed model '$EMBED_MODEL' already present in Ollama"
  fi
else
  warn "Ollama not reachable at $OLLAMA_URL — embeddings will fail until it is"
fi

# 5. Print endpoint summary.
cat <<EOF

  ╭──────────────────────────────────────────────────────────────╮
  │  LevaraOS dev stack is up                                    │
  │                                                              │
  │   Levara HTTP   →  $LEVARA_URL                       │
  │   Levara gRPC   →  localhost:50051                           │
  │   Ollama        →  $OLLAMA_URL                      │
  │   MemoryFS      →  http://localhost:7777                     │
  │   mem0          →  http://localhost:8888                     │
  │   Prometheus    →  http://localhost:9090                     │
  │                                                              │
  │   Logs:    docker compose -f $COMPOSE_FILE logs -f
  │   Stop:    make stack-dev-down                               │
  ╰──────────────────────────────────────────────────────────────╯

EOF

#!/bin/bash
# deploy-qwen-stack.sh — one-command 3-Qwen stack deployment
#
# Usage:
#   bash scripts/deploy-qwen-stack.sh [gpu-host|pi|all]
#
#   gpu-host  Start Docker containers on current machine (GPU host 10.23.0.64)
#   pi        Deploy memory-levara plugin to Pi via SSH (10.23.0.53)
#   all       Both (gpu-host first, pi second)  ← DEFAULT
#
# Env overrides:
#   GPU_HOST=10.23.0.64   target GPU host IP (for pi→host wiring)
#   PI_HOST=10.23.0.53    target Pi IP
#   PI_USER=stek0v        Pi SSH user
#   JWT_SECRET=...        Levara JWT secret (auto-generated if absent)
#   QWEN_CHAT_MODEL=...   Filename under ./models/ for chat LLM
#   LLM_PORT=11434        Exposed port for qwen-llm (use 8100 if picoclaw expects it)

set -euo pipefail

MODE="${1:-all}"
GPU_HOST="${GPU_HOST:-10.23.0.64}"
PI_HOST="${PI_HOST:-10.23.0.53}"
PI_USER="${PI_USER:-stek0v}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.qwen-stack.yml"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
ok()   { echo -e "${GREEN}✓${NC} $*"; }
warn() { echo -e "${YELLOW}⚠${NC} $*"; }
die()  { echo -e "${RED}✗${NC} $*" >&2; exit 1; }

# ─────────────────────────────────────────────
# gpu-host: bring up Docker Compose stack
# ─────────────────────────────────────────────
deploy_gpu_host() {
  echo "=== GPU host deployment ($GPU_HOST) ==="

  [[ -f "$COMPOSE_FILE" ]] || die "Not found: $COMPOSE_FILE"

  # Models directory
  MODELS_DIR="$REPO_ROOT/models"
  if [[ ! -d "$MODELS_DIR" ]]; then
    warn "models/ directory not found — creating $MODELS_DIR"
    mkdir -p "$MODELS_DIR"
    warn "Download GGUFs before starting:"
    warn "  qwen3-embedding-0.6b-q8_0.gguf"
    warn "  qwen3-reranker-0.6b-q8_0.gguf"
    warn "  \${QWEN_CHAT_MODEL:-qwen2.5-7b-instruct-q4_k_m.gguf}"
    warn "Place them in: $MODELS_DIR"
  fi

  # JWT_SECRET
  if [[ -z "${JWT_SECRET:-}" ]]; then
    if [[ -f "$REPO_ROOT/.env" ]] && grep -q JWT_SECRET "$REPO_ROOT/.env"; then
      # shellcheck disable=SC1090
      source <(grep JWT_SECRET "$REPO_ROOT/.env")
      ok "JWT_SECRET loaded from .env"
    else
      JWT_SECRET=$(openssl rand -hex 32)
      echo "JWT_SECRET=$JWT_SECRET" >> "$REPO_ROOT/.env"
      ok "JWT_SECRET generated and saved to .env"
    fi
  fi
  export JWT_SECRET

  # Bring up
  cd "$REPO_ROOT"
  docker compose -f "$COMPOSE_FILE" down --remove-orphans 2>/dev/null || true
  docker compose -f "$COMPOSE_FILE" up -d --build

  # Health wait
  echo "Waiting for Levara to be healthy..."
  for i in $(seq 1 60); do
    if curl -sf "http://localhost:8080/health" >/dev/null 2>&1; then
      ok "Levara HTTP API ready"
      break
    fi
    if [[ $i -eq 60 ]]; then
      die "Levara did not become healthy after 60s"
    fi
    sleep 2
  done

  echo ""
  echo "GPU stack ready:"
  ok "Levara HTTP:  http://localhost:8080"
  ok "Levara gRPC:  localhost:50051"
  ok "Swagger UI:   http://localhost:8080/swagger/"
  ok "Prometheus:   http://localhost:9090"
  ok "Qwen embed:   http://localhost:9001"
  ok "Qwen rerank:  http://localhost:9003"
  ok "Qwen LLM:     http://localhost:${LLM_PORT:-11434}"
}

# ─────────────────────────────────────────────
# pi: deploy memory-levara plugin to Pi
# ─────────────────────────────────────────────
deploy_pi() {
  echo "=== Pi deployment ($PI_USER@$PI_HOST) ==="

  PLUGIN_SRC="$REPO_ROOT/clawdbot/extensions/memory-levara"
  [[ -d "$PLUGIN_SRC" ]] || die "Plugin source not found: $PLUGIN_SRC"

  # Mint a JWT for the picoclaw service principal.
  JWT_TOKEN=$("$REPO_ROOT/scripts/mint-levara-jwt.sh" "picoclaw")
  ok "Minted Levara JWT for picoclaw"

  # Build, package, and copy plugin to Pi
  if command -v npm >/dev/null 2>&1; then
    (cd "$PLUGIN_SRC" && npm install && npm run build)
  else
    die "npm is required to build memory-levara before deployment"
  fi
  [[ -f "$PLUGIN_SRC/dist/index.js" ]] || die "Plugin build did not produce dist/index.js"

  TARBALL="/tmp/memory-levara-$(date +%s).tgz"
  tar czf "$TARBALL" \
    --exclude "memory-levara/node_modules" \
    -C "$REPO_ROOT/clawdbot/extensions" memory-levara
  scp "$TARBALL" "$PI_USER@$PI_HOST:/tmp/memory-levara.tgz"
  rm -f "$TARBALL"
  ok "Plugin package copied to Pi"

  # Deploy on Pi
  # shellcheck disable=SC2087
  ssh "$PI_USER@$PI_HOST" bash -s -- "$JWT_TOKEN" "$GPU_HOST" "${LLM_PORT:-11434}" << 'EOSSH'
set -euo pipefail
JWT_TOKEN="$1"
GPU_HOST="$2"
LLM_PORT="$3"

PLUGIN_DIR="$HOME/.clawdbot/plugins/memory-levara"
CONFIG_FILE="$HOME/.clawdbot/config.json"

mkdir -p "$PLUGIN_DIR"
tar xzf /tmp/memory-levara.tgz --strip-components=1 -C "$PLUGIN_DIR"

if command -v pnpm &>/dev/null; then
  cd "$PLUGIN_DIR" && pnpm install --prod --no-frozen-lockfile
elif command -v npm &>/dev/null; then
  cd "$PLUGIN_DIR" && npm install --omit=dev
fi

[[ -f "$PLUGIN_DIR/dist/index.js" ]] || { echo "dist/index.js missing after plugin deploy" >&2; exit 1; }

# Patch config.json to use memory-levara plugin
if [[ -f "$CONFIG_FILE" ]]; then
  python3 - "$JWT_TOKEN" "$GPU_HOST" "$LLM_PORT" << 'PYEOF'
import json, sys, os

jwt_token, gpu_host, llm_port = sys.argv[1], sys.argv[2], sys.argv[3]
config_path = os.path.expanduser("~/.clawdbot/config.json")

with open(config_path) as f:
    cfg = json.load(f)

cfg.setdefault("plugins", {})
cfg["plugins"]["memory"] = {
    "kind": "levara",
    "config": {
        "levaraUrl": f"http://{gpu_host}:8080",
        "jwtToken": jwt_token,
        "llmEndpoint": f"http://{gpu_host}:{llm_port}/v1",
        "fallbackFile": os.path.expanduser("~/.clawdbot/outbox.ndjson"),
    }
}

with open(config_path, "w") as f:
    json.dump(cfg, f, indent=2)

print(f"config.json updated: memory plugin → levara @ {gpu_host}:8080")
PYEOF
else
  echo "Warning: $CONFIG_FILE not found — create it manually with the plugin config"
fi

# Smoke test: can we reach Levara from Pi?
if curl -sf "http://$GPU_HOST:8080/health" >/dev/null 2>&1; then
  echo "✓ Levara reachable from Pi ($GPU_HOST:8080)"
else
  echo "⚠ Levara not reachable from Pi yet (may need to wait for GPU stack)"
fi

# Restart PicoClaw if managed by systemd
if systemctl --user is-active picoclaw-gateway &>/dev/null; then
  systemctl --user restart picoclaw-gateway
  echo "✓ PicoClaw gateway restarted"
else
  echo "⚠ picoclaw-gateway not running as user systemd service — restart manually"
fi
EOSSH

  ok "Pi deployment complete"
  ok "PicoClaw memory plugin: memory-levara → Levara @ $GPU_HOST:8080"
}

# ─────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────
case "$MODE" in
  gpu-host) deploy_gpu_host ;;
  pi)       deploy_pi ;;
  all)
    deploy_gpu_host
    echo ""
    echo "Waiting 10s before Pi deployment..."
    sleep 10
    deploy_pi
    ;;
  *) die "Unknown mode '$MODE'. Use: gpu-host | pi | all" ;;
esac

echo ""
ok "Deploy complete. Full stack command: bash scripts/deploy-qwen-stack.sh all"

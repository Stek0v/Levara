#!/usr/bin/env bash
# Deploy current levara-server to 10.23.0.64 and restart memory stack.
# Run from repo root on your Mac.
set -euo pipefail

HOST="${LEVARA_DEPLOY_HOST:-10.23.0.64}"
SSH_USER="${LEVARA_SSH_USER:-stek0v}"
REMOTE_DIR="${LEVARA_REMOTE_DIR:-/home/stek0v/levara-qwen3}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

echo "==> Building levara-server (${GOOS}/${GOARCH})"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "-s -w" -o /tmp/levara-server ./cmd/server/

echo "==> Ensuring PostgreSQL on remote"
bash scripts/postgres-remote-ensure.sh

echo "==> Uploading binary to ${SSH_USER}@${HOST}:${REMOTE_DIR}/"
scp /tmp/levara-server "${SSH_USER}@${HOST}:${REMOTE_DIR}/levara-server.new"

echo "==> Remote: swap binary, migrate schema, restart services"
ssh "${SSH_USER}@${HOST}" bash -s <<EOF
set -euo pipefail
cd "${REMOTE_DIR}"
chmod +x levara-server.new
mv levara-server levara-server.bak.\$(date +%Y%m%d%H%M%S) 2>/dev/null || true
mv levara-server.new levara-server

# Qwen embed + rerank sidecars (see docs/MIGRATION-QWEN3.md)
if [ -f docker-compose.qwen3.yml ]; then
  docker compose -f docker-compose.yml -f docker-compose.qwen3.yml up -d qwen3-embed qwen3-rerank-llm qwen3-rerank-front || true
fi

if sudo -n systemctl restart levara 2>/dev/null; then
  echo "Restarted via systemctl"
elif systemctl --user restart levara 2>/dev/null; then
  echo "Restarted via user systemctl"
else
  echo "WARN: passwordless systemctl unavailable — stopping stray processes and starting one instance"
  pkill -f "${REMOTE_DIR}/levara-server" 2>/dev/null || true
  sleep 2
  nohup ./levara-server -standalone -dim 1024 -port 8080 -grpc-port 50051 -shards 3 -data-dir ./data \\
    -llm-upstream http://127.0.0.1:11434/v1 -llm-proxy-port 0 >> levara.log 2>&1 &
fi
sleep 3
curl -sf "http://127.0.0.1:8080/health" >/dev/null && echo "Levara healthy" || echo "WARN: Levara health check failed"
curl -sf "http://127.0.0.1:9001/v1/models" >/dev/null && echo "Qwen embed healthy" || echo "WARN: embed :9001 still down"
pgrep -af levara-server || true
EOF

echo "==> Done. Re-run memory eval from Mac:"
echo "python3 benchmark/memory_eval/run_memory_eval.py --url http://${HOST}:8080 --label qwen-64 --auth -v"

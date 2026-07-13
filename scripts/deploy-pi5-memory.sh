#!/usr/bin/env bash
# Deploy levara-server to Raspberry Pi 5 (10.23.0.53) and restart.
set -euo pipefail

HOST="${LEVARA_PI_HOST:-10.23.0.53}"
SSH_USER="${LEVARA_SSH_USER:-stek0v}"
REMOTE_DIR="${LEVARA_PI_DIR:-/home/stek0v/levara}"
SSH_PROXY_JUMP="${LEVARA_SSH_PROXY_JUMP:-}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-arm64}"
GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X main.GitSHA=${GIT_SHA} -X main.BuildTime=${BUILD_TIME}"
SSH_OPTS=()
if [[ -n "${SSH_PROXY_JUMP}" ]]; then
  SSH_OPTS=(-o "ProxyJump=${SSH_PROXY_JUMP}")
fi

echo "==> Building levara-server (${GOOS}/${GOARCH}) for Pi5 (${GIT_SHA} @ ${BUILD_TIME})"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "${LDFLAGS}" -o /tmp/levara-server-pi ./cmd/server/

echo "==> Upload to ${SSH_USER}@${HOST}:${REMOTE_DIR}/"
scp "${SSH_OPTS[@]+"${SSH_OPTS[@]}"}" /tmp/levara-server-pi "${SSH_USER}@${HOST}:${REMOTE_DIR}/levara.new"

echo "==> Remote swap + restart levara (port 8090)"
ssh "${SSH_OPTS[@]+"${SSH_OPTS[@]}"}" "${SSH_USER}@${HOST}" bash -s <<EOF
set -euo pipefail
cd "${REMOTE_DIR}"
chmod +x levara.new
mv levara levara.bak.\$(date +%Y%m%d%H%M%S) 2>/dev/null || true
mv levara.new levara
sudo -n systemctl restart levara 2>/dev/null || systemctl --user restart levara 2>/dev/null || {
  pkill -f "${REMOTE_DIR}/levara" || true
  sleep 2
}
sleep 4
curl -sf http://127.0.0.1:8090/health && echo "Pi Levara healthy"
EOF

echo "==> Done. Re-run pi eval:"
echo "python3 benchmark/memory_eval/run_memory_eval.py --url http://${HOST}:8090 --label pi5 --auth -v"

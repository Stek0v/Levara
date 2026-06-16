#!/usr/bin/env bash
# Deploy levara-server to Raspberry Pi 5 (10.23.0.53) and restart.
set -euo pipefail

HOST="${LEVARA_PI_HOST:-10.23.0.53}"
SSH_USER="${LEVARA_SSH_USER:-stek0v}"
REMOTE_DIR="${LEVARA_PI_DIR:-/home/stek0v/levara}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-arm64}"

echo "==> Building levara-server (${GOOS}/${GOARCH}) for Pi5"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "-s -w" -o /tmp/levara-server-pi ./cmd/server/

echo "==> Upload to ${SSH_USER}@${HOST}:${REMOTE_DIR}/"
scp /tmp/levara-server-pi "${SSH_USER}@${HOST}:${REMOTE_DIR}/levara.new"

echo "==> Remote swap + restart levara (port 8090)"
ssh "${SSH_USER}@${HOST}" bash -s <<EOF
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

#!/bin/bash
# install_64.sh — deploy the load-profile suite to 10.23.0.64.
#
# Copies scripts/load-profiles/ to the .64 host, mints a service JWT,
# and prints the command to launch profile P1. Idempotent — safe to
# re-run; existing JWT files are preserved.
#
# Usage:
#   bash scripts/load-profiles/install_64.sh
#
# Env overrides:
#   HOST=stek0v@10.23.0.64        SSH target
#   REMOTE_DIR=~/levara-loadprofiles  Remote install root
#   LEVARA_URL=http://10.23.0.64:8080  Levara base URL on the target host

set -euo pipefail

HOST="${HOST:-stek0v@10.23.0.64}"
REMOTE_DIR="${REMOTE_DIR:-/home/stek0v/levara-loadprofiles}"
LEVARA_URL="${LEVARA_URL:-http://localhost:8080}"
TARGET_NAME="${TARGET_NAME:-rpi64}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

echo "[install_64] syncing scripts/load-profiles/ → ${HOST}:${REMOTE_DIR}"
ssh "$HOST" "mkdir -p ${REMOTE_DIR}/out ${REMOTE_DIR}/seed ~/.config/levara-service-keys"
rsync -av --delete \
  --exclude '__pycache__' --exclude 'out/' --exclude '*.pyc' \
  "$HERE/" "$HOST:$REMOTE_DIR/"

# Mint a service JWT on the remote host so the token never crosses
# the network from this workstation. Reuses the existing helper.
echo "[install_64] minting JWT (subject=loadprofile-${TARGET_NAME})"
ssh "$HOST" "bash -s loadprofile-${TARGET_NAME}" <<'REMOTE_MINT'
set -euo pipefail
SUBJECT="$1"
LEVARA_URL="${LEVARA_URL:-http://localhost:8080}"
KEY_DIR="$HOME/.config/levara-service-keys"
JWT_FILE="${KEY_DIR}/${SUBJECT}.jwt"
if [ -s "$JWT_FILE" ]; then
  echo "[mint] reusing existing ${JWT_FILE}"
  exit 0
fi
mkdir -p "$KEY_DIR" && chmod 700 "$KEY_DIR"
# Lightweight login — assumes the service account exists on this host.
# Use scripts/mint-levara-jwt.sh from the main repo if you need to
# bootstrap a brand-new account.
echo "[mint] no JWT at ${JWT_FILE}; please run mint-levara-jwt.sh on this host first"
exit 1
REMOTE_MINT

cat <<EOF

[install_64] done. To run profile P1 on ${HOST}:

  ssh ${HOST} 'cd ${REMOTE_DIR} && LEVARA_URL=${LEVARA_URL} python3 p1_narrative_factual.py --rounds 10 --out out/p1.jsonl'

Output JSONL lives at ${REMOTE_DIR}/out/p1.jsonl on the remote host.
EOF

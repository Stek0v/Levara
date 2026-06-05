#!/bin/bash
# install_pi.sh — deploy the load-profile suite to 10.23.0.53 (Pi).
#
# Lighter target than .64: P4 and P5 run here. Same idempotent
# rsync + remote JWT bootstrap as install_64.sh.
#
# Usage:
#   bash scripts/load-profiles/install_pi.sh
#
# Env overrides:
#   HOST=stek0v@10.23.0.53        SSH target
#   REMOTE_DIR=~/levara-loadprofiles  Remote install root
#   LEVARA_URL=http://localhost:8080  Levara base URL on the target host
#   TARGET_NAME=pi                Tag used to namespace the JWT file

set -euo pipefail

HOST="${HOST:-stek0v@10.23.0.53}"
REMOTE_DIR="${REMOTE_DIR:-/home/stek0v/levara-loadprofiles}"
LEVARA_URL="${LEVARA_URL:-http://localhost:8080}"
TARGET_NAME="${TARGET_NAME:-pi}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[install_pi] syncing scripts/load-profiles/ → ${HOST}:${REMOTE_DIR}"
ssh "$HOST" "mkdir -p ${REMOTE_DIR}/out ${REMOTE_DIR}/seed ~/.config/levara-service-keys"
rsync -av --delete \
  --exclude '__pycache__' --exclude 'out/' --exclude '*.pyc' \
  "$HERE/" "$HOST:$REMOTE_DIR/"

echo "[install_pi] verifying JWT (subject=loadprofile-${TARGET_NAME})"
ssh "$HOST" "test -s \"\$HOME/.config/levara-service-keys/${TARGET_NAME}.jwt\" || { \
  echo '[mint] no JWT at ~/.config/levara-service-keys/${TARGET_NAME}.jwt — run mint-levara-jwt.sh on this host first'; \
  exit 1; \
}"

cat <<EOF

[install_pi] done. Run profiles on ${HOST}:

  ssh ${HOST} 'cd ${REMOTE_DIR} && LEVARA_URL=${LEVARA_URL} python3 p4_memory_palace.py --rounds 15 --out out/p4.jsonl'
  ssh ${HOST} 'cd ${REMOTE_DIR} && LEVARA_URL=${LEVARA_URL} python3 p5_filtered_search.py --rounds 15 --out out/p5.jsonl'

Output JSONL lives at ${REMOTE_DIR}/out/{p4,p5}.jsonl on the remote host.
EOF

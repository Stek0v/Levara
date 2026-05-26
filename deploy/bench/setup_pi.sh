#!/usr/bin/env bash
# One-time Pi setup. Idempotent.
# Usage: ssh stek0v@10.23.0.53 bash -s < deploy/bench/setup_pi.sh
set -euo pipefail

EMBED_DIR=/home/stek0v/embed-bench
BENCH_DIR=/home/stek0v/levara-bench
REPO_DIR=/home/stek0v/levara-source

mkdir -p "$EMBED_DIR/hf-cache" "$BENCH_DIR/data"

if [ ! -d "$EMBED_DIR/venv" ]; then
  python3 -m venv "$EMBED_DIR/venv"
fi
"$EMBED_DIR/venv/bin/pip" install --upgrade pip
"$EMBED_DIR/venv/bin/pip" install -r "$REPO_DIR/scripts/load-profiles/embed_bench/requirements.txt"

rsync -a --delete "$REPO_DIR/scripts/" "$EMBED_DIR/scripts/"

JWT_FILE=/etc/systemd/system/levara-bench.service.d/jwt.conf
if [ ! -f "$JWT_FILE" ]; then
  SECRET=$(head -c 32 /dev/urandom | xxd -p -c 32)
  sudo mkdir -p "$(dirname "$JWT_FILE")"
  sudo tee "$JWT_FILE" >/dev/null <<EOF
[Service]
Environment=JWT_SECRET=$SECRET
EOF
fi

sudo install -m 0644 "$REPO_DIR/deploy/bench/embed-bench.service"  /etc/systemd/system/
sudo install -m 0644 "$REPO_DIR/deploy/bench/levara-bench.service" /etc/systemd/system/
sudo systemctl daemon-reload

echo "[setup_pi.sh] OK. embed-bench + levara-bench installed (not enabled)."

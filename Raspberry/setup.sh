#!/bin/bash
set -euo pipefail

# ============================================================
# Cognevra Pi Setup — One-click installer
# Run as root: sudo ./setup.sh
# ============================================================

COGNEVRA_USER="cognevra"
COGNEVRA_DIR="/var/lib/cognevra"
COGNEVRA_CONF="/etc/cognevra"
COGNEVRA_LOG="/var/log/cognevra"
COGNEVRA_BIN="/usr/local/bin/cognevra"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[x]${NC} $1"; exit 1; }

# --- Pre-checks ---
if [ "$(id -u)" -ne 0 ]; then
    err "This script must be run as root. Use: sudo ./setup.sh"
fi

ARCH=$(uname -m)
if [ "$ARCH" != "aarch64" ] && [ "$ARCH" != "arm64" ]; then
    warn "Architecture is $ARCH, not arm64. Proceeding anyway..."
fi

echo "============================================"
echo "  Cognevra Pi Setup"
echo "============================================"
echo ""

# --- 1. System user & directories ---
log "Creating user and directories..."
if ! id "$COGNEVRA_USER" &>/dev/null; then
    useradd -r -s /bin/false "$COGNEVRA_USER"
    log "Created user: $COGNEVRA_USER"
else
    log "User $COGNEVRA_USER already exists"
fi

mkdir -p "$COGNEVRA_DIR/data"
mkdir -p "$COGNEVRA_CONF"
mkdir -p "$COGNEVRA_LOG"
chown -R "$COGNEVRA_USER:$COGNEVRA_USER" "$COGNEVRA_DIR" "$COGNEVRA_LOG"

# --- 2. Install dependencies ---
log "Installing dependencies..."
apt-get update -qq
apt-get install -y -qq curl wget jq sqlite3 > /dev/null

# --- 3. Swap check ---
SWAP_TOTAL=$(free -m | awk '/Swap/ {print $2}')
if [ "$SWAP_TOTAL" -lt 2000 ]; then
    warn "Swap is ${SWAP_TOTAL}MB (recommended: 4096MB)"
    read -p "Create 4GB swap? [Y/n] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Nn]$ ]]; then
        if [ -f /swapfile ]; then
            swapoff /swapfile 2>/dev/null || true
            rm -f /swapfile
        fi
        fallocate -l 4G /swapfile
        chmod 600 /swapfile
        mkswap /swapfile
        swapon /swapfile
        if ! grep -q '/swapfile' /etc/fstab; then
            echo '/swapfile none swap sw 0 0' >> /etc/fstab
        fi
        log "Created 4GB swap"
    fi
fi

# --- 4. Install Ollama ---
if command -v ollama &>/dev/null; then
    log "Ollama already installed: $(ollama --version)"
else
    log "Installing Ollama..."
    curl -fsSL https://ollama.ai/install.sh | sh
    systemctl enable ollama
    systemctl start ollama
    log "Ollama installed and started"
fi

# Wait for Ollama to be ready
log "Waiting for Ollama..."
for i in $(seq 1 30); do
    if curl -sf http://127.0.0.1:11434/api/tags > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

if ! curl -sf http://127.0.0.1:11434/api/tags > /dev/null 2>&1; then
    err "Ollama did not start within 30 seconds"
fi

# --- 5. Pull models ---
TOTAL_MEM=$(free -m | awk '/Mem/ {print $2}')
log "Detected RAM: ${TOTAL_MEM}MB"

if [ "$TOTAL_MEM" -lt 6000 ]; then
    log "Pi 4GB detected — pulling lightweight models..."
    EMBED_MODEL="all-minilm:l6-v2"
    LLM_MODEL="qwen2:0.5b"
    DIM=384
else
    log "Pi 8GB detected — pulling recommended models..."
    EMBED_MODEL="nomic-embed-text"
    LLM_MODEL="gemma3:4b"
    DIM=768
fi

log "Pulling embed model: $EMBED_MODEL"
ollama pull "$EMBED_MODEL"

log "Pulling LLM model: $LLM_MODEL"
ollama pull "$LLM_MODEL"

# --- 6. Ollama tuning ---
log "Configuring Ollama for Pi..."
mkdir -p /etc/systemd/system/ollama.service.d
cat > /etc/systemd/system/ollama.service.d/override.conf << 'EOF'
[Service]
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_KEEP_ALIVE=30m"
EOF
systemctl daemon-reload
systemctl restart ollama

# --- 7. Install Cognevra binary ---
if [ -f "$SCRIPT_DIR/cognevra-arm64" ]; then
    log "Installing Cognevra from local binary..."
    cp "$SCRIPT_DIR/cognevra-arm64" "$COGNEVRA_BIN"
else
    log "Downloading Cognevra binary..."
    wget -q https://github.com/stek0v/cognevra/releases/latest/download/cognevra-arm64 -O "$COGNEVRA_BIN" || \
        err "Failed to download Cognevra binary. Place cognevra-arm64 in this directory and retry."
fi
chmod +x "$COGNEVRA_BIN"

# --- 8. Install config ---
log "Installing configuration..."
cat > "$COGNEVRA_CONF/cognevra.env" << ENVEOF
DB_PROVIDER=sqlite
DB_PATH=$COGNEVRA_DIR/cognevra.db
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
EMBEDDING_MODEL=$EMBED_MODEL
LLM_PROVIDER=openai
LLM_ENDPOINT=http://localhost:11434/v1
LLM_MODEL=$LLM_MODEL
LLM_TIMEOUT=120
LLM_RATE_LIMIT_REQUESTS=10
LLM_RATE_LIMIT_INTERVAL=60
CACHE_TTL=7200
CACHE_MAX_SIZE=500
LOG_LEVEL=INFO
ENVEOF

# --- 9. Install systemd service ---
log "Installing systemd service..."
SHARDS=1
if [ "$TOTAL_MEM" -ge 6000 ]; then
    SHARDS=2
fi

cat > /etc/systemd/system/cognevra.service << SVCEOF
[Unit]
Description=Cognevra Memory Server
After=network-online.target ollama.service
Wants=network-online.target
Requires=ollama.service

[Service]
Type=simple
User=$COGNEVRA_USER
Group=$COGNEVRA_USER
EnvironmentFile=$COGNEVRA_CONF/cognevra.env
ExecStart=$COGNEVRA_BIN -standalone=true -dim=$DIM -shards=$SHARDS -port=8080 -grpc-port=0 -data-dir=$COGNEVRA_DIR/data
Restart=always
RestartSec=5
WatchdogSec=30
MemoryMax=2G
MemoryHigh=1536M
CPUQuota=300%
LimitNOFILE=65536
WorkingDirectory=$COGNEVRA_DIR
ProtectSystem=strict
ReadWritePaths=$COGNEVRA_DIR $COGNEVRA_LOG
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
StandardOutput=journal
StandardError=journal
SyslogIdentifier=cognevra

[Install]
WantedBy=multi-user.target
SVCEOF

# --- 10. Enable & start ---
log "Enabling and starting Cognevra..."
systemctl daemon-reload
systemctl enable cognevra
systemctl start cognevra

# --- 11. Health check ---
log "Waiting for Cognevra to start..."
sleep 5

HEALTH_OK=false
for i in $(seq 1 15); do
    if curl -sf http://localhost:8080/health > /dev/null 2>&1; then
        HEALTH_OK=true
        break
    fi
    sleep 2
done

echo ""
echo "============================================"
if [ "$HEALTH_OK" = true ]; then
    log "Cognevra is running!"
    echo ""
    echo "  Health:     curl http://localhost:8080/health"
    echo "  Logs:       journalctl -u cognevra -f"
    echo "  Config:     $COGNEVRA_CONF/cognevra.env"
    echo "  Data:       $COGNEVRA_DIR"
    echo ""
    echo "  MCP URL:    http://$(hostname).local:8080/mcp"
    echo ""
    echo "  Models:     embed=$EMBED_MODEL, llm=$LLM_MODEL"
    echo "  Dimension:  $DIM"
    echo "  Shards:     $SHARDS"
else
    warn "Cognevra may not have started correctly."
    echo "  Check logs: journalctl -u cognevra --no-pager -n 50"
fi
echo "============================================"

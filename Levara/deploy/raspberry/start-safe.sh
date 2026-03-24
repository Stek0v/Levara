#!/bin/bash
# Safe start script for Levara on Raspberry Pi
# Prevents OOM by configuring Ollama BEFORE starting Levara
set -euo pipefail

echo "=== Levara Safe Start for Raspberry Pi ==="

# 1. Configure Ollama for Pi memory constraints
echo "[1/5] Configuring Ollama memory limits..."
sudo mkdir -p /etc/systemd/system/ollama.service.d
sudo tee /etc/systemd/system/ollama.service.d/pi-limits.conf > /dev/null << 'CONF'
[Service]
# CRITICAL for Pi 8GB: only 1 model in RAM at a time
# Ollama will swap embed ↔ LLM as needed
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_KEEP_ALIVE=5m"
Environment="OLLAMA_HOST=127.0.0.1:11434"
CONF
sudo systemctl daemon-reload
sudo systemctl restart ollama
sleep 5

# 2. Verify Ollama is running
echo "[2/5] Verifying Ollama..."
curl -sf http://localhost:11434/api/tags > /dev/null || { echo "ERROR: Ollama not responding"; exit 1; }
echo "  Ollama OK"

# 3. Pre-warm embedding model (loads into RAM)
echo "[3/5] Warming up embedding model..."
curl -s http://localhost:11434/v1/embeddings -X POST \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","input":"warmup"}' > /dev/null
echo "  Embed model loaded"

# 4. Check available RAM
AVAIL_MB=$(free -m | awk '/^Mem:/{print $7}')
echo "[4/5] Available RAM: ${AVAIL_MB}MB"
if [ "$AVAIL_MB" -lt 500 ]; then
    echo "WARNING: Low RAM (${AVAIL_MB}MB < 500MB). Consider using qwen2:0.5b instead of gemma3:4b"
fi

# 5. Start Levara
echo "[5/5] Starting Levara..."
pkill -f "levara.*-port=8080" 2>/dev/null || true
sleep 1

cd ~/levara

# Choose LLM based on available RAM
if [ "$AVAIL_MB" -gt 4000 ]; then
    LLM_MODEL="gemma3:4b"
    echo "  Using gemma3:4b (high quality, ~30s/call)"
else
    LLM_MODEL="qwen2:0.5b"
    echo "  Using qwen2:0.5b (fast, ~3s/call, lower quality)"
fi

export DB_PROVIDER=sqlite
export DB_PATH=$HOME/levara/data/levara.db
export EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
export EMBEDDING_MODEL=nomic-embed-text
export LLM_PROVIDER=openai
export LLM_ENDPOINT=http://localhost:11434/v1
export LLM_MODEL=$LLM_MODEL
export LOG_LEVEL=INFO

nohup ./levara -standalone=true -dim=768 -shards=1 -port=8080 -grpc-port=0 \
  -data-dir=$HOME/levara/data > $HOME/levara/levara.log 2>&1 &

sleep 5

# Health check
if curl -sf http://localhost:8080/health > /dev/null; then
    echo ""
    echo "=== Levara RUNNING ==="
    echo "  URL: http://$(hostname -I | awk '{print $1}'):8080"
    echo "  MCP: http://$(hostname -I | awk '{print $1}'):8080/mcp"
    echo "  LLM: $LLM_MODEL"
    echo "  RAM available: ${AVAIL_MB}MB"
    curl -s http://localhost:8080/api/v1/collections | python3 -c "
import sys,json
colls=json.load(sys.stdin)
total=sum(c.get('record_count',0) for c in colls)
print(f'  Collections: {len(colls)}, Records: {total}')
"
else
    echo "ERROR: Levara failed to start. Check ~/levara/levara.log"
    exit 1
fi

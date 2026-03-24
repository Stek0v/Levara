#!/bin/bash
# Levara Safe Start for Raspberry Pi 5 (8GB)
# Tested config: qwen3:0.6b + nomic-embed-text = 1.5GB RAM, 5.9GB free
set -euo pipefail

echo "=== Levara Safe Start ==="

# 1. Start Ollama with 2 models in RAM (LLM + embed)
echo "[1/5] Starting Ollama..."
pkill ollama 2>/dev/null || true
sleep 2

export OLLAMA_MAX_LOADED_MODELS=2   # CRITICAL: both models in RAM simultaneously
export OLLAMA_NUM_PARALLEL=1
export OLLAMA_KEEP_ALIVE=30m
nohup ollama serve > /tmp/ollama.log 2>&1 &
sleep 5

curl -sf http://localhost:11434/api/tags > /dev/null || { echo "ERROR: Ollama not responding"; exit 1; }
echo "  Ollama OK"

# 2. Pre-warm both models
echo "[2/5] Loading models..."
curl -s http://localhost:11434/v1/embeddings -X POST \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","input":"warmup"}' > /dev/null
echo "  nomic-embed-text loaded"

curl -s --max-time 30 http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3:0.6b","messages":[{"role":"user","content":"hi"}],"num_predict":3}' > /dev/null
echo "  qwen3:0.6b loaded"

# 3. Check RAM
AVAIL=$(free -m | awk '/^Mem:/{print $7}')
echo "[3/5] RAM available: ${AVAIL}MB"

# 4. Start Levara
echo "[4/5] Starting Levara..."
pkill -f "levara.*-port=8080" 2>/dev/null || true
sleep 1

cd ~/levara
export DB_PROVIDER=sqlite
export DB_PATH=$HOME/levara/data/levara.db
export EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
export EMBEDDING_MODEL=nomic-embed-text
export LLM_PROVIDER=openai
export LLM_ENDPOINT=http://localhost:11434/v1
export LLM_MODEL=qwen3:0.6b
export LOG_LEVEL=INFO

nohup ./levara -standalone=true -dim=768 -shards=1 -port=8080 -grpc-port=0 \
  -data-dir=$HOME/levara/data > $HOME/levara/levara.log 2>&1 &
sleep 5

# 5. Health check
echo "[5/5] Health check..."
if curl -sf http://localhost:8080/health > /dev/null; then
    echo ""
    echo "=== LEVARA RUNNING ==="
    echo "  URL: http://$(hostname -I | awk '{print $1}'):8080"
    echo "  MCP: http://$(hostname -I | awk '{print $1}'):8080/mcp"
    echo "  LLM: qwen3:0.6b (522MB)"
    echo "  Embed: nomic-embed-text (274MB)"
    echo "  RAM free: ${AVAIL}MB"
    curl -s http://localhost:8080/api/v1/collections | python3 -c "
import sys,json
colls=json.load(sys.stdin)
total=sum(c.get('record_count',0) for c in colls)
print(f'  Records: {total}')
" 2>/dev/null
else
    echo "ERROR: Levara failed. Check ~/levara/levara.log"
    exit 1
fi

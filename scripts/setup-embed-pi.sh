#!/bin/bash
# Setup Ollama + lightweight embedding model on Raspberry Pi

set -e

echo "=== Levara Pi Setup ==="

# Check architecture
ARCH=$(uname -m)
echo "Architecture: $ARCH"

# Install Ollama
if ! command -v ollama &> /dev/null; then
    echo "Installing Ollama..."
    curl -fsSL https://ollama.ai/install.sh | sh
fi

# Determine model based on RAM
TOTAL_RAM=$(free -m | awk '/^Mem:/{print $2}')
echo "Total RAM: ${TOTAL_RAM}MB"

if [ "$TOTAL_RAM" -lt 4096 ]; then
    echo "Low RAM (<4GB): using all-minilm:l6-v2 (33MB, dim=384)"
    EMBED_MODEL="all-minilm:l6-v2"
    EMBED_DIM=384
elif [ "$TOTAL_RAM" -lt 8192 ]; then
    echo "Medium RAM (4-8GB): using nomic-embed-text (261MB, dim=768)"
    EMBED_MODEL="nomic-embed-text"
    EMBED_DIM=768
else
    echo "High RAM (8GB+): using nomic-embed-text (261MB, dim=768)"
    EMBED_MODEL="nomic-embed-text"
    EMBED_DIM=768
fi

# Pull embedding model
echo "Pulling $EMBED_MODEL..."
ollama pull $EMBED_MODEL

# Test embedding
echo "Testing embedding..."
curl -s http://localhost:11434/v1/embeddings -X POST \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$EMBED_MODEL\",\"input\":\"test\"}" | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(f'Embedding dim: {len(d[\"data\"][0][\"embedding\"])}')"

echo ""
echo "=== Setup complete ==="
echo "Start Levara with:"
echo "  DB_PROVIDER=sqlite \\"
echo "  EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings \\"
echo "  EMBEDDING_MODEL=$EMBED_MODEL \\"
echo "  ./levara-arm64 -standalone=true -dim=$EMBED_DIM -port=8080"

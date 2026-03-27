#!/bin/bash
# Auto-load Levara project context at session start
# Fetches memories and manifest from local Levara MCP

LEVARA_URL="${LEVARA_URL:-http://localhost:8081}"

# Check if Levara is running
if ! curl -s --max-time 2 "$LEVARA_URL/health" >/dev/null 2>&1; then
    echo "[levara] Local instance not reachable at $LEVARA_URL"
    exit 0
fi

# Fetch manifest
MANIFEST=$(curl -s --max-time 3 "$LEVARA_URL/api/v1/sync/manifest" 2>/dev/null)
if [ -z "$MANIFEST" ]; then
    exit 0
fi

MEM_COUNT=$(echo "$MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['memories']['count'])" 2>/dev/null)
EMBED_MODEL=$(echo "$MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['embed_model'])" 2>/dev/null)

# Fetch project memories (compact format)
MEMORIES=$(curl -s --max-time 3 "$LEVARA_URL/api/v1/memories" 2>/dev/null | python3 -c "
import sys,json
try:
    mems = json.load(sys.stdin)
    for m in mems[:15]:
        print(f'- [{m[\"type\"]}] {m[\"key\"]}: {m[\"value\"][:120]}')
except: pass
" 2>/dev/null)

cat <<EOF
[Levara MCP Context — auto-loaded]
Instance: $LEVARA_URL | Model: $EMBED_MODEL | Memories: $MEM_COUNT
Use recall_memory/save_memory/search to interact with project knowledge.
Use sync tool to sync with Pi (http://10.23.0.53:8080/api/v1).

Project memories:
$MEMORIES
EOF

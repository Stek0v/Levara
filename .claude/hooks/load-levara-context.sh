#!/bin/bash
# Auto-load Levara project context at session start
# Detects project collection from: .levara-collection file > directory name
# Fetches only THIS project's memories, not all
#
# IMPORTANT: Skips on resume to prevent API 400 concurrency error.
# SessionStart hook fires on: startup, resume, compact, clear.
# On resume, Claude has pending tool calls — injecting stdout conflicts.

# Read event JSON from stdin to detect resume
EVENT=$(cat)
SESSION_TYPE=$(echo "$EVENT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('type',''))" 2>/dev/null)

# Skip on resume — prevents API 400 "tool use concurrency" error
if [ "$SESSION_TYPE" = "resume" ]; then
    exit 0
fi

LEVARA_URL="${LEVARA_URL:-http://localhost:8081}"

# Check if Levara is running
if ! curl -s --max-time 2 "$LEVARA_URL/health" >/dev/null 2>&1; then
    exit 0
fi

# Detect collection name for this project
if [ -f ".levara-collection" ]; then
    COLLECTION=$(cat .levara-collection | tr -d '[:space:]')
elif [ -f "../.levara-collection" ]; then
    COLLECTION=$(cat ../.levara-collection | tr -d '[:space:]')
else
    COLLECTION=$(basename "$(pwd)")
fi

# Fetch project-scoped memories
MEMORIES=$(curl -s --max-time 3 "$LEVARA_URL/api/v1/memories" 2>/dev/null | python3 -c "
import sys,json
try:
    mems = json.load(sys.stdin)
    coll = '$COLLECTION'
    # Filter: project memories matching this collection, or global (no collection)
    filtered = [m for m in mems if m.get('collection_name','') in (coll, '')]
    if not filtered:
        filtered = mems  # fallback: show all if no project-specific found
    for m in filtered[:15]:
        cn = m.get('collection_name','')
        prefix = f'[{m[\"type\"]}]'
        if cn and cn != coll:
            prefix += f' ({cn})'
        print(f'- {prefix} {m[\"key\"]}: {m[\"value\"][:120]}')
    if not filtered:
        print('- (no memories for this project)')
except: pass
" 2>/dev/null)

MEM_LINES=$(echo "$MEMORIES" | wc -l | tr -d ' ')

cat <<EOF
[Levara Context: $COLLECTION]
Instance: $LEVARA_URL | Project: $COLLECTION | Memories: $MEM_LINES
Use set_context(collection="$COLLECTION") to scope all tools to this project.
Use recall_memory(query="...") for project knowledge, save_memory after tasks.

$MEMORIES
EOF

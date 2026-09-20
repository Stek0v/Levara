#!/bin/bash
# Rebuild the chat-imports RAG layer with the current renderer.
#
# Run AFTER the active cognify run finishes (check
# /api/v1/cognify/<runId>/status): re-rendering changes every document,
# so the whole corpus is re-chunked and re-embedded — expect roughly
# docs × 50s of embedder time (≈12h for ~900 documents on the local
# potion-code-16M server). The raw layer (chat_import_messages) is
# untouched: imports are idempotent, only the searchable dataset is
# rebuilt. Deletes the existing chat-imports dataset first.
#
# Usage: scripts/rebuild_chat_imports.sh [LEVARA_URL] [CLI_BINARY]
set -euo pipefail

LEVARA_URL="${1:-http://127.0.0.1:8081/api/v1}"
CLI="${2:-levara}"

echo "== chat-imports RAG rebuild =="
echo "   server: $LEVARA_URL"

echo "-- 1/3 drop the old dataset (boilerplate-carrying renders)"
DS_ID=$(curl -fsS "$LEVARA_URL/datasets" | python3 -c \
  "import json,sys
ids=[d['id'] for d in json.load(sys.stdin) if d['name']=='chat-imports']
print(ids[0] if ids else '')")
if [ -n "$DS_ID" ]; then
  curl -fsS -X DELETE "$LEVARA_URL/datasets/$DS_ID" >/dev/null
  echo "   deleted $DS_ID"
else
  echo "   no existing dataset, creating fresh"
fi

echo "-- 2/3 re-import all platforms (clean renderer, no system noise)"
"$CLI" --url="$LEVARA_URL" chats import --platform=claude-code \
  --path="$HOME/.claude/projects" --dataset=chat-imports --force-rag
"$CLI" --url="$LEVARA_URL" chats import --platform=codex \
  --path="$HOME/.codex/sessions" --dataset=chat-imports --force-rag
"$CLI" --url="$LEVARA_URL" chats import --platform=cursor \
  --path="$HOME/Library/Application Support/Cursor/User" \
  --dataset=chat-imports --force-rag

echo "-- 3/3 cognify the fresh corpus (server-side, survives this script)"
DS_ID=$(curl -fsS "$LEVARA_URL/datasets" | python3 -c \
  "import json,sys; print(next(d['id'] for d in json.load(sys.stdin) if d['name']=='chat-imports'))")
RUN=$(curl -fsS -X POST "$LEVARA_URL/cognify" -H 'Content-Type: application/json' \
  -d "{\"datasets\":[\"$DS_ID\"],\"collection\":\"chat-imports\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('pipeline_run_id',''))")
echo "   cognify run: $RUN"
echo "   monitor: $LEVARA_URL/cognify/$RUN/status"
echo "== done: dataset rebuilt, cognify running =="

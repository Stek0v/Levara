#!/bin/bash
# Post-commit hook: sends changed files to Cognee for knowledge graph ingestion.
# Install: cp cognee-git-hook.sh .git/hooks/post-commit && chmod +x .git/hooks/post-commit
# Or use: bash install-cognee-hook.sh

COGNEE_API_URL="${COGNEE_API_URL:-http://localhost:8000}"
COGNEE_DATASET="${COGNEE_DATASET:-git_repo}"
COGNEE_API_TOKEN="${COGNEE_API_TOKEN:-}"

# Build auth header if token is set
AUTH_HEADER=""
if [ -n "$COGNEE_API_TOKEN" ]; then
    AUTH_HEADER="-H \"Authorization: Bearer $COGNEE_API_TOKEN\""
fi

# Get list of changed files from the last commit
changed_files=$(git diff --name-only --diff-filter=ACMR HEAD~1..HEAD 2>/dev/null)

if [ -z "$changed_files" ]; then
    echo "[cognee] No changed files to ingest"
    exit 0
fi

# Filter to text/code file extensions only
extensions="py|js|ts|jsx|tsx|md|txt|rst|yaml|yml|json|toml|cfg|ini|sh|go|rs|java|c|cpp|h|hpp|cs|rb|php|sql|html|css|scss|less|vue|svelte|swift|kt|scala|r|jl|lua|pl|pm|ex|exs|erl|hrl|hs|ml|mli|fs|fsi|dart|tf|hcl|proto|graphql|gql"

ingested=0

for file in $changed_files; do
    if [ -f "$file" ] && echo "$file" | grep -qE "\.($extensions)$"; then
        # Upload file via multipart/form-data
        if [ -n "$COGNEE_API_TOKEN" ]; then
            curl -s -X POST "$COGNEE_API_URL/api/v1/add" \
                -H "Authorization: Bearer $COGNEE_API_TOKEN" \
                -F "data=@$file" \
                -F "datasetName=$COGNEE_DATASET" > /dev/null 2>&1
        else
            curl -s -X POST "$COGNEE_API_URL/api/v1/add" \
                -F "data=@$file" \
                -F "datasetName=$COGNEE_DATASET" > /dev/null 2>&1
        fi
        ingested=$((ingested + 1))
    fi
done

if [ "$ingested" -eq 0 ]; then
    echo "[cognee] No text files to ingest"
    exit 0
fi

# Start cognify in background (don't block git)
if [ -n "$COGNEE_API_TOKEN" ]; then
    curl -s -X POST "$COGNEE_API_URL/api/v1/cognify" \
        -H "Authorization: Bearer $COGNEE_API_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"datasets\": [\"$COGNEE_DATASET\"]}" > /dev/null 2>&1 &
else
    curl -s -X POST "$COGNEE_API_URL/api/v1/cognify" \
        -H "Content-Type: application/json" \
        -d "{\"datasets\": [\"$COGNEE_DATASET\"]}" > /dev/null 2>&1 &
fi

echo "[cognee] Ingested $ingested files, cognify started in background"

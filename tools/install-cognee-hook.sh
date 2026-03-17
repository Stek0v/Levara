#!/bin/bash
# Install Cognee post-commit hook into the current git repository.
# Usage: cd /your/git/repo && bash /path/to/install-cognee-hook.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Verify we're in a git repo
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo "Error: not inside a git repository"
    exit 1
fi

HOOK_DIR="$(git rev-parse --git-dir)/hooks"
HOOK_PATH="$HOOK_DIR/post-commit"

# Check for existing hook
if [ -f "$HOOK_PATH" ]; then
    echo "Warning: existing post-commit hook found at $HOOK_PATH"
    echo "Backing up to $HOOK_PATH.bak"
    cp "$HOOK_PATH" "$HOOK_PATH.bak"
fi

cp "$SCRIPT_DIR/cognee-git-hook.sh" "$HOOK_PATH"
chmod +x "$HOOK_PATH"

echo "Cognee post-commit hook installed at $HOOK_PATH"
echo ""
echo "Configure with environment variables:"
echo "  export COGNEE_API_URL=http://your-server:8000"
echo "  export COGNEE_DATASET=git_repo"
echo "  export COGNEE_API_TOKEN=your_token  # optional"

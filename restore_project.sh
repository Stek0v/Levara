#!/bin/bash
# restore_project.sh — restore project from migration bundle
set -euo pipefail

SRC="${1:?Usage: bash restore_project.sh /path/to/new_db_migration}"
PROJECT_DIR="${2:-$HOME/src/new_db}"

echo "Restoring from $SRC to $PROJECT_DIR ..."

# 1. Restore git repo
echo "[1/7] Restoring git repository..."
mkdir -p "$PROJECT_DIR"
cd "$PROJECT_DIR"
if [ ! -d ".git" ]; then
    git clone "$SRC/new_db.bundle" .
else
    echo "  Git repo already exists, skipping clone"
fi

# 2. Restore .env and untracked docs
echo "[2/7] Restoring configuration..."
cp "$SRC/dot_env" .env
cp "$SRC/CLAUDE.md" CLAUDE.md 2>/dev/null || true
cp "$SRC/CONTEXT.md" CONTEXT.md 2>/dev/null || true

# 3. Restore Claude memory
echo "[3/7] Restoring Claude memory..."
# Claude uses absolute path as project key, replacing / with -
ESCAPED_PATH=$(echo "$PROJECT_DIR" | sed 's|^/||; s|/|-|g')
CLAUDE_PROJECT_DIR="$HOME/.claude/projects/-${ESCAPED_PATH}"
mkdir -p "$CLAUDE_PROJECT_DIR"
if [ -f "$SRC/claude_memory.tar.gz" ]; then
    tar xzf "$SRC/claude_memory.tar.gz" -C "$CLAUDE_PROJECT_DIR" --strip-components=1
    echo "  Memory restored to $CLAUDE_PROJECT_DIR"
else
    echo "  WARNING: claude_memory.tar.gz not found"
fi

# 4. Restore Claude plan
echo "[4/7] Restoring Claude plan..."
mkdir -p "$HOME/.claude/plans"
if [ -f "$SRC/go_rewrite_plan.md" ]; then
    cp "$SRC/go_rewrite_plan.md" "$HOME/.claude/plans/structured-tinkering-puffin.md"
fi

# 5. Restore project Claude settings
echo "[5/7] Restoring project settings..."
if [ -d "$SRC/dot_claude_project" ]; then
    cp -r "$SRC/dot_claude_project" "$PROJECT_DIR/.claude"
fi

# 6. Install Python dependencies
echo "[6/7] Installing Python dependencies..."
if [ -f requirements.txt ]; then
    pip install -r requirements.txt 2>/dev/null || \
    pip3 install -r requirements.txt 2>/dev/null || \
    echo "  WARNING: Could not install Python deps. Run: pip install -r requirements.txt"
fi

# 7. Build VectraDB
echo "[7/7] Building VectraDB..."
if command -v docker &>/dev/null; then
    docker compose up -d --build 2>/dev/null || \
    echo "  WARNING: docker compose failed. Start manually: docker compose up -d --build"
else
    echo "  WARNING: Docker not found. Install Docker first."
fi

echo ""
echo "Restore complete!"
echo ""
echo "Next steps:"
echo "  1. Edit .env — update LLM_ENDPOINT and EMBEDDING_ENDPOINT for your network"
echo "  2. Verify: docker compose up -d && curl http://localhost:8080/metrics"
echo "  3. Run unit tests: pytest tests/test_vectradb_adapter.py -v"
echo "  4. (with GPU) Full tests: pytest tests/ -v -s"
echo ""
echo "Claude memory at: $CLAUDE_PROJECT_DIR"

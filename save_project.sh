#!/bin/bash
# save_project.sh — save everything for migration to another computer
set -euo pipefail

DEST="${1:-/tmp/new_db_migration}"
mkdir -p "$DEST"
echo "Saving to $DEST ..."

cd /home/stek0v/src/new_db

# 1. Git bundle (full repo with history)
echo "[1/6] Creating git bundle..."
git bundle create "$DEST/new_db.bundle" --all

# 2. Untracked critical files
echo "[2/6] Copying untracked files..."
cp -f CLAUDE.md "$DEST/" 2>/dev/null || true
cp -f .env "$DEST/dot_env"
cp -f CONTEXT.md "$DEST/" 2>/dev/null || true

# 3. Claude memory (28 MB)
echo "[3/6] Archiving Claude memory..."
CLAUDE_PROJECT="/home/stek0v/.claude/projects/-home-stek0v-src-new-db"
if [ -d "$CLAUDE_PROJECT" ]; then
    tar czf "$DEST/claude_memory.tar.gz" \
        -C /home/stek0v/.claude/projects \
        -- "-home-stek0v-src-new-db"
else
    echo "  WARNING: Claude memory directory not found"
fi

# 4. Claude plan
echo "[4/6] Copying Claude plan..."
PLAN="/home/stek0v/.claude/plans/structured-tinkering-puffin.md"
if [ -f "$PLAN" ]; then
    cp "$PLAN" "$DEST/go_rewrite_plan.md"
fi

# 5. Project-level Claude settings
echo "[5/6] Copying Claude project settings..."
if [ -d ".claude" ]; then
    cp -r .claude "$DEST/dot_claude_project"
fi

# 6. Global Claude settings
echo "[6/6] Copying global Claude settings..."
if [ -f "/home/stek0v/.claude/settings.json" ]; then
    cp "/home/stek0v/.claude/settings.json" "$DEST/claude_global_settings.json"
fi

echo ""
echo "Done! Saved to $DEST:"
ls -lh "$DEST"
echo ""
echo "Total size:"
du -sh "$DEST"
echo ""
echo "To restore on new machine, run:"
echo "  bash restore_project.sh $DEST"

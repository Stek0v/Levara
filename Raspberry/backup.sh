#!/bin/bash
set -euo pipefail

# ============================================================
# Cognevra Backup Script
# Usage: sudo ./backup.sh [backup_dir]
# ============================================================

BACKUP_BASE="${1:-/var/backups/cognevra}"
COGNEVRA_DIR="/var/lib/cognevra"
DB_PATH="$COGNEVRA_DIR/cognevra.db"
DATA_DIR="$COGNEVRA_DIR/data"

DATE=$(date +%Y%m%d_%H%M%S)
BACKUP_DIR="$BACKUP_BASE/$DATE"
RETENTION_DAYS=7

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log()  { echo -e "${GREEN}[backup]${NC} $(date '+%H:%M:%S') $1"; }
warn() { echo -e "${YELLOW}[backup]${NC} $(date '+%H:%M:%S') $1"; }
err()  { echo -e "${RED}[backup]${NC} $(date '+%H:%M:%S') $1"; exit 1; }

# --- Pre-checks ---
if [ ! -f "$DB_PATH" ]; then
    err "Database not found: $DB_PATH"
fi

if ! command -v sqlite3 &>/dev/null; then
    err "sqlite3 not installed. Run: sudo apt install sqlite3"
fi

# --- Create backup directory ---
mkdir -p "$BACKUP_DIR"
log "Backup directory: $BACKUP_DIR"

# --- 1. SQLite backup (online, WAL-safe) ---
log "Backing up SQLite database..."
sqlite3 "$DB_PATH" ".backup '$BACKUP_DIR/cognevra.db'"
if [ $? -eq 0 ]; then
    log "SQLite backup OK ($(du -h "$BACKUP_DIR/cognevra.db" | cut -f1))"
else
    err "SQLite backup failed!"
fi

# --- 2. HNSW data directory ---
if [ -d "$DATA_DIR" ]; then
    log "Backing up HNSW data..."
    rsync -a --quiet "$DATA_DIR/" "$BACKUP_DIR/data/"
    log "Data backup OK ($(du -sh "$BACKUP_DIR/data/" | cut -f1))"
else
    warn "Data directory not found: $DATA_DIR (skipping)"
fi

# --- 3. Config backup ---
if [ -f /etc/cognevra/cognevra.env ]; then
    log "Backing up config..."
    cp /etc/cognevra/cognevra.env "$BACKUP_DIR/cognevra.env"
fi

# --- 4. Create symlink to latest ---
ln -sfn "$BACKUP_DIR" "$BACKUP_BASE/latest"
log "Symlink updated: $BACKUP_BASE/latest"

# --- 5. Cleanup old backups ---
log "Cleaning backups older than ${RETENTION_DAYS} days..."
CLEANED=0
if [ -d "$BACKUP_BASE" ]; then
    for OLD_DIR in "$BACKUP_BASE"/[0-9]*; do
        if [ -d "$OLD_DIR" ] && [ "$OLD_DIR" != "$BACKUP_DIR" ]; then
            DIR_DATE=$(basename "$OLD_DIR" | cut -d_ -f1)
            if [ -n "$DIR_DATE" ]; then
                DIR_EPOCH=$(date -d "$DIR_DATE" +%s 2>/dev/null || date -j -f "%Y%m%d" "$DIR_DATE" +%s 2>/dev/null || echo "0")
                CUTOFF_EPOCH=$(date -d "-${RETENTION_DAYS} days" +%s 2>/dev/null || date -j -v-${RETENTION_DAYS}d +%s 2>/dev/null || echo "0")
                if [ "$DIR_EPOCH" -gt 0 ] && [ "$CUTOFF_EPOCH" -gt 0 ] && [ "$DIR_EPOCH" -lt "$CUTOFF_EPOCH" ]; then
                    rm -rf "$OLD_DIR"
                    CLEANED=$((CLEANED + 1))
                fi
            fi
        fi
    done
fi
if [ "$CLEANED" -gt 0 ]; then
    log "Cleaned $CLEANED old backup(s)"
fi

# --- Summary ---
TOTAL_SIZE=$(du -sh "$BACKUP_DIR" | cut -f1)
log "Backup complete: $BACKUP_DIR ($TOTAL_SIZE)"

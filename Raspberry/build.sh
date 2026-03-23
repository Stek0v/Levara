#!/bin/bash
set -euo pipefail

# ============================================================
# Cognevra Cross-Compile for Raspberry Pi (arm64)
# Run from Raspberry/ directory on your dev machine
# ============================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_DIR="$SCRIPT_DIR/../Cognevra"
OUTPUT_DIR="$SCRIPT_DIR"

if [ ! -d "$SOURCE_DIR" ]; then
    echo "Error: Cognevra source not found at $SOURCE_DIR"
    exit 1
fi

echo "=== Cross-compiling Cognevra for arm64 ==="

# Server binary
echo "Building cognevra-arm64 (server)..."
cd "$SOURCE_DIR"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
    go build -ldflags="-s -w" -o "$OUTPUT_DIR/cognevra-arm64" ./cmd/server/

# CLI binary
if [ -d "$SOURCE_DIR/cmd/cli" ]; then
    echo "Building cognevra-cli-arm64 (CLI)..."
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
        go build -ldflags="-s -w" -o "$OUTPUT_DIR/cognevra-cli-arm64" ./cmd/cli/
else
    echo "Skipping CLI (cmd/cli/ not found)"
fi

echo ""
echo "=== Build complete ==="
ls -lh "$OUTPUT_DIR"/cognevra-*arm64 2>/dev/null || echo "No binaries found!"
echo ""
echo "Deploy to Pi:"
echo "  scp $OUTPUT_DIR/cognevra-arm64 pi@raspberrypi.local:/usr/local/bin/cognevra"

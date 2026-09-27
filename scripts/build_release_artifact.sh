#!/usr/bin/env bash
# Builds the user-facing release artifact: product binaries only.
# Dev tooling (contract, audit, loadtest, benchmark, qwen3rerank,
# agent-hosts) and source trees (scripts/, benchmark/, webui/) stay out.
# The product-binary list is provisional until the owner confirms it (Ф1 T7).
set -euo pipefail
cd "$(dirname "$0")/.."

DIST=$(mktemp -d)
trap 'rm -rf "$DIST"' EXIT

# Product binaries (server + CLI are the minimum user-facing pair).
go build -o "$DIST/levara-server" ./cmd/server/
go build -o "$DIST/levara" ./cmd/cli/
go build -o "$DIST/levara-backup" ./cmd/backup/
go build -o "$DIST/levara-reconcile" ./cmd/reconcile/

# Runnable profile presets ship with the artifact.
mkdir -p "$DIST/profiles"
cp deploy/profiles/*.env.example "$DIST/profiles/"
cp LICENSE "$DIST/"

ARTIFACT="$PWD/levara-release.tar.gz"
tar czf "$ARTIFACT" -C "$DIST" .

# Assert the artifact carries nothing it must not carry.
bad=$(tar tzf "$ARTIFACT" | grep -E '(^|/)(scripts|benchmark|webui|\.github)/|loadtest|qwen3rerank|agent-hosts|(^|/)levara-contract$|(^|/)levara-audit$' || true)
if [ -n "$bad" ]; then
  echo "release artifact contains forbidden entries:" >&2
  echo "$bad" >&2
  exit 1
fi
echo "release artifact OK: $ARTIFACT"
tar tzf "$ARTIFACT"

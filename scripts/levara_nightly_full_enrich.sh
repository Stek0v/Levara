#!/usr/bin/env bash
# Nightly full Levara enrichment for existing projects under ~/src/*.
#
# This script is intended to be launched from cron. It is deliberately
# sequential and lock-protected because --mode full can invoke slow LLM/graph
# extraction. If a full run errors/timeouts, the batch stops by default to avoid
# stacking long-running cognify jobs.

set -u
set -o pipefail

PROJECT_ROOT="${PROJECT_ROOT:-$HOME/src}"
LEVARA_URL="${LEVARA_URL:-http://127.0.0.1:8081/api/v1}"
PYTHON_BIN="${PYTHON_BIN:-/usr/bin/python3}"
INGEST_SCRIPT="${INGEST_SCRIPT:-/Users/stek0v/src/levara/scripts/levara_project_ingest.py}"
LOG_ROOT="${LOG_ROOT:-$HOME/Library/Logs/levara/nightly-full-enrich}"
LOCK_DIR="${LOCK_DIR:-$HOME/.cache/levara/nightly-full-enrich.lock}"

PIPELINE="${PIPELINE:-all}"
MODE="${MODE:-full}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-21600}"
BRANCH="${BRANCH:-main}"
MAX_PROJECTS="${MAX_PROJECTS:-0}"
DRY_RUN="${DRY_RUN:-0}"
WRITE_AGENTS="${WRITE_AGENTS:-0}"
STOP_ON_CLASSIC_ERROR="${STOP_ON_CLASSIC_ERROR:-1}"

mkdir -p "$LOG_ROOT/reports" "$(dirname "$LOCK_DIR")"

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S %Z')" "$*"
}

slugify() {
  "$PYTHON_BIN" - "$1" <<'PY'
import re, sys
value = sys.argv[1].strip().lower()
value = re.sub(r"[^a-z0-9а-яё._-]+", "-", value, flags=re.IGNORECASE)
value = re.sub(r"-+", "-", value).strip("-._")
print(value or "project")
PY
}

classic_failed() {
  "$PYTHON_BIN" - "$1" <<'PY'
import json, sys
try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception:
    sys.exit(2)
classic = data.get("classic") or {}
if str(classic.get("status", "")).lower() == "error":
    print(classic.get("error", "classic enrichment failed"))
    sys.exit(1)
sys.exit(0)
PY
}

if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  log "another nightly full enrichment is already running: $LOCK_DIR"
  exit 0
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

if [ ! -d "$PROJECT_ROOT" ]; then
  log "project root not found: $PROJECT_ROOT"
  exit 1
fi

if [ ! -x "$PYTHON_BIN" ]; then
  log "python not executable: $PYTHON_BIN"
  exit 1
fi

if [ ! -f "$INGEST_SCRIPT" ]; then
  log "ingest script not found: $INGEST_SCRIPT"
  exit 1
fi

stamp="$(date '+%Y%m%d-%H%M%S')"
batch_log="$LOG_ROOT/nightly-full-$stamp.log"
count=0
failed=0

exec > >(tee -a "$batch_log") 2>&1

log "nightly full enrichment started"
log "project_root=$PROJECT_ROOT url=$LEVARA_URL pipeline=$PIPELINE mode=$MODE timeout_seconds=$TIMEOUT_SECONDS dry_run=$DRY_RUN write_agents=$WRITE_AGENTS"

for project in "$PROJECT_ROOT"/*; do
  [ -d "$project" ] || continue
  name="$(basename "$project")"
  case "$name" in
    .* ) continue ;;
  esac
  if [ -e "$project/.levara-no-nightly" ]; then
    log "skip $name: .levara-no-nightly"
    continue
  fi

  if [ "$MAX_PROJECTS" -gt 0 ] && [ "$count" -ge "$MAX_PROJECTS" ]; then
    log "max_projects reached: $MAX_PROJECTS"
    break
  fi
  count=$((count + 1))

  collection="$(slugify "$name")"
  report="$LOG_ROOT/reports/${stamp}_${collection}.json"
  log "project start: $name collection=$collection report=$report"

  args=(
    "$INGEST_SCRIPT"
    "$project"
    "--url" "$LEVARA_URL"
    "--collection" "$collection"
    "--project-id" "$collection"
    "--branch" "$BRANCH"
    "--pipeline" "$PIPELINE"
    "--mode" "$MODE"
    "--timeout-seconds" "$TIMEOUT_SECONDS"
    "--output" "$report"
    "--allow-empty"
  )

  if [ "$DRY_RUN" = "1" ]; then
    args+=("--dry-run")
  fi
  if [ "$WRITE_AGENTS" != "1" ]; then
    args+=("--no-agents")
  fi

  if "$PYTHON_BIN" "${args[@]}"; then
    log "project command finished: $name"
  else
    failed=$((failed + 1))
    log "project command failed: $name"
    continue
  fi

  if [ "$DRY_RUN" != "1" ] && [ "$STOP_ON_CLASSIC_ERROR" = "1" ] && classic_error="$(classic_failed "$report")"; then
    :
  else
    status=$?
    if [ "$DRY_RUN" != "1" ] && [ "$STOP_ON_CLASSIC_ERROR" = "1" ] && [ "$status" -eq 1 ]; then
      failed=$((failed + 1))
      log "classic full enrichment failed for $name: $classic_error"
      log "stopping batch to avoid stacking slow full cognify jobs"
      break
    elif [ "$status" -eq 2 ]; then
      failed=$((failed + 1))
      log "could not parse report for $name"
    fi
  fi

  log "project done: $name"
done

log "nightly full enrichment finished: projects_seen=$count failed=$failed log=$batch_log"
exit 0

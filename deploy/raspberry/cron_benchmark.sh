#!/bin/bash
# Levara mini-benchmark cron wrapper
# Install: crontab -e → 0 * * * * /home/stek0v/levara/deploy/raspberry/cron_benchmark.sh
#
# Environment variables (override in levara.env or here):
#   LEVARA_URL          - Levara endpoint (default: http://localhost:8080)
#   LEVARA_METRICS_DB   - SQLite path (default: /var/lib/levara/metrics.db)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LEVARA_HOME="${LEVARA_HOME:-/home/stek0v/levara}"
LOG_FILE="/var/log/levara-cron.log"

# Source environment if exists
if [ -f "${LEVARA_HOME}/deploy/raspberry/levara.env" ]; then
    set -a
    source "${LEVARA_HOME}/deploy/raspberry/levara.env"
    set +a
fi

export LEVARA_URL="${LEVARA_URL:-http://localhost:8080}"
export LEVARA_METRICS_DB="${LEVARA_METRICS_DB:-/var/lib/levara/metrics.db}"

# Ensure metrics DB directory exists
mkdir -p "$(dirname "${LEVARA_METRICS_DB}")"

# Run benchmark
cd "${LEVARA_HOME}"
python3 benchmark/cron_benchmark.py 2>> "${LOG_FILE}"

# Rotate log if > 10MB
if [ -f "${LOG_FILE}" ]; then
    LOG_SIZE=$(stat -f%z "${LOG_FILE}" 2>/dev/null || stat -c%s "${LOG_FILE}" 2>/dev/null || echo 0)
    if [ "${LOG_SIZE}" -gt 10485760 ]; then
        mv "${LOG_FILE}" "${LOG_FILE}.old"
        touch "${LOG_FILE}"
    fi
fi

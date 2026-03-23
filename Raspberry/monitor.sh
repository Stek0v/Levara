#!/bin/bash
set -euo pipefail

# ============================================================
# Cognevra Monitoring Script (cron-friendly)
# Usage: ./monitor.sh
# Cron:  */5 * * * * /usr/local/bin/cognevra-monitor
# ============================================================

LOG_FILE="/var/log/cognevra-monitor.log"
ALERT_FILE="/var/log/cognevra-alerts.log"
HEALTH_URL="http://localhost:8080/health"
CACHE_URL="http://localhost:8080/api/v1/cache/stats"
ERRORS_URL="http://localhost:8080/api/v1/errors"
METRICS_URL="http://localhost:8080/metrics"

TIMESTAMP=$(date '+%Y-%m-%d %H:%M:%S')
DISK_WARN_PERCENT=85
MEM_WARN_PERCENT=90

log()   { echo "[$TIMESTAMP] INFO  $1" >> "$LOG_FILE"; }
alert() { echo "[$TIMESTAMP] ALERT $1" >> "$ALERT_FILE"; echo "[$TIMESTAMP] ALERT $1" >> "$LOG_FILE"; }

# Ensure log files exist
touch "$LOG_FILE" "$ALERT_FILE" 2>/dev/null || true

# --- 1. Health Check ---
HEALTH_STATUS="FAIL"
HEALTH_RESPONSE=$(curl -sf --max-time 5 "$HEALTH_URL" 2>/dev/null || echo "")
if [ -n "$HEALTH_RESPONSE" ]; then
    STATUS=$(echo "$HEALTH_RESPONSE" | jq -r '.status' 2>/dev/null || echo "unknown")
    if [ "$STATUS" = "ok" ]; then
        HEALTH_STATUS="OK"
        log "health=OK"
    else
        HEALTH_STATUS="DEGRADED"
        alert "health=DEGRADED response=$HEALTH_RESPONSE"
    fi
else
    alert "health=UNREACHABLE — Cognevra not responding on $HEALTH_URL"
fi

# --- 2. Memory Usage ---
MEM_TOTAL=$(free -m | awk '/Mem/ {print $2}')
MEM_USED=$(free -m | awk '/Mem/ {print $3}')
MEM_PERCENT=$((MEM_USED * 100 / MEM_TOTAL))
SWAP_TOTAL=$(free -m | awk '/Swap/ {print $2}')
SWAP_USED=$(free -m | awk '/Swap/ {print $3}')

log "memory=${MEM_USED}MB/${MEM_TOTAL}MB (${MEM_PERCENT}%) swap=${SWAP_USED}MB/${SWAP_TOTAL}MB"

if [ "$MEM_PERCENT" -ge "$MEM_WARN_PERCENT" ]; then
    alert "HIGH MEMORY: ${MEM_PERCENT}% (${MEM_USED}MB/${MEM_TOTAL}MB)"
fi

# --- 3. Disk Usage ---
COGNEVRA_DISK=$(df /var/lib/cognevra 2>/dev/null | tail -1 | awk '{print $5}' | tr -d '%')
if [ -n "$COGNEVRA_DISK" ]; then
    log "disk=/var/lib/cognevra=${COGNEVRA_DISK}%"
    if [ "$COGNEVRA_DISK" -ge "$DISK_WARN_PERCENT" ]; then
        alert "HIGH DISK: /var/lib/cognevra at ${COGNEVRA_DISK}%"
    fi
fi

# --- 4. SQLite DB size ---
DB_PATH="/var/lib/cognevra/cognevra.db"
if [ -f "$DB_PATH" ]; then
    DB_SIZE=$(du -h "$DB_PATH" | cut -f1)
    log "sqlite_size=$DB_SIZE"
fi

# --- 5. Cache Stats ---
if [ "$HEALTH_STATUS" != "FAIL" ]; then
    CACHE_RESPONSE=$(curl -sf --max-time 5 "$CACHE_URL" 2>/dev/null || echo "")
    if [ -n "$CACHE_RESPONSE" ]; then
        HIT_RATE=$(echo "$CACHE_RESPONSE" | jq -r '.hit_rate // "N/A"' 2>/dev/null)
        CACHE_SIZE=$(echo "$CACHE_RESPONSE" | jq -r '.size // "N/A"' 2>/dev/null)
        log "cache_hit_rate=$HIT_RATE cache_size=$CACHE_SIZE"
    fi
fi

# --- 6. Error Count ---
if [ "$HEALTH_STATUS" != "FAIL" ]; then
    ERROR_RESPONSE=$(curl -sf --max-time 5 "$ERRORS_URL" 2>/dev/null || echo "")
    if [ -n "$ERROR_RESPONSE" ]; then
        ERROR_COUNT=$(echo "$ERROR_RESPONSE" | jq -r 'length // 0' 2>/dev/null || echo "0")
        if [ "$ERROR_COUNT" -gt 0 ]; then
            log "errors=$ERROR_COUNT"
            alert "ERRORS: $ERROR_COUNT error(s) reported"
        fi
    fi
fi

# --- 7. Cognevra process ---
COGNEVRA_PID=$(pgrep -f "/usr/local/bin/cognevra" 2>/dev/null || echo "")
if [ -n "$COGNEVRA_PID" ]; then
    COGNEVRA_RSS=$(ps -o rss= -p "$COGNEVRA_PID" 2>/dev/null | tr -d ' ')
    if [ -n "$COGNEVRA_RSS" ]; then
        COGNEVRA_MB=$((COGNEVRA_RSS / 1024))
        log "cognevra_pid=$COGNEVRA_PID rss=${COGNEVRA_MB}MB"
    fi
else
    if [ "$HEALTH_STATUS" = "FAIL" ]; then
        alert "Cognevra process NOT FOUND"
    fi
fi

# --- 8. Ollama check ---
OLLAMA_STATUS="FAIL"
if curl -sf --max-time 3 http://127.0.0.1:11434/api/tags > /dev/null 2>&1; then
    OLLAMA_STATUS="OK"
    log "ollama=OK"
else
    alert "ollama=UNREACHABLE"
fi

# --- 9. CPU temperature ---
if [ -f /sys/class/thermal/thermal_zone0/temp ]; then
    TEMP_RAW=$(cat /sys/class/thermal/thermal_zone0/temp)
    TEMP_C=$((TEMP_RAW / 1000))
    log "cpu_temp=${TEMP_C}C"
    if [ "$TEMP_C" -ge 80 ]; then
        alert "HIGH TEMPERATURE: ${TEMP_C}C"
    fi
fi

# --- 10. Log rotation (keep last 10000 lines) ---
if [ -f "$LOG_FILE" ]; then
    LINE_COUNT=$(wc -l < "$LOG_FILE")
    if [ "$LINE_COUNT" -gt 10000 ]; then
        tail -5000 "$LOG_FILE" > "${LOG_FILE}.tmp"
        mv "${LOG_FILE}.tmp" "$LOG_FILE"
    fi
fi

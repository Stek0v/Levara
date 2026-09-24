#!/bin/bash
# Watchdog для embed-сервиса Levara (:9101). launchd KeepAlive ловит только
# EXIT; этот скрипт ловит HANG (наблюдалось 2026-09-24: MPS busy-loop,
# 668% CPU 3 часа, TERM игнорировал): health не отвечает дольше 100 сек
# -> kill -9, launchd поднимает процесс заново. Двойная проверка даёт
# свежезапущенному процессу ~90 сек на загрузку модели.
LOG=/Users/stek0v/src/levara/data/logs/embed-local.log
NOTIFY="${LEVARA_EMBED_WATCHDOG_NOTIFY:-1}"
healthy() { curl -sS --max-time 5 http://127.0.0.1:9101/health 2>/dev/null | grep -q '"dim":'; }
if healthy; then exit 0; fi
sleep 90
if healthy; then exit 0; fi
PID=$(pgrep -f 'venv-embed/bin/uvicorn' | head -1)
if [ -n "$PID" ]; then
  echo "[$(date '+%F %T')] watchdog: health мёртв >100с, kill -9 $PID (launchd поднимет)" >> "$LOG"
  [ "$NOTIFY" = "1" ] && osascript -e 'display notification "embed-сервис завис — watchdog перезапускает" with title "Levara embed watchdog"' 2>/dev/null
  kill -9 "$PID" 2>/dev/null
fi

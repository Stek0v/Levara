# Cron-профили

Рекомендуемые расписания обслуживания Levara. Используй внешний cron (crontab, launchd, systemd timer) для вызова MCP-инструментов по HTTP.

## Вызов MCP-инструментов из cron

Все MCP-инструменты доступны через `POST /mcp` с JSON-RPC:

```bash
# Универсальный вызов MCP-инструмента
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "doctor",
      "arguments": {}
    }
  }'
```

## Профили

### Легковесный (персональный/dev)

```crontab
# Проверка здоровья каждые 15 минут
*/15 * * * * curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doctor","arguments":{}}}' >> /tmp/levara-doctor.log 2>&1

# Проверка очистки графа (dry run) ежедневно в 3:00
0 3 * * * curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"prune_graph","arguments":{"dry_run":true,"max_age_days":90}}}' >> /tmp/levara-prune.log 2>&1

# Проверка дрифта эмбеддингов еженедельно (воскресенье 2:00)
0 2 * * 0 curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_drift","arguments":{}}}' >> /tmp/levara-drift.log 2>&1
```

### Продакшн (сервер)

```crontab
# Проверка здоровья каждые 5 минут
*/5 * * * * /usr/local/bin/levara-doctor.sh

# Синхронизация Mac <-> Pi каждые 15 минут
*/15 * * * * curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sync","arguments":{"remote_url":"http://10.23.0.53:8080/api/v1","direction":"pull","types":["memories","interactions"]}}}' >> /tmp/levara-sync.log 2>&1

# Очистка графа (реальное удаление) еженедельно
0 3 * * 0 curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"prune_graph","arguments":{"dry_run":false,"max_age_days":90,"include_orphan_nodes":true}}}' >> /tmp/levara-prune.log 2>&1

# Проверка дрифта еженедельно
0 2 * * 0 curl -s -X POST http://localhost:8080/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_drift","arguments":{}}}' >> /tmp/levara-drift.log 2>&1
```

### Вспомогательный скрипт: `levara-doctor.sh`

```bash
#!/bin/bash
RESULT=$(curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doctor","arguments":{}}}')

STATUS=$(echo "$RESULT" | jq -r '.result.content[0].text' | jq -r '.status')

if [ "$STATUS" = "fail" ]; then
  echo "[$(date)] DOCTOR FAIL: $RESULT" >> /var/log/levara-alerts.log
  # Опционально: отправить уведомление
fi
```

## macOS launchd

На Mac используй `launchd` вместо cron:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.levara.doctor</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/levara-doctor.sh</string>
    </array>
    <key>StartInterval</key>
    <integer>900</integer>
    <key>StandardOutPath</key>
    <string>/tmp/levara-doctor.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/levara-doctor-err.log</string>
</dict>
</plist>
```

Установка: `cp com.levara.doctor.plist ~/Library/LaunchAgents/ && launchctl load ~/Library/LaunchAgents/com.levara.doctor.plist`

## Мониторинг истории heartbeat

Проверка недавней активности системы через MCP или REST:

```bash
# MCP-инструмент
curl -s -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"heartbeat","arguments":{"event_type":"doctor","limit":5}}}'

# REST-эндпоинт
curl -s http://localhost:8080/api/v1/heartbeats?type=doctor&limit=5
```

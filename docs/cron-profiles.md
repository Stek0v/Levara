# Cron-профили

Планировщик внешний: cron, launchd или systemd timer. Сервер не устанавливает
эти задания сам. Сначала проверьте один вызов на выбранном окружении, затем
настройте частоту, timeout и ротацию логов. [Deployment](deployment.md) описывает
эксплуатацию; [watchdog](macos-levara-watchdog.md) — отдельный macOS helper.

## Диагностика с проверкой MCP-ошибок

Пример тела запроса для `POST /mcp`:

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doctor","arguments":{}}}
```

Для cron создайте собственный скрипт с абсолютными путями, заданным origin и
credentials из защищённого environment-файла. В нём можно использовать:

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${LEVARA_ORIGIN:?set the backend origin without /api/v1}"
: "${LEVARA_TOKEN:?set a credential for the configured server}"
result=$(curl --fail-with-body --silent --show-error --max-time 30 \
  -H "Authorization: Bearer $LEVARA_TOKEN" \
  -H 'Content-Type: application/json' \
  "$LEVARA_ORIGIN/mcp" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doctor","arguments":{}}}')
printf '%s\n' "$result" | jq -e \
  'if .error or .result.isError == true then error("MCP tool failed") else .result end'
```

Этот wrapper требует Bash, curl и jq. Он проверяет transport/tool error, но
диагностические предупреждения внутри содержимого `doctor` требуют отдельного
анализа. Нельзя считать любой HTTP 200 здоровым состоянием. Не выводите token
в лог и не помещайте его непосредственно в общедоступный crontab.

## Расписание

| Работа | Начальная частота | Условие |
|---|---|---|
| Health и `doctor` | 5–15 минут | Настроены alert/dedup и ограничение времени |
| `check_drift` | Раз в неделю или после смены модели | Проверяются конкретные коллекции и embedding contract |
| Workspace jobs/status | По нужной задержке индексации | Используйте [workspace metrics](markdown-workspace-deployment-recipes.md) |
| Backup | По допустимой потере данных | Регулярно проверяется восстановление |
| Консолидация/удаление | Сначала ручной dry-run | Отдельно одобрены scope и retention |

Например, после создания и проверки своего wrapper:

```cron
*/15 * * * * /absolute/path/levara-doctor.sh >> /absolute/path/levara-doctor.log 2>&1
```

Не копируйте расписание реального `prune_graph(dry_run=false)` на общий сервер
как часть установки. Сначала исследуйте dry-run, backup и область удаления.
[Консолидация](features-guide.md#консолидация-памяти) имеет собственные guard и
revert; она не эквивалентна prune.

## Sync

Sync настраивается отдельно: точный `LEVARA_SYNC_REMOTE_URL`, credentials и
при включённой аутентификации активный глобальный superuser. Конфигурируемый
server token отправляется только на этот точный URL; перенаправления не
поддерживаются. [Workspace recipes](markdown-workspace-deployment-recipes.md#4-sync-between-machines)
показывает MCP-вызов и различие между sync записей, коллекций и truth-файлов.
У CLI нет команды `sync`, а личные shell-функции не поставляются как API.

## Heartbeats

Вызов `heartbeat(event_type="doctor", limit=5)` через MCP или
`GET /api/v1/heartbeats?type=doctor&limit=5` показывает доступную историю.
В shell заключайте URL с `&` в кавычки и передавайте credentials. История
событий не заменяет проверку результата конкретного текущего задания.

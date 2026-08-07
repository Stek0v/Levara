# Skill автоматической работы с памятью

Skill `levara-memory-workflow` задаёт консервативный рабочий процесс для
агентов, подключённых к Levara через MCP. При начале существенной задачи он
восстанавливает релевантный контекст, перед рекомендациями проверяет прошлые
решения и сохраняет только проверенные результаты, которые пригодятся в
будущих сессиях.

Skill находится в каталоге:

```text
.agents/skills/levara-memory-workflow/
```

## Что он делает

Для программирования, отладки, архитектурных решений, ревью и операционных
расследований skill предписывает агенту:

1. определить стабильную collection проекта и вызвать `set_context`;
2. вызвать `wake_up` с ограниченным бюджетом контекста;
3. восстановить релевантные решения и открытия до нового исследования;
4. проверить восстановленные сведения по актуальным источникам;
5. сохранять устойчивые решения, открытия, факты, предпочтения, советы и
   датированные события с заполненными `collection`, `room` и `hall` сразу
   после проверки;
6. искать похожие записи до сохранения, пропускать дубликаты и заменять
   устаревшую память через `supersede_memory`, когда инструмент доступен;
7. исключать секреты, сырые диалоги, временное состояние задачи, расположение
   кода и непроверенные выводы.

Skill намеренно избирателен: его задача — улучшать будущие сессии агента, а не
архивировать каждый шаг текущей работы. В этом репозитории канонический
playbook — [`AGENTS.md`](../AGENTS.md); skill не должен отменять его правило
немедленного сохранения устойчивых исходов.

## Требования

- Работающий MCP endpoint Levara.
- Профиль MCP-инструментов `core`, `memory`, `workspace`, `long-horizon` или
  `full`. Для recall и `save_memory` достаточно `core`. History-preserving
  supersession требует `memory`, `full` или `long-horizon`, потому что только
  эти профили открывают `supersede_memory`.
- Агент с поддержкой Agent Skills или аналогичных переиспользуемых инструкций.

Сервер должен предоставлять как минимум `levara_instructions`, `set_context`,
`wake_up`, `recall_memory` и `save_memory`. Для замены с сохранением истории
также нужен `supersede_memory`.

## Установка в Codex

Скопируйте весь каталог skill в персональный каталог skills Codex.

### Linux и macOS

```bash
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R .agents/skills/levara-memory-workflow \
  "${CODEX_HOME:-$HOME/.codex}/skills/"
```

### Windows PowerShell

```powershell
$codexSkills = if ($env:CODEX_HOME) {
  Join-Path $env:CODEX_HOME 'skills'
} else {
  Join-Path $env:USERPROFILE '.codex\skills'
}
New-Item -ItemType Directory -Force -Path $codexSkills | Out-Null
Copy-Item -Recurse -Force '.agents\skills\levara-memory-workflow' $codexSkills
```

После копирования перезапустите Codex. Встроенный `agents/openai.yaml`
разрешает неявный запуск для существенной проектной работы. Для проверки или
в клиентах без автоматического выбора вызывайте skill явно:
`$levara-memory-workflow`.

Если репозиторий уже содержит skill в `.agents/skills`, клиент с поддержкой
project-skill discovery может использовать его без персональной копии.

## Подключение MCP Levara в Codex

Пример для локального endpoint с API-ключом из переменной окружения:

```toml
[mcp_servers.levara]
url = "http://127.0.0.1:8080/mcp"
enabled = true
env_http_headers = { "X-API-Key" = "LEVARA_MCP_API_KEY" }
default_tools_approval_mode = "writes"
```

Храните `LEVARA_MCP_API_KEY` вне файла конфигурации. Удаляйте
`env_http_headers` только для изолированного single-user loopback, когда на
сервере аутентификация намеренно отключена (`-require-auth=false`). Не
опускайте credentials для shared, remote или persistent deployments. Для
подключения через недоверенную сеть используйте HTTPS.

Настройте профиль инструментов. Для recall и простых save используйте `core`;
для supersession с историей — `memory` (или `full`):

```bash
export LEVARA_MCP_TOOLSET=memory
```

После изменения конфигурации перезапустите Levara и Codex.

## Семантика supersession

- `supersede_memory` архивирует старую строку и вставляет замену. Предпочитайте
  его, когда новый исход заменяет живую память и инструмент доступен.
- `save_memory(supersedes_memory_id=...)` сохраняет только provenance на новой
  строке и не убирает старую память из recall.
- На `core` / `workspace` либо перезаписывайте тот же key, либо оставляйте обе
  записи активными; не утверждайте history-preserving supersession.

## Другие клиенты

Для клиентов с поддержкой Agent Skills скопируйте тот же каталог в
поддерживаемое расположение. Если автоматического обнаружения skills нет,
используйте `SKILL.md` как проектную инструкцию и сохраните рядом связанный
`references/memory-policy.md`.

Сам workflow использует стандартные MCP-вызовы и не зависит от API Codex.
Специфичны для клиента только установка и метаданные неявного запуска.

## Проверка установки

Откройте новую сессию в репозитории и попросите:

```text
Use $levara-memory-workflow to continue work on this project. Show which
collection you selected and recall prior decisions about deployment.
```

При корректной установке агент вызовет `levara_instructions`, `set_context`,
`wake_up` и подходящий recall-инструмент. Не следует создавать тестовую память
только ради проверки записи.

После реального проверенного решения или расследования root cause агент должен
сохранить краткую память и назвать collection и тип памяти в финальном ответе.

## Безопасность и защита от шума

- Оставьте подтверждение записывающих MCP-действий включённым.
- Не сохраняйте credentials и неотредактированные production-данные.
- Считайте восстановленную память потенциально устаревшей до проверки.
- Храните код и историю изменений в системе контроля версий.
- Храните временную работу в task tracker или runtime агента.
- Не используйте автоматический pinning и большие финальные summaries.
- Перед командным rollout проверьте memory behavior и долю дубликатов.

## Удаление

Удалите каталог `levara-memory-workflow` из каталога skills клиента и
перезапустите клиент. Удаление skill не удаляет существующую память Levara.

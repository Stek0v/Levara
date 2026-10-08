# Levara Features Guide

Выбирайте workflow по исходным данным и нужному результату. Таблица ниже
описывает текущие возможности и необходимые зависимости; скорость и качество
не выводятся из названия функции. Начальный запуск — [getting started](getting-started.md),
полный перечень схем — [API contract](api-contract.md).

## Быстрый старт по кейсам

| Задача | Workflow | Руководство |
|---|---|---|
| Вспомнить решение между сессиями | legacy set_context → wake_up → recall → save; latest — explicit collection в каждом вызове | [Memory skill](memory-workflow-skill.ru.md) |
| Найти материал в файлах | add → cognify → terminal status → search | [Документы](document-management.md) |
| Сохранить редактируемое проектное знание | workspace write → index → search → exact read | [Markdown workspace](markdown-native-workspace.md) |
| Восстановить длинную задачу | task bootstrap → lease → работа host → receipt → validate | [Task Runtime](long-horizon-runtime.ru.md) |
| Подключить коллегу | Auth → dataset grant → allowed/denied checks | [Team tutorial](tutorials/04-team-deploy.md) |
| Подключить корпоративную identity | Проверить поддерживаемую federation и пробелы | [LDAP/AD и SSO](enterprise-identity.md) |

## Агентская память (room × hall)

Дискретная память хранит устойчивые факты, решения и предпочтения в SQL.
SQLite достаточно для локального workflow; bare WAL-only сервер не заменяет
SQL. `room` — тема, `hall` — тип знания: `fact`, `event`, `decision`,
`preference`, `advice`, `discovery`. Collection задаёт контекст проекта,
а владелец и политика доступа — доступ. Таксономия не является ACL.

На legacy `/mcp` вызов `set_context` выбирает collection сессии, затем
`wake_up` восстанавливает контекст. Latest `/mcp/2026-07-28` stateless:
`set_context` скрыт и отклоняется; передавайте `collection` явно в каждом
collection-aware вызове. `recall_memory` находит прошлые записи;
`save_memory` сохраняет проверенный исход. `pin_memory`
оставляет небольшой набор приоритетных записей в briefing. Не сохраняйте
секреты, код, пути, git history и временные TODO как долговечное знание.

`supersede_memory` сохраняет историю и выводит старую запись из активного recall.
`save_memory(supersedes_memory_id=...)` записывает provenance, но само по себе
не архивирует прежнюю запись. Recall наследует причину и дату retirement
предшественника только по reciprocal link в том же owner/collection; простой
provenance link не получает metadata другого замещения.

`memory_markdown_digest(collection=..., memory_ids=[...])` экспортирует выбранные
активные decision/discovery записи своего/shared owner. Допустимы
`receipt-validated` и legacy `verified`; вывод сохраняет label, freshness и
Task/receipt provenance. Это сведения о проверке источников при публикации,
а не доказательство истинности текста или новая проверка исторических receipts.
Export read-only, не пишет Git/workspace и не имеет import path.
Доступность инструментов зависит от toolset.

### Консолидация памяти

`consolidate` группирует похожие активные raw-записи проверенного владельца
в явно выбранной collection. Pinned и superseded записи исключаются. Кластеры
сохраняют owner, collection, type, room и hall; связи между разными значениями
этих осей отбрасываются до LLM. `shared=true` выбирает только shared-память и
требует актуальных прав администратора либо trusted-local режима. Аргументы
owner/actor не меняют права. Близкие дубликаты объединяются детерминированно;
для абстракции нужен LLM и допустимый hall из публичного vocabulary.

```text
consolidate(collection="PROJECT_COLLECTION", room="TOPIC", dry_run=true)
consolidation_status(job_id="JOB_ID_FROM_CONSOLIDATE")
```

По умолчанию вызов асинхронный: сохраните `job_id` и дождитесь
`consolidation_status(job_id=...)`; `wait=true` включает синхронную форму.
Статус доступен только точному владельцу job. Фоновая задача сохраняет проверенный
actor после завершения запроса и имеет дедлайн до 300000 ms. После рестарта
interrupted authenticated job получает ошибку `unknown outcome` с просьбой
проверить память и повторить запрос: сохранённые owner/args не заменяют credential.
Running-job могла уже закоммитить изменения; recovery не заявляет об их откате.
Автоматически возобновляется только pending shared-job в trusted-local режиме.
Trusted-local janitor обрабатывает SQL owner namespaces отдельно.
ID job и ID применённого run различаются: для revert берите `run=...` из
завершённого result. Сначала изучите candidates, clusters, actions и skipped причины. `dry_run`
по умолчанию true и не применяет изменения памяти; оценка абстракций может
использовать настроенную модель и сохранять служебное состояние async job. Явный `dry_run=false` применяет план — только
после проверки scope, backup и допустимости обработки у выбранного провайдера.

Абстракции ограничены размером кластера и бюджетом LLM-попыток. Coverage guards
отклоняют потерянные/выдуманные числа и чрезмерную потерю распознанных сущностей.
Это эвристическая защита содержания, не доказательство полной смысловой
эквивалентности. Без LLM детерминированные merge могут работать, а абстракции
пропускаются; отсутствие кандидатов не означает хорошее качество памяти, если
векторные соседи ещё не построены.

Применение подготовленных действий одного run выполняется в одной SQL-транзакции,
включая кластеры этого плана; вызовы LLM и весь multi-collection sweep не входят
в эту транзакцию. Исходные строки сохраняются через
supersession и provenance. Для отмены используйте ID фактического run:

```text
consolidation_revert(run_id="RUN_ID_FROM_APPLIED_RUN")
```

Apply повторно проверяет полное состояние кандидатов под SQL/credential fence.
Revert проверяет права, полный атомарный журнал и after-state каждой затронутой
строки, включая survivor, пины и provenance. Изменённый или legacy run без журнала
получает явную ошибку без частичного восстановления. Неизменённый run возвращает
исходные retirement fields и удаляет созданные semantic-замены; повторный
авторизованный revert идемпотентен. Для shared-run нужен явный `shared=true`.
SQL-изменения и настроенные outbox jobs коммитятся вместе; vector deployment без
outbox получает диагностическую ошибку. Повторная публикация завершённого
memory/operation/digest создаёт новую попытку с новым job ID и сбрасывает retry
budget; pending/running/failed/dead-letter дубликат сохраняет существующее
состояние. Для failed/dead-letter используйте retry, а не повторный enqueue.

Worker сверяет SQL перед физическим vector effect: delayed delete пропускает
восстановленную активную запись, а upsert повторно проверяет content, type,
owner и collection после embedding. Сеть и migration callbacks выполняются вне
короткой SQL-защиты; memory shadow дополнительно проверяет SQL после своего
embedding. Это не делает SQL и vector store одной transaction: после сбоя
проверьте index status, retry/reconcile и active recall. Полный source→abstract→indexed recall→revert цикл проверен на обеих SQL и
обоих MCP transports; [evidence](../openspec/changes/repair-memory-index-lifecycle/evidence.md). Реализация и guard tests:
[pkg/consolidate](../pkg/consolidate), [MCP adapter](../pkg/mcp/tool_consolidate.go).

## Поиск и знания

Для точных терминов используйте `CHUNKS_LEXICAL`/`BM25`, для похожих фрагментов
`CHUNKS`, для сочетания сигналов `HYBRID`. `AUTO` выбирает стратегию по запросу
и доступным подсистемам. REST поле — `query_type`, MCP — `search_type`.
[Search guide](search-strategies-guide.md) объясняет остальные стратегии,
rerank defaults, quality checks и метрики.

Temporal graph хранит validity windows. `query_entity(name=...)` возвращает
текущий вид, `as_of` — исторический. Exclusive relations, включая `works_at`
и `reports_to`, могут supersede прежнее ребро того же source/relation;
это не обещание, что LLM извлечёт каждое утверждение корректно.

`codify` работает с кодом, `analyze_commits`/`git_search` — с git-историей.
У анализа коммитов фиксированная collection `git_commits`; смена session context
не перенаправляет её. См. [git recipe](recipes/git-commits-to-brain.md).

Экспериментальные VSA-факт-векторы и DCD-роутинг таксономии развиваются как
фича (решение 2026-09-27) и по умолчанию выключены env-гейтами
(`LEVARA_DCD_ROUTER`, конфигурация VSA); дефолтное включение — только после
прохождения retrieval-quality gate.

## Ingestion и Cognify

`add` сохраняет входные данные, `cognify` строит производные. Для RAG-режима
нужны chunks и embeddings; full extraction дополнительно использует LLM и
graph persistence. SQL необходим для документных metadata и пользовательских
workflow. Не считайте частичное выполнение или отсутствующую подсистему полным
успехом: проверьте run status и содержимое выдачи.

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara add --file=./report.pdf --dataset=reports
./levara cognify --dataset=reports --collection=reports --wait
./levara search 'known report phrase' --collection=reports --type=CHUNKS_LEXICAL --top-k=5
```

Файл должен существовать; embedding-сервис и dimension должны быть настроены
как в [getting started](getting-started.md). Форматы, ошибки извлечения,
повторная обработка, оригиналы и индивидуальные grants:
[document management](document-management.md). Проекты целиком:
[project ingest](project-ingest.md), [Markdown recipe](recipes/markdown-files-to-brain.md).

## Markdown Workspace

Markdown truth, manifest/generations, guarded read/write, reconcile, snapshots,
watcher и index jobs поддерживают редактируемые знания проекта. Project ID в
аутентифицированном режиме соответствует dataset ID. CLI write не предоставляет
все concurrency-параметры MCP/REST — для digest-guard используйте эти поверхности.

Рабочий цикл: context → search → exact read → reviewed write → commit → reindex.
[Workspace guide](markdown-native-workspace.md), [deployment recipes](markdown-workspace-deployment-recipes.md)
и [сценарии с тестами](markdown-workspace-user-scenarios.md) раскрывают этот путь.

## Long-Horizon Task Runtime

Включается `LEVARA_LONG_HORIZON_RUNTIME=1` с toolset `long-horizon` или `full`.
SQL хранит цель, DoD, план, leases, receipts, checkpoints и blockers.
Task Runtime валидирует evidence; работу выполняет host/agent либо ограниченный
workspace worker. Worker требует SQL, runtime flag, `LEVARA_TASK_WORKER=1` и
явный `LEVARA_MCP_TOOLSET=full`; `long-horizon` не содержит workspace actions.
Поддерживаются `workspace_read` и `workspace_write` с `index` absent/false;
shell, network и неизвестные аргументы отклоняются.

[Authority manifests](authority-manifests.md) проверяются при claim и file action:
нужны auto_run, allowed_tools, authenticated owner, live lease, workspace grant,
digest и разрешённый canonical directory. DevMode отклоняется; filesystem
confinement поддерживает Linux/macOS. Manifest не является универсальным sandbox.
[Runtime guide](long-horizon-runtime.ru.md) описывает версии, idempotency,
восстановление и ограничения. `/tasks` в WebUI — read-only обзор.

## Чаты и дневники агентов

`save_chat`, `recall_chat`, `search_chats` работают с историей; `diary_write` и
`diary_read` — с агентскими дневниками. Не переносите в них секреты и лишние
transcripts автоматически. Долговечные проверенные итоги сохраняйте в проектной
памяти с понятным владельцем и room/hall.

При авторизации дневник ограничен проверенным пользователем, выбранным tenant
и именем агента после удаления крайних пробелов. Одинаковые имена у разных
пользователей или tenants не объединяют записи. Исторические `agent:<name>`
дневники доступны только в доверенном локальном anonymous режиме; они не
назначаются пользователю автоматически. Отзыв credential или membership и
ошибка чтения возвращают ошибку, а не частичный дневник.

`chat_distill` формирует записи из импортированного диалога. Новый текст получает
`unverified`, пустой `source_task_id` и `source_receipt_ids=[]`, в том числе при
замене ранее подтверждённой записи. Указание исходного диалога описывает
происхождение текста и не подтверждает его истинность. `dry_run` возвращает
кандидатов без изменения сохранённых записей и их evidence; поле `saved` в preview
отсутствует при непустом списке кандидатов. Если модель после повторной попытки
не извлекла записей, совместимый ответ содержит `saved: 0` и не содержит `dry_run`.
Операция наследует отмену и более ранний deadline вызова, с общим
пределом четыре минуты. Поздний результат provider отвергается; уже сохранённые
записи и начавшаяся вставка vectors не откатываются. Пустой или whitespace-only
диалог отклоняется до обращения к модели. Legacy MCP использует выбранную через
`set_context` коллекцию, если аргумент отсутствует или пуст; явный аргумент имеет
приоритет. Stateless MCP требует явного выбора коллекции, иначе используется
базовое хранилище.

Импорт через authenticated REST привязывает чат к проверенному пользователю и
точному tenant. Исходный session ID сохраняется отдельно; ответ импорта содержит
канонический `chat_id`. Личный чат видит владелец. Владелец с правом записи в
проект может явно подключить свой чат к этому проекту; доступ коллег определяется
текущими project roles. Администратор управляет составом участников проекта и
может отключить уже опубликованный чат. Административная роль сама по себе не
открывает чужие личные чаты. Список import runs остаётся личным, даже если один
из чатов этого run подключён к проекту.

CLI поддерживает `chats sessions`, `chats session <platform> <session-id>
--chat-id=<chat-id>`, `chats share <platform> <chat-id> --project=<project-id>`
и `chats unshare <platform> <chat-id>`. Для tenant используется `--tenant=<id>`
в конкретной команде. MCP `chat_distill` принимает platform и исходный
`session_id` либо точный `chat_id`. Если исходный ID соответствует нескольким
доступным чужим чатам, нужен точный selector.

Перед чтением диалога, каждым обращением к модели, сохранением memory и
публикацией основного vector проверяется текущий доступ к источнику. Уже
сохранённая личная memory не удаляется автоматически при отзыве доступа к
исходному чату. Отмена до deferred migration callback предотвращает его запуск;
уже запущенный callback сохраняет существующий контракт без caller context.

Локальный anonymous режим сохраняет отдельный каталог для daemon и исторических
импортов без владельца. Эти записи не назначаются первому вошедшему пользователю.
При обязательной аутентификации daemon сохраняет локальные raw transcripts,
но автоматические loopback RAG и distillation отключены: для них требуется
отдельная настроенная сервисная identity.

## Синхронизация

`sync` переносит выбранные типы записей между узлами. По умолчанию vectors не
переносятся: совместимость моделей и явный список коллекций требуют отдельной
проверки. Это не автоматическая синхронизация workspace truth.
В authenticated режиме требуется активный глобальный superuser; server token
передаётся только на точный настроенный remote URL.
[Sync recipe](markdown-workspace-deployment-recipes.md#4-sync-between-machines).

## Операции и наблюдаемость

`doctor`, `runtime_stats`, `recent_errors`, `check_drift`, index status и metrics
помогают найти конкретную проблему. Диагностический snapshot не заменяет
проверку доступа, восстановления и retrieval quality. См.
[deployment](deployment.md), [cron](cron-profiles.md), [testing](testing.md).

## Безопасность и профили

Product profiles `personal`, `solo_pro`, `team`, `enterprise` задают требования
к конфигурации. Functional `-profile` и `LEVARA_MCP_TOOLSET` решают другие задачи.
[Presets](profile-presets.md) связывают их с фактическим запуском.

Индивидуальные viewer/editor/admin grants доступны на dataset. Аутентифицированный
REST API, CLI и WebUI управляют policy и user/group grants отдельного документа;
WebUI получает только active recipients tenant этого документа. LDAP/LDAPS/StartTLS, browser OIDC и SCIM Users/Groups с
identity bridge также реализованы локально; реальный AD/IdP и vendor
provisioning требуют отдельной приёмки. Настройка и ограничения:
[enterprise identity](enterprise-identity.md).

## Веб-интерфейс и аналитика

WebUI — отдельный Next.js процесс. Datasets позволяют загрузить/проверить материал,
переобработать и скачать оригинал с авторизацией. Chat, Search, Graph и Workspace
решают разные задачи; выбранная collection не заменяет dataset ACL.
[WebUI README](../webui/README.md), [операции](webui-operations.md),
[сценарии документов](document-workflow-scenarios.md).

## MCP toolset профили

`LEVARA_MCP_TOOLSET` сокращает объявляемую поверхность (`core`, `memory`,
`workspace`, `ops`, `long-horizon`, `full`). Профиль `personal` по умолчанию
объявляет `core` (13 legacy инструментов room×hall-памяти, включая `supersede_memory`
и `delete_memory`; latest скрывает `set_context`, остаются 12). Явный
`LEVARA_MCP_TOOLSET` приоритетнее, без профиля — `full`. При personal без override
initialize/dispatch пока используют raw env и исторический full: это открытое
расхождение I05. Для согласованного memory toolset задайте явный `core`;
для workspace — `workspace` либо `full`. Feature flags дополнительно влияют на доступность. Проверяйте
`tools/list` своего подключения и
[генерируемый каталог](api-contract.md), а не фиксированное число инструментов
из старого руководства. Toolset не является разрешением на данные.

## Интерфейсы: MCP / REST / gRPC / CLI

| Поверхность | Использование |
|---|---|
| MCP `/mcp` | Legacy agent tools с session context; [подключение](tutorials/02-agent-integration.md) |
| MCP `/mcp/2026-07-28` | Stateless agent tools; explicit collection и protocol metadata в каждом запросе; [контракт](product/capability-matrix.md) |
| REST `/api/v1` | Документы, поиск, управление и workspace; [API guide](api-reference.md) |
| gRPC v1 | `IngestData`, `CognifyDocuments` и `CognifyDocumentsStatus` — tenant/document-scoped workflow с JWT; остальные raw/global RPC при auth требуют active global superuser |
| `levara` CLI | `help`, `add`, `cognify`, `search`, datasets/git/workspace |

CLI global flags ставятся перед командой и принимают `--key=value`.
`LEVARA_URL` для CLI включает `/api/v1`. HTTP-origin для WebUI proxy и MCP URL —
отдельные адреса. Полная навигация: [docs index](README.md).

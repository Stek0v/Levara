# Levara Features Guide

Выбирайте workflow по исходным данным и нужному результату. Таблица ниже
описывает текущие возможности и необходимые зависимости; скорость и качество
не выводятся из названия функции. Начальный запуск — [getting started](getting-started.md),
полный перечень схем — [API contract](api-contract.md).

## Быстрый старт по кейсам

| Задача | Workflow | Руководство |
|---|---|---|
| Вспомнить решение между сессиями | set_context → wake_up → recall_memory → verified save | [Memory skill](memory-workflow-skill.ru.md) |
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

`set_context` и `wake_up` восстанавливают сессию; `recall_memory` находит
прошлые записи; `save_memory` сохраняет проверенный исход. `pin_memory`
оставляет небольшой набор приоритетных записей в briefing. Не сохраняйте
секреты, код, пути, git history и временные TODO как долговечное знание.

`supersede_memory` сохраняет историю и выводит старую запись из активного recall.
`save_memory(supersedes_memory_id=...)` записывает provenance, но само по себе
не архивирует прежнюю запись. Доступность инструментов зависит от toolset.

### Консолидация памяти

`consolidate` группирует похожие активные raw-записи выбранной collection.
Pinned и уже superseded записи исключаются. Близкие дубликаты могут объединяться
детерминированно; для абстракции связанных записей нужен LLM.

```text
consolidate(collection="PROJECT_COLLECTION", room="TOPIC", dry_run=true)
consolidation_status(job_id="JOB_ID_FROM_CONSOLIDATE")
```

По умолчанию вызов асинхронный: сохраните `job_id` и дождитесь
`consolidation_status(job_id=...)`; `wait=true` включает синхронную форму.
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

Revert транзакционно возвращает соответствующие исходные записи и удаляет
созданные semantic-замены. После применения/отката проверьте активный recall
и состояние индекса; это не удаление всего проекта. Реализация и guard tests:
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
Task Runtime валидирует evidence, а полезную работу выполняет host/agent.
Встроенный опциональный worker сейчас подключён к logging executor и не
подтверждает реальное выполнение задачи.

[Authority manifests](authority-manifests.md) поддерживают digest binding при
claim; per-tool/path/network helpers не являются универсально включённым sandbox.
[Runtime guide](long-horizon-runtime.ru.md) описывает версии, idempotency,
восстановление и ограничения. `/tasks` в WebUI — read-only обзор.

## Чаты и дневники агентов

`save_chat`, `recall_chat`, `search_chats` работают с историей; `diary_write` и
`diary_read` — с агентскими дневниками. Не переносите в них секреты и лишние
transcripts автоматически. Долговечные проверенные итоги сохраняйте в проектной
памяти с понятным владельцем и room/hall.

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
REST API также управляет policy и user/group grants отдельного документа;
WebUI пока показывает только grants набора. LDAP/LDAPS/StartTLS, browser OIDC и SCIM Users/Groups с
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
`workspace`, `ops`, `long-horizon`, `full`). Feature flags дополнительно влияют
на доступность. Проверяйте `tools/list` своего подключения и
[генерируемый каталог](api-contract.md), а не фиксированное число инструментов
из старого руководства. Toolset не является разрешением на данные.

## Интерфейсы: MCP / REST / gRPC / CLI

| Поверхность | Использование |
|---|---|
| MCP `/mcp` | Agent tools с session context; [подключение](tutorials/02-agent-integration.md) |
| REST `/api/v1` | Документы, поиск, управление и workspace; [API guide](api-reference.md) |
| gRPC | Raw-storage API; при auth нужен активный global superuser |
| `levara` CLI | `help`, `add`, `cognify`, `search`, datasets/git/workspace |

CLI global flags ставятся перед командой и принимают `--key=value`.
`LEVARA_URL` для CLI включает `/api/v1`. HTTP-origin для WebUI proxy и MCP URL —
отдельные адреса. Полная навигация: [docs index](README.md).

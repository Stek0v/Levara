# Levara Features Guide

Полный справочник возможностей Levara: назначение, кейсы и команды для каждой
фичи. Для разных аудиторий: агент (MCP-инструменты), оператор (REST/CLI),
разработчик (примеры и контракты).

> Источники истины: сгенерированный контракт `docs/api-contract.md`
> (`docs/contract.json` — 82 MCP tools, 144 REST routes, 45 gRPC methods),
> `docs/product-ladder.md` (границы продукта), `docs/current-state.md`
> (проверенный локальный снимок). Этот гайд группирует поверхность по
> пользовательским кейсам.

---

## Содержание

1. [Быстрый старт по кейсам](#быстрый-старт-по-кейсам)
2. [Агентская память (room × hall)](#агентская-память-room--hall)
3. [Поиск и знания](#поиск-и-знания)
4. [Ingestion и Cognify](#ingestion-и-cognify)
5. [Markdown Workspace](#markdown-workspace)
6. [Long-Horizon Task Runtime](#long-horizon-task-runtime)
7. [Чаты и дневники агентов](#чаты-и-дневники-агентов)
8. [Синхронизация](#синхронизация)
9. [Операции и наблюдаемость](#операции-и-наблюдаемость)
10. [Безопасность и профили](#безопасность-и-профили)
11. [Веб-интерфейс и аналитика](#веб-интерфейс-и-аналитика)
12. [MCP toolset профили](#mcp-toolset-профили)
13. [Интерфейсы: MCP / REST / gRPC / CLI](#интерфейсы-mcp--rest--grpc--cli)
14. [Карта документации](#карта-документации)

---

## Быстрый старт по кейсам

| Кейс | Что использовать | Быстрая команда |
|---|---|---|
| «Запомни решение по проекту» | Agent memory | `save_memory(key=..., value=..., room=..., hall="decision")` |
| «Что мы решили про auth?» | Recall | `recall_memory(query="auth", hall="decision")` |
| «Найди похожий код/документ» | Hybrid search | `search(search_query="...", search_type="AUTO")` |
| «Загрузи репозиторий/документы» | Ingest + Cognify | `add(source=...)` → `cognify(dataset=...)` |
| «Веди проектную документацию в .md» | Workspace | `workspace_context` → `workspace_write` → `workspace_commit` |
| «Долгая задача с проверяемым результатом» | Task Runtime | `task_open` → `task_plan` → `task_step` → `task_receipt` → `task_complete` |
| «Синхронизировать Mac ↔ сервер» | Sync | `sync(direction="push")` / `sync_status` |
| «Здоровье системы» | Ops | `doctor`, `runtime_stats`, `recent_errors` |

---

## Агентская память (room × hall)

Модель: каждая запись памяти живёт в **room** (подсистема/тема: `auth`,
`deploy`, `mcp`, …) × **hall** (тип факта — контролируемый словарь:
`fact`, `event`, `decision`, `preference`, `advice`, `discovery`).
Пустые room/hall убивают точность recall — заполняются всегда.

Ключевые свойства:

- Upsert-идентичность задаёт `(key, owner_id, collection_name)` — один ключ
  может существовать в разных контекстах (P1-изоляция).
- Пины: `pin_memory` с priority 1–10 поднимает запись в `wake_up`.
- Supersession: `supersede_memory` заменяет активное значение с сохранением
  provenance (`supersedes_memory_id`, `supersession_reason`).
- Консолидация: `consolidate` схлопывает кластеры близких записей в одну
  семантическую (tier `raw|consolidated|semantic`), `consolidation_revert`
  откатывает — операция обратимая.
- Транзакционный Memory Commit (`memory_commit_preview`/`memory_commit_apply`,
  флаг `LEVARA_MEMORY_COMMIT`) — атомарное применение набора правок памяти.

### Команды

| Задача | MCP tool |
|---|---|
| Сохранить факт/решение | `save_memory` |
| Найти по смыслу+фильтрам | `recall_memory` |
| Список с фильтрами room/hall | `list_memories` |
| Пин/анпин | `pin_memory`, `unpin_memory` |
| Заменить значение с provenance | `supersede_memory` |
| Удалить | `delete_memory` |
| Утренний брифинг | `wake_up` |
| Консолидация | `consolidate`, `consolidation_status`, `consolidation_revert` |
| Сад/марковский обзор памяти | `memory_garden` |
| Markdown-дайджест памяти | `memory_markdown_digest` |
| Блок-скаффолд для скиллов агента | `memory_scaffold_block` |

REST: `/memories` (CRUD), `/memory-reviews`, `/memory-scaffold/proposals`,
`/memory-behavior`, `/memory-traces/export`.

Гайд по автоматическому workflow памяти: `docs/memory-workflow-skill.md`.

---

## Поиск и знания

Гибридный поиск: HNSW (vector, WAL-backed) + BM25 (lexical, персистентные
снапшоты) + RRF-фьюжн. Роутер (`AUTO`) за ~50–200μs по лёгким сигналам
выбирает стратегию; адаптивные веса настраиваются по feedback
(`routing_weights`).

### Стратегии (кратко)

| Стратегия | Backend | LLM | Когда |
|---|---|---|---|
| `CHUNKS` | HNSW | нет | Дефолт «найди похожее» |
| `CHUNKS_LEXICAL` | BM25 | нет | Точные термины, ID, код |
| `HYBRID` | HNSW+BM25+RRF | нет | Лучший quality/latency |
| `TEMPORAL` | HNSW+KG | нет | Запросы с датами |
| `SUMMARIES` | HNSW | нет | Навигация по большому корпусу |
| `RAG_COMPLETION` | HNSW+LLM | да | Готовый ответ с цитатами |
| `GRAPH_COMPLETION` | KG+LLM | да | Вопросы о связях |
| `CYPHER` | Neo4j | нет | Power users, гейтится `ALLOW_CYPHER_QUERY` |
| `CODING_RULES` | Code KG | опц. | Функции/классы/patterns |
| `COMMUNITY_LOCAL/GLOBAL` | Louvain | да | Обзоры кластеров графа |
| `AUTO` | router | — | Делегировать выбор роутеру |

Полная таблица: `docs/search-strategies-guide.md`.

### Temporal knowledge graph

Рёбра графа несут `valid_from`/`valid_until`/`superseded_by`. Эксклюзивные
отношения (`assigned_to`, `role_is`, `status_is`, `located_in`, `owns`,
`reports_to`, `current_state`, `is_a`, …) авто-замещаются при вставке нового
ребра с тем же source+relation. `query_entity(name, as_of=ISO)` даёт снимок
на дату.

### VSA fact memory

`pkg/vsa`/`pkg/vsamemory` — детерминированные vector-symbolic примитивы
(sha256-хот-векторы, binding), поверх SQL-графа: predicate-sharded факт-векторы
для быстрой приблизительной проверки фактов без LLM.

### Команды

| Задача | MCP tool | REST |
|---|---|---|
| Универсальный поиск | `search` | `POST /search` |
| Кросс-коллекционный поиск | `cross_search` | — |
| Сущность с валидностью | `query_entity` | `GET /graph/path` |
| Сообщества | `list_communities` | — |
| Анализ коммитов | `analyze_commits`, `git_search`, `prune_graph` | — |

---

## Ingestion и Cognify

Путь данных: `add` (хэш + классификация + сохранение) → `cognify`
(чанкинг → [LLM-извлечение сущностей] → дедупликация → temporal extraction →
запись в vector/BM25/граф → Louvain-сообщества).

Режимы:

| Режим | Embed | LLM | PG/Neo4j | Что получается |
|---|---|---|---|---|
| `rag` (skip_graph) | нужен | не нужен | не нужен | чанки + HNSW + BM25 |
| `full` | нужен | нужен | нужен | + граф сущностей, сообщества |

Pipeline устойчив к отсутствующим подсистемам: nil LLM/Neo4j/PG →
соответствующие стадии пропускаются с warning.

Статус фоновой прогонки: `cognify_status` (per-stage event log без
удержания SSE). Codify/`analyze_commits` — извлечение знаний из git-истории.

| Задача | MCP tool | REST |
|---|---|---|
| Загрузить файл/URL/текст | `add` | `POST /add` |
| Запустить cognify | `cognify` | `POST /cognify` (+ `/cognify/:runId/status|stream`) |
| Статус | `cognify_status` | — |
| Кодификация | `codify` | — |
| Список/удаление/чистка | `list_data`, `delete`, `prune`, `check_drift` | `/datasets`, `/prune/*` |

Гайды: `docs/project-ingest.md`, `docs/recipes/`.

---

## Markdown Workspace

Markdown-файлы — source of truth; векторные/BM25 индексы — производные и
пересобираемые. 25 workspace MCP-инструментов и 25 workspace REST-маршрутов.

Модель:

- **Manifest + generations**: активная генерация индекса; `workspace_reconcile`
  пересобирает её из текущего дерева файлов.
- **Optimistic locking**: `workspace_read` отдаёт digest; запись с
  `expected_file_digest` отклоняется при конфликте.
- **Conflicts**: детектируются и перечисляются (`workspace_conflicts`).
- **Watch mode**: `workspace_watch_status` — здоровье слежения за деревом.
- **Jobs**: индексация как фоновые job-ы с retry (`workspace_index_jobs`,
  `workspace_retry_index_job`).
- **Audit**: полный журнал операций (`workspace_audit_log`).
- **GC**: `workspace_gc` чистит потерянные артефакты.

### Типовой цикл

```text
workspace_context        # bootstrap: статус проекта + guidance
workspace_write          # записать .md (с expected_file_digest)
workspace_commit         # зафиксировать ревизию
workspace_search         # найти
workspace_revert         # откат при ошибке
```

Документация: `docs/markdown-native-workspace.md`,
`docs/markdown-workspace-conflict-model.md`,
`docs/markdown-workspace-user-scenarios.md` (30 сценариев с привязкой к
автотестам).

---

## Long-Horizon Task Runtime

Alpha, включается `LEVARA_LONG_HORIZON_RUNTIME=1` (+ toolset `long-horizon`
или `full`). Исполняемый реестр длинных работ: цель, authority, Definition of
Done, версионируемый план, атомарные lease на шаги, append-only receipts,
чекпоинты, блокеры — всё в SQL. Сам runtime работу не выполняет и authority
не расширяет: он делает состояние и доказательства восстановимыми, а
завершение — детерминированно валидируемым.

Жизненный цикл:

```text
task_open (цель + DoD) → task_plan (шаги + зависимости)
  → task_step: claim → работа → task_receipt (evidence) → task_checkpoint
  → task_step: pass/fail/release → task_bootstrap (восстановление)
  → task_validate → task_complete (+ промоция memory-кандидатов)
```

Гарантии:

- идемпотентность по `idempotency_key` (task/receipt/checkpoint);
- оптимистическая версионность (`base_version` в каждой мутации);
- lease с TTL — падение актора не блокирует задачу навсегда;
- `task_validate` детерминированно сообщает недостающие/stale/failed evidence;
- `task_complete` промоутит проверенные memory-кандидаты в durable memory;
- завершённая задача неизменяемая.

Гайд с примерами: `docs/long-horizon-runtime.md` (RU: `long-horizon-runtime.ru.md`),
отчёт приёмки: `docs/long-horizon-alpha-report.md`.

---

## Чаты и дневники агентов

- **Chat history**: `save_chat` / `recall_chat` / `search_chats` — сохранение
  и поиск истории разговоров как первоклассных записей.
- **Diaries**: `diary_write(agent=..., key=..., value=...)` / `diary_read` —
  изолированные namespace-ы (`owner_id="agent:<name>"`) для специализированных
  сабагентов (reviewer, architect, oncall), не засоряющие общую память.

---

## Синхронизация

Двунаправленная синхронизация инстансов (Mac ↔ Pi ↔ сервер):

```text
sync(remote_url="http://10.23.0.53:8080/api/v1", direction="pull")
sync_status
```

- По умолчанию переносит `memories + interactions + graph`.
- Векторные коллекции исключены (нужен re-embed) — включаются явно:
  `types=["collections"], collections=[...]`.
- Пустой инкрементальный ответ `{"graph":"no data to push", ...}` — успех.
- 401 при sync = отсутствует/устарел `LEVARA_TOKEN` для удалённого `JWT_SECRET`.

REST: 12 маршрутов `/sync*`. Гайд: `docs/setup-levara-workstation.md`,
`references/rpi5-deploy-sync.md` (skill).

---

## Операции и наблюдаемость

| Задача | MCP tool | REST |
|---|---|---|
| Полная диагностика | `doctor` | `GET /health`, `/health/details` |
| Снимок рантайма | `runtime_stats` | — |
| Последние ошибки | `recent_errors` | — |
| История событий | `heartbeat` | `GET /heartbeats` |
| Статус ingestion | `ingestion_status` | `GET /datasets/status` |
| SQL↔vector сверка памяти | `reconcile_memory` | — |
| Индексные job-ы памяти | `memory_index_status`, `memory_index_retry` | `GET /memory-index/status` |
| Health workspace | `workspace_ops_status` | — |
| Контракт и инструкция | `levara_instructions` | `GET /` contract docs |

Метрики Prometheus (`prometheus.yml`), JSONL audit export, backup/restore CLI
(`cmd/backup`), watchdog-ранбуки (`docs/macos-levara-watchdog.md`).

Сверка индексов после сбоев: `docs/reconcile-guide.md`.

---

## Безопасность и профили

Три независимых контура (см. `docs/security-diff-checklist.md`):

1. **Аутентификация**: JWT (login/users), API keys (хэшированные, с
   permissions и revoke), per-agent credentials. Включается
   `-require-auth` / конфигом профиля.
2. **Авторизация**: dataset sharing + ACL (RBAC-маршруты `/acl`),
   workspace ACL (`workspace_access_check`), tenant membership (`user_tenant`,
   `/tenants`). MCP toolset-профиль — НЕ граница авторизации.
3. **Аудит**: асинхронный JSONL export с retry/backpressure (`pkg/audit`),
   workspace audit log, MCP request audit.

Продуктовые профили (`LEVARA_PROFILE`):

| Профиль | Аудитория | Ключевое |
|---|---|---|
| `personal` | один разработчик | SQLite, auth опционален |
| `solo_pro` | несколько машин | sync + backup + S3-compatible storage |
| `team` | команда + агенты | Postgres, обязательный auth, ACL, аудит |
| `enterprise` | governance | tenant enforcement, identity/audit/ KMS seams |

`LEVARA_PROFILE_STRICT=1` валит небезопасные комбинации Team/Enterprise до
открытия слушателей. Точная матрица: `docs/profile-presets.md`,
граница честности enterprise: `docs/product-ladder.md`.

---

## Веб-интерфейс и аналитика

Next.js WebUI (`webui/`, `:3000` в dev) — операционная поверхность над тем же
бэкендом: Knowledge (датасеты, cognify, поиск, граф), Memory (записи,
notebooks, behavior, scaffold), Workspace (manifest, артефакты, job-ы, аудит),
Operations (дашборд, sync, аналитика, админка).

Аналитические API: `/mcp-analytics`, `/agent-trajectories`, `/memory-behavior`,
`/feedback`, `/memory-reviews`. Feedback-петля: `add_feedback` /
`get_feedback_stats` → адаптивные веса роутера.

Гайд: `docs/webui-operations.md`.

---

## MCP toolset профили

`LEVARA_MCP_TOOLSET` снижает стоимость schema для агента:

| Профиль | Инструменты | Кейс |
|---|---|---|
| `core` | 11 | контекст + память + поиск + doctor |
| `memory` | 25 | полный жизненный цикл памяти |
| `workspace` | 16 | память + безопасный авторинг Markdown |
| `ops` | 14 | эксплуатация и здоровье |
| `long-horizon` | 22 | память + Task Runtime |
| `full` | 82 (79 canonical + variants) | обратная совместимость |

`light` — legacy-алиас `memory`. Профили не являются границей авторизации.
Флаги: `LEVARA_LONG_HORIZON_RUNTIME` (task-инструменты), `LEVARA_MEMORY_COMMIT`
(transactional memory commit).

Реализация: `pkg/mcp/tools_light.go`.

---

## Интерфейсы: MCP / REST / gRPC / CLI

| Поверхность | Путь/порт | Контракт | Для кого |
|---|---|---|---|
| MCP Streamable HTTP (latest) | `/mcp/2026-07-28` | stateless, per-request metadata | современные MCP-клиенты |
| MCP Streamable HTTP (legacy) | `/mcp` | session-based | существующие агенты и IDE |
| REST | `:8080` | 144 маршрута | WebUI, приложения, операции |
| gRPC v1/v2 | `:50051` | 45 методов | типизированные SDK |
| CLI | `./levara/cli` | health/add/cognify/search/datasets/workspace/git | операторы и автоматизация |
| MCP stdio | `cmd/server/mcp_stdio.go` | локальный stdio-транспорт | локальные агентские хосты |

Пример подключения MCP-клиента:

```json
{ "mcpServers": { "levara": { "url": "http://127.0.0.1:8080/mcp" } } }
```

CLI-примеры:

```bash
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health --details
./levara/cli add ./report.pdf --dataset=reports
./levara/cli cognify --dataset=reports --wait
./levara/cli search "rate limiting" --type=HYBRID --top-k=10
```

---

## Карта документации

| Тема | Документ |
|---|---|
| Контракт API (генерируемый) | `docs/api-contract.md`, `docs/contract.json` |
| Быстрый старт | `docs/getting-started.md`, `docs/getting-started-guide.md` (RU, подробно) |
| Текущий снимок рантайма | `docs/current-state.md` |
| Деплой | `docs/deployment.md`, `docs/deployment-matrix.md`, `docs/profile-presets.md` |
| Стратегии поиска | `docs/search-strategies-guide.md` |
| Workspace | `docs/markdown-native-workspace.md` + 5 соседних docs/markdown-workspace-*.md |
| Task Runtime | `docs/long-horizon-runtime.md`, `docs/long-horizon-alpha-report.md` |
| Память (workflow) | `docs/memory-workflow-skill.md`, `docs/memory-commit-design.md` |
| Интеграции | `docs/integrations.md` |
| Продуктовая лестница | `docs/product-ladder.md`, `docs/adr/002` |
| Безопасность | `docs/security-diff-checklist.md` |
| Миграции | `docs/MIGRATION-*.md` |
| Эксплуатация | `docs/webui-operations.md`, `docs/reconcile-guide.md`, `docs/macos-levara-watchdog.md` |
| Рецепты | `docs/recipes/` |

---

*Сгенерировано в ходе полного code review 2026-09-03. Проверять числа
(количество tools/routes) по `docs/contract.json` после изменений контракта.*

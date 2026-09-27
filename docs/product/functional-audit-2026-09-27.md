# Функциональный аудит Levara и план декомпозиции

Дата: 2026-09-27. Статус: проект решения — финальные решения по cut/freeze
принимает владелец. Документ опирается на [product ladder](../product-ladder.md),
[market segments](market-segments.md), [unimplemented roadmap](unimplemented-roadmap.md)
и замеры рабочего дерева от 2026-09-27 (см. «Метод»).

## 1. Метод и источники

- Замеры объёма: `git ls-files` + `wc -l` по дереву (включая тесты).
- Реестр MCP-инструментов: сгенерированный контракт (`api-contract.md`) и
  `pkg/mcp/tools_light.go` (профили toolset).
- HTTP-поверхность: графовый анализ зарегистрированных роутов
  (`internal/http/api.go`, `workspace.go`, `mcp.go`, `chat_import.go`,
  `embedding_migration.go`, `notebooks.go`, SCIM/SAML в `cmd/server`).
- Продуктовый контекст: существующие документы продукта и маркетинга, memory-записи
  проекта (эпики P1–P5 закрыты, профиль-система и профили toolset реализованы).

## 2. Масштаб продукта (факты)

| Измерение | Значение |
|---|---|
| Go-код | 777 файлов, ~197 500 строк (с тестами) |
| Самая тяжёлая зона | `internal/http` — 66 100 строк; `pkg/` — 46 пакетов, 39 600 строк только в топ-10 |
| MCP | ~83 канонических инструмента в 13 группах + 2 флаговых (`memory_commit_*`); 6 профилей toolset |
| REST | 100+ шаблонов роутов (векторное ядро, datasets/documents, workspace ×27, auth/SCIM/SAML, tenants/ACL, analytics, notebooks, embedding-migrations) |
| gRPC | v1 + v2, `internal/grpc` 5 300 строк + proto |
| CLI | `cmd/cli` 4 600 строк |
| WebUI | Next.js, 60 TS/TSX файлов ~10 200 строк, 19 страниц |
| Бинарники | 10 (`server`, `cli`, `contract`, `backup`, `reconcile`, `audit`, `benchmark`, `loadtest`, `qwen3rerank`, `agent-hosts`) |
| Python | 12 файлов бенчмарков + 47 файлов load-profiles (embed bench) |
| Внешние сервисы | embed-server (Gemma, `:9101`), rerank sidecar (`deploy/rerank`), Prometheus |

Отдельный бинарник «поставляется один, но содержит всё»: в сборку по умолчанию
входят и персональная память, и SCIM/SAML, и notebooks, и trajectories, и
chat-import, и Task Runtime.

## 3. Инвентарь функциональных доменов

Оценка ценности: 1 (низкая) … 5 (высокая) для сегментов из
[market segments](market-segments.md): S1 — AI-agent разработчики, S2 —
self-hosters, S3 — RAG/KG-исследователи, S4 — small teams, E — enterprise.

### D1. Memory Palace (room × hall) — ядро

`set_context`, `wake_up`, `save/recall/list/delete_memory`, `pin/unpin`,
`supersede_memory`, консолидация (`consolidate/_status/_revert`, `pkg/consolidate`
1 300 строк), `memory_garden`, diaries, feedback, `memory_scaffold_block`,
`memory_markdown_digest`. SQL-схема room×hall + pinned + supersession.
WebUI: `/memories`, `/memory-scaffold`.

- Ценность: S1=5, S2=4, S3=4, S4=5, E=4. **Это продукт.** Дифференциатор —
  жанр знания (hall) + тема (room) + wake-up с бюджетом + supersession с
  provenance. Ни один конкурент не даёт ровно эту комбинацию из коробки через MCP.

### D2. Векторно-поисковый движок — субстрат ядра

Collections, HNSW, BM25 (`pkg/bm25` 2 000), chunker (1 900), hybrid/AUTO,
pipeline (`dedup`, `multi_query`, `rerank_apply`), `pkg/embed` (1 600) +
внешний embed-server, rerank-адаптер + опциональный sidecar. REST `/insert`,
`/search`, `/collections`; MCP `search`.

- Ценность: S1=3, S2=3, S3=5, S4=4, E=4. Как **самостоятельный vector DB**
  рынок перегрет (Qdrant, Weaviate, pgvector…). Как **встроенная память поиска**
  для room×hall — необходим. Вердикт: ядро, но никогда не позиционируется отдельно.

### D3. Ingestion документов + cognify (RAG)

`pkg/ingest` 4 200, `pkg/orchestrator` 2 400, extract/structuredextract/
classify/docdetect, OCR, whisper-аудио (280), `/datasets*`, `/upload`, `/ocr`,
`/memify`, gRPC `IngestData`/`CognifyDocuments`.

- Ценность: S1=2, S2=3, S3=4, S4=4, E=4. Нужен Team-ступени («память из
  документов»), но для personal-запуска — балласт в дефолте. OCR/whisper —
  самые дорогие в сопровождении единицы ценности.

### D4. Temporal knowledge graph

`pkg/graph*` + community (2 050) + ontology + temporal + graphrank ≈ 5 600;
`cognify`, `codify`, `query_entity(as_of)`, `list_communities`, `/graph/path`,
WebUI `/graph`.

- Ценность: S1=2, S2=3, S3=5, S4=3, E=3. Сильный дифференциатор против
  «плоской векторной памяти» (Zep/Graphiti — прямой конкурент), но для S1 это
  вторичная функция. Вердикт: модуль, поставляется вместе с cognify, не в
  personal-дефолте.

### D5. Markdown Workspace (truth-слой)

`pkg/workspace` 1 800 + `internal/http/workspace.go` 2 300 + тесты 3 700;
**25 MCP-инструментов** `workspace_*`, 27 REST-роутов, watcher, index jobs,
manifests/generations, WebUI `/workspace`, 6+ документов.

- Ценность: S1=3, S2=3, S3=3, S4=4, E=4. Реальный дифференциатор («Markdown —
  источник правды»), но это второй продукт внутри продукта: четверть всей
  MCP-поверхности. Для memory-first запуска — модуль, toolset отдельный (уже есть).

### D6. Long-Horizon Task Runtime

`tool_task.go` 1 280 + `pkg/runreg` 470 + authority manifests; 8 инструментов
`task_*`; уже спрятан за `LEVARA_LONG_HORIZON_RUNTIME=1`; WebUI `/tasks` (read-only).

- Ценность: S1=2, S2=2, S3=3, S4=3, E=2. Концептуально тяжёлый (DoD, leases,
  receipts, checkpoints), конкурирует с готовыми оркестраторами. Roadmap сам
  держит его в P1-приёмке с открытыми критериями. Вердикт: freeze — не
  расширять, пока нет спроса от пилотов.

### D7. Чаты: capture и импорт

Capture: `save/recall/search_chats`, `chat_distill`, `/interactions`, WebUI `/chat`.
Импорт: `pkg/chatimport` 2 900 + `/chats/import*` (6 PR-недель #123–#159).

- Ценность capture: S1=3, S2=3, S3=2, S4=2, E=1. Ценность import: разовая
  миграция, S1=4 один раз. Вердикт: capture — оставить компактным; import —
  модуль с CLI-first интерфейсом (MCP-инструменты импорту не нужны), развитие
  заморозить после закрытия текущего бэклога.

### D8. Git-анализ

`pkg/git` 250, `analyze_commits`, `git_search`, `prune_graph`, `/datasets/{id}/commits`.
Уже admin-gated.

- Ценность: S1=2, S2=2, S3=3, S4=2, E=1. Вердикт: кандидат на перенос в CLI
  (`levara git-*`) и исключение из MCP-дефолта.

### D9. Sync, cluster, backup

`internal/http/sync.go` 1 500, `internal/cluster` 2 100 (WAL-стрим, snapshot,
chaos-тесты), `pkg/backup` 3 400 + `cmd/backup`, `cmd/reconcile`.

- Ценность: S1=2, S2=5, S3=2, S4=4, E=4. Критичен для Solo Pro/Edge (S2/S5 —
  лояльные ниши). Cluster/replication — experimental по собственному roadmap.
  Вердикт: sync+backup — модуль «в коробке» для solo_pro+; Raft/multi-node —
  не заявлять production до отдельной приёмки.

### D10. Identity / Access / Enterprise

`pkg/access` 8 500 + `pkg/auth` 2 000 + `pkg/audit` 3 900 + `pkg/storage` 5 400
(S3/KMS) + JWT/API-keys/сессии, tenants, RBAC shares, document ACL,
LDAP/OIDC/SAML/SCIM, audit spool/webhook + `cmd/audit`. Суммарно >20 000 строк.

- Ценность: S1=1, S2=2, S3=1, S4=4, E=5. Это монетизация Enterprise, но
  главный источник сложности и самой длинной части незакрытой приёмки.
  Вердикт: не выпиливать физически (границы `pkg/access` уже в коде), а
  выделить в отдельный релизный поезд и не допускать его в критический путь
  memory-запуска.

### D11. Аналитика и исследовательские подсистемы

`internal/trajectory` 520, `/mcp-analytics`, `/events`, `/behavior`,
`/memory-behavior`, `/memory-reviews`, `/proposals`, VSA/VSA-memory (1 270,
predicate-шардированные факт-векторы), `pkg/observe` (langfuse), WebUI
`/analytics`, `/memory-behavior`.

- Ценность: S1=2, S2=1, S3=4, S4=2, E=2. Исследования, а не продукт.
  Вердикт: freeze/research-ветка; из дефолтной сборки и дефолтных роутов —
  выключить.

### D12. Notebooks

`internal/http/notebooks.go` + WebUI `/notebooks`: исполняемые ячейки в
сервере памяти.

- Ценность: 1 по всем сегментам. Вердикт: **cut** из продукта (выключатель
  в Ф1, удаление в Ф2 по решению владельца).

### D13. WebUI

19 страниц, ~10 200 строк TS. Ops-дашборд, datasets, memories, graph, sync,
tasks, chat, notebooks, onboarding.

- Ценность: S1=3, S2=3, S3=2, S4=4, E=4. Отдельный процесс (уже отдельный
  deliverable). Вердикт: модуль со своим релизным циклом; для personal-MVP
  достаточно `/memories` + doctor-страницы.

### D14. Ops / наблюдаемость / миграции

`doctor`, `heartbeat`, `runtime_stats`, `recent_errors`, Prometheus `/metrics`,
`/health`, embedding-migrations (1 040), `/reembed`, prune, drift,
`cmd/loadtest|benchmark`, python-bench.

- Ценность: S1=3, S2=4, S3=3, S4=4, E=5. Обрезанная часть (doctor+health+
  metrics+index-status) — ядро; тяжёлое (migrations, load-харнесс) — модуль
  эксплуатации. Bench-инструменты не должны попадать в релизный артефакт.

### D15. Интерфейсы

MCP (две spec-версии + SSE + session-auth), REST, gRPC v1+v2, CLI, WebUI.

- Сила (MCP-native — главный канал дистрибуции S1) и стоимость одновременно.
  gRPC raw-admin поверхность — самый слабый актив (roadmap уже планирует
  compatibility/sunset). Вердикт: MCP + REST — ядро; gRPC v1 — sunset-план;
  вторая датированная MCP-версия — держать минимальной.

## 4. Диагноз: что именно мешает выходу на рынок

1. **Размывание позиционирования.** README обещает «durable memory + hybrid
   search + temporal KG + workspace + sync + observability + tasks» — семь
   продуктов в одном предложении. Покупатель S1 не может понять за 30 секунд,
   что он ставит и зачем.
2. **Порог входа.** 85 инструментов в полном toolset, 6 профилей toolset,
   4 product-профиля, 2 флага — богатая система, но первый запуск требует
   выбрать между режимами ещё до первого `wake_up`.
3. **Стоимость сопровождения пропорциональна самой широкой поверхности, а не
   ядру.** `internal/http` 66k строк: баг в notebooks или SCIM блокирует релиз
   memory-ядра (единый release gate, единый контракт, единый CI).
4. **Продуктовая лестница есть, но она не упаковка.** `product-ladder.md`
   правильно делит engine/слои, профили toolset реализованы — однако дефолт
   везде `full`, сборка одна, и вся поверхность видна в контракт-файлах,
   README и WebUI сразу.
5. **Скорость итераций ядра.** Memory-фичи (GEMMA-миграция, breaker, retrieval
   quality) идут через тот же gate, что и enterprise-identity, workspace и
   chat-import.

## 5. Принцип декомпозиции

Зафиксировать инвариант: **room × hall memory в SQL — публичный контракт ядра**
(схема, hall-словарь, supersession, pin, wake-up бюджет). Совместимость этого
контракта проверяется отдельным тестом и не ломается ни одним из следующих
шагов.

Всё остальное классифицируется по одному из четырёх состояний:

| Состояние | Значение | Механизм |
|---|---|---|
| **Core** | всегда в бинарнике, часть MVP | без флагов |
| **Module** | в «коробке», включается профилем, не в personal-дефолте | profile→toolset binding, конфиг |
| **Opt-in** | существует, но выключено везде по умолчанию | env-флаг (уже есть у task/memory-commit) |
| **Cut** | выводится из продукта | выключатель → удаление через 1 релиз |

## 6. Целевая карта модулей

| Модуль | Домены | Состояние | Механизм включения |
|---|---|---|---|
| **levara-core** (один бинарник) | D1 + D2 + D14(легковес) + MCP/REST/CLI | Core | всегда; personal-профиль = этот набор |
| **rag** (ingest+cognify+KG) | D3 + D4 | Module | профиль solo_pro+; CLI `add/cognify` |
| **workspace** | D5 | Module | toolset `workspace`, профиль team+ |
| **sync-backup** | D9 (без Raft) | Module | профиль solo_pro+ |
| **webui** | D13 | Module (отдельный процесс/релиз) | конфиг |
| **task-runtime** | D6 | Opt-in (уже) | `LEVARA_LONG_HORIZON_RUNTIME`, freeze |
| **chat-import** | D7(import) | Opt-in, CLI-first | отдельная подкоманда CLI |
| **analytics-research** (Р3: promote) | D11 + VSA/DCD | Feature в развитии | до quality-gate — env-гейт (`LEVARA_DCD_ROUTER`, конфиг VSA) |
| **enterprise** | D10 | Module, отдельный релизный поезд | профиль enterprise |
| ops-heavy (migrations, load-harness) | D14 | Module | отдельные бинарники/скрипты |
| notebooks | D12 | **Cut** | выключатель в Ф1 |
| whisper/audio | D3 | Cut из дефолта | в `rag`, опционально |
| gRPC v1 raw | D15 | Sunset по существующему плану | deprecation window |

Существующие профили toolset (`core`, `memory`, `workspace`, `ops`,
`long-horizon`, `full`) уже почти совпадают с этой картой — их нужно связать с
product-профилями (personal→`core`/`memory`) вместо ручного `LEVARA_MCP_TOOLSET`.

## 7. План по фазам

### Фаза 0 — решение (1–2 дня)

#### Р1. Дефолтный toolset personal-профиля — РЕШЕНО 2026-09-27: вариант A+

Связка «product-профиль → toolset» сейчас отсутствует: без `LEVARA_MCP_TOOLSET`
объявляются все ~85 инструментов при любом профиле.

| Вариант | Состав | Плюсы | Минусы |
|---|---|---|---|
| A: personal → `core` | 11 инструментов: instructions, set/get_context, wake_up, save/recall/list/pin/unpin, search, doctor | Минимальная нагрузка на tool-calling, чистое «memory-first» позиционирование | Нет `supersede_memory`/`delete_memory`/consolidation/diaries — верх дифференциатора доступен только после апгрейда |
| B: personal → `memory` | 25 инструментов: полный memory-цикл + consolidation + garden + diaries + feedback | Вся room×hall-история доступна сразу | 25 инструментов в первом запуске; scaffold/garden — служебный шум |
| C: статус-кво (`full`) | ~85 | Нулевой риск | Ничего не меняется ни в восприятии, ни в tool-calling |

Рекомендация: **A** с явным upgrade-путём (одна строка env + абзац в quick-start:
`LEVARA_MCP_TOOLSET=memory`). Без профиля дефолт не меняется — существующие
инсталляции (Mac/Pi, `full`) не затронуты.

##### Углублённое сравнение A / A+ / B (2026-09-27)

Замер фактических дескрипторов (`ToolDescriptorsForMode`, JSON): core = 11
инструментов, 14 760 байт ≈ 3 700 токенов контекста на запрос агента; memory =
23 инструмента (`memory_commit_*` скрыты флагом `LEVARA_MEMORY_COMMIT`), 26 090
байт ≈ 6 500 токенов. База A и B одинакова — 11 инструментов core покрывают
минимальный цикл (контекст, брифинг, запись/чтение/список, пины, поиск,
диагностика). Разница — только в дельте из 12 инструментов:

| Инструмент дельты | Байт | Когда реально нужен | Что без него в core |
|---|---|---|---|
| `delete_memory` | 1 018 | недели 1–4 | битую запись не убрать через MCP (только WebUI/REST) |
| `supersede_memory` | 1 313 | недели 2–8 | устаревший факт остаётся активным в recall — главный риск деградации |
| `consolidate` + `_status` + `_revert` | 2 819 | месяц+ | дубли накапливаются, recall засоряется |
| `memory_garden` | 1 335 | месяц+ | нет обзора состояния памяти |
| `diary_write` / `diary_read` | 1 679 | при субагентах | нет изолированных дневников |
| `add_feedback` + `get_feedback_stats` | 1 494 | опционально | нет петли обратной связи |
| `memory_scaffold_block` | 892 | эпизодически | нет scaffolding AGENTS.md |
| `memory_markdown_digest` | 768 | эпизодически | нет markdown-выгрузки |

Вариант **A+** (гибрид): core + `supersede_memory` + `delete_memory` = 13
инструментов ≈ 16 700 байт ≈ 4 200 токенов. Логика: «убрать неверное» и
«заменить устаревшее» — это минимальная гигиена жизненного цикла записи, а не
продвинутая функция; остальная дельта — операции зрелого использования, им
место в режиме B.

| Критерий | A (core) | A+ (core+2) | B (memory) |
|---|---|---|---|
| Инструментов / контекст схем | 11 / ~3 700 т | 13 / ~4 200 т | 23 / ~6 500 т |
| Гигиена записи | нет | удаление + замещение | полная (+ консолидация) |
| Точность tool-calling у малых/локальных моделей | максимум | близко к максимуму | ниже (широкий выбор, служебный шум) |
| Первое впечатление | чистое | чистое | перегруженное |
| Качество памяти к месяцу использования | деградирует (риск «память врёт») | сохраняется | сохраняется |
| Метрика Р5 «≤12» | выполняется | требует пересмотра до ≤15 | не выполняется |

Стратегический вывод: **A+ как дефолт personal**, `memory` — режим «взрослой
памяти» одной строкой env; лестница 13 → 23 сама становится фичей
(«когда память подрастёт — включите режим ухода»). Механика A+ — правка одного
списка в `pkg/mcp/tools_light.go` + тест, обратима в любой момент.

**Реализовано 2026-09-27** (решение владельца «A+ для personal»): список
`core` в `pkg/mcp/tools_light.go` дополнен `delete_memory` и
`supersede_memory`. Замер после правки: core = 13 инструментов, 17 093 байт
≈ 4 273 токена; `memory` без изменений (23, 26 090 байт ≈ 6 522). Инвариант
теста «core не позволяет `delete`/`workspace_delete`/`sync`/`consolidate`»
сохранён; toolset-тесты `pkg/mcp` PASS. Ограничение: связка
`LEVARA_PROFILE=personal` → toolset ещё не сделана (это шаг Ф1) — до неё
эффективный набор задаётся вручную `LEVARA_MCP_TOOLSET=core`, дефолт без
переменной остаётся `full`.

##### Качество ответов как главный критерий (2026-09-27)

Toolset не меняет движок извлечения (HNSW/BM25/room×hall-фильтры одинаковы) —
он меняет три множителя качества ответа: точность маршрутизации агента,
свободный контекст и здоровье базы на дистанции; плюс обратный канал —
мутационные промахи агента, портящие базу.

| Множитель качества | День 1–7 | Недели 2–8 | Месяц+ |
|---|---|---|---|
| Маршрутизация (неверный инструмент → нерелевантный контекст) | A/A+ лучше: 13 непересекающихся инструментов против 23 с 5 близкими парами | то же | то же |
| Свободный контекст (схемы съедают место) | A+ ≈ −2 300 т к B | то же | то же |
| Recall-precision (stale-факты, дубли) | паритет | pure A деградирует: протухший факт неотличим в выдаче, агент «уверенно цитирует устаревшее»; A+ и B держат через `supersede` | pure A и A+ без периодического `consolidate` копят дубли в top-k; B держит консолидацией руками агента |
| Мутационный риск (промах портит базу) | A+ минимальный: 2 атомарных мутатора | то же | у B мутационная поверхность шире (consolidate/delete/supersede в дефолте агента) |

Проверяемо существующими инструментами: телеметрия `/memory-behavior`
(`zero_result_rate`, `empty_recall_rate`, `repeat_save_rate`,
`recall_before_save_rate`, `context_bytes`) + golden-сценарии
`benchmark/memory_eval` (`run_all_hosts.sh`). Предложенный gate: одинаковая БД,
toolset A+ против B, реальные агенты на golden-сценариях; сравнить поведенческие
метрики и токены. `benchmark/retrieval_quality.py` от toolset не зависит
(бьёт в REST мимо агента) — это отдельный серверный gate.

#### Р2. Судьба notebooks — РЕШЕНО 2026-09-27: вариант A (cut)

Факты: 2 файла (485 строк `internal/http/notebooks.go` + 156 строк WebUI),
0 выделенных тестов, 10 роутов в общем `RegisterAPI`. По коду это не
интерпретатор, а меню из ~10 захардкоженных команд (`collections`, `datasets`,
`stats`, `env`, `search`, `graph`, `embed`, `count`, `info`, `help`); ячейка
`cognify` — заглушка, возвращающая ложный успех без запуска. Авторизации на
уровне данных нет: search-ячейка идёт по всем коллекциям мимо ACL, `datasets` и
`graph` — без фильтра владельца, `env` раскрывает конфигурацию сервера любому
аутентифицированному пользователю. В документации упомянуты одной строкой
(README «Product surfaces» и capability map, таблица в `webui-operations.md`)
без описания workflow. Исторически — интерактивная отладочная консоль ранних
релизов (файл тянется с эпохи Levara 1.0); сегодня каждая команда дублируется
CLI, REST или профильными страницами WebUI.

| Вариант | Суть | Последствия |
|---|---|---|
| A: cut | Ф1 — выключатель роутов; Ф2 — удаление файлов | Потеря ~640 строк, минус один release-gate-житель, исчезают дыры ACL/env из дефолтной поверхности |
| B: freeze | Флаг, дефолт off, код остаётся | Налог на сопровождение и контракт остаётся |
| C: research | Перенос в ветку | Чисто, но поддержка ветки без потребителя деградирует |

Рекомендация: **A**, при условии что владелец не использует notebooks лично.

#### Р3. Судьба research-кластера (VSA + DCD-роутинг + behavior-аналитика) — РЕШЕНО 2026-09-27: вариант C (promote)

Факты (уточнены 2026-09-27): VSA самодостаточна — `pkg/vsa` (236 строки) +
`pkg/vsamemory` (1 041), HTTP-обёртка `/vsa/query|rebuild|status`
(`internal/http/vsa.go` 454), внешние импортёры — только собственные тесты
(vsa_ab, vsa_quant_eval, dcd_route_vsa_boost). В прод-пути VSA участвует ровно
в одном месте: `assembleGraphContext` → `vsaGraphContextItems` — обогащение
graph-контекста стратегий `GRAPH_COMPLETION` и `CONTEXT_EXTENSION`; в
`CHUNKS`/`HYBRID`/`BM25` VSA не входит. Данные появляются только после явного
`/vsa/rebuild` (`RebuildFromGraph`). DCD-роутер (`dcd_route_resolver.go` 291 +
`dcd_route_observe.go`) — BM25-резолвер таксономии
domain→collection→document над таблицами `knowledge_domains/_collections/
_documents`, env-гейт `LEVARA_DCD_ROUTER` (+ `LEVARA_DCD_ROUTE_MAX_CANDIDATES`,
`LEVARA_DCD_ROUTE_MIN_CONFIDENCE`); бустит VSA-кандидатов
(`rerankVSACandidatesByDCDRoute`). Ключевое: у `knowledge_*` таблиц нет
продакшн-писателя — единственные INSERT в тестах; без заполненной таксономии
DCD резолвит ноль кандидатов и на поиск не влияет. Behavior-аналитика —
read-model API (`/memory-behavior`: recall_before_save_rate, repeat_save_rate,
zero_result_rate, context_bytes; `/memory-reviews`; `/memory-scaffold`) +
`pkg/observe` (langfuse). Roadmap (P3) уже требует решения
«promote/keep-flag/remove» для graph extras и экспериментального routing.

| Вариант | Суть | Плюсы | Минусы |
|---|---|---|---|
| A: keep-flag | Env-флаг, дефолт off; роуты `/vsa/*` и DCD-boost не регистрируются | Обратимо; точно соответствует формулировке roadmap | Код в дереве, тесты в общем gate |
| B: cut → research | Убрать из основной ветки в ветку/репо | Самый дешёвый релизный gate | Необратимо в рамках main; при возобновлении — реанимация |
| C: promote | Развивать как фичу «поведение памяти» | — | Дорого; нет продуктового спонсора |

Рекомендация: **A** сейчас + DCD-boost в поиске выключить по умолчанию
(влияет на retrieval quality — это gate ядра); повторное решение A→B через
2 релиза при отсутствии спроса.

**РЕШЕНО владельцем 2026-09-27: вариант C (promote)** — осознанно вопреки
рекомендации A. Обязательные условия promote:

1. **Эпик «писатель таксономии `knowledge_*`»** — без прод-источника данных
   DCD-роутер мёртв (сегодня единственные INSERT — тестовые). Кандидаты:
   автогенерация доменов в cognify-пайплайне из графа либо ручное заполнение
   через CLI/WebUI с review.
2. **Quality-gate перед дефолтным включением**: DCD-boost и VSA-обогащение
   включаются по умолчанию только после подтверждения на retrieval-бенчмарках
   (`retrieval_quality.py` — серверный gate; `memory_eval`/A-B — агентский).
   До прохождения gate runtime-гейтинг (`LEVARA_DCD_ROUTER` и конфиг VSA)
   сохраняется — это условие promote, а не его отмена.
3. Кластер остаётся в общем релизном гейте, включая combined race `pkg/vsamemory`.
4. В карте модулей (§6) analytics-research меняет статус:
   opt-in/research → **feature в развитии**.

#### Р4. Окно sunset gRPC v1 raw — РЕШЕНО 2026-09-27: вариант C (freeze)

Факты (уточнены 2026-09-27): v1 `LevaraService` — 40 канонических методов
(сырой векторный движок, админка коллекций, ingestion/embedding, админка графа,
LLM-кэш, даже `ListDirectory`); v2 `LevaraServiceV2` — 8 методов, из них
Add/Create/Save в статусе alias, то есть v2 сам является тонким compat-слоем,
а не новой архитектурой. Живое ядро v1 — document-scoped трио
`IngestData`/`CognifyDocuments`/`CognifyDocumentsStatus` (приёмка 2026-09-14);
остальное — raw/global-admin RPC, при required auth доступное только active
superuser. Собственных gRPC-потребителей в репо нет: `grpc.NewClient`
встречается только в тестах; CLI, sync, WebUI и load-харнессы ходят через REST.
Единственный возможный потребитель raw v1 — внешний сторонний клиент; продукт
ещё не выпущен, публичного SDK нет. `internal/grpc` 5 300 строк + proto.
Roadmap (P3) уже планирует compatibility/sunset с измерением использования и
окном «минимум 2 релиза наблюдения».

| Вариант | Суть | Плюсы | Минусы |
|---|---|---|---|
| A: полный sunset v1 | Вся v1 в deprecation | Максимальное упрощение | Убивает свежий document-scoped путь |
| B: раздельный | document-scoped остаётся; raw-admin — deprecation сейчас, удаление через 2 минорных | Убирает самую тяжёлую часть приёмки | Требует аудита собственных потребителей raw (CLI/sync/loadtest) |
| C: freeze admin-only | Не развивать, задокументировать | Нулевая работа сейчас | Поверхность и gate остаются |

Рекомендация: **B**; аудит потребителей raw-RPC — первый шаг Ф1.

**РЕШЕНО владельцем 2026-09-27: вариант C (freeze)** — осознанно вопреки
рекомендации B. Следствия:

1. Raw/global-admin методы v1 остаются в контрактах как admin-only
   поверхность; deprecation не объявляется, удаления не планируется.
2. План sunset из roadmap (P3 «compatibility/sunset legacy vector API»)
   считается перекрытым этим решением; при следующем обновлении
   `docs/product/unimplemented-roadmap.md` пункт помечается решением
   владельца от 2026-09-27 (сделать в Ф1-проходе документации).
3. Открытые P1/P2 roadmap-пункты по составным raw-путям остаются в очереди
   приёмки — осознанная цена freeze.
4. Наблюдаемость raw-поверхности (закрыто 2026-09-27): per-method счётчики
   **уже существовали** — `levara_grpc_requests_total{method,status}` и
   `levara_grpc_duration_seconds{method}` (`internal/grpc/metrics.go`, в цепочке
   после auth/rate-limit, v1+v2, unary+stream, экспозиция на `/metrics`).
   Добавлены: юнит-тесты интерцепторов (`internal/grpc/metrics_test.go`,
   ок/ошибка + per-method + per-status, PASS) и alert-правило
   `GRPCRawMethodUsage` в `docs/prometheus-verify.rules.yml` — warning при
   любом трафике за 15 минут на методы вне санкционированного набора (Info +
   document-scoped трио на v1/v2).

#### Р5. Метрики запуска

Агрессивный порог: 5 мин, ≤11 инструментов, 2 команды. Консервативный:
15 мин, ≤25, 5 команд. Рекомендация: 10 мин / **≤15 инструментов** (пересмотрено
2026-09-27 при принятии A+: дефолт personal = 13) / 3 команды — совпадает с
вариантом A решения Р1.

Шаг верификации Ф0: smoke «чистой установки» выбранного toolset на SQLite
(лейбл-тест: install → MCP-конфиг → `wake_up`), результат фиксируется в
`docs/testing.md`. Итог Ф0 — обновлённый статус-блок этого документа.

### Фаза 1 — упаковка без переезда кода (1–2 недели)

1. Profile→toolset binding: `LEVARA_PROFILE=personal` поднимает toolset `core`
   (переопределяется env). Обратная совместимость: без профиля остаётся `full`.
2. Выключатели (флаги) для notebooks, analytics/behavior/VSA-роутов; дефолт —
   off в personal, on в остальных до отдельных решений.
3. README/quick-start rewrite: три команды до первого `wake_up`; полный
   capability-map — во вторичный docs. Разделить docs на «start here» (memory)
   и «по модулям».
4. Релизный артефакт без `scripts/`, `benchmark/`, dev-бинарников
   (`loadtest`, `benchmark`); embed-server и rerank — внешние сервисы (уже так).
5. Контракт: сгенерировать «core contract» (подмножество) рядом с полным;
   full остаётся каноническим.

### Фаза 2 — границы кода (2–4 недели)

1. Внутри Go-модуля оформить границы пакетов: `chatimport`, `task`, `analytics`,
   `rag/ingest` получают интерфейсные фасады и не импортируются из ядра
   напрямую (проверить gortex-графом: ядро не зависит от модулей).
2. Отдельные бинарники для тяжёлых ops (`reconcile`, `backup` уже есть) и
   опционально build-tags для task-runtime.
3. WebUI: собственный релизный цикл и CHANGELOG; из MVP-дистрибуции — опция.
4. Для каждого модуля — свой владелец generated-контракта и свой набор
   приемочных тестов; общий release gate ядра сужается до ядра.

### Фаза 3 — по спросу (не раньше первых пилотов)

1. Enterprise-адаптеры: отдельный релизный поезд, свой acceptance-план
   (реальные AD/IdP/KMS/SIEM — уже описаны в roadmap).
2. Repo-split (levara-core / модули) — только если появится вторая команда или
   требование лицензирования; до этого моно-репо с жёсткими границами пакетов
   дешевле.
3. Sunset gRPC v1 raw-admin по плану compatibility (2 релиза наблюдения).

## 8. Риски и меры

| Риск | Мера |
|---|---|
| Слом существующих инсталляций (Mac + Pi на full) | Дефолт без профиля не меняется; binding personal→core только при явном профиле; `tools/list` проверять в CI |
| Контракт-дрейф при разделении | Generated-файлы остаются с одним владельцем; core-contract генерируется из полного, не вручную |
| Тестовая сцепка (`internal/http` 66k с тестами workspace/mcp) | Границы пакетов вводить вместе с переносом тестов; gortex-граф для контроля зависимостей ядра |
| Пользовательские фичи, которые владелец сам использует (chat-import, git, notebooks) | Явно перечислить в Ф0 и решить до cut, а не после |
| Демпинг качества ядра ради скорости | Quality-first правило остаётся: ни один cut не трогает room×hall контракт и recall-качество (retrieval_quality — gate) |
| Размывание после рефактора («модули, но всё равно всё в README») | Метрика Ф0: README первого экрана описывает только core; всё остальное — по ссылкам на модули |

## 9. Что НЕ делать

- Не выпиливать enterprise-код физически — это монетизация; изолировать
  релизным поездом.
- Не ломать «один бинарник» как канал дистрибуции personal-сегмента.
- Не трогать SQL-контракт room×hall (миграции GEMMA уже прошли боль смены
  embedding-пространства).
- Не начинать repo-split до появления организационной причины.
- Не добавлять новые домены (WebUI-планы, новые коннекторы) до Ф2.

## 10. Ожидаемый результат

- Personal-пользователь видит продукт из одного предложения («память для ваших
  AI-агентов, локально, через MCP»), 3 команд до `wake_up` и 13 инструментов
  (toolset `core`, решение Р1=A+).
- Team/Enterprise получают те же возможности через профили, без нового кода.
- Релиз ядра перестаёт зависеть от notebooks/SCIM/workspace-поверхности.
- Полная поверхность сохраняется для существующих инсталляций.

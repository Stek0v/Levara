# Levara

> Высокопроизводительный движок графов знаний для AI-приложений. Написан на Go.

[![Go Version](https://img.shields.io/badge/Go-1.26+-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## Обзор

Levara -- production-ready движок графов знаний, который преобразует сырые данные в персистентные, поисковые графы знаний. Объединяет векторный поиск (HNSW + AVX2 SIMD), полнотекстовый BM25, граф знаний с темпоральной валидностью, write-ahead log и LLM-пайплайн cognify в одном Go-бинарнике.

Levara -- движковый слой платформы **LevaraOS**, единой системы персистентной памяти для AI-агентов и IDE (Claude Code, Cursor). Поверх векторно-графового ядра она предоставляет **memory palace** (долговременную память агента с таксономией room × hall), **верифицируемый workspace** (слой записи, где Markdown -- источник истины), **System2-консолидацию** (фоновое сжатие памяти) и **синхронизацию Mac ↔ Pi**.

**Ключевые возможности:**

- **15 типов поиска** -- векторный, графовый, гибридный, темпоральный, chain-of-thought, NL-to-Cypher и другие
- **22+ формата документов** -- PDF, DOCX, аудио через Whisper, изображения, код и другие
- **Мульти-провайдер LLM** -- OpenAI, Anthropic, Ollama (локальный)
- **MCP-сервер** для Claude Desktop / Cursor / Claude Code -- 66 инструментов на поверхностях графа, памяти, workspace и наблюдаемости
- **Memory palace** -- долговременная память агента с таксономией room × hall, пиннингом, дневниками и брифингами wake-up
- **Верифицируемый workspace (Variant B)** -- `.md`-файлы являются источником истины; векторные/графовые индексы -- расходные деривативы, с коммитами, ACL и журналом аудита
- **System2-консолидация** -- фоновый janitor кластеризует почти-дубликаты памяти и сливает (cosine ≥ 0.97) или LLM-абстрагирует (0.85–0.97) их; полностью обратимо
- **Граф знаний с темпоральной валидностью** -- рёбра сущностей несут окна valid-from/valid-until и автоматически супершедятся при обновлении
- **gRPC v1 + v2** -- канонический surface из 47 RPC (v1) плюс минимальный write-only v2, оба на `:50051`
- **JWT-аутентификация + rate limiting** -- общий HS256-секрет для HTTP и gRPC, per-user и per-IP token bucket'ы
- **Кластеризация на Raft** -- опциональные шардирование и репликация
- **Cross-encoder + graph-aware реранкинг** -- Cohere-совместимый реранкер и фьюжн α·vector + β·graph + γ·rerank
- **Louvain-детекция сообществ** -- кластеризация графа для community-aware-поиска
- **Синхронизация Mac ↔ Pi** -- двунаправленный sync с bearer-аутентификацией и детекцией расхождения версий
- **SQLite + PostgreSQL** -- двойная поддержка баз данных
- **ARM64** -- работает на Raspberry Pi
- **S3 облачное хранилище** + трейсинг Langfuse
- **Zero-copy arena allocator** -- минимизация GC-пауз
- **HNSW-индексация** с AVX2 SIMD -- O(log N) ANN-поиск
- **WAL group commit** -- 100% восстановление после сбоев
- **Prometheus-метрики** -- готово для Grafana

## Быстрый старт

```bash
# Установка
go install github.com/stek0v/levara/cmd/server@latest

# Запуск с SQLite (без зависимостей)
./levara-server -standalone=true -dim=768 -port=8080

# Или через Docker
docker compose up -d --build
# Levara: http://localhost:8080 | gRPC: localhost:50051
```

## Архитектура

```
Клиент --> Levara HTTP :8080 / gRPC :50051 (v1 + v2) / MCP
              |
   транспорт  |-- JWT-аутентификация (HS256, общая HTTP + gRPC) + rate limiting
              |-- MCP-сервер (66 инструментов)
              |
   хранилище  |-- HNSW Vector Index (in-process, AVX2 SIMD)
              |-- WAL (group commit, 100% crash recovery)
              |-- Arena Memory Allocator (zero-copy, без GC)
              |-- PostgreSQL / SQLite
              |-- Neo4j Graph DB (опционально, SQL fallback)
              |
   вычисления |-- BM25 полнотекстовый + Hybrid RRF
              |-- Cognify-пайплайн (chunk -> extract -> dedup -> embed -> write)
              |-- Cross-encoder + graph-aware реранкинг
              |-- Louvain-детекция сообществ
              |-- LLM (OpenAI / Anthropic / Ollama)
              |
   платформа  |-- Memory palace (room x hall, пины, дневники)
              |-- Верифицируемый workspace (Variant B, .md -- источник истины)
              |-- System2-консолидация (merge / abstract, обратимо)
              |-- Синхронизация (Mac <-> Pi, bearer auth, warning о версиях)
              |-- Raft-кластер (опц. шардирование / репликация)
              |-- Prometheus-метрики
```

```
Levara/
  cmd/
    server/         # Точка входа HTTP + gRPC (регистрирует v1 + v2)
    cli/            # CLI levara
    backup/         # levara-backup (backup / restore)
    reconcile/      # Инструмент консистентности SQL <-> vector
    contract/       # Codegen / проверка agent-contract для MCP
    agent-hosts/    # Тулинг реестра agent host
    loadtest/       # Нагрузочное тестирование
    qwen3rerank/    # Хелпер rerank-сайдкара
  internal/
    store/          # db, wal, hnsw, arena, disk, collections
    http/           # REST-роутер + 50+ хендлеров (search, cognify, mcp,
                    #   auth, sync, workspace, ratelimit, observe)
    grpc/           # service v1 (47 RPC) + v2 + auth / ratelimit интерсепторы
    cluster/        # Raft шардирование / репликация (shard, node, fsm)
    metrics/        # Prometheus-телеметрия + bounded-cardinality user buckets
    contract/       # Типы MCP-контракта
  pkg/
    orchestrator/   # Cognify-пайплайн (chunk -> extract -> dedup -> embed -> write)
    bm25/           # Инвертированный индекс BM25 + hybrid RRF
    graph/          # Построение графа знаний
    graphdb/        # Интеграция с Neo4j + SQL fallback
    graphstore/     # Персистентность графа
    graphrank/      # Graph-aware реранкинг (vector + graph + rerank)
    community/      # Louvain-детекция сообществ
    rerank/         # Cross-encoder реранкинг (Cohere-совместимый)
    router/         # Умная маршрутизация поиска + адаптивные веса
    mcp/            # 66 MCP-инструментов + Deps interface + output schemas
    consolidate/    # System2-консолидация памяти (merge / abstract)
    workspace/      # Верифицируемый workspace памяти (Variant B)
    auth/           # Общая верификация JWT
    audit/          # Журнал аудита
    embed/          # Провайдеры эмбеддингов
    extract/        # Извлечение сущностей
    chunker/        # Разбиение документов
    classify/       # Классификация текста
    fileio/         # Файловый I/O (22+ форматов)
    fetch/          # Загрузка URL / документов
    ingest/         # Пайплайн загрузки данных
    llm/            # LLM-провайдеры (OpenAI, Anthropic, Ollama)
    llmcache/       # Кэширование ответов LLM
    llmproxy/       # LLM-прокси / маршрутизация
    observe/        # Трейсинг Langfuse
    ontology/       # Управление онтологиями
    temporal/       # Темпоральная валидность / time-aware рёбра
    aggregator/     # Агрегация результатов поиска
    audio/          # Транскрипция аудио (Whisper)
    git/            # Анализ Git-репозиториев
    storage/        # S3 облачное хранилище
    vectorstore/    # Абстракция векторного стора
    backup/         # Библиотека backup / restore
    runreg/         # Реестр фоновых run'ов (TTL janitor)
    agenthosts/     # Реестр agent host
  proto/            # gRPC-определения (v1 + v2)
  webui/            # WebUI на Next.js 15
```

## Производительность

Тесты на i7-7700 @ 3.60 GHz, Linux 6.8, dim=1024, gRPC.

### Задержка поиска

| Масштаб | p50 задержка | QPS | Примечание |
|---------|-------------|-----|------------|
| 1K векторов | **0.99 мс** | **589** | HNSW + AVX2 |
| 10K векторов | **7.88 мс** | **480** | +695% масштаб, +3.7% задержка |
| 100K векторов | **23.7 мс** | **143** | O(log N) стабильно |

### vs LanceDB (1.4K реальных эмбеддингов)

| Метрика | Levara | LanceDB | Ускорение |
|---------|--------|---------|-----------|
| Поиск p50 | **2.6 мс** | 9.1 мс | **3.5x** |
| Конкурентный QPS | **589** | 109 | **5.4x** |
| Загрузка данных | **0.08 мс/элемент** | 287+ мс | **3,379x** |
| 100K поиск | **23.7 мс** | 203.7 мс | **8.6x** |
| Восстановление | **100%** | N/A | Levara |

**Levara** выигрывает на нагрузках с преобладанием чтения. **LanceDB** выигрывает на пакетной загрузке сырых векторов.

## Типы поиска (15)

| # | Тип | Описание |
|---|-----|----------|
| 1 | `VECTOR` | Поиск ближайших соседей по косинусному сходству |
| 2 | `GRAPH_COMPLETION` | Графовый поиск с обходом связей сущностей |
| 3 | `HYBRID` | Комбинированный векторный + BM25 полнотекстовый поиск |
| 4 | `TEMPORAL` | Поиск с учетом времени по версиям документов |
| 5 | `COT` | Chain-of-thought многошаговый поиск с рассуждением |
| 6 | `NL_CYPHER` | Перевод естественного языка в Cypher-запросы |
| 7 | `ENTITY` | Извлечение и поиск именованных сущностей |
| 8 | `KEYWORD` | BM25 полнотекстовый поиск по ключевым словам |
| 9 | `SUMMARY` | Поиск по суммаризации документов |
| 10 | `CLASSIFICATION` | Поиск по классификации текста |
| 11 | `ONTOLOGY` | Семантический поиск на основе онтологии |
| 12 | `PROVENANCE` | Отслеживание происхождения и источников данных |
| 13 | `AGGREGATE` | Агрегированный ранжированный поиск по нескольким источникам |
| 14 | `STRUCTURED` | Фильтрация по метаданным + векторный поиск |
| 15 | `GIT` | Анализ Git-репозиториев и поиск по коду |

### Реранкинг (включён по умолчанию)

Phase 2 поставляет cross-encoder реранкер, который работает по умолчанию,
если сконфигурирован `RERANK_ENDPOINT`. Клиентам не нужно ставить никаких
флагов -- ответы поиска несут per-result `reranked: true`, указывающий, какие
строки были переупорядочены сайдкаром. Чтобы отключить, отправьте
`"rerank": false` в теле `/api/v1/search`. Адаптивный гейт
(`RERANK_SCORE_GAP_THRESHOLD`) может пропускать cross-encoder, когда разброс
оценок кандидатов уже широк. См. `docs/api-reference.md` и
`docs/phase2-rerank-default-design.md` для tri-state-семантики, бюджета
задержки (`RERANK_BUDGET_MS`, по умолчанию 1500мс) и Prometheus-счётчика
`levara_rerank_invocations_total{outcome=...}`.

Поверх cross-encoder, `pkg/graphrank` сливает три сигнала --
`α·vector + β·graph + γ·rerank` -- так что graph-связанные результаты
усиливаются вместе с семантической релевантностью.

## Подсистемы

### Memory palace

Слой долговременной памяти агента, адресуемый по двум независимым осям:
**room** (*о чём* память -- свободная строка подсистемы/темы) и **hall**
(*что это за* память -- контролируемый словарь: `fact`, `event`, `decision`,
`preference`, `advice`, `discovery`). Поддерживает пиннинг критических фактов
для дешёвых брифингов `wake_up`, per-agent дневники (изолированные
namespace'ы) и recall с фильтром по room/hall. Работает на том же стеке
HNSW + SQL, удерживая консистентность SQL ↔ vector на каждой записи плюс
sweep `reconcile_memory`.

### Верифицируемый workspace (Variant B)

Слой записи, где `.md`-файлы являются источником истины, а векторные/графовые
индексы Levara -- расходные деривативы. Агенты пишут через `workspace_write` +
`workspace_commit`; workspace затем индексирует закоммиченный Markdown в
Levara. Люди читают `.md`-файлы напрямую в репозитории. Поставляется с
ACL/проверками доступа, журналом аудита, детекцией конфликтов, фоновыми
index-job'ами и операциями reconcile/GC.

### System2-консолидация

Фоновый janitor (включается через `CONSOLIDATION_INTERVAL`) периодически
сжимает память коллекции: кластеризует почти-дубликаты/связанные записи, затем
механически **сливает** их (cosine ≥ 0.97, оставить новейшую) или
LLM-**абстрагирует** (0.85 ≤ cosine < 0.97) в одну запись. Полностью обратимо --
источники супершедятся и архивируются, а не удаляются, и восстановимы через
`consolidation_revert(run_id)`. Запускайте по требованию с
`consolidate(..., dry_run=true)` для предпросмотра.

### Синхронизация (Mac ↔ Pi)

Двунаправленная sync с bearer-аутентификацией между локальным инстансом и
edge-сервером (Raspberry Pi). `sync(remote_url=..., direction=pull|push)`
сводит коллекции; проверка version-skew предупреждает, когда два бинарника
расходятся.

### gRPC v1 + v2

Оба сервиса регистрируются на одном слушателе `:50051`.
`levara.v1.LevaraService` -- канонический surface (47 RPC -- write, read,
cognify, graph, hybrid search). `levara.v2.LevaraServiceV2` -- минимальное
write-only-подмножество (Insert, BatchInsert, Delete, Search, Info) с
типизированным `ErrorDetail`. v1 -- долгосрочный; v2 позиционируется как
минимальный write-API для новых клиентов, которым не нужны graph/cognify
эндпоинты.

## MCP-интеграция

Levara предоставляет MCP-сервер (Model Context Protocol) для интеграции с
Claude Desktop, Cursor, Claude Code и другими MCP-совместимыми клиентами.
Зарегистрировано **66 инструментов** (`pkg/mcp/tools.go`), сгруппированных по поверхностям:

| Поверхность | # | Инструменты |
|-------------|---|-------------|
| Граф знаний и загрузка | 9 | `add`, `cognify`, `cognify_status`, `codify`, `list_data`, `delete`, `prune`, `prune_graph`, `ingestion_status` |
| Поиск и извлечение | 6 | `search`, `cross_search`, `query_entity`, `list_communities`, `analyze_commits`, `git_search` |
| Memory palace | 8 | `save_memory`, `recall_memory`, `list_memories`, `pin_memory`, `unpin_memory`, `wake_up`, `diary_write`, `diary_read` |
| Консолидация (System2) | 3 | `consolidate`, `consolidation_revert`, `reconcile_memory` |
| История чатов | 3 | `save_chat`, `recall_chat`, `search_chats` |
| Верифицируемый workspace | 25 | `workspace_write`, `workspace_read`, `workspace_index`, `workspace_search`, `workspace_commit`, `workspace_log`, `workspace_revert`, `workspace_manifest`, `workspace_delete`, `workspace_reconcile`, `workspace_reindex_paths`, `workspace_reindex_artifacts`, `workspace_context_artifacts`, `workspace_index_jobs`, `workspace_enqueue_index_job`, `workspace_retry_index_job`, `workspace_watch_status`, `workspace_run_start`, `workspace_run_get`, `workspace_access_check`, `workspace_audit_log`, `workspace_ops_status`, `workspace_conflicts`, `workspace_context`, `workspace_gc` |
| Контекст и sync | 7 | `set_context`, `get_project_context`, `sync`, `sync_status`, `add_feedback`, `get_feedback_stats`, `levara_instructions` |
| Наблюдаемость | 5 | `doctor`, `heartbeat`, `runtime_stats`, `recent_errors`, `check_drift` |

### Конфигурация

Добавьте в конфиг вашего MCP-клиента:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## CLI

```bash
# Проверка состояния
levara health

# Добавление данных
levara add "Текстовое содержимое" --dataset=my_project
levara add ./documents/ --dataset=my_project

# Обработка в граф знаний
levara cognify --dataset=my_project --wait

# Поиск
levara search "Какая основная архитектура?" --type=GRAPH_COMPLETION
levara search "последние изменения" --type=TEMPORAL --since=2025-01-01

# Управление датасетами
levara datasets list
levara datasets delete my_project

# Git-анализ
levara git analyze --repo=. --since=2024-01-01
levara git diff-summary --repo=. --branch=main
```

## Конфигурация

### Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|-------------|----------|
| `DB_PROVIDER` | `postgres` | Бэкенд БД (`sqlite` или `postgres`) |
| `DATABASE_URL` | `data/levara.db` | Строка подключения к БД (SQLite) |
| `VECTOR_DIM` | `768` | Размерность векторов |
| `HTTP_PORT` | `8080` | HTTP-порт |
| `GRPC_PORT` | `50051` | gRPC-порт |
| `JWT_SECRET` | _(авто)_ | Общий HS256-секрет для HTTP + gRPC. Случайные 32 байта на пустом (ок для dev, **обязателен в prod**, чтобы токены пережили рестарт) |
| `ENV` | _(unset)_ | `dev` включает `/swagger/*`; любое другое значение отключает |
| `NEO4J_URI` | _(отключено)_ | URI подключения к Neo4j (SQL fallback при отсутствии) |
| `LLM_PROVIDER` | `ollama` | LLM-провайдер (`openai`, `anthropic`, `ollama`) |
| `LLM_MODEL` | `qwen3.5:latest` | Название LLM-модели |
| `LLM_API_KEY` | _(нет)_ | API-ключ для OpenAI/Anthropic |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | URL Ollama-сервера |
| `EMBED_URL` | `http://localhost:9001` | URL сервера эмбеддингов |
| `RERANK_ENDPOINT` / `RERANK_MODEL` | _(отключено)_ | Cross-encoder реранкер; default-on при заданном endpoint |
| `RERANK_BUDGET_MS` / `RERANK_TIMEOUT_MS` | `1500` / `5000` | Бюджет rerank-прохода и per-request HTTP-таймаут |
| `RERANK_SCORE_GAP_THRESHOLD` | `0` | Адаптивный гейт: пропустить rerank, когда разброс оценок выше порога (0 = всегда rerank) |
| `CONSOLIDATION_INTERVAL` | _(off)_ | Тик фонового janitor'а консолидации (Go duration, напр. `30m`) |
| `S3_BUCKET` | _(отключено)_ | S3-бакет для облачного хранилища |
| `LANGFUSE_PUBLIC_KEY` | _(отключено)_ | Публичный ключ Langfuse |
| `MCP_ENABLED` | `true` | Включить MCP-сервер |
| `HNSW_M` | `16` | Макс. HNSW-соединений на узел |
| `HNSW_EF_MULT` | `8` | Множитель efConstruction |
| `HNSW_EF_MIN` | `32` | Минимальная ширина луча efSearch |

### Флаги CLI

```bash
levara-server \
  -standalone=true \
  -dim=768 \
  -port=8080 \
  -grpc-port=50051 \
  -hnsw-m=20 \
  -hnsw-ef-mult=10 \
  -hnsw-ef-min=50 \
  -data-dir=./data
```

## Развертывание

### Docker

```bash
docker compose up -d --build
```

### Raspberry Pi (ARM64)

```bash
# Кросс-компиляция
make arm64

# Копирование на Pi
scp levara-arm64 pi@raspberrypi:~/levara/

# Установка как systemd-сервис
# См. deploy/raspberry/ для полной настройки
```

### systemd

```ini
[Unit]
Description=Levara Knowledge Graph Engine
After=network.target

[Service]
Type=simple
ExecStart=/opt/levara/levara-server -standalone=true -dim=768
WorkingDirectory=/opt/levara
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Мониторинг

Prometheus-метрики на `http://localhost:8080/metrics`:

- `levara_insert_requests_total` / `levara_insert_duration_seconds`
- `levara_search_requests_total` / `levara_search_duration_seconds`
- `levara_vectors_total`
- `levara_wal_sync_duration_seconds`
- `levara_arena_pages_allocated`
- `levara_rate_limit_rejected_total{channel, bucket}`
- `levara_rerank_invocations_total{outcome}`

## Справочник API

См. [docs/api-reference.md](docs/api-reference.md) для полной документации HTTP и gRPC API.

## Участие в разработке

См. [CONTRIBUTING.md](CONTRIBUTING.md) для настройки среды разработки, стиля кода и правил pull request.

## Лицензия

[MIT](LICENSE)

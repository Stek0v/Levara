# Levara

> Высокопроизводительный движок графов знаний для AI-приложений. Написан на Go.

[![Go Version](https://img.shields.io/badge/Go-1.26+-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## Обзор

Levara -- production-ready движок графов знаний, который преобразует сырые данные в персистентные, поисковые графы знаний. Объединяет векторный поиск (HNSW), графовые базы данных (Neo4j) и LLM-извлечение сущностей в одном бинарнике.

**Ключевые возможности:**

- **15 типов поиска** -- векторный, графовый, гибридный, темпоральный, chain-of-thought, NL-to-Cypher и другие
- **22+ формата документов** -- PDF, DOCX, аудио через Whisper, изображения, код и другие
- **Мульти-провайдер LLM** -- OpenAI, Anthropic, Ollama (локальный)
- **MCP-сервер** для Claude Desktop / Cursor (15 инструментов)
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
Клиент --> Levara HTTP :8080 / gRPC :50051 / MCP
              |-- HNSW Vector Index (in-process, AVX2 SIMD)
              |-- WAL (group commit, 100% crash recovery)
              |-- Arena Memory Allocator (zero-copy, без GC)
              |-- PostgreSQL / SQLite
              |-- Neo4j Graph DB (опционально)
              |-- LLM (OpenAI / Anthropic / Ollama)
              |-- BM25 полнотекстовый поиск
              |-- Prometheus-метрики
```

```
levara/
  cmd/
    server/         # Точка входа
    cli/            # CLI-инструмент
    benchmark/      # Бенчмарки
    loadtest/       # Нагрузочное тестирование
  internal/
    store/          # db.go, wal.go, hnsw.go, arena.go, disk.go
    http/           # handler.go (Fiber HTTP)
    cluster/        # shard.go, node.go, fsm.go (Raft)
  pkg/
    aggregator/     # Агрегация результатов поиска
    audio/          # Транскрипция аудио (Whisper)
    bm25/           # Полнотекстовый поиск
    chunker/        # Разбиение документов
    classify/       # Классификация текста
    embed/          # Провайдеры эмбеддингов
    extract/        # Извлечение сущностей
    fetch/          # Загрузка URL/документов
    fileio/         # Файловый I/O (22+ форматов)
    git/            # Анализ Git-репозиториев
    graph/          # Построение графов знаний
    graphdb/        # Интеграция с Neo4j
    ingest/         # Пайплайн загрузки данных
    llm/            # LLM-провайдеры (OpenAI, Anthropic, Ollama)
    llmcache/       # Кэширование ответов LLM
    llmproxy/       # LLM-прокси/маршрутизация
    observe/        # Трейсинг Langfuse
    ontology/       # Управление онтологиями
    orchestrator/   # Оркестратор пайплайнов
    storage/        # S3 облачное хранилище
    temporal/       # Темпоральный поиск
  pipeline/         # Определения пайплайнов
  proto/            # Protobuf-определения
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

## MCP-интеграция

Levara предоставляет MCP-сервер (Model Context Protocol) для интеграции с Claude Desktop, Cursor и другими MCP-совместимыми клиентами.

**15 инструментов:** `add`, `cognify`, `search`, `datasets_list`, `dataset_delete`, `health`, `prune`, `status`, `git_analyze`, `git_diff_summary`, `classify`, `summarize`, `extract_entities`, `ontology_build`, `graph_query`.

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
| `DB_PROVIDER` | `sqlite` | Бэкенд БД (`sqlite` или `postgres`) |
| `DATABASE_URL` | `data/levara.db` | Строка подключения к БД |
| `VECTOR_DIM` | `768` | Размерность векторов |
| `HTTP_PORT` | `8080` | HTTP-порт |
| `GRPC_PORT` | `50051` | gRPC-порт |
| `NEO4J_URI` | _(отключено)_ | URI подключения к Neo4j |
| `NEO4J_USER` | `neo4j` | Имя пользователя Neo4j |
| `NEO4J_PASSWORD` | _(нет)_ | Пароль Neo4j |
| `LLM_PROVIDER` | `ollama` | LLM-провайдер (`openai`, `anthropic`, `ollama`) |
| `LLM_MODEL` | `qwen3.5:latest` | Название LLM-модели |
| `LLM_API_KEY` | _(нет)_ | API-ключ для OpenAI/Anthropic |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | URL Ollama-сервера |
| `EMBED_URL` | `http://localhost:9001` | URL сервера эмбеддингов |
| `S3_BUCKET` | _(отключено)_ | S3-бакет для облачного хранилища |
| `S3_REGION` | `us-east-1` | AWS-регион |
| `LANGFUSE_PUBLIC_KEY` | _(отключено)_ | Публичный ключ Langfuse |
| `LANGFUSE_SECRET_KEY` | _(нет)_ | Секретный ключ Langfuse |
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

Grafana-дашборд включен в `deploy/docker/docker-compose.yml`.

## Справочник API

См. [docs/api-reference.md](docs/api-reference.md) для полной документации HTTP и gRPC API.

## Участие в разработке

См. [CONTRIBUTING.md](CONTRIBUTING.md) для настройки среды разработки, стиля кода и правил pull request.

## Лицензия

[MIT](LICENSE)

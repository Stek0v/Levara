# Levara — Документация по интеграциям

## Содержание
1. [LLM Providers](#1-llm-providers)
2. [Embedding Servers](#2-embedding-servers)
3. [Neo4j Graph Database](#3-neo4j-graph-database)
4. [PostgreSQL](#4-postgresql)
5. [Whisper Audio Transcription](#5-whisper-audio-transcription)
6. [S3 Cloud Storage](#6-s3-cloud-storage)
7. [Langfuse LLM Tracing](#7-langfuse-llm-tracing)
8. [Prometheus Monitoring](#8-prometheus-monitoring)
9. [MCP (Model Context Protocol)](#9-mcp-model-context-protocol)
10. [Ollama](#10-ollama)
11. [Docker Compose](#11-docker-compose)

---

## 1. LLM Providers

### Поддерживаемые провайдеры
- OpenAI (GPT-4o, GPT-3.5)
- Ollama (gemma3, qwen3.5, llama3.1, любая модель)
- Anthropic Claude (Claude 3.5/4)

### Настройка

#### OpenAI / Ollama (OpenAI-compatible)
```bash
LLM_PROVIDER=openai
LLM_ENDPOINT=http://localhost:11434/v1    # Ollama
# или
LLM_ENDPOINT=https://api.openai.com/v1   # OpenAI
LLM_API_KEY=sk-...                         # для OpenAI
LLM_MODEL=gemma3:4b                        # для Ollama
# или
LLM_MODEL=gpt-4o-mini                     # для OpenAI
```

#### Anthropic Claude
```bash
LLM_PROVIDER=anthropic
LLM_API_KEY=sk-ant-...
LLM_MODEL=claude-sonnet-4-20250514
# ENDPOINT не нужен — hardcoded https://api.anthropic.com/v1/messages
```

### Structured Output
Автоматически: JSON Schema mode для OpenAI, prompt-based для Anthropic.
Retry до 3 раз при невалидном JSON.

### Rate Limiting
```bash
LLM_RATE_LIMIT_REQUESTS=60   # max requests
LLM_RATE_LIMIT_INTERVAL=60   # per N seconds
```

### LLM Cache
Автоматический — persistent JSONL file (`data/llm_cache.jsonl`).
Key = SHA256(model + prompt + system_prompt + temperature).
Повторный cognify того же текста: 77x speedup.

---

## 2. Embedding Servers

### Поддерживаемые
- Ollama (nomic-embed-text, mxbai-embed-large)
- OpenAI Embeddings API (text-embedding-3-small)
- Любой OpenAI-compatible embedding server

### Настройка
```bash
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
EMBEDDING_MODEL=nomic-embed-text-v2-moe
```

### Размерность
Сервер автоматически определяет dim из первого embedding. Поддерживаемые: 384, 768, 1024, 1536, 3072.

---

## 3. Neo4j Graph Database

### Назначение
Knowledge graph: entities, relationships, temporal events.

### Настройка
```bash
# CLI flags:
-neo4j-url="bolt://localhost:7687"
-neo4j-user=neo4j
-neo4j-password=password
-neo4j-database=neo4j
```

### Docker
```bash
docker run -d --name neo4j \
  -p 7687:7687 -p 7474:7474 \
  -e NEO4J_AUTH=neo4j/password \
  -e NEO4J_PLUGINS='["apoc"]' \
  neo4j:5-community
```

### Что хранится
- Entity nodes (name, type, description, dataset_id)
- Relationship edges (source -> target, relationship_name)
- TemporalEvent nodes (date, text context)
- HAPPENED_AT edges (entity -> temporal event)

### Cypher Queries
```bash
ALLOW_CYPHER_QUERY=true  # разрешить raw Cypher через API
# Optional startup toggle (default: enabled):
NEO4J_BOOTSTRAP_SCHEMA=false  # не выполнять startup CREATE INDEX/CONSTRAINT
```
Защита: CREATE/MERGE/DELETE/SET/REMOVE/DROP блокируются.

### Fallback
Без Neo4j -> graph data хранится в PostgreSQL (graph_nodes, graph_edges).

---

## 4. PostgreSQL

### Назначение
Metadata, users, datasets, sessions, ACL, notebooks.

### Настройка
```bash
DB_HOST=localhost
DB_USERNAME=cognee
DB_PASSWORD=cognee
DB_NAME=cognee_db
DB_PORT=5432
```

### Таблицы (18)
users, datasets, data, dataset_data, user_settings, graph_nodes, graph_edges,
notebooks, notebook_cells, dataset_shares, tenants, user_tenant, roles, user_role,
acl, interactions, ontologies, principals

### Auto-migration
При старте сервер автоматически создаёт все таблицы.

### Без PostgreSQL
Сервер работает, но: нет auth, нет datasets persistence, нет RBAC.

---

## 5. Whisper Audio Transcription

### Назначение
Транскрипция аудио файлов (mp3, wav, m4a, ogg, flac, webm) в текст.

### Настройка
```bash
WHISPER_ENDPOINT=http://localhost:9002/v1/audio/transcriptions
WHISPER_API_KEY=sk-...    # для OpenAI, опционально для local
WHISPER_MODEL=whisper-1   # или "base", "small", "medium", "large"
```

### Поддерживаемые форматы
mp3, mp4, mpeg, mpga, m4a, wav, webm, ogg, flac

### Как работает
1. POST /add с аудио файлом
2. Detect audio format по расширению
3. Отправить в Whisper API (multipart/form-data)
4. Получить текст транскрипции
5. Текст проходит через обычный pipeline (chunk -> cognify -> search)

### Варианты Whisper сервера

#### OpenAI API
```bash
WHISPER_ENDPOINT=https://api.openai.com/v1/audio/transcriptions
WHISPER_API_KEY=sk-...
WHISPER_MODEL=whisper-1
```

#### whisper.cpp (local)
```bash
# Запуск whisper.cpp server:
./server -m models/ggml-base.bin --host 0.0.0.0 --port 9002

WHISPER_ENDPOINT=http://localhost:9002/v1/audio/transcriptions
WHISPER_MODEL=base
```

#### Faster-Whisper (Python)
```bash
pip install faster-whisper
# Запуск: faster-whisper-server --host 0.0.0.0 --port 9002

WHISPER_ENDPOINT=http://localhost:9002/v1/audio/transcriptions
```

### Без Whisper
Аудио файлы -> ошибка "WHISPER_ENDPOINT not configured". Остальные форматы работают.

---

## 6. S3 Cloud Storage

### Назначение
Хранение загруженных файлов в AWS S3 или совместимом хранилище.

### Настройка
```bash
STORAGE_BACKEND=s3
S3_BUCKET=levara-data
S3_REGION=us-east-1
S3_ENDPOINT=https://s3.amazonaws.com  # или MinIO URL
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=secret...
```

### MinIO (self-hosted S3)
```bash
# Docker:
docker run -d --name minio \
  -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=admin \
  -e MINIO_ROOT_PASSWORD=password \
  minio/minio server /data --console-address ":9001"

S3_ENDPOINT=http://localhost:9000
S3_BUCKET=levara
AWS_ACCESS_KEY_ID=admin
AWS_SECRET_ACCESS_KEY=password
```

### Операции
- Save: PUT с AWS Signature V4
- Load: GET
- Delete: DELETE (idempotent)
- List: GET с prefix + pagination
- Exists: HEAD

### Без S3
По умолчанию `STORAGE_BACKEND=local` -- файлы в `data/uploads/`.

---

## 7. Langfuse LLM Tracing

### Назначение
Мониторинг LLM вызовов: latency, tokens, input/output, ошибки.

### Настройка
```bash
LANGFUSE_PUBLIC_KEY=pk-lf-...
LANGFUSE_SECRET_KEY=sk-lf-...
LANGFUSE_ENDPOINT=https://cloud.langfuse.com  # или self-hosted
```

### Что трейсится
Каждый LLM вызов (entity extraction, RAG completion, COT reasoning, NL->Cypher):
- Model, input prompt, output response
- Latency (ms)
- Token count (input + output)
- Status (success/error)
- Trace ID для корреляции

### Self-hosted Langfuse
```bash
docker run -d --name langfuse \
  -p 3000:3000 \
  -e DATABASE_URL=postgresql://user:pass@host:5432/langfuse \
  langfuse/langfuse

LANGFUSE_ENDPOINT=http://localhost:3000
```

### Без Langfuse
Трейсинг отключён. Нет overhead.

---

## 8. Prometheus Monitoring

### Назначение
Метрики производительности: latency, throughput, errors.

### Endpoint
```
GET /metrics
```

### Метрики
- `levara_http_requests_total` -- счётчик запросов
- `levara_http_request_duration_seconds` -- гистограмма latency
- `levara_search_requests_total` -- поисковые запросы по типу
- `levara_cognify_duration_seconds` -- время cognify pipeline
- `levara_vectors_total` -- количество vectors в коллекциях

### Prometheus config
```yaml
scrape_configs:
  - job_name: levara
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: /metrics
    scrape_interval: 10s
```

### Grafana Dashboard
Импортировать metrics в Grafana для визуализации.

---

## 9. MCP (Model Context Protocol)

### Назначение
Интеграция с AI-агентами: Claude Desktop, Cursor, Cline.

### Endpoint
```
POST /mcp  (JSON-RPC 2.0)
```

### Tools (7)
| Tool | Описание |
|------|----------|
| cognify | Запустить pipeline extraction |
| search | Поиск по knowledge graph |
| add | Добавить данные |
| list_data | Список datasets |
| delete | Удалить данные |
| prune | Очистить данные |
| cognify_status | Статус pipeline |

### Claude Desktop config
```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

---

## 10. Ollama

### Назначение
Локальный LLM + embedding server.

### Установка
```bash
curl -fsSL https://ollama.ai/install.sh | sh
```

### Модели
```bash
ollama pull gemma3:4b            # LLM (3.2GB)
ollama pull nomic-embed-text     # Embeddings (261MB)
ollama pull qwen3.5:latest       # Альтернативный LLM
```

### Настройка для Levara
```bash
LLM_PROVIDER=openai
LLM_ENDPOINT=http://localhost:11434/v1
LLM_MODEL=gemma3:4b
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
EMBEDDING_MODEL=nomic-embed-text
```

### GPU ускорение
Ollama автоматически использует GPU если доступен.
Для принудительного GPU: `OLLAMA_NUM_GPU=99`

---

## 11. Docker Compose

### Полный стек
```yaml
services:
  levara:
    build: ./Levara
    ports: ["8080:8080", "50051:50051"]
    environment:
      - DB_HOST=postgres
      - LLM_ENDPOINT=http://ollama:11434/v1
      - EMBEDDING_ENDPOINT=http://ollama:11434/v1/embeddings
    depends_on: [postgres, neo4j, ollama]

  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: cognee
      POSTGRES_PASSWORD: cognee
      POSTGRES_DB: cognee_db
    ports: ["5432:5432"]

  neo4j:
    image: neo4j:5-community
    environment:
      NEO4J_AUTH: neo4j/password
      NEO4J_PLUGINS: '["apoc"]'
    ports: ["7687:7687", "7474:7474"]

  ollama:
    image: ollama/ollama
    ports: ["11434:11434"]
    deploy:
      resources:
        reservations:
          devices:
            - capabilities: [gpu]

  prometheus:
    image: prom/prometheus
    ports: ["9090:9090"]
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
```

### Минимальный стек (без GPU)
```bash
docker compose up -d levara postgres
# LLM через внешний Ollama или OpenAI API
```

---

## Переменные окружения (полный список)

| Переменная | Описание | Default |
|-----------|----------|---------|
| `LLM_PROVIDER` | openai, anthropic | openai |
| `LLM_ENDPOINT` | URL LLM API | -- |
| `LLM_MODEL` | Имя модели | -- |
| `LLM_API_KEY` | API ключ | -- |
| `LLM_RATE_LIMIT_REQUESTS` | Max requests | -- (unlimited) |
| `LLM_RATE_LIMIT_INTERVAL` | Interval (sec) | 60 |
| `EMBEDDING_ENDPOINT` | URL embedding API | -- |
| `EMBEDDING_MODEL` | Имя модели | -- |
| `DB_HOST` | PostgreSQL host | -- |
| `DB_USERNAME` | PG user | cognee |
| `DB_PASSWORD` | PG password | cognee |
| `DB_NAME` | PG database | cognee_db |
| `DB_PORT` | PG port | 5432 |
| `WHISPER_ENDPOINT` | Whisper API URL | -- |
| `WHISPER_API_KEY` | Whisper API key | -- |
| `WHISPER_MODEL` | Whisper model | whisper-1 |
| `STORAGE_BACKEND` | local, s3 | local |
| `S3_BUCKET` | S3 bucket name | -- |
| `S3_REGION` | AWS region | us-east-1 |
| `S3_ENDPOINT` | Custom S3 endpoint | -- |
| `AWS_ACCESS_KEY_ID` | AWS key | -- |
| `AWS_SECRET_ACCESS_KEY` | AWS secret | -- |
| `LANGFUSE_PUBLIC_KEY` | Langfuse key | -- |
| `LANGFUSE_SECRET_KEY` | Langfuse secret | -- |
| `LANGFUSE_ENDPOINT` | Langfuse URL | https://cloud.langfuse.com |
| `ALLOW_CYPHER_QUERY` | Allow raw Cypher | false |
| `LOG_LEVEL` | DEBUG/INFO/WARN/ERROR | INFO |

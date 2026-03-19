# Перенос проекта Cognevra на другой компьютер

## Быстрый старт

```bash
# 1. Клонировать репозиторий (или скопировать)
git clone <repo_url> new_db
cd new_db

# 2. Установить зависимости
# Go 1.25+
cd Cognevra && go mod download && cd ..

# Python 3.10+
pip install grpcio grpcio-tools protobuf pytest pytest-asyncio aiohttp lancedb pydantic

# Опционально: cognee-plugin
cd cognee-plugin && pip install -e ".[dev]" && cd ..

# 3. Сгенерировать proto stubs
make proto

# 4. Запустить Cognevra
docker compose up -d --build

# 5. Проверить
python3 -c "import grpc; ch=grpc.insecure_channel('localhost:50051'); grpc.channel_ready_future(ch).result(timeout=5); print('OK'); ch.close()"

# 6. Запустить тесты
python3 -m pytest tests/ -v      # Python: 80+ integration tests
cd Cognevra && go test ./... -v   # Go: ~50 tests
```

## Что нужно на новом компьютере

### Обязательно
- Go 1.25+ (`go version`)
- Python 3.10+ (`python3 --version`)
- Docker + Docker Compose (`docker compose version`)
- protoc + protoc-gen-go + protoc-gen-go-grpc (`protoc --version`)
- grpcio-tools (`pip install grpcio-tools`)

### Для GPU тестов (embed-server)
- NVIDIA GPU с CUDA
- Docker image `local_net-embed-server:latest` (или собрать)
- Запуск: `docker run -d --gpus all -p 9001:8001 -e TRUST_REMOTE_CODE=1 local_net-embed-server:latest`

### Для LLM тестов
- Ollama с моделью Qwen 3.5 (`ollama run qwen3.5`)

## Файлы НЕ в git (скопировать отдельно)

```
cognee/                              # Внешний Cognee репо (в .gitignore)
  cognee/infrastructure/databases/
    vector/cognevra/                 # Наша копия адаптера + generated stubs
Edvards_Dzanet_Uragan_r4_P61XH.txt  # Тестовая книга "Ураган" (1.2MB)
.env                                # Локальные настройки (скопировать из .env.template)
```

### Восстановление cognee tree copy

```bash
# Скопировать адаптер из plugin в cognee tree
mkdir -p cognee/cognee/infrastructure/databases/vector/cognevra/generated
cp cognee-plugin/cognevra_adapter/CognevraAdapter.py \
   cognee/cognee/infrastructure/databases/vector/cognevra/CognevraAdapter.py
# Добавить health_check() метод в cognee tree copy (есть только там)

# Сгенерировать Python proto stubs
make proto
```

## Структура проекта

```
new_db/
  Cognevra/                     # Go server (15 gRPC RPCs)
    cmd/server/main.go          # Entry point, CLI flags
    internal/store/             # HNSW, WAL, Arena, DiskStore, Collections
    internal/grpc/service.go    # gRPC handlers
    pkg/chunker/                # Text chunking
    pkg/embed/                  # Embedding client
    pkg/fileio/                 # HashFiles, ListDirectory
    pkg/aggregator/             # Search result ranking
    pipeline/                   # In-process search pipeline
    proto/cognevra.proto        # gRPC service definition
  cognee-plugin/                # Python gRPC adapter (pip installable)
    cognevra_adapter/
      CognevraAdapter.py        # 13 methods
      generated/                # Proto stubs (.gitignore)
  tests/                        # 80+ Python integration tests
  docs/
    cognevra-integration/       # LLM optimizations, coordinators
    archived/                   # Old plans and specs
  docker-compose.yml            # Cognevra + Prometheus
  Makefile                      # up, down, test, proto, clean
```

## Ключевые порты

| Service | Port | Описание |
|---------|------|----------|
| Cognevra gRPC | 50051 | Primary API |
| Cognevra HTTP | 8080 | Prometheus metrics |
| Prometheus | 9090 | Metrics dashboard |
| embed-server | 9001 | Sentence embeddings |
| Ollama | 11434 | LLM (для RAG тестов) |

## Конфигурация (docker-compose.yml)

```
COGNEVRA_DIM=1024        # Vector dimension
COGNEVRA_SHARDS=3        # Number of shards
HNSW_M=20                # HNSW graph connectivity
HNSW_EF_MULT=10          # efSearch multiplier
HNSW_EF_MIN=64           # Minimum efSearch
```

## Claude Code memory

Директория `.claude/` содержит:
- `projects/-home-stek0v-src-new-db/memory/` — память проекта (MEMORY.md, user_role.md, project_cognevra.md, feedback_style.md)
- `plans/` — текущие планы

При переносе скопировать `.claude/` целиком для сохранения контекста.

## Текущие задачи (TODO)

1. **Full pipeline benchmark** — скрипт готов, нужен GPU embed-server
2. **Production deployment** — K8s manifests, Grafana
3. **LLM multi-chunk batching** — prompt engineering для batch extraction

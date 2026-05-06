# Levara на Raspberry Pi — Полное руководство

> **Миграция с Cognevra (актуально для Pi-устройств, на которые ставили `cognevra` до 2026-05).**
> Имена файлов и сервиса изменились в рамках ребрендинга:
>
> | Было | Стало |
> |---|---|
> | `cognevra.service` | `levara.service` |
> | `cognevra.env` | `levara.env` |
> | `/etc/cognevra/cognevra.env` | `/etc/levara/levara.env` |
> | `/var/lib/cognevra/` | `/var/lib/levara/` |
> | `/usr/local/bin/cognevra` | `/usr/local/bin/levara` |
> | systemd unit `cognevra` | `levara` |
> | system user `cognevra` | `levara` |
>
> На уже-установленных устройствах:
>
> ```bash
> sudo systemctl stop cognevra
> sudo systemctl disable cognevra
> sudo mv /etc/cognevra /etc/levara
> sudo mv /etc/levara/cognevra.env /etc/levara/levara.env
> sudo mv /var/lib/cognevra /var/lib/levara
> # Опционально пересоздать пользователя — или оставить cognevra и поправить .service:
> sudo cp Raspberry/levara.service /etc/systemd/system/levara.service
> sudo systemctl daemon-reload
> sudo systemctl enable --now levara
> sudo rm -f /etc/systemd/system/cognevra.service
> ```
>
> Кодовая база за бинарём тоже сменилась: Dockerfile теперь собирает из `Levara/` (current code, replaces legacy `Cognevra/` → `Levara-legacy/`). API совместимо, флаги те же.

## Содержание

1. [Системные требования](#1-системные-требования)
2. [Быстрый старт (5 минут)](#2-быстрый-старт-5-минут)
3. [Установка (подробно)](#3-установка-подробно)
4. [Конфигурация](#4-конфигурация)
5. [Подключение Claude Code](#5-подключение-claude-code)
6. [Мониторинг](#6-мониторинг)
7. [Тюнинг производительности](#7-тюнинг-производительности)
8. [Backup & Recovery](#8-backup--recovery)
9. [Troubleshooting](#9-troubleshooting)

---

## 1. Системные требования

### Поддерживаемые платы

| Плата | RAM | Статус | Примечание |
|-------|-----|--------|------------|
| Pi 4B 4GB | 4GB | Минимум | Ограниченный LLM, только маленькие модели |
| Pi 4B 8GB | 8GB | **Рекомендуемый** | Полноценная работа с nomic-embed + gemma3:4b |
| Pi 5 8GB | 8GB | **Оптимальный** | 2x CPU perf, PCIe SSD, лучший thermal |

### Распределение RAM

| Компонент | Pi 4GB | Pi 8GB | Описание |
|-----------|--------|--------|----------|
| OS + system | 500MB | 500MB | Headless Raspberry Pi OS Lite |
| Levara HNSW | 500MB-1GB | 1-2GB | Зависит от кол-ва vectors |
| Ollama embed | 300MB | 500MB | nomic-embed-text / all-minilm |
| Ollama LLM | 1-2GB | 3-5GB | Зависит от модели |
| SQLite cache | 100MB | 200MB | WAL + page cache |
| Swap headroom | 1.5GB | 0-1GB | Запас для пиков |

### Storage

- **SSD (USB 3.0 или PCIe на Pi 5)** — настоятельно рекомендуется
  - SATA SSD через USB 3.0: ~350 MB/s
  - NVMe через PCIe (Pi 5): ~800 MB/s
- **microSD** — только для boot, НЕ для данных
  - Random write: ~2 MB/s (в 100x медленнее SSD)
  - WAL fsync на microSD = bottleneck

### Swap

Swap обязателен — Ollama загружает модели целиком в RAM.

```bash
# Минимум 2GB, рекомендуемо 4GB
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

---

## 2. Быстрый старт (5 минут)

```bash
# 1. Скачать binary
wget https://github.com/stek0v/levara/releases/latest/download/levara-arm64
chmod +x levara-arm64

# 2. Установить Ollama
curl -fsSL https://ollama.ai/install.sh | sh
ollama pull nomic-embed-text

# 3. Запустить
DB_PROVIDER=sqlite \
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBEDDING_MODEL=nomic-embed-text \
./levara-arm64 -standalone=true -dim=768 -shards=1 -port=8080

# 4. Проверить
curl http://localhost:8080/health
```

Ожидаемый ответ:
```json
{"status":"ok","version":"...","uptime":"..."}
```

---

## 3. Установка (подробно)

### 3.1 Подготовка OS

```bash
# Обновление системы
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl wget git sqlite3 jq

# Swap (если ещё не настроен)
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab

# Проверка
free -h
```

### 3.2 SSD Setup (рекомендуемо)

USB SSD даёт 10-50x ускорение I/O по сравнению с microSD.

```bash
# Найти SSD
lsblk

# Форматировать (пример: /dev/sda1)
sudo mkfs.ext4 /dev/sda1

# Создать mount point
sudo mkdir -p /mnt/ssd

# Временный mount для проверки
sudo mount /dev/sda1 /mnt/ssd

# Постоянный mount через fstab
# Получить UUID
sudo blkid /dev/sda1
# Добавить в /etc/fstab:
# UUID=<your-uuid> /mnt/ssd ext4 defaults,noatime 0 2

# Создать директорию данных
sudo mkdir -p /mnt/ssd/levara
sudo chown $(whoami):$(whoami) /mnt/ssd/levara
```

Если SSD используется как data dir, укажите:
```bash
DB_PATH=/mnt/ssd/levara/levara.db
```

### 3.3 Установка Ollama

```bash
curl -fsSL https://ollama.ai/install.sh | sh

# Включить автозапуск
sudo systemctl enable ollama
sudo systemctl start ollama

# Проверить
ollama --version
curl http://127.0.0.1:11434/api/tags
```

### 3.4 Выбор моделей

| RAM | Embed Model | LLM Model | Dim | Примечание |
|-----|-------------|-----------|-----|------------|
| 4GB | all-minilm:l6-v2 (33MB) | qwen2:0.5b (500MB) | 384 | Минимальный набор, базовое качество |
| 8GB | nomic-embed-text (261MB) | gemma3:4b (3.2GB) | 768 | Оптимальный баланс качество/ресурсы |
| 8GB+ | nomic-embed-text (261MB) | qwen3.5 (6.3GB) | 768 | Лучшее качество, возможен swap |

```bash
# Pi 4GB
ollama pull all-minilm:l6-v2
ollama pull qwen2:0.5b

# Pi 8GB (рекомендуемо)
ollama pull nomic-embed-text
ollama pull gemma3:4b

# Pi 8GB+ (максимальное качество)
ollama pull nomic-embed-text
ollama pull qwen3.5
```

### 3.5 Установка Levara

#### Вариант A: Binary (рекомендуемо)

```bash
# Скачать
wget https://github.com/stek0v/levara/releases/latest/download/levara-arm64
sudo mv levara-arm64 /usr/local/bin/levara
sudo chmod +x /usr/local/bin/levara

# Создать директории
sudo mkdir -p /var/lib/levara/data
sudo mkdir -p /etc/levara
sudo mkdir -p /var/log/levara

# Создать пользователя
sudo useradd -r -s /bin/false levara
sudo chown -R levara:levara /var/lib/levara /var/log/levara

# Скопировать env файл
sudo cp levara.env /etc/levara/levara.env

# Установить systemd service
sudo cp levara.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable levara
sudo systemctl start levara
```

#### Вариант B: Docker

```bash
# Установить Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER

# Запустить
docker compose -f docker-compose.yml up -d
```

#### Вариант C: Автоматический setup

```bash
chmod +x setup.sh
sudo ./setup.sh
```

---

## 4. Конфигурация

### 4.1 Переменные окружения

| Переменная | Default | Pi-рекомендация | Описание |
|------------|---------|-----------------|----------|
| `DB_PROVIDER` | `sqlite` | `sqlite` | Storage backend (sqlite/postgres) |
| `DB_PATH` | `./levara.db` | `/var/lib/levara/levara.db` | Путь к SQLite БД |
| `EMBEDDING_ENDPOINT` | — | `http://localhost:11434/v1/embeddings` | URL embed сервера |
| `EMBEDDING_MODEL` | — | `nomic-embed-text` | Модель для embeddings |
| `LLM_PROVIDER` | `openai` | `openai` | OpenAI-compatible API |
| `LLM_ENDPOINT` | — | `http://localhost:11434/v1` | URL LLM сервера |
| `LLM_MODEL` | — | `gemma3:4b` | LLM модель |
| `LOG_LEVEL` | `INFO` | `INFO` | DEBUG/INFO/WARN/ERROR |
| `LLM_RATE_LIMIT_REQUESTS` | `60` | `10` | Макс запросов к LLM |
| `LLM_RATE_LIMIT_INTERVAL` | `60` | `60` | Интервал rate limit (сек) |
| `CACHE_TTL` | `3600` | `7200` | TTL кеша (сек) |
| `CACHE_MAX_SIZE` | `1000` | `500` | Макс записей в кеше |

### 4.2 CLI флаги

| Флаг | Default | Pi 4GB | Pi 8GB | Описание |
|------|---------|--------|--------|----------|
| `-standalone` | `false` | `true` | `true` | Standalone mode (без Raft) |
| `-dim` | `1024` | `384` | `768` | Размерность embeddings |
| `-shards` | `3` | `1` | `1-2` | Количество HNSW shards |
| `-port` | `8080` | `8080` | `8080` | HTTP порт |
| `-grpc-port` | `50051` | `0` | `0` | gRPC порт (0=disabled) |
| `-data-dir` | `./data` | `/var/lib/levara/data` | `/var/lib/levara/data` | Директория данных |
| `-hnsw-m` | `16` | `12` | `16` | HNSW connectivity |
| `-hnsw-ef-mult` | `8` | `6` | `8` | efConstruction multiplier |
| `-hnsw-ef-min` | `64` | `32` | `64` | Минимальный efSearch |

### 4.3 Пример конфигурации

```bash
# /etc/levara/levara.env
DB_PROVIDER=sqlite
DB_PATH=/var/lib/levara/levara.db
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
EMBEDDING_MODEL=nomic-embed-text
LLM_PROVIDER=openai
LLM_ENDPOINT=http://localhost:11434/v1
LLM_MODEL=gemma3:4b
LOG_LEVEL=INFO
LLM_RATE_LIMIT_REQUESTS=10
LLM_RATE_LIMIT_INTERVAL=60
CACHE_TTL=7200
CACHE_MAX_SIZE=500
```

---

## 5. Подключение Claude Code

### 5.1 MCP конфигурация

Добавить в `~/.claude/settings.json` или `.mcp.json` проекта:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://raspberrypi.local:8080/mcp"
    }
  }
}
```

> Замените `raspberrypi.local` на IP или hostname вашего Pi.

### 5.2 Доступные MCP tools

| Tool | Описание |
|------|----------|
| `cognee_add` | Добавить текст/данные в memory |
| `cognee_search` | Семантический поиск по памяти |
| `cognee_cognify` | Запустить cognify pipeline (extract entities, build graph) |
| `cognee_delete` | Удалить данные из memory |
| `cognee_get_status` | Статус pipeline |
| `cognee_get_graphs` | Получить knowledge graph |
| `cognee_get_chunks` | Получить raw chunks |
| `cognee_get_entities` | Получить extracted entities |
| `cognee_get_relationships` | Получить связи между entities |
| `cognee_get_summaries` | Получить summaries |
| `cognee_temporal_search` | Поиск с учётом времени |
| `cognee_graph_search` | Graph-based поиск |
| `cognee_get_collections` | Список коллекций |
| `cognee_create_collection` | Создать коллекцию |
| `cognee_health` | Health check |

### 5.3 Примеры использования

#### Сохранение проектной памяти

В Claude Code:
```
Сохрани в память: архитектура проекта — Go backend, React frontend,
PostgreSQL, Redis cache. Деплой через Docker Compose на VPS.
```

Claude вызовет `cognee_add` -> `cognee_cognify` автоматически.

#### Поиск по знаниям

```
Найди в памяти всё что мы обсуждали про архитектуру кеширования
```

Claude вызовет `cognee_search` с query "архитектура кеширования".

#### Анализ git коммитов

```
Запомни: за последнюю неделю мы сделали рефакторинг auth модуля,
добавили rate limiting, исправили N+1 в orders endpoint.
```

#### Temporal queries

```
Что мы обсуждали на прошлой неделе про деплой?
```

Claude вызовет `cognee_temporal_search`.

---

## 6. Мониторинг

### 6.1 Health Check

```bash
# Простой health check
curl -s http://localhost:8080/health | jq

# Подробный health check
curl -s http://localhost:8080/health/details | jq
```

Пример ответа:
```json
{
  "status": "ok",
  "version": "1.0.0",
  "uptime": "2h35m",
  "components": {
    "hnsw": "ok",
    "sqlite": "ok",
    "embedding": "ok",
    "llm": "ok"
  }
}
```

### 6.2 Prometheus Metrics

```bash
curl -s http://localhost:8080/metrics
```

Ключевые метрики:

| Метрика | Описание |
|---------|----------|
| `levara_search_duration_seconds` | Латентность поиска |
| `levara_insert_duration_seconds` | Латентность вставки |
| `levara_vectors_total` | Общее кол-во vectors |
| `levara_memory_bytes` | Потребление памяти |
| `levara_wal_size_bytes` | Размер WAL |
| `levara_cache_hits_total` | Cache hits |
| `levara_cache_misses_total` | Cache misses |

### 6.3 Error Tracking

```bash
curl -s http://localhost:8080/api/v1/errors | jq
```

### 6.4 Cache Statistics

```bash
curl -s http://localhost:8080/api/v1/cache/stats | jq
```

Пример:
```json
{
  "hits": 1523,
  "misses": 87,
  "hit_rate": 0.946,
  "size": 342,
  "max_size": 500,
  "evictions": 12
}
```

### 6.5 systemd watchdog

```ini
# /etc/systemd/system/levara.service
# (см. levara.service в этой папке)
```

Мониторинг через systemd:
```bash
# Статус
sudo systemctl status levara

# Логи
sudo journalctl -u levara -f

# Последние ошибки
sudo journalctl -u levara --priority=err --since="1 hour ago"
```

### 6.6 Автоматический мониторинг (cron)

```bash
# Установить monitor.sh
sudo cp monitor.sh /usr/local/bin/levara-monitor
sudo chmod +x /usr/local/bin/levara-monitor

# Добавить в cron (каждые 5 минут)
echo "*/5 * * * * /usr/local/bin/levara-monitor" | sudo crontab -
```

---

## 7. Тюнинг производительности

> Подробный гайд см. в [TUNING.md](TUNING.md)

### 7.1 HNSW параметры

| Параметр | Default | Pi 4GB | Pi 8GB | Описание |
|----------|---------|--------|--------|----------|
| `-shards` | 3 | 1 | 2 | Меньше shards = меньше RAM |
| `-hnsw-m` | 16 | 12 | 16 | Connectivity. Меньше M = меньше RAM, ниже recall |
| `-hnsw-ef-mult` | 8 | 6 | 8 | efConstruction = dim * mult. Меньше = быстрее build, ниже recall |
| `-hnsw-ef-min` | 64 | 32 | 64 | Минимальный efSearch |

### 7.2 Ollama тюнинг

```bash
# Создать override для Ollama
sudo mkdir -p /etc/systemd/system/ollama.service.d
sudo tee /etc/systemd/system/ollama.service.d/override.conf << 'EOF'
[Service]
Environment="OLLAMA_NUM_PARALLEL=1"
Environment="OLLAMA_MAX_LOADED_MODELS=1"
Environment="OLLAMA_KEEP_ALIVE=30m"
EOF
sudo systemctl daemon-reload
sudo systemctl restart ollama
```

- `OLLAMA_NUM_PARALLEL=1` — один запрос за раз (экономия RAM)
- `OLLAMA_MAX_LOADED_MODELS=1` — одна модель в памяти
- `OLLAMA_KEEP_ALIVE=30m` — выгрузить модель через 30 мин неактивности

### 7.3 SQLite тюнинг

SQLite автоматически настраивается в WAL mode. Дополнительно:

- **Busy timeout**: 5000ms (default) — достаточно для Pi
- **НЕ использовать microSD** для файла БД — SSD обязателен для WAL performance
- Journal size limit: автоматический checkpoint при 1000 pages

### 7.4 OS тюнинг

```bash
# Отключить ненужные сервисы (headless)
sudo systemctl disable bluetooth
sudo systemctl disable avahi-daemon
sudo systemctl disable cups
sudo systemctl disable triggerhappy

# GPU memory минимум (headless)
echo "gpu_mem=16" | sudo tee -a /boot/config.txt

# Overclock Pi 5 (опционально, требует охлаждение!)
# echo "arm_freq=3000" | sudo tee -a /boot/config.txt

# I/O scheduler для SSD
echo "none" | sudo tee /sys/block/sda/queue/scheduler

# Увеличить лимиты файлов
echo "levara soft nofile 65536" | sudo tee -a /etc/security/limits.conf
echo "levara hard nofile 65536" | sudo tee -a /etc/security/limits.conf
```

---

## 8. Backup & Recovery

### 8.1 Backup

```bash
# Использовать backup.sh из этой папки
sudo cp backup.sh /usr/local/bin/levara-backup
sudo chmod +x /usr/local/bin/levara-backup

# Ручной запуск
sudo /usr/local/bin/levara-backup

# Автоматический backup (ежедневно в 3:00)
echo "0 3 * * * /usr/local/bin/levara-backup" | sudo crontab -
```

#### SQLite backup (онлайн, WAL-safe)

```bash
sqlite3 /var/lib/levara/levara.db ".backup '/backup/levara-$(date +%Y%m%d).db'"
```

#### Полный backup (данные + WAL)

```bash
rsync -a /var/lib/levara/data/ /backup/levara-data/
```

### 8.2 Recovery

```bash
# Остановить сервис
sudo systemctl stop levara

# Восстановить SQLite
sudo cp /backup/levara-latest.db /var/lib/levara/levara.db

# Восстановить data
sudo rsync -a /backup/levara-data/ /var/lib/levara/data/

# Права
sudo chown -R levara:levara /var/lib/levara

# Запустить
sudo systemctl start levara

# Проверить
curl -s http://localhost:8080/health | jq
```

---

## 9. Troubleshooting

### Ollama не запускается

```bash
# Проверить статус
sudo systemctl status ollama
sudo journalctl -u ollama --no-pager -n 50

# Частая причина: не хватает RAM
free -h

# Решение: уменьшить модель или добавить swap
ollama rm qwen3.5
ollama pull qwen2:0.5b
```

### Out of Memory (OOM)

```bash
# Проверить что съедает RAM
ps aux --sort=-%mem | head -10

# Проверить OOM kills
dmesg | grep -i "oom\|killed"

# Решения:
# 1. Уменьшить shards: -shards=1
# 2. Уменьшить HNSW M: -hnsw-m=12
# 3. Использовать меньшую LLM модель
# 4. Увеличить swap
# 5. Установить MemoryMax в systemd (см. levara.service)
```

### SQLite locked

```bash
# Проверить locks
fuser /var/lib/levara/levara.db

# Частая причина: два процесса Levara
ps aux | grep levara

# Решение: остановить дубликат
sudo systemctl stop levara
sudo killall levara
sudo systemctl start levara
```

### Медленный search

```bash
# Проверить латентность
curl -s http://localhost:8080/metrics | grep search_duration

# Причины:
# 1. Данные на microSD -> переехать на SSD
# 2. Swap thrashing -> уменьшить модели
# 3. efSearch слишком высокий -> уменьшить -hnsw-ef-min

# Проверить I/O
iostat -x 1 5
```

### LLM timeout

```bash
# Проверить Ollama
curl -s http://127.0.0.1:11434/api/tags | jq

# Увеличить timeout в env
LLM_TIMEOUT=120

# Первый запрос после старта медленный (загрузка модели)
# Это нормально, подождать 30-60 сек

# Если постоянно медленно — модель слишком большая для RAM
# Уменьшить модель:
ollama rm gemma3:4b
ollama pull qwen2:0.5b
```

### Levara не видит Ollama

```bash
# Проверить что Ollama слушает
ss -tlnp | grep 11434

# Проверить embed endpoint
curl -s http://localhost:11434/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","input":"test"}' | jq '.data[0].embedding | length'

# Должно вернуть 768 (или 384 для all-minilm)
```

### Перезапуск с нуля

```bash
sudo systemctl stop levara
sudo rm -rf /var/lib/levara/data/*
sudo rm -f /var/lib/levara/levara.db
sudo systemctl start levara
```

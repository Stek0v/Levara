# Cognevra Pi — Подробный гайд по тюнингу

## Содержание

1. [HNSW параметры](#1-hnsw-параметры)
2. [Memory Budget Calculator](#2-memory-budget-calculator)
3. [SSD vs microSD](#3-ssd-vs-microsd)
4. [Ollama: сравнение моделей](#4-ollama-сравнение-моделей)
5. [Rate Limiting](#5-rate-limiting)
6. [Local vs Remote LLM](#6-local-vs-remote-llm)
7. [OS-level тюнинг](#7-os-level-тюнинг)

---

## 1. HNSW параметры

### M (connectivity)

M определяет количество связей на каждом уровне графа.

| M | RAM на 10K vectors (dim=768) | Search latency | Recall@10 | Рекомендация |
|---|------------------------------|----------------|-----------|--------------|
| 8 | ~50MB | ~1.5ms | ~0.90 | Экстремальная экономия RAM |
| 12 | ~75MB | ~2.0ms | ~0.94 | **Pi 4GB** |
| 16 | ~100MB | ~2.5ms | ~0.97 | **Pi 8GB (default)** |
| 24 | ~150MB | ~3.5ms | ~0.98 | Не рекомендуется для Pi |
| 32 | ~200MB | ~5.0ms | ~0.99 | Только если RAM много |

**Правило**: M = 12-16 для Pi. Ниже 8 — деградация качества.

```
RAM на 1 vector ≈ dim * 4 bytes + M * 2 * 8 bytes + overhead
Пример (dim=768, M=16): 768*4 + 16*2*8 + 64 ≈ 3.4 KB/vector
10K vectors ≈ 34 MB только HNSW graph
+ arena overhead ≈ 100 MB total
```

### efSearch (query-time parameter)

efSearch = max(hnsw-ef-min, k * hnsw-ef-mult), где k = количество результатов.

| efSearch | Search latency | Recall@10 | Описание |
|----------|----------------|-----------|----------|
| 16 | ~0.5ms | ~0.85 | Слишком низкий |
| 32 | ~1.0ms | ~0.92 | **Минимум для Pi 4GB** |
| 64 | ~2.0ms | ~0.96 | **Default** |
| 128 | ~4.0ms | ~0.98 | Высокая точность |
| 256 | ~8.0ms | ~0.99 | Не нужен для Pi |

**Правило**: efSearch = 32-64 для Pi.

### efConstruction

efConstruction = dim * hnsw-ef-mult. Влияет только на скорость INSERT, не на search.

| ef-mult | efConstruction (dim=768) | Insert time | Graph quality |
|---------|--------------------------|-------------|---------------|
| 4 | 3072 | ~5ms | Базовый |
| 6 | 4608 | ~8ms | **Pi 4GB** |
| 8 | 6144 | ~12ms | **Pi 8GB (default)** |
| 12 | 9216 | ~20ms | Не нужен для Pi |

### Shards

| Shards | RAM overhead | QPS | Описание |
|--------|-------------|-----|----------|
| 1 | Базовый | 1x | **Pi 4GB** — меньше RAM, один lock |
| 2 | +30% | ~1.7x | **Pi 8GB** — баланс |
| 3 | +60% | ~2.3x | Только Pi 5 8GB с SSD |

---

## 2. Memory Budget Calculator

### Pi 4GB

```
Total RAM:              4096 MB
- OS + system:           500 MB
- Ollama embed (minilm):  80 MB
- Ollama LLM (qwen2:0.5b): 800 MB
- Cognevra overhead:     200 MB
---------------------------------
Available for HNSW:    2516 MB
- С 1 shard, dim=384:
  Max vectors ≈ 2516MB / 2.2KB ≈ ~1.1M vectors

Реалистично (с headroom):
  Max vectors ≈ 500K (dim=384, M=12, 1 shard)
```

### Pi 8GB

```
Total RAM:              8192 MB
- OS + system:           500 MB
- Ollama embed (nomic):  400 MB
- Ollama LLM (gemma:4b): 3200 MB
- Cognevra overhead:     300 MB
---------------------------------
Available for HNSW:    3792 MB
- С 2 shards, dim=768:
  Max vectors ≈ 3792MB / 3.4KB ≈ ~1.1M vectors

Реалистично (с headroom):
  Max vectors ≈ 500K (dim=768, M=16, 2 shards)
```

### Формула

```
RAM_per_vector = dim * 4 + M * 2 * 8 + 64  (bytes)
Max_vectors = Available_RAM / RAM_per_vector / num_shards
```

---

## 3. SSD vs microSD

### Benchmark результаты (типичные)

| Операция | microSD (Class 10) | USB 3.0 SATA SSD | NVMe (Pi 5) |
|----------|--------------------|--------------------|-------------|
| Sequential read | 90 MB/s | 350 MB/s | 800 MB/s |
| Sequential write | 30 MB/s | 300 MB/s | 700 MB/s |
| Random read 4K | 2.5 MB/s | 40 MB/s | 80 MB/s |
| Random write 4K | 0.5 MB/s | 35 MB/s | 70 MB/s |
| WAL fsync | 10-30ms | 0.5-2ms | 0.2-1ms |

### Влияние на Cognevra

| Метрика | microSD | SSD | Разница |
|---------|---------|-----|---------|
| Insert latency | 30-80ms | 3-8ms | **10x** |
| WAL checkpoint | 500ms-2s | 50-200ms | **10x** |
| SQLite write | 20-50ms | 2-5ms | **10x** |
| Search latency | ~3ms | ~2.5ms | ~1.2x (в основном in-memory) |
| Startup (10K vectors) | 5-15s | 1-3s | **5x** |

**Вывод**: SSD критичен для write operations и startup. Search почти не зависит от storage (HNSW in-memory).

### Рекомендации по SSD

- **Budget**: любой SATA SSD через USB 3.0 ($15-20)
- **Optimal**: Samsung T7 / SanDisk Extreme Portable
- **Pi 5**: NVMe через HAT (Pimoroni, Geekworm)
- Не нужен большой объём — 64-128GB достаточно

---

## 4. Ollama: сравнение моделей

### Embedding модели

| Модель | Size | Dim | Speed (Pi 4) | Speed (Pi 5) | Качество |
|--------|------|-----|-------------|-------------|----------|
| all-minilm:l6-v2 | 33MB | 384 | ~50ms/text | ~30ms/text | Базовое |
| nomic-embed-text | 261MB | 768 | ~150ms/text | ~80ms/text | **Хорошее** |
| mxbai-embed-large | 670MB | 1024 | ~400ms/text | ~200ms/text | Отличное |

**Рекомендация**: nomic-embed-text — лучший баланс качества и скорости для Pi.

### LLM модели

| Модель | Size | RAM | Speed (Pi 4) | Speed (Pi 5) | Качество |
|--------|------|-----|-------------|-------------|----------|
| qwen2:0.5b | 500MB | ~800MB | 5-8 tok/s | 10-15 tok/s | Базовое |
| phi3:mini | 2.3GB | ~2.5GB | 2-4 tok/s | 5-8 tok/s | Среднее |
| gemma3:4b | 3.2GB | ~3.5GB | 1-3 tok/s | 4-7 tok/s | **Хорошее** |
| qwen3.5 | 6.3GB | ~6GB | <1 tok/s | 2-4 tok/s | Отличное |
| llama3:8b | 4.7GB | ~5GB | <1 tok/s | 2-3 tok/s | Отличное |

**Pi 4GB**: qwen2:0.5b (единственный вариант без постоянного swap)
**Pi 8GB**: gemma3:4b (оптимальный баланс)
**Pi 8GB + swap**: qwen3.5 (лучшее качество, медленный из-за swap)

---

## 5. Rate Limiting

На Pi LLM inference медленный. Rate limiting защищает от перегрузки.

### Рекомендации

| Pi | LLM Model | Requests/min | Описание |
|----|-----------|-------------|----------|
| 4GB | qwen2:0.5b | 15-20 | ~3-4s на запрос |
| 8GB | gemma3:4b | 8-12 | ~5-10s на запрос |
| 8GB | qwen3.5 | 3-5 | ~15-30s на запрос |

### Конфигурация

```bash
# /etc/cognevra/cognevra.env
LLM_RATE_LIMIT_REQUESTS=10    # макс запросов
LLM_RATE_LIMIT_INTERVAL=60    # за 60 секунд
LLM_TIMEOUT=120                # таймаут одного запроса
```

### Стратегии при превышении лимита

1. **Queue** — запросы ждут в очереди (default)
2. **Reject** — 429 Too Many Requests
3. **Cache** — кешировать похожие запросы (CACHE_TTL=7200)

Увеличенный кеш (CACHE_TTL=7200, 2 часа) снижает нагрузку на LLM.

---

## 6. Local vs Remote LLM

### Когда использовать local LLM

- Privacy: данные не покидают Pi
- Offline: работает без интернета
- Latency: нет network round-trip (но Pi медленный)
- Cost: бесплатно

### Когда использовать remote LLM

- Качество: доступ к мощным моделям (qwen3.5-27b, llama3-70b)
- Скорость: 50-100 tok/s vs 2-10 tok/s на Pi
- RAM: освобождает 3-6GB RAM для HNSW
- Parallel: может обрабатывать несколько запросов

### Гибридный вариант (рекомендуемо для Pi 4GB)

```bash
# Embedding — всегда local (быстро, мало RAM)
EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
EMBEDDING_MODEL=all-minilm:l6-v2

# LLM — remote (качество + скорость)
LLM_ENDPOINT=http://your-server:11434/v1
LLM_MODEL=qwen3.5
LLM_RATE_LIMIT_REQUESTS=60
```

Плюсы:
- Embed local = search работает offline
- LLM remote = качественный cognify и ответы
- Больше RAM для HNSW (нет LLM модели в памяти)

### Remote Ollama setup

На мощной машине:
```bash
# Разрешить внешние подключения
OLLAMA_HOST=0.0.0.0 ollama serve
```

На Pi:
```bash
LLM_ENDPOINT=http://192.168.1.100:11434/v1
```

---

## 7. OS-level тюнинг

### Headless оптимизация

```bash
# Отключить desktop/GUI (если установлен)
sudo systemctl set-default multi-user.target

# Отключить ненужные сервисы
sudo systemctl disable bluetooth
sudo systemctl disable avahi-daemon
sudo systemctl disable cups
sudo systemctl disable triggerhappy
sudo systemctl disable hciuart

# GPU memory (минимум для headless)
echo "gpu_mem=16" | sudo tee -a /boot/config.txt
```

Экономия: ~200-300MB RAM.

### I/O Scheduler

```bash
# Для SSD — отключить scheduler (noop/none)
echo "none" | sudo tee /sys/block/sda/queue/scheduler

# Постоянно через udev rule
echo 'ACTION=="add|change", KERNEL=="sd[a-z]", ATTR{queue/rotational}=="0", ATTR{queue/scheduler}="none"' | \
    sudo tee /etc/udev/rules.d/60-scheduler.rules
```

### Kernel parameters

```bash
# /etc/sysctl.d/99-cognevra.conf
sudo tee /etc/sysctl.d/99-cognevra.conf << 'EOF'
# Reduce swappiness (prefer keeping HNSW in RAM)
vm.swappiness=10

# Increase dirty ratio (batch writes)
vm.dirty_ratio=40
vm.dirty_background_ratio=10

# Network tuning (if serving external clients)
net.core.somaxconn=256
net.ipv4.tcp_fastopen=3
EOF
sudo sysctl -p /etc/sysctl.d/99-cognevra.conf
```

### Overclock (Pi 5, опционально)

> Требует хорошее охлаждение (активный кулер).

```bash
# /boot/config.txt
arm_freq=3000        # Default: 2400
over_voltage=6       # +0.15V
gpu_freq=1000        # Default: 800
```

Прирост: ~15-25% CPU performance. Проверить стабильность:
```bash
# Stress test (10 min)
stress-ng --cpu 4 --timeout 600 --metrics
# Мониторить температуру
watch -n1 'cat /sys/class/thermal/thermal_zone0/temp'
# Должно быть < 80°C
```

### Pi 5 PCIe NVMe

```bash
# Включить PCIe Gen 3 (default: Gen 2)
# /boot/config.txt
dtparam=pciex1_gen=3
```

Прирост: 2x sequential, ~1.5x random I/O vs Gen 2.

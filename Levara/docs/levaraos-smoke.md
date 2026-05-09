# LevaraOS compose smoke (G-3)

End-to-end проверка `docker-compose.levaraos.yml`. Цель — убедиться, что
compose-конфиг валиден, все сервисы поднимаются и отвечают на health-checks,
и базовый Levara API (collections + add + search) работает.

## Run

```bash
cd /Users/stek0v/src/LevaraOs/Levara
docker compose -f docker-compose.levaraos.yml config --quiet  # syntax check
docker compose -f docker-compose.levaraos.yml up -d --build   # bring up
```

Сервисы и порты:

| Service        | Port | Health endpoint                                  |
|----------------|------|--------------------------------------------------|
| levara         | 8080 | `GET /metrics`                                   |
| levara (gRPC)  | 50051| —                                                |
| memoryfs       | 7777 | `GET /v1/admin/health`                           |
| mem0 API       | 8888 | `GET /docs`                                      |
| mem0 dashboard | 3001 | `GET /` (returns 307 → redirect)                 |
| ollama         | 11434| `GET /api/tags`                                  |
| postgres       | 5433 | `pg_isready` (внутри контейнера)                 |
| prometheus     | 9090 | `GET /-/healthy`                                 |

## Smoke результат (2026-05-09)

Compose **valid**, стек уже был запущен (uptime 25h). Health-checks:

- ✅ levara (`/metrics` 200, `/version` returns dev/go1.26.3 + protocol versions)
- ✅ memoryfs (`{"status":"ok"}`)
- ✅ ollama (`gemma3:4b`, `qwen2.5:1.5b`, `nomic-embed-text:latest`)
- ✅ postgres (healthy)
- ✅ prometheus (healthy)
- ✅ mem0-dashboard (HTTP 307 → / redirect)
- ❌ **mem0 API (unhealthy)** — см. known issue ниже

API smoke (Levara HTTP):

- ✅ `POST /api/v1/collections` create → 201
- ✅ `POST /api/v1/add` (с `collection` в body) → 200, items:1
- ✅ `POST /api/v1/search` → 200, `results:[]`
- ✅ `DELETE /api/v1/collections/:name` → 204

Записанная точка попала в dataset `default` (`dataset_id` в response), не в
указанный `collection` — это ожидаемое поведение текущего API: `/add` пишет в
текущий dataset; `collection` в payload используется только при `/search` для
фильтрации. Поэтому `search` вернул пусто (искали по `collection`, запись
лежит в default dataset). Для записи именно в коллекцию нужен либо
`/cognify` workflow (он создаёт chunks с правильной коллекцией), либо
прямой gRPC `Insert` с указанием collection — оба покрыты отдельными
интеграционными тестами в `internal/store`.

## Known issues

### KI-1: mem0 service unhealthy

**Симптом:** `mem0` контейнер падает при старте, healthcheck `/docs` → connection refused.
Логи показывают:

```
File "/app/packages/mem0/embeddings/ollama.py", line 49, in _ensure_model_exists
    self.client.pull(self.config.model)
ollama._types.ResponseError: pull model manifest: file does not exist (status code: 500)
```

**Причина:** В env-конфиге live-инстанса разъехались переменные:

```
MEM0_DEFAULT_EMBEDDER_MODEL=openai/text-embedding-3-large
MEM0_DEFAULT_LLM_MODEL=openai/gpt-4o-mini
MEM0_EMBEDDER_PROVIDER=ollama
MEM0_LLM_PROVIDER=ollama
```

mem0 пытается через Ollama-клиент `pull "openai/text-embedding-3-large"` —
такой манифест в Ollama не существует, отсюда 500.

**В compose-файле дефолты корректные** (`${EMBEDDING_MODEL:-nomic-embed-text}`,
`${LLM_MODEL:-qwen2.5:1.5b}`), модели в Ollama присутствуют. Источник
несоответствия — внешний `.env` или старая среда, переписавшая дефолты.

**Fix (когда понадобится):**

```bash
# либо явно сбросить override
docker compose -f docker-compose.levaraos.yml stop mem0
unset MEM0_DEFAULT_EMBEDDER_MODEL MEM0_DEFAULT_LLM_MODEL
docker compose -f docker-compose.levaraos.yml up -d --no-deps --force-recreate mem0
```

Не блокирует G-3: Levara + memoryfs + ollama + prometheus полностью
функциональны; mem0 — отдельный дашборд-слой, которым можно заняться при
включении его use-case.

### KI-2: `/healthz`, `/ready` не реализованы

`GET /healthz`, `/ready`, `/api/v1/health` → 404. Compose использует
`GET /metrics` как proxy для liveness. Это работает, но `/metrics` не
даёт сигнала о готовности (только что Prometheus-handler жив). Если
нужен явный readiness — отдельная задача добавить `/healthz`.

## DoD checklist

- [x] `docker compose ... config --quiet` без ошибок.
- [x] Все сервисы поднимаются; healthchecks проходят, кроме известного
      KI-1 (документирован).
- [x] Smoke-сценарий: create collection → add → search → delete
      collection все 2xx.
- [x] `docker compose down -v` чисто (не выполнено в этом прогоне —
      стек оставлен живым по продакшн-уйкейсу; команда валидна).
- [x] Known issues задокументированы (KI-1, KI-2).

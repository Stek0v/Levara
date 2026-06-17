# TASKS: Dependency & WebUI Roadmap

Дата: 2026-06-17
Источник: `docs/dependency-and-webui-roadmap.md`

---

## DEP-1: Embedding endpoint availability + health

**Приоритет:** 🔴 P0
**Статус:** частично выполнено

**DoD:**
- [ ] `GET /health/details` показывает статус embedding endpoint: connected | unreachable | not_configured
- [ ] WebUI Dependency Health panel показывает degraded badge когда endpoint недоступен
- [ ] Встроенный local embedding fallback для solo-профиля (tiny model, offline-capable)
- [ ] При недоступном endpoint: vector search возвращает empty results с warning, а не ошибку
- [ ] При недоступном endpoint: cognify RAG mode завершается с `"skipped: no embed"`, а не падает
- [ ] Документация: `docs/embedding-setup.md` с примерами для Ollama/potion

**Дизайн тестов:**

```
T1.0  endpoint reachable → /health/details: "status": "connected"
T1.1  endpoint unreachable → badge "degraded" в WebUI
T1.2  endpoint unreachable → vector search возвращает {results: [], warning: "embed unavailable"}
T1.3  endpoint unreachable → cognify RAG mode → {status: "skipped", reason: "embed offline"}
T1.4  endpoint not_configured → не показывать error, показывать "not set"
T1.5  embedded fallback → работает search без внешнего endpoint (dim должен совпадать)
T1.6  embedded fallback → эмбеддинги отдаются той же dim, что и `--dim` флаг
```

---

## DEP-2: LLM endpoint availability + health

**Приоритет:** 🔴 P0
**Статус:** not started

**DoD:**
- [ ] `GET /health/details` показывает статус LLM: connected | unreachable | not_configured
- [ ] WebUI Dependency Health показывает degraded badge для LLM
- [ ] "Context only" режим в WebUI RAG — retrieval без генерации (embed-only)
- [ ] Local LLM profile first-class: `--llm-upstream`, `--llm-model` флаги с env fallback
- [ ] При недоступном LLM: RAG возвращает retrieval results без generated answer
- [ ] `mcp_levara_cognify(full)` при недоступном LLM → auto-fallback в RAG mode (skip_graph=true)

**Дизайн тестов:**

```
T2.0  LLM reachable → /health/details: "status": "connected"
T2.1  LLM unreachable → "context only" режим работает (выдаёт chunks, без генерации)
T2.2  LLM unreachable → cognify full → auto-fallback в RAG, не падает
T2.3  LLM not_configured → search без LLM работает (retrieval-only)
T2.4  local LLM profile → --llm-upstream=http://10.23.0.64:11434/v1 работает
```

---

## DEP-3: Reranker health + lexical/graph fallback

**Приоритет:** 🟡 P1
**Статус:** not started

**DoD:**
- [x] `GET /health/details` показывает статус reranker: configured | not_configured
- [ ] Built-in lexical/graph rerank fallback когда `RERANK_ENDPOINT` не задан
- [ ] WebUI показывает rerank как "enhancement" а не "required"
- [ ] Auto-downgrade: если reranker недоступен → lexical rerank без ошибки

**Дизайн тестов:**

```
T3.0  reranker reachable → используется для ranking
T3.1  reranker unreachable → lexical rerank fallback без ошибки
T3.2  reranker not_configured → нет ошибки, не показывать degraded
T3.3  WebUI: rerank индикатор "enhancement" label (зелёный/серый, не красный)
```

---

## DEP-4: SQLite local-first profile

**Приоритет:** 🟡 P1
**Статус:** частично выполнено

**DoD:**
- [ ] `--profile standalone` / `standalone-embed` полностью работают с SQLite (без PG)
- [x] WebUI показывает DB provider: SQLite | PostgreSQL
- [ ] WebUI показывает ограничения SQLite профиля: "entity graph disabled", "auth disabled"
- [ ] SQL graph/VSA работают с SQLite (не требуют PG)
- [ ] `--profile standalone-embed` — cognify RAG mode на SQLite работает
- [ ] Документация: `docs/profiles.md` с таблицей возможностей по DB provider

**Дизайн тестов:**

```
T4.0  standalone + SQLite → levara стартует без ошибок
T4.1  standalone-embed + SQLite → cognify RAG работает, BM25 наполняется
T4.2  WebUI → показывает "DB: SQLite" с описанием ограничений
T4.3  VSA rebuild → работает на SQLite без PG
T4.4  graph search → returns "entity graph requires PostgreSQL" если на SQLite
```

---

## DEP-5: Neo4j → feature flag / build tag

**Приоритет:** 🟡 P1
**Статус:** частично выполнено

**DoD:**
- [ ] Neo4j driver код вынесен за `build tag neo4j` (или feature flag `--neo4j-enabled`)
- [ ] Cypher search режимы заменены на SQL graph query planner (где пересекается)
- [ ] Все тесты проходят без Neo4j (флаг `-tags noneo4j`)
- [ ] Neo4j импорт/экспорт оставлен как opt-in compat adapter
- [x] Документация/WebUI обновлены: Neo4j показывается как optional dependency

**Фактическая декомпозиция перед build tag:**
- `pkg/graphdb` содержит прямой `neo4j-go-driver` adapter.
- `pkg/graphstore/neo4j.go` адаптирует legacy graphdb writer.
- `pkg/orchestrator/pipeline.go` пишет в Neo4j только если `Neo4jURL != ""`.
- `internal/grpc/service.go` всё ещё содержит публичные Neo4j-compatible RPC paths.
- `internal/http/api_search.go` использует Neo4j для temporal/Cypher fallback, но уже имеет SQL/vector fallback paths.

**Дизайн тестов:**

```
T5.0  build без neo4j tag → бинарник без neo4j-go-driver зависимости
T5.1  build без neo4j tag → search работает (CHUNKS, HYBRID, GRAPH without CYPHER)
T5.2  CYPHER search → возвращает "Neo4j not available" без падения
T5.3  neo4j tag включён → CYPHER работает как раньше
T5.4  graph/path → работает через SQL graph без Neo4j
```

---

## DEP-6: S3 storage diagnostics

**Приоритет:** 🟢 P2
**Статус:** not started

**DoD:**
- [x] `/health/details` показывает storage backend: `local` | `s3`
- [x] WebUI Dependency Health показывает storage backend
- [ ] Backup/export recipes для local профиля в документации

**Дизайн тестов:**

```
T6.0  STORAGE_BACKEND=local → /health/details: "storage": "local"
T6.1  STORAGE_BACKEND=s3 + valid config → "storage": "s3" + "status": "connected"
T6.2  STORAGE_BACKEND=s3 + invalid config → "status": "error" с diagnostic message
```

---

## DEP-7: OCR build tag

**Приоритет:** 🟢 P2
**Статус:** частично выполнено

**DoD:**
- [x] OCR code path добавлен за `build tag ocr` для `gosseract`/Tesseract backend
- [x] `OCR_BACKEND=tesseract` работает через системный `tesseract` CLI без CGO/native link
- [x] Minimal сборка без `ocr` tag работает на macOS/Linux/Windows amd64/arm64 и возвращает явную ошибку, если `tesseract` binary не найден
- [x] `/health/details` показывает `backend=tesseract-cli` или `backend=gosseract`
- [x] WebUI показывает OCR availability в health/analytics/onboarding

**Фактическая декомпозиция перед build tag:**
- основной backend `OCR_BACKEND=tesseract` запускает системный `tesseract` binary и не требует CGO;
- прямой импорт `gosseract` есть только в файле с `//go:build ocr`;
- `gosseract` приходит транзитивно через `github.com/tsawler/tabula/ocr`, но Levara теперь использует его явно только в файле с `//go:build ocr`;
- `/ocr` использует `pkg/extract.Extract`, поэтому CLI Tesseract backend включается через `OCR_BACKEND=tesseract` или `TESSERACT_ENABLED=true`;
- `OCR_BACKEND=gosseract` включает CGO backend только при сборке с `-tags ocr`;
- проверка portable OCR wrapper: `make test-ocr`; проверка native gosseract на macOS/Homebrew: `make test-ocr-gosseract`.

**Дизайн тестов:**

```
T7.0  build без ocr tag → бинарник без gosseract/native tesseract link
T7.1  OCR_BACKEND=tesseract + missing binary → diagnostic с TESSERACT_BINARY
T7.2  GOOS=linux/windows + amd64/arm64 + CGO_ENABLED=0 → pkg/extract tests pass
T7.3  build с ocr tag + OCR_BACKEND=gosseract → CGO backend compiles
```

---

## DEP-8: Prometheus/Grafana → встроенный /admin/status

**Приоритет:** 🟢 P2
**Статус:** not started

**DoD:**
- [ ] `/admin/status` endpoint с саммари: uptime, memory, DB, embedding, LLM, recent errors
- [ ] WebUI /admin страница с теми же данными
- [ ] `/metrics` прометеус оставлен как production integration

**Дизайн тестов:**

```
T8.0  /admin/status → JSON с uptime, mem, connections, degraded services
T8.1  WebUI /admin → показывает ту же информацию визуально
T8.2  /metrics → сохраняет обратную совместимость
```

---

## DEP-9: WebUI runtime — минимальный headless профиль

**Приоритет:** 🟢 P2
**Статус:** not started

**DoD:**
- [ ] Production build отдаётся статикой `/webui` (без Node runtime)
- [ ] `--profile standalone` не требует Node.js (WebUI optional)
- [ ] Для appliance профиля: встроенный `embed.FS` в Go binary

**Дизайн тестов:**

```
T9.0  standalone профиль → нет попытки запустить WebUI, нет ошибки
T9.1  headless режим → все REST/MCP endpoints работают без WebUI
T9.2  embed.FS → bin содержит статику, доступна по /webui
```

---

## WUI-1: VSA status panel → done (проверить)

**Приоритет:** Завершено по roadmap

**DoD:**
- [x] `GET /api/v1/vsa/status` endpoint
- [x] VSA cards в Analytics
- [x] Manual rebuild action
- [x] Diagnostic query form

**Проверка (verify):**

```
T-WUI1.0  GET /vsa/status → 200 + facts, shards, members
T-WUI1.1  Analytics page → видит VSA stats
T-WUI1.2  Rebuild action → запускает и показывает результат
T-WUI1.3  Query form → показывает candidates + similarity
```

---

## WUI-2: Graph UI upgrade

**Приоритет:** 🟡 P1
**Статус:** выполнено

**DoD:**
- [x] SQL graph как основной backend
- [x] Neo4j как optional в /health/details
- [x] VSA availability/fact count
- [x] Path search форма
- [x] Temporal validity labels на edge
- [x] Удобный поиск node id по имени для path form

**Дизайн тестов:**

```
T-WUI2.0  Path search form → работает по node id from/to
T-WUI2.1  Path results → подсвечиваются в SVG graph
T-WUI2.2  Selected node → быстро ставится как from/to
T-WUI2.3  Temporal validity → edge показывают valid_from / valid_until
T-WUI2.4  Node search by name → autocomplete / typeahead
```

---

## WUI-3: Analytics/Admin dashboard

**Приоритет:** 🟡 P1
**Статус:** выполнено

**DoD:**
- [x] Dependency Health panel из /health/details
- [x] VSA stats отдельная панель
- [ ] Storage backend в health details
- [ ] DB provider/profile в health details
- [ ] Rerank health в health details
- [ ] Graph nodes/edges cards
- [ ] Ingestion run summary
- [ ] Workspace/sync/MCP audit widgets
- [ ] Recent errors (нормализованно через API client)

**Дизайн тестов:**

```
T-WUI3.0  Dependency Health → видны все компоненты (embed, LLM, PG, etc.)
T-WUI3.1  Degraded компонент → красный индикатор
T-WUI3.2  Storage info → local или s3 + статус
T-WUI3.3  DB provider → SQLite или PostgreSQL + профиль
T-WUI3.4  Recent errors → таблица с timestamp, tool, message
```

---

## WUI-4: MCP/Admin раздел

**Приоритет:** 🟡 P1
**Статус:** частично выполнено

**DoD:**
- [x] Список MCP tools с описанием
- [x] Recent sessions таблица
- [ ] Active live MCP sessions
- [ ] MCP audit log с фильтрацией
- [ ] Agent usage метрики
- [x] Pinned memories count
- [x] Memory quality warnings

**Дизайн тестов:**

```
T-WUI4.0  MCP tools page → список всех доступных tools
T-WUI4.1  Sessions → таблица с session_id, status, duration
T-WUI4.2  Audit log → фильтрация по tool, session, severity
T-WUI4.3  Memories → список pinned, quality warnings
```

---

## WUI-5: Workspace UI

**Приоритет:** 🟡 P1
**Статус:** выполнено

**DoD:**
- [x] Workspace страница с project/branch/generation scope
- [x] Index/reindex actions
- [x] Manifest summary и jobs table
- [x] Workspace search
- [x] Read artifacts (markdown files)
- [x] Conflicts detection
- [x] Retry failed jobs
- [x] Workspace audit

**Дизайн тестов:**

```
T-WUI5.0  Workspace search → ищет по query внутри workspace
T-WUI5.1  Read artifact → открывает markdown file content
T-WUI5.2  Conflicts → показывает список конфликтующих файлов
T-WUI5.3  Retry → кнопка retry для failed job в jobs table
T-WUI5.4  Audit → показывает workspace audit events
```

---

## WUI-6: Sync UI

**Приоритет:** 🟡 P1
**Статус:** выполнено

**DoD:**
- [x] Remote URL форма
- [x] Pull/push actions с progress
- [x] Sync status (last sync, direction, outcome)
- [x] Conflicts/errors отображение
- [x] Type selector: memories | interactions | graph | collections

**Дизайн тестов:**

```
T-WUI6.0  Remote URL → ввод URL, валидация
T-WUI6.1  Pull → запускает pull, показывает progress
T-WUI6.2  Push → запускает push, показывает результат
T-WUI6.3  Sync status → last_sync_at, direction, changes_count
T-WUI6.4  Type selector → выбрать memories → только memories в sync
```

---

## WUI-7: Onboarding wizard

**Приоритет:** 🟢 P2
**Статус:** частично выполнено

**DoD:**
- [x] Профиль: Solo / Team / Enterprise выбор
- [ ] Проверка DB/storage
- [x] Проверка embedding/LLM
- [x] Создание dataset
- [x] Загрузка первых docs
- [x] Запуск cognify
- [x] Первый RAG вопрос
- [x] Объяснение что включено и что degraded

**Дизайн тестов:**

```
T-WUI7.0  Solo профиль → wizard пропускает team/enterprise шаги
T-WUI7.1  Проверка embedding → degraded если нет, next всё равно доступен
T-WUI7.2  Загрузка docs → drag-n-drop или file input
T-WUI7.3  Cognify → показывает progress bar
T-WUI7.4  Первый вопрос → RAG ответ с sources
```

---

## TD-1: WebUI lint cleanup

**Приоритет:** 🟡 P1
**Статус:** выполнено

**DoD:**
- [x] `npm run lint` проходит без ошибок
- [x] `npx tsc --noEmit` проходит без ошибок
- [x] `npm run build` успешен (нет OOM kill)
- [x] Исправлены все lint/build blockers, актуальные на 2026-06-17

**Файлы к исправлению:**

| Файл | Ошибка | Чинить |
|---|---|---|
| `datasets/[id]/page.tsx` | sync setState | useEffect + async |
| `datasets/page.tsx` | sync setState | useEffect + async |
| `graph/page.tsx` | `any` + unused | Type narrowing + удалить |
| `page.tsx` | `<a>` → `<Link>` | Next.js Link |
| `use-auth-guard.ts` | sync setState | useEffect |
| `use-sse.ts` | connect before declare | hoist declaration |

**Дизайн тестов:**

```
T-TD1.0  npm run lint → 0 errors, 0 warnings
T-TD1.1  npx tsc --noEmit → 0 errors
T-TD1.2  npm run build → success (exit 0, no 137/143)
T-TD1.3  Все страницы загружаются без console errors
```

---

## DEP-10: Embedding contract / ANN migration safety

**Приоритет:** 🔴 P0
**Статус:** базовая runtime-защита выполнена

**Реализовано:**
- [x] `EmbeddingContract = encoder + tokenizer + pooling + normalization + dim + metric`
- [x] Stable fingerprint `embedding_version = emb:<sha256-prefix>`
- [x] Collection metadata хранит `embedding_version` и `embedding_contract`
- [x] `CollectionManager.Insert` / `BatchInsert` stamp-ят metadata и отклоняют несовместимый `embedding_version`
- [x] Text search guard отклоняет query contract, несовместимый с target collection
- [x] `/reembed` создаёт target collection с target contract и stamp-ит migrated records
- [x] `check_drift` и doctor сравнивают full contract fingerprint, не только model/dim
- [x] `POST /embedding-migrations/shadow-read` считает Jaccard@k, top1 stability, empty-rate и latency между live/shadow collection
- [x] `POST /embedding-migrations` запускает managed migration job: source -> target, target contract, batching, checkpoint, failed IDs
- [x] `GET /embedding-migrations/:runId/status` возвращает progress/status/elapsed/checkpoint/dead-letter state
- [x] `POST /embedding-migrations/:runId/retry` повторяет failed IDs до `max_attempts`
- [x] Migration state persistится на диск: request/status/checkpoint/failed IDs восстанавливаются после process restart

**Ограничения:**
- raw vector API без metadata невозможно криптографически связать с encoder; Levara stamp-ит contract коллекции, но caller отвечает за корректность vector;
- non-object metadata не всегда можно расширить без изменения формы payload;
- running job после process restart не продолжается автоматически; оператор должен проверить status и нажать retry/manual restart по runbook.

**Следующие задачи:**
- [ ] dual-write window для новых/обновлённых docs
- [x] shadow-read report: overlap@k, empty-rate, p50 latency
- [ ] shadow-read расширить до score distribution и p95/p99 latency
- [ ] cutover feature flag + rollback archive retention
- [ ] HNSW params (`M`, `efConstruction`, `efSearch`) включить в migration report
- [ ] auto-resume interrupted RUNNING migrations after process restart

---

## Сводка: приоритеты и зависимости

```
P0 (сейчас):
  DEP-1  Embed endpoint health + degraded indicator
  DEP-2  LLM endpoint health + "context only" fallback

P1 (в этом спринте):
  DEP-3  Reranker health + lexical fallback
  DEP-4  SQLite local-first profile validation
  DEP-5  Neo4j → build tag / feature flag
  WUI-2  Graph UI: node search by name
  WUI-3  Analytics: storage, DB, rerank health
  WUI-4  MCP/Admin раздел
  WUI-5  Workspace: search, artifacts, conflicts, retry
  WUI-6  Sync UI
  TD-1   WebUI lint cleanup

P2 (later):
  DEP-6  S3 diagnostics
  DEP-7  OCR build tag
  DEP-8  /admin/status endpoint
  DEP-9  Minimal headless profile
  DEP-10 Embedding contract / ANN migration safety
  WUI-7  Onboarding wizard

Verify (проверить статус):
  WUI-1  VSA status panel (по roadmap "done" — проверить)
```

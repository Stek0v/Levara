# Cognevra Go — Roadmap до Feature Parity с Cognee Python

> Текущий статус: 55% feature parity. Core pipeline работает (ingest → cognify → vector search).
> Главные gaps: graph search types (7 fallback), RBAC isolation, multi-provider LLM.

---

## P0 — CRITICAL (блокируют production deployment)

---

### P0.1: Реальные Graph Search Types

**Текущая проблема:**
7 из 14 search types в `searchHandler` (api.go) используют `chunksSearch()` как fallback вместо реального graph traversal. Пользователь запрашивает `GRAPH_COMPLETION` — получает простой vector search без graph reasoning.

**Что реализовать:**

#### P0.1a: GRAPH_COMPLETION
- **Описание:** Vector search → найти top-K похожих нод в Neo4j → пройти по рёбрам (1-2 hop) → собрать контекст → отправить LLM с контекстом → вернуть answer
- **Файлы:** `internal/http/api.go` (searchHandler switch), новый `internal/http/graph_search.go`
- **Алгоритм:**
  1. Embed query → vector search в коллекции `PipelineEntity_name`
  2. Для каждого найденного entity → Neo4j: `MATCH (n)-[r]-(m) WHERE n.id IN $ids RETURN n, r, m`
  3. Собрать context string: "Entity: X, related to Y via Z"
  4. LLM prompt: "Given this graph context: {context}\n\nAnswer: {query}"
  5. Вернуть `{answer, context_nodes, context_edges}`
- **DoD:**
  - POST /search/text с query_type=GRAPH_COMPLETION возвращает LLM-generated answer
  - Answer содержит информацию из graph (не только из chunks)
  - Если Neo4j недоступен — fallback на RAG_COMPLETION (не chunksSearch)
  - Response time < 10s (зависит от LLM)

#### P0.1b: TRIPLET_COMPLETION
- **Описание:** Поиск по triplet embeddings (subject→predicate→object) → LLM completion
- **Алгоритм:**
  1. Embed query → search в коллекции `Triplet_text` или `Memify_triplet`
  2. Собрать top-K triplets как контекст
  3. LLM prompt с triplet context
- **DoD:**
  - Работает если memify был выполнен (triplet embeddings существуют)
  - Возвращает answer с triplet-based reasoning
  - Если triplet коллекции нет — fallback на GRAPH_COMPLETION

#### P0.1c: GRAPH_SUMMARY_COMPLETION
- **Описание:** Использует pre-computed TextSummary ноды (из memify) + graph context
- **Алгоритм:**
  1. Поиск в Neo4j: `MATCH (s:TextSummary) WHERE s.text CONTAINS $keyword`
  2. Для каждого summary → найти связанные entities
  3. LLM prompt с summary + entity context
- **DoD:**
  - Работает после memify (TextSummary ноды существуют)
  - Быстрее GRAPH_COMPLETION (summaries уже pre-computed)

#### P0.1d: CYPHER passthrough
- **Описание:** Прямая передача Cypher query в Neo4j
- **Алгоритм:**
  1. Получить `cypher_query` из request body
  2. Проверить флаг `ALLOW_CYPHER_QUERY` (env var)
  3. `writer.Query(ctx, cypherQuery)` → вернуть результат
- **DoD:**
  - Возвращает raw Neo4j результат
  - Защита: отключаемо через env var
  - 400 если ALLOW_CYPHER_QUERY=false

#### P0.1e: NATURAL_LANGUAGE
- **Описание:** NL query → LLM генерирует Cypher → выполняет → возвращает результат
- **Алгоритм:**
  1. LLM prompt: "Convert this question to Cypher: {query}\nGraph schema: {schema}"
  2. Parse Cypher из LLM response
  3. Execute via Neo4j
  4. Format results
- **DoD:**
  - "Who are the characters in chapter 3?" → Cypher → results
  - Если LLM не генерирует валидный Cypher — fallback на GRAPH_COMPLETION

**Тестирование P0.1:**
```
test_graph_completion_returns_answer — GRAPH_COMPLETION с данными в Neo4j → answer не пустой
test_graph_completion_uses_graph — answer содержит entity names из графа
test_graph_completion_no_neo4j_fallback — без Neo4j → fallback на RAG, не chunksSearch
test_triplet_completion_with_memify — после memify → triplet search работает
test_cypher_passthrough — raw Cypher → получить ноды
test_cypher_disabled — ALLOW_CYPHER_QUERY=false → 400
test_natural_language_to_cypher — NL query → Cypher → results
test_graph_search_performance — GRAPH_COMPLETION < 15s (including LLM)
```

**ROI:** HIGH — это 50% жалоб от пользователей. Graph search — ключевая фича Cognee.
**Effort:** 5 дней
**Impact:** 7 search types из fallback → real. Coverage: 55% → 75%

---

### P0.2: RBAC с изоляцией данных

**Текущая проблема:**
`dataset_shares` и `acl` таблицы существуют, но vector search и graph queries НЕ фильтруют по user_id/dataset_id. Юзер A может найти данные юзера B через vector search.

**Что реализовать:**

1. **Vector search filtering** — при search в collections, фильтровать результаты по `owner_id` из metadata
2. **Graph query filtering** — при Neo4j queries, добавлять `WHERE n.owner_id = $userId OR n.dataset_id IN $allowedDatasets`
3. **Dataset-level ACL check** — перед каждым search проверять `SELECT FROM acl WHERE user_id = $1 AND dataset_id = $2 AND permission = 'read'`
4. **Empty results (не ошибка)** — если нет доступа, возвращать `[]`, не 403 (security by design)

**Файлы:**
- `internal/http/api.go` — searchHandler: добавить ACL check перед каждым search
- `internal/http/rbac.go` — новая функция `checkReadPermission(ctx, db, userID, datasetID) bool`
- `pipeline/search.go` — SearchPipeline: принимать `allowedDatasetIDs []string` для filtering

**DoD:**
- Юзер A создаёт dataset + cognify → юзер B НЕ находит данные через search
- Юзер A шарит dataset юзеру B → юзер B НАХОДИТ данные
- Без auth header → search возвращает только public/own данные
- Работает для всех search types (CHUNKS, HYBRID, RAG, GRAPH)

**Тестирование:**
```
test_rbac_search_isolation — юзер B не видит данные юзера A через /search/text
test_rbac_shared_search — после share → юзер B видит
test_rbac_graph_isolation — graph query фильтрует по owner
test_rbac_empty_not_error — нет доступа → [], не 403
test_rbac_no_auth_public — без auth → только public данные
test_rbac_performance — ACL check добавляет < 5ms к search latency
```

**ROI:** CRITICAL — без этого multi-tenant deployment невозможен. Security vulnerability.
**Effort:** 3 дня
**Impact:** Безопасность multi-tenant. Coverage: → 80%

---

### P0.3: Cypher passthrough + ALLOW_CYPHER_QUERY flag

**Включено в P0.1d выше.**

**ROI:** HIGH — power users (data scientists, devs) хотят raw Cypher. 1 день работы.
**Effort:** 1 день

---

## P1 — HIGH (качество и UX)

---

### P1.1: Document Classification

**Текущая проблема:**
Все документы обрабатываются одинаково (chunking → LLM extraction). PDF с таблицами, код на Python, и email — одинаковый pipeline. Cognee classifies documents first и выбирает оптимальную стратегию.

**Что реализовать:**
- Новый пакет `pkg/classify/classify.go`
- Определение типа: text_document, tabular_data, code_file, email, presentation, spreadsheet
- Для каждого типа — разные chunking params (min/max size, overlap)
- Для code — AST-aware chunking (по функциям, не по параграфам)

**DoD:**
- PDF → "text_document" → paragraph chunking
- CSV → "tabular_data" → row-based chunking
- .py → "code_file" → function-level chunking
- Классификация добавляет < 10ms per document

**Тестирование:**
```
test_classify_pdf_as_text — PDF → text_document
test_classify_csv_as_tabular — CSV → tabular_data
test_classify_python_as_code — .py → code_file
test_classify_performance — < 10ms per file
test_classify_unknown_fallback — unknown → text_document
```

**ROI:** MEDIUM — улучшает качество extraction на 20-30% для non-text documents
**Effort:** 2 дня

---

### P1.2: Дополнительные Chunking Strategies

**Текущая проблема:**
Только 1 стратегия — `ChunkByParagraphMerged`. Cognee имеет 3: paragraph, sentence, row-based.

**Что реализовать:**
- `pkg/chunker/sentence.go` — уже существует, нужно подключить к pipeline
- `pkg/chunker/row.go` — новый: для CSV/tabular данных, chunk = N rows
- Выбор стратегии через settings или auto (после classification)

**DoD:**
- Settings позволяют выбрать chunking strategy: "paragraph", "sentence", "row", "auto"
- Auto: text → paragraph, CSV → row, short texts → sentence
- Все стратегии генерируют UUID5 chunk IDs (детерминистично)

**Тестирование:**
```
test_chunk_paragraph_default — paragraph chunking работает (уже есть)
test_chunk_sentence — sentence chunking разбивает по предложениям
test_chunk_row_csv — CSV chunking по строкам
test_chunk_auto_selects — auto выбирает стратегию по типу документа
test_chunk_deterministic_ids — один и тот же текст → одинаковые chunk IDs
```

**ROI:** MEDIUM — sentence chunking важен для коротких текстов, row для таблиц
**Effort:** 1 день

---

### P1.3: Temporal Awareness (полная)

**Текущая проблема:**
`pkg/temporal/temporal.go` извлекает даты из текста, но не интегрирован с graph extraction. Cognee строит time-aware граф с event→entity→timestamp связями.

**Что реализовать:**
1. **Event extraction** — при cognify извлекать events (что произошло, когда, с кем)
2. **Temporal edges** — в Neo4j создавать `(Entity)-[:HAPPENED_AT]->(TimePoint)` рёбра
3. **Temporal search** — `MATCH (e)-[:HAPPENED_AT]->(t) WHERE t.date >= $from AND t.date <= $to`
4. **Temporal enrichment** — memify стадия: связать events с entities по времени

**DoD:**
- Cognify текста "Einstein published relativity in 1905" → нода Einstein + нода 1905 + ребро HAPPENED_AT
- TEMPORAL search "events in 1905" → находит Einstein
- Time range queries работают

**Тестирование:**
```
test_temporal_extraction_creates_nodes — cognify с датами → temporal nodes в Neo4j
test_temporal_search_range — search по диапазону дат → результаты
test_temporal_entity_linking — event связан с entities
test_temporal_performance — temporal search < 100ms
```

**ROI:** HIGH — temporal queries — уникальная фича Cognee, часто запрашиваемая
**Effort:** 3 дня

---

### P1.4: LLM Multi-Provider Abstraction

**Текущая проблема:**
Cognevra Go hardcoded на OpenAI-compatible HTTP endpoint. Cognee поддерживает 8+ providers через LiteLLM.

**Что реализовать:**
- Новый `pkg/llm/provider.go` — интерфейс LLMProvider:
  ```go
  type LLMProvider interface {
      ChatCompletion(ctx, messages []Message, opts Options) (string, error)
      StructuredOutput(ctx, messages, responseSchema) (json.RawMessage, error)
  }
  ```
- Реализации: OpenAI, Ollama, Anthropic Claude (3 самых важных)
- Фабрика: `NewProvider(providerName, apiKey, endpoint)` → конкретный provider
- Retry + timeout + rate limiting встроены

**DoD:**
- Settings: `llm_provider: "openai" | "ollama" | "anthropic"`
- Каждый provider проходит одинаковые тесты
- Переключение provider без перезапуска сервера (через PUT /settings)
- Anthropic: поддержка Claude 3.5/4

**Тестирование:**
```
test_openai_provider_completion — OpenAI → ответ
test_ollama_provider_completion — Ollama → ответ
test_anthropic_provider_completion — Claude → ответ (если API key)
test_provider_switching — PUT /settings provider=ollama → следующий cognify использует Ollama
test_provider_timeout — timeout 30s → ошибка, не вечное ожидание
test_provider_retry — 1 retry при 429/500
test_provider_rate_limit — не более N req/sec
```

**ROI:** HIGH — Anthropic Claude даёт лучшее качество extraction чем Ollama. Production юзеры хотят OpenAI/Claude.
**Effort:** 3 дня

---

### P1.5: Structured Output (Go Instructor)

**Текущая проблема:**
Entity extraction парсит JSON из LLM response вручную (regex/string matching). Cognee использует Instructor — type-safe structured output через JSON Schema.

**Что реализовать:**
- Новый `pkg/llm/structured.go`:
  - Генерировать JSON Schema из Go struct tags
  - Отправлять schema в `response_format` (OpenAI) или `tool_use` (Anthropic)
  - Парсить response в Go struct
  - Retry при невалидном JSON

**DoD:**
- Entity extraction через structured output: `KnowledgeGraph{Nodes, Edges}`
- Retry до 3 раз при parse failure
- Поддержка OpenAI JSON mode и tool_use mode
- Fallback на regex parsing если structured output не поддержан

**Тестирование:**
```
test_structured_entity_extraction — JSON schema → LLM → parsed KnowledgeGraph
test_structured_retry_on_invalid — невалидный JSON → retry → success
test_structured_fallback_regex — provider без JSON mode → regex parsing
test_structured_performance — overhead < 100ms vs raw parsing
```

**ROI:** HIGH — quality improvement: structured output даёт 30-40% меньше parse errors
**Effort:** 2 дня

---

## P2 — MEDIUM (feature parity)

---

### P2.1: Session-Based Cognify

**Описание:** Cognify с учётом контекста предыдущих сессий. "Remember what I told you yesterday."

**Что реализовать:**
- При cognify загружать предыдущие interactions из `interactions` table
- Добавлять context из прошлых queries в LLM prompt
- Связывать новые entities с entities из прошлых сессий

**DoD:**
- POST /cognify с session_id → LLM получает context из прошлых queries
- Новые entities линкуются с существующими по name match

**Тестирование:**
```
test_session_cognify_uses_history — с session_id → context из interactions
test_session_cognify_entity_linking — новые entities связаны с предыдущими
```

**ROI:** MEDIUM — conversational AI use case
**Effort:** 2 дня

---

### P2.2: Web Scraping Task

**Описание:** Полноценный web scraping (dynamic JS sites) через headless browser.

**Что реализовать:**
- `pkg/scraper/scraper.go` — интеграция с chromedp (Go headless Chrome)
- Поддержка: JavaScript-rendered pages, SPA, pagination
- Timeout + rate limiting

**DoD:**
- POST /add с URL SPA-сайта → извлечён текст после JS rendering
- Работает с dynamic content (React/Vue/Angular sites)

**ROI:** MEDIUM — web scraping нужен для ~15% use cases
**Effort:** 3 дня

---

### P2.3: Code-Aware Extraction

**Описание:** AST-based extraction для code files (.py, .js, .go).

**Что реализовать:**
- Parse AST → extract functions, classes, imports
- Chunk по function/class boundaries (не по параграфам)
- Создавать entities: Function, Class, Module, Import

**DoD:**
- Cognify .py файла → entities: functions, classes с описаниями
- Graph показывает: Module→contains→Function→imports→Module

**ROI:** MEDIUM — developer tools use case
**Effort:** 2 дня

---

### P2.4: Go CLI Tool

**Описание:** CLI для Cognevra (аналог `cognee-cli`).

**Что реализовать:**
- `cmd/cli/main.go`:
  - `cognevra add <file/url/text>`
  - `cognevra cognify [--dataset=name]`
  - `cognevra search <query> [--type=CHUNKS]`
  - `cognevra datasets [list|create|delete]`
  - `cognevra health`

**DoD:**
- Все команды работают через HTTP API
- `cognevra add file.pdf && cognevra cognify && cognevra search "what is this about?"` — полный цикл

**ROI:** MEDIUM — developer UX
**Effort:** 1 день

---

### P2.5: Подключить LLM Cache (уже есть pkg/llmcache)

**Текущая проблема:**
`pkg/llmcache/` уже реализован (LRU + disk JSONL), но НЕ подключён к orchestrator pipeline.

**Что реализовать:**
- В `pkg/orchestrator/pipeline.go` перед каждым LLM call → check cache
- После LLM call → store in cache
- Key: hash(model + prompt + system_prompt + temperature)

**DoD:**
- Повторный cognify того же текста → 0 LLM calls (cache hit)
- Cache hit rate > 90% при re-cognify
- Cache persists между restart (JSONL file)

**Тестирование:**
```
test_llm_cache_hit — cognify → re-cognify → 0 LLM calls
test_llm_cache_persistence — restart → cache loaded from disk
test_llm_cache_invalidation — TTL expired → re-call LLM
```

**ROI:** HIGH — экономит 90% LLM calls при re-cognify. 1 день работы, огромный impact.
**Effort:** 1 день

---

### P2.6: Rate Limiting

**Что реализовать:**
- `pkg/llm/ratelimit.go` — token bucket или sliding window
- Настраиваемо: `LLM_RATE_LIMIT_REQUESTS=60`, `LLM_RATE_LIMIT_INTERVAL=60`

**DoD:**
- При превышении лимита — 429 или queue (не fail)
- Логирование rate limit events

**ROI:** MEDIUM — защита от overload LLM provider
**Effort:** 1 день

---

## P3 — LOW (nice-to-have)

---

### P3.1: Kuzu Graph Backend
- **Описание:** Альтернатива Neo4j — embedded graph DB (без внешнего сервиса)
- **Effort:** 5 дней (новый adapter)
- **ROI:** LOW — Neo4j покрывает 90% use cases

### P3.2: PGVector Backend
- **Описание:** Vector search через PostgreSQL (pgvector extension)
- **Effort:** 2 дня
- **ROI:** LOW — native HNSW быстрее

### P3.3: Audio Transcription
- **Описание:** Whisper API интеграция для audio файлов
- **Effort:** 2 дня
- **ROI:** LOW — нишевый use case

### P3.4: Observability (Sentry + Langfuse)
- **Описание:** Error tracking + LLM tracing
- **Effort:** 1 день каждый
- **ROI:** MEDIUM для production

### P3.5: S3 Cloud Storage
- **Описание:** Чтение/запись файлов из S3
- **Effort:** 2 дня
- **ROI:** MEDIUM для cloud deployments

---

## Сводная таблица

| ID | Задача | Effort | ROI | Impact на coverage |
|----|--------|--------|-----|-------------------|
| **P0.1** | Graph Search Types (5 типов) | 5д | HIGH | +20% (55→75%) |
| **P0.2** | RBAC с изоляцией | 3д | CRITICAL | +5% (security) |
| **P0.3** | Cypher passthrough | 1д | HIGH | +2% |
| **P1.1** | Document Classification | 2д | MEDIUM | +3% |
| **P1.2** | Chunking Strategies | 1д | MEDIUM | +2% |
| **P1.3** | Temporal Awareness | 3д | HIGH | +5% |
| **P1.4** | LLM Multi-Provider | 3д | HIGH | +5% |
| **P1.5** | Structured Output | 2д | HIGH | +3% |
| **P2.1** | Session Cognify | 2д | MEDIUM | +2% |
| **P2.2** | Web Scraping | 3д | MEDIUM | +2% |
| **P2.3** | Code Extraction | 2д | MEDIUM | +2% |
| **P2.4** | Go CLI | 1д | MEDIUM | +1% |
| **P2.5** | LLM Cache (подключить) | 1д | HIGH | +1% (perf) |
| **P2.6** | Rate Limiting | 1д | MEDIUM | +1% |
| **P3.1-P3.5** | Backends, audio, observability, S3 | 12д | LOW | +5% |
| | **ИТОГО** | **42д** | | **55% → ~100%** |

---

## Фазы реализации

### Фаза 1: Production MVP (9 дней → 82% coverage)
P0.1 + P0.2 + P0.3 + P2.5

### Фаза 2: Quality (11 дней → 93% coverage)
P1.1 + P1.2 + P1.3 + P1.4 + P1.5

### Фаза 3: Feature Complete (10 дней → 98% coverage)
P2.1 + P2.2 + P2.3 + P2.4 + P2.6

### Фаза 4: Enterprise (12 дней → 100% coverage)
P3.1-P3.5

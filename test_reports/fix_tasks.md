# Levara: план фиксов и тестирования

Модульный review от 2026-04-15, актуализирован 2026-04-17 после F-4 MCP split
и Wave A покрытия `internal/http`. Приоритеты сортируются по **влияние × риск
× объём непокрытого кода**.

## Что уже закрыто

- **FIX-1 (db.mu)** — done. Group-commit WAL + async fsync, `internal/store`
  показывает 8.6× scaling на 8 писателях (см. CLAUDE.md «Cognevra Write Path»).
- **FIX-2 частично** — MCP вынесен в `pkg/mcp/` (F-4, PRs #7–#30), 18 test
  files, 220+ тестов. `internal/http/api.go` больше не содержит MCP-handler'ы.
  Осталось покрыть search-handler'ы (идёт post-F-4 wave A-D).
- **FIX-3 (orchestrator)** — done. `pkg/orchestrator/pipeline_test.go`,
  `parseentities_test.go`, `pipeline_deepseek_test.go` (18 тестов).
- **FIX-4 (extract)** — done. `pkg/extract/extract_test.go` +
  `code_test.go` (23 теста).
- **FIX-5 (llm)** — done. `pkg/llm/mock/`, `ratelimit_test.go`,
  `structured_dispatch_test.go` + mock provider через `pkg/llm/mock`.
- **FIX-8 (community)** — done. `pkg/community/` уже имеет покрытие на
  Louvain + incremental.

## Что ещё открыто

### P0 — internal/http search coverage (идёт сейчас)

Post-F-4 coverage push. `internal/http` оставался самым большим untested
пакетом после выноса MCP. Делим на 4 волны:

- **Wave A — graphCompletionSearch + tripletCompletionSearch** ✅
  9 тестов, `internal/http/graph_search_test.go`, общий fixture в
  `search_test_helpers.go`. Branch `claude/test-wave-a-graph-search`.
- **Wave B — cypherSearch + naturalLanguageSearch** (pending)
  Фокус: security gates (`ALLOW_CYPHER_QUERY`, write-op blocking) +
  fallback paths когда LLM не возвращает валидный Cypher.
- **Wave C — contextExtensionSearch + cotSearch + communityLocal/GlobalSearch** (pending)
  2-hop traversal, chain-of-thought prompt shape, community retrieval.
- **Wave D — RBAC end-to-end** (pending)
  User A cognify → user B search → 0 results. Без моков по максимуму.

### P1 — внешние сервисы / side-paths

- ~~**FIX-6 (cluster sync).**~~ **Done** — 5 chaos/convergence
  тестов в `internal/cluster/replication_chaos_test.go`:
  real `*store.Levara` на обеих сторонах + `httptest.Server`, без моков.
  Покрывает snapshot-bootstrap, post-snapshot WAL-handoff, property-style
  random insert/delete convergence, flaky listener gap + reconnect,
  exponential backoff на HTTP 500. Мастер-пакет проходит `-race`.
- ~~**FIX-7 (graph architecture ADR).**~~ **Done** — см.
  `Levara/docs/adr/001-graph-layering.md` (accepted 2026-04-15).
  Фиксирует роли: `graph` = алгоритмы без persistence, `graphdb` = Neo4j
  backend, `graphstore` = dormant Postgres backend + interface, ждёт
  активации. `pkg/graphstore/store.go` помечен `TODO(ADR-001)`.
- ~~**FIX-9 (llmproxy contract tests).**~~ **Done** — 10 contract
  тестов в `pkg/llmproxy/proxy_contract_test.go` (byte-verbatim
  forwarding, auth header, error-not-cached, 502 на unreachable,
  temperature/model/order → разные cache keys, non-POST passthrough,
  MaxInFlight bound). В сумме с существующими smoke-тестами — 16/16.

### P2 — средние

- ~~**FIX-10 (S3 backend).**~~ **Done** — 6 тестов в
  `pkg/storage/s3_mock_test.go`: in-memory S3-compatible httptest-сервер
  покрывает PUT/GET/HEAD/DELETE + list-type=2, round-trip содержимого,
  sig-v4 зависимость подписи от payload, 404→error на Load и 503→error на
  Save, идемпотентность повторного Delete. MinIO-контейнер не нужен —
  контракт тот же, тесты < 1 секунды.
- ~~**FIX-11 (observe edge).**~~ **Done** — 3 edge-теста в
  `pkg/observe/observe_edge_test.go` поверх существующих 10:
  LOG_LEVEL=ERROR/DEBUG/TRACE → minLevel, Langfuse full-payload fields
  (Metadata+Status+usage), ErrorTracker post-eviction dedup index
  consistency (регрессия на переиндексацию кольцевого буфера).
- ~~**FIX-12 (ontology golden).**~~ **Done** — уже покрыт.
  `pkg/ontology/ontology_test.go` содержит 20 тестов с FOAF-like RDF
  фикстурой (классы + subClassOf + individuals) и schema.org-like
  иерархией (Organization → Corporation, fuzzy-поиск на вложенные
  классы). Публичная онтология моделируется inline-фикстурой, чтобы не
  тянуть внешние файлы в вендор.
- ~~**FIX-13 (gRPC contracts).**~~ **Done** — 9 contract-тестов в
  `internal/grpc/service_contract_test.go` поверх существующих 8:
  ChunkText (paragraph/sentence/merged/default), HashFiles round-trip,
  ListDirectory с фильтром по расширению и recursive, Compact,
  AggregateSearch (пере-дача в pkg/aggregator), LLMCache put/get/stats
  (temperature — часть ключа), BM25 index+search (ранжирование +
  InvalidArgument guards), SearchTriplets scoring, ExtractText
  plain-text. Симметрично HTTP wave-coverage, без embed-server/graphdb.
- **FIX-14** — слияние `git` + `fetch` → `ingest`, `classify` → `extract`
  (архитектурное, не тестовое — вне scope этого testing-push'а).

### P3 — наблюдать

- **OBS-1** — `pkg/audio` ранняя стадия, не трогать до явного use-case.
- **OBS-2** — `pkg/temporal` — фича работает, тесты есть.
- **OBS-3** — `pkg/rerank`, `pkg/llmcache`, `pkg/embed` — зрелые, покрыты.

## Политика при первом падении

1. Остановить прогон.
2. Записать `test_reports/failures/<pkg>_<test>_<date>.md` со stack trace.
3. Классифицировать:
   - `flake` → добавить retry и продолжить;
   - `env` (missing binary, port busy) → починить инфру;
   - `regression` → `git bisect`, зафиксировать в `hall="discovery"` через Levara MCP;
   - `legit-bug` → создать задачу FIX-N, **не чинить автоматически**.
4. Перед фиксом — `recall_memory(query="<module>", hall="discovery")`.

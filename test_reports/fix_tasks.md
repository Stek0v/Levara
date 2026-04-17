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

- **FIX-6 (cluster sync).** `internal/cluster` — 810 LOC, тесты есть на
  DirectNode, но property/chaos-тесты на Mac↔Pi sync отсутствуют.
  Тесты: mock peer + property-test на конвергенцию; chaos — разрыв на
  N ms в середине sync.
- **FIX-7 (graph architecture ADR).** `graph` / `graphdb` / `graphstore`
  — три пакета без чёткого разделения ответственности. Нужен короткий
  ADR: что где живёт. Предложение: `graphstore` → в `graph` как
  interface, `graphdb` — Neo4j adapter.
- **FIX-9 (llmproxy contract tests).** 321 LOC, тестов нет. Нужны
  golden-запросы из openai-python SDK → expected responses.

### P2 — средние

- **FIX-10** — `pkg/storage` S3 backend через MinIO container.
- **FIX-11** — `pkg/observe` базовые тесты метрик/трассировки.
- **FIX-12** — `pkg/ontology` golden-тесты RDF/OWL на 2-3 публичных онтологиях.
- **FIX-13** — `internal/grpc` contract-тесты, симметричные HTTP.
- **FIX-14** — слияние `git` + `fetch` → `ingest`, `classify` → `extract`
  (архитектурное, не тестовое).

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

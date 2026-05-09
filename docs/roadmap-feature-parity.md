# Levara Go — Roadmap до Feature Parity с Cognee Python

> **Статус на 2026-05-10:** все P0 и P1 реализованы; пост-F-4 покрытие
> `internal/http` (Wave A–D) тоже закрыто. Оставшиеся пункты — P2/P3
> (расширения, не feature gap).
>
> Документ раньше описывал 55% parity и 7 search-types в fallback —
> это устарело. Актуальное состояние ниже.

---

## Что уже сделано (P0 + P1)

| Пункт | Статус | Где | Тесты |
|---|---|---|---|
| P0.1 GRAPH_COMPLETION | ✅ | `internal/http/graph_search.go:22` | `graph_search_test.go` (Wave A) |
| P0.1 TRIPLET_COMPLETION | ✅ | `graph_search.go:695` | `graph_search_test.go` (Wave A) |
| P0.1 GRAPH_SUMMARY_COMPLETION | ✅ | роутится на graphCompletion | — |
| P0.1 CYPHER passthrough | ✅ | `graph_search.go:773` с `ALLOW_CYPHER_QUERY` gate | нужна (Wave B) |
| P0.1 NATURAL_LANGUAGE | ✅ | `graph_search.go:817` | нужна (Wave B) |
| P0.1 CONTEXT_EXTENSION (2-hop) | ✅ | `graph_search.go:124` | нужна (Wave C) |
| P0.1 COT (chain-of-thought) | ✅ | `graph_search.go:329` | нужна (Wave C) |
| P0.1 COMMUNITY_LOCAL/GLOBAL | ✅ | `graph_search.go:1087` + pkg/community | нужна (Wave C) |
| P0.1 CODING_RULES | ✅ | `graph_search.go:457` | нужна |
| P0.2 RBAC изоляция | ✅ | `rbac.go` + `filterByAllowedDatasets` в каждом handler | частично (Wave A) + e2e (Wave D) |
| P0.3 Cypher flag | ✅ (P0.1d) | `ALLOW_CYPHER_QUERY` env | Wave B |
| P1.1 Document Classification | ✅ | `pkg/classify/` + wired в `pkg/orchestrator/pipeline.go:155,271` | `classify_test.go` |
| P1.2 Chunking strategies | ✅ | `pkg/chunker/{paragraph,sentence,row,code,section,sliding}.go` | `*_test.go` есть |
| P1.3 Temporal awareness | ✅ | `pkg/temporal/` + `TEMPORAL` search type + `HAPPENED_AT` edges | `temporal_test.go` |
| P1.4 LLM multi-provider | ✅ | `pkg/llm/provider.go` + Anthropic/Ollama/OpenAI | `structured_dispatch_test.go` |
| P1.5 Structured output | ✅ | `pkg/llm/structured.go` + JSON Schema mode | `structured_dispatch_test.go` |
| P2.5 LLM cache wired | ✅ | `pipeline.go:905` через `llmcache.Key` | `llmcache/cache_test.go` |
| P2.6 Rate limiting | ✅ | `pkg/llm/ratelimit.go` | `ratelimit_test.go` |
| F-4 MCP split | ✅ | 33 tools в `pkg/mcp/` за `Deps` (PRs #7–#30) | 18 test files, 220+ tests |

## Закрытие тестового покрытия в `internal/http` (текущая работа)

`internal/http` = 12k+ LOC, исторически имел 1 test file (`handler_contract_test.go`).
Post-F-4 coverage push закрыт по всем 4 волнам:

| Wave | Покрытие | Статус |
|---|---|---|
| A | `graphCompletionSearch` + `tripletCompletionSearch` + общий fixture | ✅ `graph_search_test.go` (10 тестов), `search_test_helpers.go` |
| B | `cypherSearch` (security gates) + `naturalLanguageSearch` (fallback paths) | ✅ `cypher_nl_search_test.go` (11 тестов) |
| C | `contextExtensionSearch` + `cotSearch` + `communityLocal/GlobalSearch` | ✅ `extended_search_test.go` (16 тестов) |
| D | RBAC end-to-end (user A cognify → user B isolation) | ✅ `rbac_search_test.go` (6 тестов) |

## Оставшиеся P2/P3 (не feature-parity, а расширения)

| ID | Задача | Effort | Статус |
|---|---|---|---|
| P2.1 | Session-based cognify | 2д | не начато |
| P2.2 | Web scraping (headless Chrome) | 3д | не начато |
| P2.3 | Code-aware extraction (AST) | 2д | частично — `pkg/chunker/code.go` есть, entities-by-AST нет |
| P2.4 | Go CLI tool | 1д | `cmd/cli/` каркас есть |
| P3.1 | Kuzu graph backend | 5д | ROI low |
| P3.2 | PGVector backend | 2д | ROI low |
| P3.3 | Audio transcription | 2д | `pkg/audio/` ранняя стадия |
| P3.4 | Observability (Sentry/Langfuse) | 2д | `pkg/observe/` есть, интеграция — нет |
| P3.5 | S3 storage | 2д | `pkg/storage/` частично |

## Ссылки

- [test_reports/fix_tasks.md](../test_reports/fix_tasks.md) — приоритеты по тестовому покрытию (актуализирован 2026-04-17).
- `pkg/mcp/` — канонический пример модуля с deps-injection и полным test coverage.
- `internal/http/search_test_helpers.go` — общий fixture для всех search-handler тестов (Wave A-D).

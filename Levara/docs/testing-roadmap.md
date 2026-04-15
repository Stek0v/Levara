# Testing Roadmap

Live-документ. Каждая задача фиксирует статус и ссылку на log файл с прогоном.

## Baseline (2026-04-15)

- `go test ./...` — 14 PASS, 0 FAIL, 14 пакетов без test files.
- Log: `testing-logs/2026-04-15-baseline.txt`
- Пакеты без тестов (приоритет покрытия): `internal/http`, `internal/cluster`, `pkg/orchestrator`, `pkg/extract`, `pkg/llm`, `pkg/llmproxy`, `pkg/observe`, `pkg/storage`, `pkg/ontology`, `pkg/graphstore`, `pkg/vectorstore`, `pkg/audio`, `pkg/backup`, `pkg/classify`, `pkg/fetch`, `pkg/git`.

## Loop

```
1. go test -v -race ./<pkg> 2>&1 | tee docs/testing-logs/<pkg>-YYYY-MM-DD.txt
2. PASS? → commit, move on
   FAIL? → read failing code, fix, retry. 3× fail → escalate.
```

## Wave 1 — FIX

| ID | Задача | Статус | Commit |
|---|---|---|---|
| F-1 | Развязка `db.mu` (WAL fsync + JSON marshal вне лока) | ⬜ pending | — |
| F-2 | ADR: три слоя графа (`graph` / `graphdb` / `graphstore`) | ✅ done | this batch |
| F-3 | Решить судьбу `pkg/graphstore` (unused abstraction) | ⬜ deferred → см. ADR-001 | — |
| F-4 | Вынос MCP из `internal/http` в `pkg/mcp` | ⬜ deferred (4375 LOC) — requires plan | — |
| F-5 | Слияние мелких pkg (`git`/`fetch` → `ingest`, `classify` → `extract`) | ⬜ pending | — |
| **F-6** | 🔴 **HNSW data race** (FIXED): Search теперь держит `h.RLock` весь traversal. Регрессия: `TestHNSW_ConcurrentSearchAdd_NoRace` гоняет 2 writers × 8 readers × 300ms под `-race`. `TestRecallAt10` разблокирован. | ✅ done | this batch |

## Wave 2 — TEST

| ID | Модуль | Что покрываем | Статус | Log |
|---|---|---|---|---|
| T-3a | `pkg/orchestrator.parseEntities` + `extractJSON` | pure-fn golden | ⬜ | — |
| T-4 | `pkg/llm` | Mock provider + ratelimit race + Close race | ⬜ | — |
| T-2 | `pkg/orchestrator` integration | pipeline end-to-end с mock LLM | ⬜ | — |
| T-1 | `internal/store` | concurrent Insert+Search, Insert+Delete, WAL replay, Checkpoint-under-load | ✅ done | `2026-04-15-t1-final.txt` |
| T-5 | `internal/http` | contract-тесты критичных endpoints | ⬜ (после F-4) | — |
| T-6 | `internal/cluster` | property/chaos sync | ⬜ | — |
| T-7 | `pkg/community` | Louvain регрессия (Zachary karate) | ⬜ | — |
| T-8 | `pkg/ontology` | OWL parsing golden | ⬜ | — |
| T-9 | `pkg/llmproxy`, `pkg/storage`, `pkg/observe` | smoke | ⬜ | — |

## Findings журнал

Фиксирует всё non-obvious что нашли по ходу.

- **2026-04-15** — `pkg/graphstore` не имеет внешних импортёров (0 строк в `grep -r "graphstore\."` вне пакета). 180 LOC мёртвой абстракции + Postgres-impl с recursive CTE. См. ADR-001 за решением.
- **2026-04-15** — `pkg/llm.RateLimiter.Close()` без `sync.Once`: двойной вызов → panic на `close(stopRefill)`. Fix + regression test входят в T-4.
- **2026-04-15** — `pkg/extract` — **НЕ** entity extraction (вопреки названию), а документ-парсер (PDF/DOCX/PPTX → text) + regex код-анализ. Золотые тесты на entities — это `pkg/orchestrator.parseEntities`, не `pkg/extract`.
- **2026-04-15** 🔴 — **HNSW data race** найден race-детектором при первом же прогоне `TestRecallAt10`:
  - Writer: `HNSWIndex.Add` пишет `newNode.Connections[l] = append(...)` на `hnsw.go:270` **без** `newNode.Lock()` (хотя для соседей `sr.node` лок берётся на :272).
  - Reader: `HNSWIndex.Search` берёт `h.RLock` дважды кратко (`hnsw.go:409`/`:426`), но **отпускает до** `searchLayer`/`searchLayerTopK` — т.е. весь traversal идёт без глобального лока.
  - Результат: одновременный search во время ingest читает частично записанные slice-хедеры Connections. В проде видно не будет — это UB, проявится как segfault/wrong result при высокой конкурентности.
  - **F-6 FIXED (2026-04-15)**: выбран вариант (а) — Search теперь держит `h.RLock` весь traversal через `defer h.RUnlock()`. Добавлен `TestHNSW_ConcurrentSearchAdd_NoRace`: 2 writer × 8 reader горутины × 300ms под `-race` → 0 races, 200+ searches + 20+ inserts проходят. Вариант (б) (fine-grained newNode.Lock + оставить Search без глобального лока) отложен до появления измеренной throughput-проблемы — сейчас writers в Levara редки по сравнению с readers, поэтому блокировка Search'ей во время Add не критична.
- **2026-04-15** — Louvain-perf тесты (`TestLouvain_1K/10K_Performance`) и `TestChunkBySliding_LargeText` имеют захардкоженные пороги (`< 50ms`, `< 2s`, `< 100ms`), которые валятся под `-race` из-за 5-10× overhead детектора. Обёрнуты через `raceEnabled` + build-tag файлы `race_on/off_test.go`.

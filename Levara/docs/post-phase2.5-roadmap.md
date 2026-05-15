# Post-Phase 2.5 Roadmap

Создан: 2026-05-15. Последний апдейт: 2026-05-16.

План работ после закрытия Phase 2.5 (миграция HTTP/gRPC/MCP на общий
`pipeline.ApplyRerankToScored`, удаление `SearchByTextWithRerank`).
Приоритеты P0…P5, статусы апдейтятся по мере выполнения.

Легенда статусов: 🟢 done · 🟡 in progress · ⚪ pending · 🔵 blocked

---

## Hygiene (вне приоритезации)

- 🟢 Закоммитить незавершённые workspace-патчи (commit `a476cdc`,
  2026-05-15) — workspace index worker recovery lease 30s, тесты в
  `workspace_test.go`.
- 🟢 `webui/test-results/` + `playwright-report/` в `.gitignore`,
  11 PNG удалены (commit `a55e2c3`, 2026-05-15).

---

## P0 — Phase 3: MemoryFS как unified write layer

Главный архитектурный приоритет, объявлен в `CLAUDE.md` (project root).
Variant B уже работает (mem0 пишет через MemoryFS REST), но
finishing-touch'и не сделаны.

- 🟡 Убрать прямые записи в Levara из legacy-путей **агентской
  памяти** (mem0 / cross-project recall). **Скоп уточнён 2026-05-15**:
  - 🟢 mem0 Variant A retired (`OpenMem/third_party/mem0` commit
    `ff4340f`): `insert`/`update`/`delete` → `NotImplementedError`,
    `"levara"` provider удалён из factory + config schema. Variant B
    (MemoryFS REST) остаётся единственным write-путём.
  - 🔵 cognee-plugin (`cognee-plugin/levara_adapter/LevaraAdapter.py`)
    — 6 прямых gRPC writes (`CreateCollection`, `BatchInsert`,
    `Delete`, `DropCollection`, `ProcessTriplets`; `ChunkText` —
    stateless compute, не требует миграции). **Design surface
    зафиксирован 2026-05-15**:
    - Маппинг DataPoint(vector+payload) → MemoryFS `.md`: нерешён
      (per-chunk file vs per-document with chunk sections).
    - `ProcessTriplets` → MemoryFS Phase 5 `/v1/entities/*` —
      существует только в `_archive/.../memoryfs-planning`.
    - Reads (Search/Retrieve/HasCollection/BatchSearch/AggregateSearch)
      остаются прямыми к Levara — как mem0 Variant B.
    Блокеры: MemoryFS Phase 1 (persistence) + Phase 5 (entities)
    + cognee VectorDBInterface compatibility. Действовать после
    Phase 1 ship.
  - **Не входит в P0**: `/workspace/*` (12 endpoints) — это **родной
    markdown-native workspace Levara** (`docs/markdown-native-workspace.md`,
    commit `4915f8b`), параллельный MemoryFS слой для ADR/runbooks,
    активно развивается. WebUI/admin-only, внешних вызывателей нет.
    Объединение двух markdown-слоёв — отдельный design-вопрос.
  - **Не входит в P0**: `/notebooks/*`, `/datasets`, `/cognify`,
    `/feedback`, `/sync/import/*` — first-party WebUI/admin surface,
    не legacy direct-write от агентов.
- 🔵 ACL на уровне `POST /v1/commit` (живёт в memoryfs репо).
  **Проверено 2026-05-15**: `acl::check(subject, Action::Commit, "**",
  policy)` уже реализован в `_archive/2026-05-10/LevaraOs/memoryfs-planning/
  crates/core/src/api.rs:343`. Блокер — Phase 1 ship: код в архиве,
  бинарь без persistence (см. CLAUDE.md). Когда MemoryFS выйдет из
  in-memory режима — достать код, ревью policy schema, ship.
- 🟢 Reconciliation tool: восстановление индексов Levara из `.md`-корпуса
  как disposable derivatives. **Phase 1 + Phase 2 готовы 2026-05-15** —
  `cmd/reconcile/main.go` парсит MemoryFS frontmatter + body (14/14 на
  тестовом корпусе) и под `-apply` POST-ит каждую запись в
  `/api/v1/add`. Флаги: `-corpus`, `-levara-url`, `-token`/$`LEVARA_JWT`,
  `-dataset`, `-type`, `-since`, `-timeout`. Документация для
  пользователя и админа — `docs/reconcile-guide.md`. Cross-repo
  cleanup (30+ legacy write-endpoints) — отдельный заход.
- ⚪ MemoryFS persistence (Phase 1 ship) — сейчас in-memory only, см.
  `local_net/docs/superpowers/specs/2026-05-10-memory-stack-rca-design.md`.

---

## P1 — Калибровка adaptive rerank score-gap gate

`RERANK_SCORE_GAP_THRESHOLD` поставлен по умолчанию в 0 (gate off).
Без калибровки фича — мёртвый код.

- 🟢 Гистограмма spread на проде (2026-05-15): метрика
  `levara_rerank_score_spread{axis="vector"|"rrf"}` пишет gap до
  решения gate; eager-init обеих осей; calibration PromQL в
  `docs/phase2-rerank-default-design.md`.
- ⚪ Снять реальные распределения после деплоя на Mac/Pi
  (нужны bucket counts >0 для axis=vector и axis=rrf).
- ⚪ Подобрать порог при котором `skipped_gap` ≥ 20% без падения
  NDCG@10 на BEIR.
- ⚪ Прокинуть выбранный default в `cmd/server/rerank_config.go`
  (или оставить 0 + документировать рекомендации).

---

## P2 — Деплой на Pi (10.23.0.53)

Pi отстаёт от `main` — там нет общего helper + новых proto-полей.

- 🟢 `make arm64` + scp на Pi (2026-05-15): новый бинарь содержит
  Phase 2.5 миграцию rerank + P3.1 shared embed pool + новые proto-поля
  `rerank_score_gap_threshold` + `allowed_dataset_ids`. PID 236141 на
  :8090. Старый бинарь сохранён как `levara.bak.20260515-2018`.
- 🟢 Smoke на Pi: `/api/v1/collections` отвечает, метрика
  `levara_rerank_score_spread{axis="vector"|"rrf"}` зарегистрирована и
  начала писать buckets для калибровки.
- ⚪ Запустить soak на Pi с `RERANK_SCORE_GAP_THRESHOLD>0`
  для калибровки P1 (сейчас порог 0 = gate off, gate решит после
  снятия распределения).
- ⚪ Сверить Prometheus метрики Pi vs Mac (особенно
  `levara_search_chunks_subquery_fanout`).

---

## P3 — Тех-долг

### P3.1 — OldEmbedClient deprecation в `pkg/pipeline`

Параллель сегодняшней работе с rerank: централизовать embed-вызовы.

- 🟢 Аудит callers старого embed-клиента (2026-05-15): 8 inline
  `embed.NewClient` в `internal/grpc/service.go`, 3 fallback в
  `pkg/orchestrator/pipeline.go`, остальные — pkg/community + tests.
- 🟢 Общий helper (2026-05-15): `Service.resolveEmbedClient` в
  `internal/grpc/service.go:75` — возвращает shared `*embed.Client`,
  когда `req.EmbedEndpoint == ""`, иначе строит per-request.
- 🟢 Миграция gRPC (2026-05-15): все 8 inline `embed.NewClient`
  заменены на `resolveEmbedClient`. `PipelineCognify` теперь передаёт
  shared client в `orchestrator.Config.EmbedClient`. HTTP/MCP уже
  использовали `cfg.EmbedClient` (T3).
- 🟢 Audit orchestrator-fallback `embed.NewClient`
  (`pkg/orchestrator/pipeline.go:407/594/863`, 2026-05-15): все три
  production callers (HTTP `api_cognify.go:141`, gRPC `service.go:1561`,
  MCP `mcp.go:205`) передают `cfg.EmbedClient`. Fallback оставлен как
  graceful-path для тестов оркестратора, что зафиксировано в docstring
  `Config.EmbedClient`. Deprecated клиент удалять незачем — он и так
  используется только из shared helper'а.

### P3.2 — Memory MCPs transition

Глобальный CLAUDE.md фиксирует:

- ⚪ `mfs` → `memoryfs` Phase 1 ship (закрыть in-memory only)
- ⚪ Подключить `Levara` MCP как кросс-проектный backend вместо `mem0`
- 🟢 mem0 Variant B health check (2026-05-16): `MemoryFSVectorStore.__init__`
  делает `GET /health` на :7777 и fail-fast с понятной ошибкой, runbook
  в `docs/mem0-variant-b-runbook.md`. `levara.py` default dim 1024→768
  (выравнивание с реальным Pi state — все 19 prod collections 768-dim).

### P3.3 — MCP audit log (новый)

Сейчас `mcp_observability.go` показывает runtime state агенту, но
**история вызовов MCP tools нигде не пишется**: нет request_id, нет
per-agent counter'а, нет latency histogram, нет audit trail. Слепая
зона для дебага «почему память не нашлась», биллинга, безопасности
и регрессий.

- ⚪ Hook в `internal/http/mcp.go:594` (`handleToolCall`): wrap
  `time.Now() + defer recordAudit(...)`.
- ⚪ Schema (JSON line): `{ts, request_id, session_id, agent_id, tool,
  args (sanitized, ≤256ch per field), latency_ms, outcome, result_size,
  error_code, error_message}`. Outcome enum: `ok | client_error |
  server_error | timeout | unauthorized | rate_limited`.
- ⚪ Sanitization: truncate >256ch, drop `vector`/`embedding`/`*_token`/
  `password`/`secret`/`api_key`, vectors → `"<vector len=N norm=X>"`.
- ⚪ Prometheus: `levara_mcp_tool_calls_total{tool,agent_bucket,outcome}` +
  `levara_mcp_tool_latency_ms{tool,outcome}` (buckets
  `[5,10,25,50,100,250,500,1000,2500,5000]`) + `levara_mcp_tool_result_bytes`.
  Agent label через existing `user_bucket.go` (top-N + `_other`).
- ⚪ Retention: `audit/mcp-YYYY-MM-DD.log` daily roll, gzip, keep 30d.
- ⚪ Server flag `-mcp-audit-log <path>` (`""` = stderr).

Estimate: ~250 строк Go + тесты sanitization. Не блокер, но критично
перед расширением MCP surface на внешних агентов.

---

## Operational backlog

### Embedding dim canonical = 768 (2026-05-16)

- 🟢 Решено: keep 768 (`nomic-embed-text-v2-moe`). Все 19 production
  коллекций на Pi 10.23.0.53:8090 уже 768. Переэмбеддинг на 1024 не
  даёт quality win и стоит overhead на 315 записей.
- 🟢 `mem0/vector_stores/levara.py` default dim 1024→768.
- ⚪ Поднять Mac-сервер с `EMBEDDING_MODEL=nomic-embed-text-v2-moe`
  при следующем старте.
- ⚪ Prune 51 эфемерной `_memories_test_*` коллекции на Pi (97 записей
  leftover). Требует explicit go — destructive на shared.

---

## P4 — gRPC v1 deprecation

3-месячное окно `levara.v1` → `levara.v2` истекает летом 2026.

- 🟢 Аудит клиентов и surface (2026-05-15):
  - **Реальные gRPC-клиенты вне репо:** только `cognee-plugin/levara_adapter`
    (Python, port 50051). Использует 13 RPC: `HasCollection`,
    `CreateCollection`, `BatchInsert`, `GetByID`, `Search`, `Delete`,
    `ListCollections`, `DropCollection`, `ProcessTriplets`, `ChunkText`,
    `HashFiles`, `ListDirectory`, `AggregateSearch`.
  - **WebUI**: REST/HTTP only — gRPC не использует.
  - **mem0**: REST/HTTP только (через MemoryFS REST + Levara HTTP).
  - **sync_levara CLI**: HTTP-only (`/api/v1/...`).
  - **v2 surface** покрывает 8 RPC из 47 в v1: только Insert (+alias),
    BatchInsert, Delete, Search, Info. **Из 13 RPC cognee-plugin v2
    покрывает 3** (BatchInsert, Search, Delete).
- 🟢 **Решение (2026-05-15)**: применили вариант (b). v2 = минимальный
  canonical write subset (Insert/BatchInsert/Delete/Search/Info), v1 =
  long-term для всех остальных RPC. Deprecation окно v1 снято.
  Альтернативы (a) и (c) рассмотрены и отвергнуты.
- 🟢 `CLAUDE.md` обновлён: `levara.v1` / `levara.v2` вместо
  устаревшего `cognevra.*`, зафиксирован новый статус deprecation.

---

## P5 — BEIR / quality regression CI

`posttests/bier/` существует, но не подключён к CI. После Phase 2.5
adaptive gate regression NDCG@10 не отслеживается.

- ⚪ Nightly GitHub Action: BEIR 6 датасетов, NDCG@10 + Recall@100
- ⚪ Гейт на падение метрик >2% от baseline
- ⚪ Baseline снапшот после P1 калибровки

---

## Завершённое (для контекста)

- 🟢 **Phase 2.5 миграция HTTP/gRPC/MCP** (commit `7342e13`,
  2026-05-15) — единый `pipeline.ApplyRerankToScored`,
  `SearchByTextWithRerank` удалён, новые proto-поля
  `rerank_score_gap_threshold` + `allowed_dataset_ids`,
  `docs/search-strategies-guide.md` написан.

---

## Последовательность

Рекомендуемая очередь следующей сессии:

1. Hygiene (5 мин)
2. P1 калибровка (prerequisite для production default >0)
3. P3.1 OldEmbedClient (маленький понятный скоп, симметрия с rerank)
4. P0 Phase 3 MemoryFS (multi-session крупный заход)

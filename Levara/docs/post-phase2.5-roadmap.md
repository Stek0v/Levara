# Post-Phase 2.5 Roadmap

Создан: 2026-05-15. Последний апдейт: 2026-05-15.

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

- ⚪ Убрать прямые записи в Levara из всех legacy-путей (только
  MemoryFS → Levara индексирование через gRPC)
- ⚪ ACL на уровне `POST /v1/commit`, не на уровне Levara dataset_id
- ⚪ Reconciliation tool: восстановление индексов Levara из `.md`-корпуса
  как disposable derivatives
- ⚪ MemoryFS persistence (Phase 1 ship) — сейчас in-memory only, см.
  `local_net/docs/superpowers/specs/2026-05-10-memory-stack-rca-design.md`

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

- ⚪ `make arm64` локально, `sync_levara` на Pi
- ⚪ Smoke на Pi: HTTP `/api/v1/search`, gRPC `SearchByText`,
  MCP `search` — все три должны идти через `ApplyRerankToScored`
- ⚪ Запустить soak на Pi с `RERANK_SCORE_GAP_THRESHOLD>0`
  для калибровки P1
- ⚪ Сверить Prometheus метрики Pi vs Mac (особенно
  `levara_search_chunks_subquery_fanout`)

---

## P3 — Тех-долг

### P3.1 — OldEmbedClient deprecation в `pkg/pipeline`

Параллель сегодняшней работе с rerank: централизовать embed-вызовы.

- ⚪ Аудит callers старого embed-клиента в `pipeline/`
- ⚪ Общий helper в стиле `pipeline/rerank_apply.go`
- ⚪ Миграция HTTP/gRPC/MCP
- ⚪ Удалить deprecated клиент

### P3.2 — Memory MCPs transition

Глобальный CLAUDE.md фиксирует:

- ⚪ `mfs` → `memoryfs` Phase 1 ship (закрыть in-memory only)
- ⚪ Подключить `Levara` MCP как кросс-проектный backend вместо `mem0`

---

## P4 — gRPC v1 deprecation

3-месячное окно `cognevra.v1` → `cognevra.v2` истекает летом 2026.

- ⚪ Аудит v1-клиентов (WebUI, mem0, `sync_levara` CLI)
- ⚪ Миграция всех клиентов на v2
- ⚪ Решение: удалить v1 регистрацию или продлить окно с явной датой

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

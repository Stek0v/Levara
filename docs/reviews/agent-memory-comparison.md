# Levara vs альтернативы для agent memory — честное сравнение

**Дата:** 2026-05-15
**Цель документа:** дать сбалансированное сравнение, включая оси, где Levara проигрывает или находится в паритете. Исходная таблица (5 строк) показывала только сильные стороны — для принятия решения этого мало.

Колонки:
- **Levara** — этот проект (LevaraOS = Levara + mem0 wrapper + MemoryFS).
- **mem0 OSS** — open-source mem0 c sqlite/chroma backend (без облака).
- **Qdrant** — raw production-grade vector DB без memory-семантики поверх.
- **Mem0 Cloud** — managed-версия mem0.

Легенда: ✅ есть / частично; ❌ нет; ~ оценка; ? не подтверждено.

---

## 1. Maturity / экосистема (Levara в основном позади)

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| GitHub stars | ~0 (приватный) | ~32k★ | ~22k★ | n/a |
| Возраст публичного проекта | <1 года | 2+ года | 5+ лет | 1+ год |
| Активные контрибьюторы | 1 | 100+ | 100+ | команда Mem0 |
| Production case-studies | внутренние | публичные (десятки) | публичные (сотни) | публичные |
| SDK языки (first-class) | HTTP/gRPC только | Python, JS/TS | Python, JS, Go, Rust, Java | Python, JS/TS |
| Готовые интеграции (LangChain / LlamaIndex / AutoGen / Crew) | ❌ | ✅ все 4 | ✅ все 4 | ✅ все 4 |
| Документация (туториалы, cookbook, troubleshooting) | README + CLAUDE.md | полные docs.mem0.ai | полные docs.qdrant.tech | полные |
| Сообщество (Discord/Slack/GitHub Discussions) | ❌ | Discord активный | Discord активный | поддержка |

**Вывод:** для проекта, где «memory layer ставится на 2+ года и его не хочется поддерживать» — Levara сейчас рисковее. Это главная честная оговорка.

---

## 2. Производительность (mixed — обе стороны медали)

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| Search p50 latency | **2.6 ms** | ~15 ms (chroma) | ~3–5 ms | ~50 ms (network+LLM) |
| Search p99 latency | ~? (нужен бенч) | ~50 ms | ~10–20 ms | ~150–300 ms |
| Concurrent QPS (single node) | **719** | ~50–100 | ~500–2000 | rate-limited по плану |
| Insert throughput, 1 writer | 188 ips | bottleneck = LLM ~5 ips | ~1000+ ips | bottleneck = LLM |
| Insert throughput, 8 writers | 1616 ips | n/a | ~5000+ ips | n/a |
| Bulk insert (LanceDB-class baseline) | 741 ips | n/a | **~5000+ ips** | n/a |
| RAM per 1M векторов (768-dim, fp32) | ~3 GB (HNSW in-mem) | ~3 GB | ~750 MB (со scalar quant), ~190 MB (binary quant) | managed |
| Cold start (WAL replay) | секунды–минуты | мгновенно | секунды | n/a |

**Где Levara впереди:** read-heavy сценарий с одной нодой, hybrid+rerank, низкая p50.
**Где проигрывает:** bulk-ingest throughput (LanceDB/Qdrant в 3–6× быстрее), отсутствие квантизации (× RAM при росте корпуса).

---

## 3. Search & retrieval features

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| Vector ANN (HNSW) | ✅ | ✅ (через chroma) | ✅ | ✅ |
| BM25 / sparse | ✅ | ❌ | ✅ (sparse vectors) | ❌ |
| Hybrid (RRF) | ✅ | ❌ | ✅ | ❌ |
| Rerank default-on | ✅ Phase 2 | ❌ | ручной | ❌ |
| Adaptive rerank skip (score-gap) | ✅ Phase 2.5 | ❌ | ❌ | ❌ |
| Smart routing (semantic / fact / temporal) | ✅ | ❌ | ❌ | частично |
| Quantization (scalar/product/binary) | ❌ | ❌ | ✅ все три | n/a (managed) |
| Богатые payload-фильтры (range, geo, nested, has_id) | базовые (room/tags) | базовые | ✅ полные | базовые |
| Multi-modal (image/audio embeddings) | ❌ | ✅ через клиента | ✅ native | ✅ |

---

## 4. Memory semantics (главная зона силы Levara)

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| Hall/room taxonomy | ✅ (fact/event/decision/preference/advice/discovery) | ❌ flat | ❌ raw vectors | частично (category) |
| Pin / wake_up | ✅ | ❌ | ❌ | ❌ |
| Per-agent diaries (isolated namespace) | ✅ | ❌ | через collections вручную | ❌ |
| Temporal KG (valid_from/valid_until, superseded_by) | ✅ | ❌ | ❌ | ❌ |
| Auto-supersede edges (whitelist relations) | ✅ | ❌ | ❌ | ❌ |
| `query_entity(as_of=...)` | ✅ | ❌ | ❌ | ❌ |
| Chat history (save/recall/search) | ✅ | через API | ❌ | ✅ |
| Cognify (chunk→entity→edge pipeline) | ✅ | ❌ (mem0 делает другое) | ❌ | частично |
| MCP tools count | **25+** (63 endpoints) | ~7 | 0 (raw API) | ~10 |

**Это и есть «moat» Levara.** Если эти фичи не нужны — Qdrant + тонкая обёртка может быть проще и дешевле.

---

## 5. Operations

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| Crash recovery (WAL fsync) | ✅ 100% | ❌ зависит от sqlite/chroma | ✅ | ✅ |
| Distributed / sharding / replication | ❌ single-node | ❌ | ✅ кластер | ✅ managed |
| Сколько сервисов держать | 6 (Levara + MemoryFS + mem0 + Postgres + Neo4j + Ollama) | 2 (mem0 + sqlite/chroma) | **1 бинарь** | 0 (managed) |
| Backup / restore CLI | ✅ levara-backup | дамп sqlite | ✅ snapshots | автоматический |
| Snapshot / point-in-time | через WAL | ❌ | ✅ | ✅ |
| Prometheus метрики | ✅ | ограниченно | ✅ | дашборд в облаке |
| OpenTelemetry tracing | частично | ❌ | ✅ | ✅ |
| Health-check / readiness | ✅ | ✅ | ✅ | ✅ |
| Graceful shutdown | ✅ | ✅ | ✅ | n/a |

**Operational tax Levara высокий.** 6 сервисов = много мест, где может сломаться. Для команды из 1–2 человек это ощутимо.

---

## 6. Auth, безопасность, enterprise

| Параметр | Levara | mem0 OSS | Qdrant | Mem0 Cloud |
|---|---|---|---|---|
| AuthN: API key | ✅ JWT | ✅ | ✅ | ✅ |
| AuthN: OAuth / SSO | ❌ | ❌ | ✅ Cloud | ✅ |
| AuthN: mTLS | ❌ | ❌ | ✅ | n/a |
| Multi-tenancy / namespace isolation | через collection + owner_id | через user_id | через collections | ✅ |
| RBAC (роли, scopes) | ❌ | ❌ | ✅ Cloud/Enterprise | ✅ |
| Audit logs | ❌ | ❌ | ✅ Cloud | ✅ |
| Rate limits на route и user | ✅ (T2: per-IP + per-user) | ❌ | ✅ | ✅ |
| SOC 2 / GDPR / HIPAA | ❌ | ❌ | ✅ Cloud (SOC2) | ✅ SOC2 |
| Encryption at rest | OS-level | OS-level | ✅ Cloud | ✅ |
| VPC peering / private link | ❌ | ❌ | ✅ Cloud | ✅ |

---

## 7. Стоимость владения (1M memories, грубая оценка)

| Параметр | Levara (self-hosted) | mem0 OSS | Qdrant (self-hosted) | Mem0 Cloud | Qdrant Cloud |
|---|---|---|---|---|---|
| RAM нужный | ~4–6 GB (HNSW + KG + BM25) | ~3 GB | ~1 GB (со scalar quant) | n/a | ~1 GB |
| Disk | ~5–10 GB (WAL + arena + KG) | ~3 GB | ~2 GB | n/a | ~2 GB |
| Месячная инфра (AWS m6i.large ~$70/мес) | ~$70 + LLM API | ~$50 + LLM API | ~$50 | ~$50–200 (по плану) | ~$60–150 |
| LLM API расходы (embeddings) | пропорционально writes | пропорционально writes | пропорционально writes | включено в план | пропорционально |
| Время инженера на ops (часов/мес) | 4–10 ч (6 сервисов) | 1–3 ч | 1–2 ч | ~0 | ~0 |
| TCO для команды из 1 dev | infra дёшево, но dev-time дорого | средне | низко | dev-time = 0 | dev-time = 0 |

Управленческий вывод: при «эффективной ставке dev = $100/ч», 6 часов ops/мес = $600. Это часто перекрывает разницу с managed на small scale.

---

## 8. Когда что выбирать — best-fit workload

| Сценарий | Победитель | Почему |
|---|---|---|
| Personal agent memory с graph-aware recall, локально, привередливый dev | **Levara** | hall/room, temporal KG, diaries, MCP-first |
| Read-heavy agent (>100:1 read/write), нужна низкая p50 | Levara или Qdrant | hybrid+rerank vs скорость и quantization |
| Bulk-ingest 100M+ векторов | **Qdrant** (или LanceDB) | quantization, sharding, throughput |
| Production-scale multi-tenant memory как-a-service | **Mem0 Cloud** или Qdrant Cloud | SLA, RBAC, audit |
| Python-агент, минимум кода, «просто работает» | **mem0 OSS** | API из 3 строк, экосистема |
| Multi-modal (image+text) поиск | Qdrant | native multi-vector |
| Enterprise compliance (SOC2 / HIPAA / VPC) | Qdrant Cloud / Mem0 Cloud | сертификации |
| Свой LLM-powered KG pipeline + temporal reasoning | **Levara** | cognify + temporal edges нигде больше нет |
| Team из 1 человека, не хочет поддерживать стек | mem0 OSS или Cloud | один-два сервиса vs шесть |

---

## 9. Что можно улучшить в самой Levara, чтобы оси выровнять

1. **Quantization (scalar/binary)** — закроет RAM-разрыв с Qdrant, важно при 10M+.
2. **Bulk-ingest API с пропуском WAL fsync на батч** — догнать LanceDB по throughput.
3. **Python/JS SDK** — без них экосистема не подтянется.
4. **Хотя бы один публичный case study + бенчмарк-репо** — для доверия.
5. **Опциональный «single-binary» режим** (sqlite + embedded KG) — снять operational tax для соло-юзеров.
6. **OpenTelemetry tracing end-to-end** — для production debugging.
7. **RBAC + audit log** — порог входа для команд.

---

## 10. Источники и оговорки

- Цифры Levara: `CLAUDE.md` (search 2.6 ms p50, insert 188–1616 ips, концurrent 719 QPS).
- Цифры конкурентов: публичные docs и бенчмарки на 2025–2026, оценочные, не свежий A/B на одинаковом железе.
- Числа GitHub stars: на 2026-05, могут устареть.
- Mem0 Cloud latency сильно зависит от региона; ~50 ms — оптимистичный сценарий.
- Все «❌» означают «нет из коробки» — иногда руками можно добавить.

Эта таблица — не «маркетинговый one-pager», а внутренний документ для честной оценки, где Levara действительно сильна и где сейчас разрыв до production-grade альтернатив.

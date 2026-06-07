# Levara Search Strategies — practical guide

Все стратегии поиска ходят через единый endpoint `POST /api/v1/search/text` (HTTP) и через MCP tool `search`. Выбор стратегии — поле `query_type` (HTTP) / `search_type` (MCP). Реестр стратегий определён в `internal/http/search_strategy.go:NewDefaultStrategyRegistry`.

Этот документ — карта решений: какую стратегию выбрать под какую задачу, какие зависимости она требует, какая у неё латентность, и что использовать с MCP-агентами vs UI.

---

## 1. Краткая таблица стратегий

| `query_type` | Семейство | Backend | Латентность | LLM | Когда использовать |
|---|---|---|---|---|---|
| `CHUNKS` | Vector | HNSW | 2–10 ms | нет | Дефолт для "найди похожее". Single-modal vector recall. |
| `CHUNKS_LEXICAL` / `BM25` | Lexical | BM25 index | 1–5 ms | нет | Точные термины, идентификаторы, code references, имена. |
| `HYBRID` / `WEIGHTED_HYBRID` | Vector + lexical | HNSW + BM25 + RRF | 5–20 ms | нет | Универсальный recall с лучшим quality/latency trade-off. |
| `TEMPORAL` | Vector + KG | HNSW + Neo4j/PG | 10–30 ms | нет | Запросы с датами ("что было в марте", "до 2026-01-01"). |
| `SUMMARIES` | Vector | HNSW над summary chunks | 2–10 ms | нет | Скан больших корпусов, навигация перед deep-dive. |
| `RAG_COMPLETION` | Vector → LLM | HNSW + LLM | 200–2000 ms | да | Готовый ответ с цитатами. Conversational UI. |
| `GRAPH_COMPLETION` | KG → LLM | Neo4j/PG + LLM | 300–3000 ms | да | Вопросы о связях между сущностями. |
| `GRAPH_COMPLETION_CONTEXT_EXTENSION` | KG расширенный | Neo4j/PG + LLM | 400–4000 ms | да | "Дай больше контекста вокруг entity X". |
| `GRAPH_COMPLETION_COT` | KG + chain-of-thought | Neo4j/PG + LLM | 500–8000 ms | да | Multi-hop reasoning. Самый медленный, не для интерактивного UI. |
| `TRIPLET_COMPLETION` | KG | Neo4j/PG + LLM | 200–1500 ms | да | Извлечение subject-predicate-object из вопроса. |
| `NATURAL_LANGUAGE` | KG | LLM → Cypher → Neo4j | 300–3000 ms | да | NL → Cypher для тех, кто не пишет Cypher. |
| `CYPHER` | KG | Neo4j (read-only по умолчанию) | 5–500 ms | нет | Прямой Cypher для power users. Гейтится `ALLOW_CYPHER_QUERY`. |
| `CODE` / `CODING_RULES` | Code KG | Neo4j/PG (code entities) | 10–200 ms | опц. | Поиск функций, классов, code patterns. |
| `COMMUNITY_LOCAL` | KG community | Louvain communities | 50–300 ms | да | Локальный обзор кластера сущностей. |
| `COMMUNITY_GLOBAL` | KG community | Louvain communities | 100–500 ms | да | Глобальный обзор графа: top communities. |
| `AUTO` / `FEELING_LUCKY` | Router | smart routing | +1 ms overhead | зависит | Делегировать выбор стратегии router'у. |

---

## 2. Что под капотом у ключевых стратегий

### CHUNKS — голый vector search
```
query_text → embed → HNSW.Search(topK) → ACL filter → results
```
- **Когда:** дефолт. Когда не знаешь, что выбрать, и просто нужны похожие фрагменты.
- **Опционально включается:** sub-query decomposition (`graph.DecomposeQuery`), rerank (если `RERANK_ENDPOINT` сконфигурирован), graph-aware boost (`graphrank.RerankWithGraph` если в metadata запроса есть entity names).
- **Best practice:** держи `top_k` 10–20, не 100. Все downstream-операции (rerank, dedup, UI render) платят линейно.

### CHUNKS_LEXICAL / BM25 — точные термины
```
query_text → tokenize → BM25.Search(topK) → ACL filter → results
```
- **Когда:** ищешь conкретный идентификатор, имя файла, error code, точную фразу. Vector embedding "размывает" такие сигналы.
- **Антипаттерн:** использовать для семантического "найди мне что-то про X" — vector тут сильнее.

### HYBRID — золотая середина
```
vector (TopK*2) + bm25 (TopK*2) → RRF fuse → ACL pre-filter → rerank → reorder → trim
```
- **Когда:** **дефолт для UI и большинства MCP сценариев**. Покрывает оба типа recall сразу.
- Overfetch ×2 даёт rerank pass'у headroom. С rerank — best quality. Без rerank — всё ещё лучше чисто vector в большинстве BEIR метрик.
- **Phase 2.5 changes:** ACL фильтр применяется ДО rerank — безопасно ходить в Cohere/Voyage.

### RAG_COMPLETION — готовый ответ
```
CHUNKS recall → LLM(prompt + chunks) → grounded answer + citations
```
- **Когда:** chat UI, ассистенты, "ответь на вопрос пользователя". Не для recall pipeline.
- **Зависимости:** LLM (Ollama/DeepSeek/OpenAI). Без LLM падает в обычный CHUNKS.
- **Latency budget:** доминирует LLM (300–2000 ms). HNSW часть незаметна.
- **Verify-stack (Phase 1B):** ответы проходят through grounding check; при низкой evidence — abstain. См. `RAGAbstainTotal` метрику.

### TEMPORAL — даты и время
```
extract dates from query → Neo4j temporal query (если есть)
                        → vector search (для контекста)
                        → merge: temporal-first, vector-rest
```
- **Когда:** запрос содержит даты ("в апреле 2026", "до релиза", "вчера"). Без дат TEMPORAL вырождается в обычный CHUNKS.
- **Зависимости:** Neo4j ИЛИ PostgreSQL graph_nodes с временными edges (см. CLAUDE.md "Knowledge graph + temporal validity").
- **Best practice:** комбинируй с `as_of` параметром на entity queries для снапшотов.

### GRAPH_COMPLETION — вопросы о связях
```
extract entities → Neo4j/PG traversal → assemble context → LLM(prompt + context)
```
- **Когда:** "что связывает X и Y?", "кто работает над Z?", "от чего зависит W?".
- **Антипаттерн:** использовать для "найди похожие документы". Граф не про similarity — он про explicit relationships.

### CODE / CODING_RULES — код как граф
```
extract code entities → query code-graph (functions, classes, patterns)
                     → assemble rules/snippets
```
- **Когда:** Levara разобрала кодовую базу через `codify` MCP tool и в графе лежат code entities.
- **Best practice:** комбинируй с MCP tool `git_search` для blame/diff контекста.

---

## 3. AUTO router — как он принимает решения

`AUTO` (или пустой `query_type`) идёт через `pkg/router/router.go`. Капабилити-чек: смотрит на `capabilitiesFromConfig` — есть ли embed, BM25, Neo4j, LLM, communities — и далее решает по сигналам в query:

| Сигнал в query | Куда роутит |
|---|---|
| Содержит дату/период | TEMPORAL |
| Cypher keywords (`MATCH`, `WHERE`) | CYPHER (если разрешено) |
| Имена сущностей + "связь/между/related" | GRAPH_COMPLETION |
| Question form (`?`, "what/how/why") + LLM available | RAG_COMPLETION |
| Code patterns (camelCase identifiers, `def `, `class `) | CODE |
| Default | HYBRID (если есть BM25), иначе CHUNKS |

`FEELING_LUCKY` — алиас AUTO, оставлен для backwards-compat с ранней версией WebUI.

**Best practice:** для MCP агентов — НЕ передавай `search_type`, дай router'у решить. Для UI с power users — выстави dropdown с явным выбором.

---

## 4. Что нужно для MCP

### Дефолтная стратегия для агента: AUTO
MCP tool `search` (`pkg/mcp/tool_search.go`) принимает `search_type` со значением по умолчанию `AUTO`. Агент **не должен** жёстко пинить стратегию, если у него нет специальной причины (например, "мне нужен chain-of-thought reasoning" → `GRAPH_COMPLETION_COT`).

### Параметры, которые агент должен уметь использовать

| Параметр | Зачем агенту |
|---|---|
| `search_query` | сам запрос |
| `search_type` | оставляй `AUTO` пока агент не уверен |
| `mode` (`rag`/`graph`/`full`/`auto`) | grosser-уровневая подсказка router'у; `rag` отсечёт graph-стратегии, `graph` отсечёт vector |
| `top_k` | 10 для chat-UI агента; 5 для context-injection (latency); 20+ только если делаешь reranking сам |
| `collection` | **критично для multi-tenancy** — пинить collection текущего проекта (`set_context` + `get_project_context`) |
| `room` / `tags` | post-filter по сабтопику. Когда коллекция большая и без фильтра выползают результаты из соседних доменов |
| `rerank` | дефолт `false` в MCP (чтобы агент сам решал бюджет). Включай когда нужно высокое precision |
| `multi_query` | дефолт `false`. Включай только если уверен, что query сложный — даёт +1 LLM call |
| `parent_child` | если кодовая база загружена с child chunks для precision, верни parent chunks для context |
| `dedup` | дефолт `true`, оставляй так |

### Контракт для агента (best practice)

1. **На старте сессии:** `set_context(collection=...)` + `get_project_context()` — узнаёт recommended_search_type и доступные стратегии.
2. **При первом recall:** `search(search_query=..., search_type="AUTO", top_k=10, dedup=true)`.
3. **Если результаты пустые или нерелевантные:** retry с `mode="full"` + `multi_query=true` + `top_k=20`.
4. **Для "ответь на вопрос":** `search_type="RAG_COMPLETION"` — backend сам соберёт citations.
5. **Для exploration графа:** `search_type="GRAPH_COMPLETION"` или `query_entity(name=..., as_of=...)`.

### Антипаттерны MCP

- ❌ Жёстко пинить `CHUNKS` без оснований — игноришь HYBRID, который почти всегда лучше.
- ❌ Включать `rerank=true` на каждый запрос — добавляет 100–500 ms и ACL surface к third-party.
- ❌ Огромный `top_k=100` "на всякий случай" — пожрёт твой context window.
- ❌ Использовать `CYPHER` если не уверен, что запрос валиден — лучше `NATURAL_LANGUAGE` (LLM сам соберёт Cypher).
- ❌ Опускать `collection` в multi-tenant сетапе — словишь результаты из соседних проектов.

---

## 5. Что лучше для UI

UI ≠ MCP агент — другие требования:

| Требование | UI | MCP |
|---|---|---|
| Latency budget | strict (<300 ms perceived) | гибкий (агент часто off-thread) |
| Result count | визуально 10 | в context injection 3–5 |
| Ranking quality | критично — пользователь видит первые 3 | агент читает все top_k |
| LLM answer | опционально (chat mode) | обычно агент сам генерирует |
| Explainability | нужна (highlight, snippet) | не нужна |

### Дефолт для UI: HYBRID + rerank
```json
{
  "query_text": "<user input>",
  "query_type": "HYBRID",
  "top_k": 10,
  "rerank": true,
  "collection": "<active project>"
}
```
- HYBRID покрывает оба recall-режима — пользовать пишет и semantic queries, и точные идентификаторы.
- `rerank: true` даёт +200 ms но заметный quality bump на top-3, что и видит пользователь.
- Если RERANK_ENDPOINT недоступен — graceful degradation, UI ничего не теряет.

### Когда UI должен переключать стратегию

- **Chat mode / "Ask Levara":** `RAG_COMPLETION` — backend вернёт answer + citations, UI рендерит pretty.
- **Search bar + filters by date:** `TEMPORAL` (явно), не AUTO.
- **Knowledge graph view:** `GRAPH_COMPLETION` или `query_entity` MCP-style.
- **Code search view:** `CODE` / `CODING_RULES`.
- **Power-user advanced mode:** dropdown со всеми стратегиями + tooltip с описанием.

### UI best practices

1. **Debounce input ≥300 ms** — иначе одна сессия пользователя сожжёт сотни поисков и rerank-инвокаций.
2. **Show search_type used** — особенно когда AUTO роутит на неожиданную стратегию. Прозрачность повышает доверие к router'у.
3. **Skeleton/loader для RAG_COMPLETION** — там 1–2 секунды LLM, без визуальной обратной связи это воспринимается как залип.
4. **Stream RAG answer когда возможно** — backend пока шлёт целиком, но при переходе на SSE/WS лучше показывать токены сразу.
5. **Inline `rerank_score` если доступен** — power users любят видеть, как rerank поменял порядок.

---

## 6. Лучшие практики по фичам

### Rerank (Phase 2 + 2.5)
- **Default-on когда `RERANK_ENDPOINT` сконфигурирован.** Клиент tri-state'ит: `rerank: true` (force), `false` (opt-out), `null/omit` (server default).
- **Score-gap gate (Phase 2.5):** если `RERANK_SCORE_GAP_THRESHOLD` > 0 и спред между top/bottom уже широкий — rerank пропускается (outcome=`skipped_gap`). Экономит сидкар-round-trip когда HNSW уже уверен.
- **ACL pre-filter (Phase 2.5):** запрещённые датасеты вылетают ДО отправки в reranker. Безопасно ходить в Cohere/Voyage.
- **Budget:** `RERANK_BUDGET_MS` (default 1500). На превышении — fallback на vector order, outcome=`budget`.
- **Outcome counter** `levara_rerank_invocations_total{outcome}`: `ok|budget|error|disabled|no_text|skipped_gap`. Дашборды должны мониторить отношения.

### Multi-query decomposition
- `graph.DecomposeQuery` разбивает сложные queries ("X and Y but not Z") на sub-queries.
- Fan-out видно через histogram `levara_search_chunks_subquery_fanout` (buckets 1,2,3,5,8,13,21).
- **Не путать с `multi_query=true` MCP параметром** — тот делает LLM-driven query rewriting (3 варианта).

### Dedup
- По умолчанию `dedup=true` (UI и MCP). Cosine threshold 0.85 для merge near-duplicates.
- **Когда выключать:** у тебя sliding-window chunks и ты хочешь все overlaps (например, для precise citation extraction).

### Graph-aware boost
- Когда в metadata запроса есть entity names, `graphrank.RerankWithGraph` бустит результаты, которые являются graph-соседями этих entities.
- Включается автоматически в `chunksSearch` если `cfg.DB != nil`. Не нужно явно просить.
- MCP параметр `graph_rerank=true` форсирует это даже если автоматика не сработала.

### Parent-child chunks
- Кодифицируется при cognify через chunk parent-child relationships.
- `parent_child=true` ищет по child chunks (точнее), возвращает parent chunks (больше контекста).
- Зачем: balance precision vs context window. Particularly polezno для code search.

---

## 7. Decision tree — какую стратегию

```
question с датой? ─yes→ TEMPORAL
       │no
       ▼
вопрос ожидает готовый answer (chat)? ─yes→ RAG_COMPLETION
       │no
       ▼
запрос о связях между entities? ─yes→ GRAPH_COMPLETION
       │no
       ▼
ищешь код (function/class names)? ─yes→ CODE
       │no
       ▼
точный термин / идентификатор / имя? ─yes→ BM25
       │no
       ▼
есть BM25 индекс? ─yes→ HYBRID  ◄── дефолт для UI и большинства агентов
       │no
       ▼
                    CHUNKS  ◄── fallback
```

**Если не хочется думать:** `AUTO` + правильный `mode`. Router решит.

---

## 8. Что мониторить в проде

| Метрика | Что говорит | Тревога |
|---|---|---|
| `levara_http_requests_total{route="/search/text"}` | общий поисковый QPS | спайк = взлом / runaway agent |
| `levara_rerank_invocations_total{outcome}` | breakdown rerank outcome | `budget`/`error` > 5% — sidecar тормозит |
| `levara_search_chunks_subquery_fanout` (histogram) | сложность queries | P95 ≥ 10 — DecomposeQuery off-rails |
| `levara_rate_limit_rejected_total` | rate-limit отклонения | спайк = misconfigured client |
| `levara_external_call_duration{target="rerank"}` | latency reranker'а | P95 > budget — урезай budget или меняй модель |
| `RAGAbstainTotal{reason}` | абстейны RAG | `strict_grounded_no_evidence` высокий — recall плохой |

---

## 9. Связанные документы

- `docs/api-reference.md` — полный OpenAPI surface всех стратегий.
- `docs/phase2-rerank-default-design.md` — design Phase 2 + 2.5 rerank пути.
- `docs/PHASE1_RAG_MODE.md`, `docs/PHASE1B_RAG_QUALITY.md` — RAG verify-stack.
- `docs/PHASE2_DAG_MODE.md` — graph-side стратегии в деталях.
- `CLAUDE.md` § "Levara MCP Memory — usage guide" — MCP параметры для recall.

---

## 10. TL;DR

- **UI default:** `HYBRID` + `rerank: true` + `top_k: 10`.
- **MCP default:** `AUTO` + `top_k: 10` + `dedup: true`, без явного `rerank`.
- **Chat / "Ask":** `RAG_COMPLETION`.
- **Точный термин:** `BM25`.
- **Связи / граф:** `GRAPH_COMPLETION`.
- **Даты:** `TEMPORAL`.
- **Не уверен:** `AUTO`. Серьёзно.

# Levara Search Strategies — practical guide

REST text search использует `POST /api/v1/search/text`, поле `query_type`;
MCP `search` — `search_type`. Для векторного API существует отдельная форма
`POST /api/v1/search`; не смешивайте её raw-vector payload с текстовым запросом.
Схемы: [API contract](api-contract.md), запуск: [getting started](getting-started.md).

## 1. Краткая таблица стратегий

| Стратегия | Задача | Необходимые данные/подсистемы |
|---|---|---|
| `CHUNKS_LEXICAL` / `BM25` | Идентификатор, имя, точный термин | Заполненный lexical index |
| `CHUNKS` | Семантически похожие фрагменты | Совместимые embeddings и vectors |
| `HYBRID` / `WEIGHTED_HYBRID` | Совместить lexical и semantic signals | Оба индекса; fusion scores |
| `SUMMARIES` | Найти summary chunks | Проиндексированные summaries |
| `TEMPORAL` | Поиск с временным контекстом | Релевантные временные metadata/graph и поддерживаемый backend |
| `RAG_COMPLETION` | Ответ на основе найденного контекста | Retrieval; LLM для генерации |
| `GRAPH_COMPLETION` и варианты | Связанные сущности и расширенный контекст | Наполненный graph, подходящая стратегия и LLM |
| `CYPHER` / `NATURAL_LANGUAGE` | Запросы к графу, включая NL→Cypher | Neo4j и отдельные разрешённые возможности |
| `CODE` / `CODING_RULES` | Структуры кода и правила | Предварительно построенные code entities |
| `COMMUNITY_LOCAL` / `COMMUNITY_GLOBAL` | Обзор communities | Построенные сообщества и соответствующий backend |
| `AUTO` / `FEELING_LUCKY` | Выбор по запросу и доступным возможностям | Набор реально настроенных подсистем |

Названия стратегий не гарантируют конкретную задержку или качество. Результат
зависит от корпуса, модели, индекса, top-k и доступных backend. Сравнивайте
известные запросы с source evidence; не переносите latency из чужого стенда.

## 2. Выбор и проверка

Начните с `CHUNKS_LEXICAL` для уникальной фразы загруженного документа, затем
сравните `CHUNKS` и `HYBRID` на перефразированном вопросе. Для ответа chat
проверьте grounded sources, а для графового вопроса — реально извлечённые
сущности и рёбра. Дата в запросе не создаёт отсутствующую историческую информацию.

```bash
curl -fsS http://127.0.0.1:8080/api/v1/search/text \
  -H 'Content-Type: application/json' \
  -d '{"query_text":"Acme","query_type":"CHUNKS_LEXICAL","collection":"demo","top_k":5,"rerank":false}'
```

Это запрос к no-auth loopback tutorial. Для защищённого сервера добавьте свой
bearer header и используйте доступные datasets. Документ должен пройти обработку
в ту же collection: [knowledge-base tutorial](tutorials/03-knowledge-base.md).
Collection scope и metadata filters не заменяют проверку dataset grants.

## 3. AUTO router

Пустой `query_type` в REST и default `search_type` MCP выбирают AUTO. Router
учитывает возможности и сигналы запроса; это не обещание одной стратегии для
всех natural-language вопросов. Для воспроизводимого сравнения укажите тип явно.
Стратегия может использовать доступный fallback; проверяйте фактический ответ
и диагностику, а не только запрошенный label.

## 4. MCP параметры

```text
search(search_query="who maintains payments", collection="demo",
       search_type="HYBRID", top_k=5, rerank=false)
```

`top_k`, а не `limit`, ограничивает число результатов `search`.
`recall_memory` имеет другую схему и собственный `limit`. Room/tags помогают
сузить предметную область. `dedup` по умолчанию включён; специальные параметры
`multi_query`, `parent_child`, `graph_rerank` требуют подходящих данных и
зависимостей. Проверяйте текущую [схему](api-contract.md) перед их использованием.

## 5. Rerank: defaults, бюджет и fallback

Reranker — отдельный provider, [настройка sidecar](../deploy/rerank/README.md).
Текст разрешённых кандидатов может отправляться этому провайдеру. ACL pre-filter
в соответствующих search paths не означает, что внешний провайдер одобрен для
ваших данных автоматически.

| REST `rerank` | Поведение при настроенном endpoint |
|---|---|
| Поле отсутствует / `null` | Server default: rerank включён |
| `false` | Явный opt-out |
| `true` | Явный запрос rerank, обходит adaptive score-gap gate |

Без endpoint успешного cross-encoder прохода не будет. MCP schema использует
boolean с default `false`, поэтому REST default-on нельзя переносить на MCP.
Явное `true` не отменяет timeout, ошибки провайдера или отсутствие текста.

`RERANK_BUDGET_MS` ограничивает rerank budget (default 1500 ms);
`RERANK_TIMEOUT_MS` задаёт client timeout. При budget/error/no_text сохраняется
предыдущий порядок: vector order для CHUNKS, fused order для HYBRID. HTTP-успех
не означает, что rerank сработал: смотрите per-result `reranked` и outcomes.

## 6. Score-gap gate и разные шкалы

`RERANK_SCORE_GAP_THRESHOLD > 0` позволяет пропустить дополнительный проход,
если разрыв между верхним и нижним candidate score выше порога. В CHUNKS это
шкала vector similarity, в HYBRID — значительно иная шкала RRF fusion.
Один числовой порог нельзя трактовать одинаково для двух стратегий или моделей.

`levara_rerank_score_spread{axis}` различает эти шкалы (`vector`, `rrf`).
Сначала соберите распределение на своём query set и сравните качество с
`rerank:true`; затем выбирайте threshold. Большой score gap — эвристика, а не
доказательство правильного top-1. Смена модели требует новой калибровки.

## 7. Fan-out и метрики

`levara_rerank_invocations_total{outcome}` различает
`ok`, `budget`, `error`, `disabled`, `no_text`, `skipped_gap`.
Это счётчик внутренних rerank решений, не обязательно число HTTP-запросов:
CHUNKS может разложить запрос на несколько subqueries и пройти несколько
collections. `levara_search_chunks_subquery_fanout` измеряет именно число
итераций subquery × collection на запрос.

Сравнивайте outcome rate с внутренней нагрузкой и её fan-out, а request latency
измеряйте отдельно. Доля `budget`/`error`, нехватка text у candidates и задержка
provider — разные причины. Не назначайте универсальный порог тревоги без
baseline. MCP `multi_query=true` также нельзя считать тем же механизмом, что
внутренняя декомпозиция CHUNKS.

## 8. Проверка качества

Сохраните корпус, выбранные IDs/collection, модель и dimension, query set,
ожидаемые релевантные источники, k, rerank settings и workload. Сравните
лексический baseline, dense/hybrid и rerank по одинаковому набору. Проверьте
нулевую выдачу, верные источники, нерелевантные кандидаты и отказ по известному
чужому dataset ID. Качество нельзя доказать только успешным `/health` или
наличием любого hit.

[Document scenarios](document-workflow-scenarios.md) связывает UI/API с источниками;
[testing](testing.md) разделяет mock tests, package tests и реальные provider
прогоны. [Deployment](deployment.md) содержит смену embedding-модели и rollback.

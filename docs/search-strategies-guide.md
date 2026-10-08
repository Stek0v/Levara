# Levara Search Strategies — practical guide

REST text search использует `POST /api/v1/search/text`, поле `query_type`;
MCP `search` — `search_type`. Для векторного API существует отдельная форма
`POST /api/v1/search`; не смешивайте её raw-vector payload с текстовым запросом.
Схемы: [API contract](api-contract.md), запуск: [getting started](getting-started.md).

## 1. Стратегии REST и MCP

| Стратегия | Задача | Необходимые данные/подсистемы | Transport |
|---|---|---|---|
| `CHUNKS_LEXICAL` / `BM25` | Идентификатор, имя, точный термин | Заполненный lexical index | REST и MCP |
| `CHUNKS` | Семантически похожие фрагменты | Совместимые embeddings и vectors | REST и MCP |
| `HYBRID` / `WEIGHTED_HYBRID` | Совместить lexical и semantic signals | Оба индекса; fusion scores | REST и MCP |
| `SUMMARIES` | Найти summary chunks | Проиндексированные summaries | REST |
| `TEMPORAL` | Поиск с временным контекстом | Релевантные временные metadata/graph и поддерживаемый backend | REST |
| `RAG_COMPLETION` | Ответ на основе найденного контекста | Retrieval; LLM для генерации | REST |
| `GRAPH_COMPLETION` и варианты | Связанные сущности и расширенный контекст | Наполненный graph, подходящая стратегия и LLM | REST |
| `CYPHER` / `NATURAL_LANGUAGE` | Запросы к графу, включая NL→Cypher | Neo4j и отдельные разрешённые возможности | REST |
| `CODE` / `CODING_RULES` | Структуры кода и правила | Предварительно построенные code entities | REST |
| `COMMUNITY_LOCAL` / `COMMUNITY_GLOBAL` | Обзор communities | Построенные сообщества и соответствующий backend | REST |
| `AUTO` / `FEELING_LUCKY` | Выбор по запросу и доступным возможностям | Набор реально настроенных подсистем | REST и MCP |

MCP дополнительно поддерживает `PARENT_CHILD`, `MULTI_QUERY`, `RERANK` и `GRAPH_RERANK`; `BASIC` — legacy alias векторного поиска. Явные REST-only и неизвестные strategy labels в MCP отклоняются до обращения к providers. `mode=graph` в MCP также отклоняется; `rag`, `full` и `auto` используют доступные retrieval paths. `workspace_search` использует тот же контракт стратегий.

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
В MCP AUTO ограничен исполнимыми retrieval paths; `search_type`, `routing.selected_type` и alternatives описывают этот выбор. При отсутствии LLM для MULTI_QUERY, endpoint для RERANK или разрешённого graph backend для GRAPH_RERANK сохраняется векторный fallback с фактическим label. `PARENT_CHILD` обозначает выбранный алгоритм; его native pipeline может использовать векторный fallback при отсутствии child collection или child hits, сохраняя label PARENT_CHILD. `reranked=false` означает, что cross-encoder не применился, включая score-gap/error cases. Без embeddings vector paths сохраняют совместимый ответ «No results (embedding service not configured)»; lexical-only путь работает независимо. HYBRID без настроенной vector pipeline использует доступный lexical index и сообщает CHUNKS_LEXICAL. При runtime-ошибке одной retrieval leg HYBRID сохраняет успешную leg и выполняет fusion; label HYBRID обозначает этот алгоритм, а не успешность обоих providers. REST per-leg scores показывают фактические сигналы. Ошибка обеих legs, отмена или отказ authority guard не превращаются в успешный fallback.

## 4. MCP параметры

```text
search(search_query="who maintains payments", collection="demo",
       search_type="HYBRID", top_k=5, rerank=false)
```

`top_k`, а не `limit`, ограничивает число результатов `search`.
`recall_memory` имеет другую схему без параметров `top_k` и `rerank`. Room/tags помогают
сузить предметную область. `dedup` по умолчанию включён; специальные параметры
`multi_query`, `parent_child`, `graph_rerank` требуют подходящих данных и
зависимостей. Проверяйте текущую [схему](api-contract.md) перед их использованием.

Фильтры room/tags и ACL применяются до dedup, fusion caps и передачи source text в reranker/LLM; final authority check сохраняется. Tags используют ANY/OR: достаточно совпадения одного выбранного тега. При явном фильтре ноль совпадений означает пустую выдачу. MCP сравнивает значения tags точно; REST сохраняет существующее сравнение без учёта регистра и legacy metadata-key matching. Фиксированный overfetch ограничивает окно кандидатов; это не обещание исчерпывающего filtered top-k.

PARENT_CHILD возвращает parent metadata, поэтому room/tags проверяются у parents до ограничения выдачи; children могут не содержать эти поля. Parents выбираются по точным IDs, сортируются по лучшему child score; отсутствующие и недоступные parents не занимают result budget.

Admin-only `/api/v1/search/dual` проверяет embedding contract каждой collection через native query pipeline. Одинаковая dimension не позволяет объединять разные encoders. Query alias, общий client/QoS и request deadline сохраняются; поиск collections идёт последовательно в пределах общего deadline.

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

В MCP `rerank:true` поддерживается для `CHUNKS`, `HYBRID` и
`WEIGHTED_HYBRID`; lexical-only поиск остаётся без cross-encoder прохода.
`workspace_search` по умолчанию выбирает HYBRID, но rerank нужно запросить
явно. Сначала объединяются кандидаты, затем проверяется доступ, выполняется
rerank и применяется лимит. MCP возвращает общий флаг `reranked`, REST —
флаг в каждом результате. `recall_memory` и `task_bootstrap` этим проходом
не пользуются. В MCP явное `true` не обходит настроенный score-gap gate.

`RERANK_BUDGET_MS` ограничивает rerank budget (default 1500 ms);
`RERANK_TIMEOUT_MS` задаёт client timeout. При budget/error/no_text сохраняется
предыдущий порядок: vector order для CHUNKS, fused order для HYBRID. HTTP-успех
не означает, что rerank сработал: смотрите per-result `reranked` и outcomes.

## 6. Score-gap gate и разные шкалы

`RERANK_SCORE_GAP_THRESHOLD > 0` позволяет пропустить дополнительный проход,
если разрыв между верхним и нижним candidate score выше порога. В CHUNKS это
шкала vector similarity, в HYBRID — значительно иная шкала RRF fusion.
Один числовой порог нельзя трактовать одинаково для двух стратегий или моделей.

В REST `levara_rerank_score_spread{axis}` различает эти шкалы (`vector`, `rrf`).
Общий MCP helper пока пишет `axis="vector"` и для HYBRID: не смешивайте это
наблюдение с vector similarity при калибровке.
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

В REST `RAG_COMPLETION` confidence и `evidence_ids` относятся к источникам,
которые действительно вошли в context генерации. Короткий непустой факт
может служить контекстом; один retrieval ID без текста этого не доказывает.
При отсутствии usable context модель не вызывается: `abstained=true`,
`no_usable_evidence` (или `strict_grounded_no_evidence` при strict flag).
Failed/empty generation возвращает `generation_unavailable`; пустой ответ
не сохраняется как session interaction. Успешная форма HTTP-ответа сохранена.

Default abstention threshold 0 отключает low-confidence threshold, но не
проверку наличия context. Metadata verification и strict_grounded opt-in.
Confidence — конечная эвристика, не калиброванная вероятность истины.
Refusal, сформулированный самой моделью при нерелевантных источниках,
оценивается отдельно: server `abstained` описывает решения retrieval/generation,
а не семантическую проверку произвольного текста ответа.


Сохраните корпус, выбранные IDs/collection, модель и dimension, query set,
ожидаемые релевантные источники, k, rerank settings и workload. Сравните
лексический baseline, dense/hybrid и rerank по одинаковому набору. Проверьте
нулевую выдачу, верные источники, нерелевантные кандидаты и отказ по известному
чужому dataset ID. Качество нельзя доказать только успешным `/health` или
наличием любого hit.

При сравнении реранкеров сохраняйте также одинаковые списки кандидатов с
исходным порядком и query-specific relevance labels. Отдельно измеряйте
полноту первого этапа: rerank не найдёт документ вне списка. Не располагайте
все положительные примеры первыми — сохранение такого порядка даст идеальный
NDCG даже без модели. Проверка должна сравнивать качество с исходным порядком
и подтверждать фактический проход реранкера.

[Document scenarios](document-workflow-scenarios.md) связывает UI/API с источниками;
[testing](testing.md) разделяет mock tests, package tests и реальные provider
прогоны. [Deployment](deployment.md) содержит смену embedding-модели и rollback.

# ADR-001: Слои графа — `pkg/graph`, `pkg/graphdb`, `pkg/graphstore`

**Дата:** 2026-04-15
**Статус:** accepted
**Контекст:** во время аудита выявлено, что `pkg/graphstore` не имеет внешних импортёров — абстракция введена, но не подключена.

## Проблема

Три пакета с близкими именами:

| Пакет | LOC | Тесты | Суть |
|---|---|---|---|
| `pkg/graph` | 908 | 5 | Алгоритмы над графом (dedup, LSH, multiquery, semantic_dedup, triplet). **Без persistence.** |
| `pkg/graphdb` | 672 | 2 | Neo4j-клиент + cache. Конкретный backend. |
| `pkg/graphstore` | 180 | 0 | Интерфейс `GraphStore` + `PostgresGraphStore` (recursive CTE). **Нет импортёров.** |

Без ADR новый разработчик не знает куда класть код → размножение случайных зависимостей.

## Решение

**Роли фиксируются так:**

- **`pkg/graph`** — чистые алгоритмы над in-memory структурами (`DedupNode`, `DedupEdge`, triplets). Не знает о persistence.
- **`pkg/graphdb`** — persistent graph backend (Neo4j). Источник истины, когда Neo4j включён в стэк.
- **`pkg/graphstore`** — альтернативный persistent backend поверх PostgreSQL (recursive CTE для multi-hop) + интерфейс `GraphStore` для подмены.

**Почему три, а не два:**

- `graph` и `graphdb` концептуально разные — алгоритмы vs I/O. Сливать нельзя.
- `graphstore` — **candidate for activation**, не deletion. 133 LOC CTE-логики в `postgres.go` — не мусор. Но сейчас мёртвый код.

## План активации `pkg/graphstore` (отдельная задача, не в этом worktree)

1. Добавить `GraphStore` интерфейс к контракту `internal/http.Handler` и `internal/grpc` — там, где сейчас прямой импорт `graphdb.Neo4jClient`.
2. Сделать `graphdb.Neo4jClient` **тоже** имплементацией `graphstore.GraphStore` (добавить `QueryNHop` через Cypher).
3. Выбор backend — по конфигу (`GRAPH_BACKEND=neo4j|postgres`).
4. Контракт-тесты: одна и та же fixture прогоняется через обе реализации, результаты должны совпасть (topological equivalence).

**До активации** `pkg/graphstore` остаётся в репозитории с `// TODO(ADR-001)` комментарием в `store.go`, чтобы новый разработчик не подумал, что это действующая абстракция.

## Правила для разработчиков (tl;dr)

| Задача | Куда класть |
|---|---|
| Новый графовый алгоритм (dedup, community, rerank без I/O) | `pkg/graph` или отдельный pkg (`pkg/community`, `pkg/graphrank` уже так сделаны) |
| Работа с Neo4j (Cypher, драйвер) | `pkg/graphdb` |
| Работа с Postgres для графа | `pkg/graphstore` **только если** активация ADR-001 выполнена, иначе — никуда не класть, сначала активация |
| HTTP/gRPC хендлер, нуждающийся в графе | Импортировать backend напрямую (`graphdb`) до активации, через интерфейс `graphstore.GraphStore` — после |

## Альтернативы отвергнуты

- **Удалить `pkg/graphstore` целиком.** Отвергнуто: CTE-логика ценная, её переписывание позже будет стоить дороже, чем поддерживать dormant code.
- **Слить `graphdb` в `graphstore`.** Отвергнуто: инверсия зависимости — интерфейс должен принимать реализации, а не наоборот. Миграция `graphdb` под `graphstore.GraphStore` — это **step 2** активации, не переезд.

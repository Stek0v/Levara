# Levara × Neo4j Coverage Plan (Tasks + DoD)

Дата: 2026-04-28  
Цель: расширить покрытие neo4j-функционала в Levara без деградации безопасности и стабильности.

## Принципы выполнения

- Безопасность по умолчанию: read-only policy остаётся дефолтом.
- Все изменения с автотестами (unit + handler/integration где применимо).
- Каждый этап завершать измеримым DoD.

---

## Этап 1 — Cypher policy hardening + controlled write mode

### Задачи
1. Вынести проверку Cypher в отдельный валидатор.
2. Ужесточить read-only профиль:
   - разрешённые префиксы: `MATCH`, `OPTIONAL MATCH`, `WITH`, `CALL`, `UNWIND`;
   - запрет write-операций: `CREATE`, `MERGE`, `DELETE`, `DETACH`, `SET`, `REMOVE`, `FOREACH`.
3. Добавить управляемый write-режим через env-флаг `ALLOW_CYPHER_WRITE=true`.
4. Даже в write-режиме блокировать admin/destructive операции:
   - `DROP`, `DATABASE`, `CONSTRAINT`, `INDEX`, `DBMS`, `TERMINATE`, `LOAD CSV`.

### DoD
- [ ] `CYPHER` запросы с write-клаузами в дефолтном режиме возвращают 403.
- [ ] `CYPHER` запросы с write-клаузами при `ALLOW_CYPHER_WRITE=true` проходят policy-check.
- [ ] Admin/destructive запросы блокируются всегда.
- [ ] Покрытие тестами:
  - unit-тесты policy-валидатора,
  - handler-тесты для 403/500 веток без поднятого Neo4j.

---

## Этап 2 — Index/Constraint manager для Neo4j

### Задачи
1. Ввести декларативный список индексов/constraint'ов (name/type/dataset_id/temporal поля).
2. Добавить `EnsureSchema(ctx)` в graphdb-слой.
3. Встроить schema-check в doctor (warn/fail + remediation hint).

### DoD
- [ ] При старте система создаёт/проверяет обязательные индексы.
- [ ] Doctor показывает отсутствующие индексы и рекомендации.
- [ ] Тесты на генерацию/применение schema statements.

---

## Этап 3 — Transaction API для graphdb

### Задачи
1. Добавить `RunReadTx` / `RunWriteTx` обёртки.
2. Перенести multi-step graph write в explicit transaction.
3. Добавить retry на transient ошибки (bounded retry).

### DoD
- [ ] Multi-step write атомарен.
- [ ] При transient ошибке наблюдается успешный bounded retry.
- [ ] Покрытие unit-тестами и happy-path integration-тестом.

---

## Этап 4 — Path/Traversal API

### Задачи
1. Добавить endpoint/tool для shortest-path и k-hop traversal.
2. Параметры: `source`, `target`, `max_hops`, `rel_allowlist`.
3. Возвращать explainable путь (узлы + рёбра + сводка).

### DoD
- [ ] API выдаёт корректный путь на тестовом графе.
- [ ] Невозможный путь возвращает корректный empty result.
- [ ] Есть тесты для ограничений по `max_hops` и фильтрации типов рёбер.

---

## Этап 5 — Temporal graph parity в Neo4j path

### Задачи
1. Добавить `as_of` и временные фильтры в Neo4j-поиск/контекст.
2. Привести семантику к MCP `query_entity` (valid window).
3. Индексы по temporal полям.

### DoD
- [ ] Для одинаковых тест-кейсов PostgreSQL и Neo4j пути дают согласованную temporal-выборку.
- [ ] Есть тесты на active-now и snapshot-as_of.

---

## Статус текущей итерации

- [x] Этап 1: реализован (policy hardening + controlled write mode + тесты).
- [~] Этап 2: частично реализован (bootstrap индексов/constraint'ов + doctor schema-check; остаются интеграционные проверки).
- [ ] Этап 3: запланирован.
- [ ] Этап 4: запланирован.
- [ ] Этап 5: запланирован.

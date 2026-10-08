# Taxonomy Writer Design — гибрид для knowledge_* (Р3, T8)

Дата исходного дизайна: 2026-09-27. Уточнение фазы A: 2026-10-06, реализация T17
по `implement-taxonomy-seed-writer` (решение Р3=C promote зафиксировано в [functional audit](functional-audit-2026-09-27.md)).
Решение по форме: **гибрид** (ОВ4, владелец 2026-09-27) — ручной seed плюс
автогенерация как proposals.

## 1. Проблема

DCD-роутер (`LEVARA_DCD_ROUTER`) резолвит маршруты domain→collection→document
по BM25 над таблицами `knowledge_domains` / `knowledge_collections` /
`knowledge_documents`. Сегодня у таблиц **нет продакшн-писателя** — единственные
INSERT в тестах, поэтому роутер на реальных данных спит. Без решения-писателя
promote (Р3=C) не существует физически.

## 2. Решение: гибрид из двух фаз

### Фаза A — ручной seed (MVP, без LLM)

`levara taxonomy import <file> --dataset <id>` импортирует таксономию из человекочитаемого
markdown-файла (источник правды коммитится в репозиторий проекта рядом с
AGENTS.md):

```markdown
# Domains
## auth
Описание: аутентификация, сессии, токены.
Алиасы: authentication, login
### Collections
#### sessions
Описание: JWT и browser-сессии.
```

Инструменты: `levara taxonomy import|list|remove`. Импорт **идемпотентен по
натуральном ключу** (verified caller + exact selected tenant + dataset + parent + normalized name): повторный запуск обновляет
описания/алиасы, не дублируя строки. Формат файла и точная грамматика
фиксируются в `docs/taxonomy-import.md`.

### Фаза B — автогенерация как proposals (после quality-gate фазы A)

Cognify-пайплайн предлагает домены/коллекции из графа, но **не пишет в
knowledge_\* напрямую**: предложения попадают в очередь `knowledge_proposals`
(паттерн существующего memory-scaffold: proposal → human decision → apply).
Человек принимает/правит/отклоняет; применённые предложения проходят тот же
код-путь, что ручной импорт.

## 3. Инварианты и границы

- **ACL**: каталог сохраняет существующую приватность пользователя. `owner_id`
  берётся из проверенного caller, `team_id` — из точного выбранного tenant, включая
  пустой, `dataset_id` задаётся явно. У dataset нет поля team, поэтому наследование
  из него невозможно. Импорт/удаление требуют dataset write, список — read;
  привязка документа требует read к source в этом dataset. Shared catalog — отдельный
  audience contract. Подсказка таксономии не предоставляет доступ к source.
- **Provenance фазы A**: `knowledge_taxonomy_runs` хранит content-free hash,
  source label/revision и отчёт одной транзакции с изменениями. Raw seed/source bytes
  не сохраняются. Повтор request ID требует того же action и body hash.
  `knowledge_proposals` относится только к будущей фазе B.
- **Determinism**: импорт — одна SQL-транзакция на файл; частичный импорт
  невозможен.
- **Не в MVP**: автоприменение предложений, change feed каталога,
  кросс-датасетные домены, ACL на уровне домена.

## 4. Corner cases

| Кейс | Поведение |
|---|---|
| Пустая таксономия | DCD резолвит ноль кандидатов, поиск не меняется (текущее поведение) |
| Дубликат имени в одном датасете | Идемпотентное обновление, не вторая строка |
| Алиас-конфликт между доменами | BM25-скор решает; конфликтные алиасы репортятся import-отчётом как warning |
| Удаление домена при живых документах | Удаление коллекции/документа отцепляет ребро, документы не удаляются; `taxonomy remove` требует `--force` для непустых узлов |
| Переименование | Импорт по натуральному ключу обновляет имя? Нет: имя — часть ключа; переименование = remove + import (ревизия в журнале) |
| Межарендаторская утечка | Тест: домен A не бустит выборку пользователя B (скope-фильтр резолвера) |

## 5. Приёмка (gate перед дефолтным включением DCD)

1. Unit/интеграционные: идемпотентный импорт, изоляция owner/team/dataset,
   транзакционность, edge-кейсы §4 (SQLite + PostgreSQL).
2. Native A/B: поддерживаемые graph completion стратегии с реально импортированными
   source bindings; off/observe сохраняют порядок, boost измеряет eligible lift,
   foreign/retired sources не появляются. Старый `benchmark/retrieval_quality.py`
   использует стратегии без DCD и сам по себе не доказывает этот gate.
   Внешний размеченный корпус/model/hardware quality остаётся отдельным release gate.
   **Критерий включения по умолчанию**: деградации нет (ни одного регресса
   recall@k сверх шума) и есть измеримый lift хотя бы на одном типе запросов.
3. Агентский gate: `memory_behavior_eval` — zero_result_rate не растёт.

## 6. Объём и порядок

- Фаза A: CLI import/list/remove + транзакционный writer + тесты + док —
  ~3–5 дней.
- Инфраструктура proposals (таблица + API + apply) — отдельная задача после
  фазы A (~3–4 дня).
- Фаза B (cognify-генератор предложений) — после первых пилотов с фазой A.

## 7. Зафиксированный контракт фазы A

Markdown-грамматика, CLI/REST и ограничения описаны в [taxonomy import](../taxonomy-import.md).
Опциональный document node сохраняет отдельные taxonomy ID и source DataID;
нативный boost сопоставляет dataset + source после ACL. Удаление bindings не
удаляет источники/graph. Native fixture evidence не включает default boost и
не заменяет внешний release gate. Общая зависимость T15/Neo4j остаётся видимой.

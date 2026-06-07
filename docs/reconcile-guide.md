# `reconcile` — восстановление индексов Levara из MemoryFS

**Назначение.** Перестроить Levara-индекс из markdown-корпуса MemoryFS, не
трогая исходные `.md` файлы. MemoryFS — источник истины; Levara-индекс —
disposable derivative, который можно снести и собрать заново.

**Расположение бинаря:** `cmd/reconcile/` в репозитории Levara. Сборка:
`go build -o bin/reconcile ./cmd/reconcile`. Готовый бинарь — однофайловый,
переносим, не требует gRPC-клиента (использует HTTP `/api/v1/add`).

---

## Когда использовать

| Сценарий | Действие |
|---|---|
| Снёс Postgres / WAL / HNSW Levara — нужно вернуть знания | Полный reconcile с `-apply` |
| Хочешь увидеть, что сейчас лежит в корпусе MemoryFS, без записи | `-v` без `-apply` |
| Часть подкорпуса (например, только `decisions`) перенести в отдельный dataset | `-type decision -dataset decisions-only -apply` |
| Свежие записи догнать после инцидента в индексаторе | `-since 2026-05-10 -apply` |
| Готовишь staging-копию: тот же корпус → другой Levara | `-levara-url http://staging:8080 -apply` |

**Не подходит** для инкрементальной двусторонней синхронизации — это
односторонний bulk write. Для текущих записей пиши через MemoryFS REST
(`POST /v1/commit`), а Levara проиндексирует через свой gRPC writer.

---

## Гарантии и ограничения

- **Идемпотентность.** Каждый `-apply` создаёт новый набор записей —
  endpoint `/api/v1/add` не дедуплицирует по `slug`. Канонический
  workflow: **`prune` dataset → `reconcile -apply`**, не «накатить
  поверх».
- **Атомарность отсутствует.** Каждая запись — отдельный HTTP POST. При
  падении на половине пути половина уже в индексе. Exit code `1` если
  были фейлы; читай stderr для списка путей.
- **Только текст.** Картинки, PDF, бинарные вложения не поддерживаются —
  reconcile предполагает, что MemoryFS хранит только `.md`.
- **Frontmatter — flat key/value.** Вложенные YAML-структуры
  игнорируются (см. парсер в `cmd/reconcile/main.go`). Это сознательное
  решение: реальные `description:` содержат двоеточия, и строгий YAML на
  них падал.
- **Файлы `INDEX.md` и `MEMORY.md` пропускаются** — они индексы, не
  факты.

---

## Установка и подготовка

### Сборка

```bash
cd /path/to/Levara
go build -o bin/reconcile ./cmd/reconcile
```

### JWT

`reconcile` пишет через `/api/v1/add`, который требует JWT. Получить
токен:

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example","password":"..."}' | jq -r .access_token)
export LEVARA_JWT="$TOKEN"
```

`reconcile` подхватит `$LEVARA_JWT` автоматически, либо передавай явно
через `-token`. **Не коммить токен в git и не клади в shared dotfiles**.

### Целевой dataset

По умолчанию `-dataset memoryfs-reconcile`. Если dataset существует —
addHandler найдёт его по имени и допишет. Если нет — создаст. Чтобы
изолировать reconcile-данные от прод-коллекций, используй уникальное имя
(`memoryfs-reconcile-2026-05-15` и т.п.).

---

## Использование

### Dry-run (по умолчанию)

```bash
./bin/reconcile -corpus ~/.claude/projects/<proj>/memoryfs -v
```

Печатает список записей и счётчик. Никаких HTTP-вызовов. Сначала всегда
запускай dry-run — убедись, что путь к корпусу правильный и счётчик
совпадает с ожиданиями.

### Apply

```bash
./bin/reconcile \
    -corpus ~/.claude/projects/<proj>/memoryfs \
    -levara-url http://localhost:8080 \
    -token "$LEVARA_JWT" \
    -dataset memoryfs-reconcile \
    -apply \
    -v
```

### Фильтры

```bash
# только decisions, созданные не раньше 2026-05-11
./bin/reconcile -corpus ./memoryfs -type decision -since 2026-05-11 -apply
```

### Полный список флагов

| Флаг | По умолчанию | Описание |
|---|---|---|
| `-corpus` | (обязателен) | Путь к корню MemoryFS-корпуса |
| `-v` | false | Печатать каждую запись (parse + write) |
| `-apply` | false | Если не указан — dry-run, ничего не пишется |
| `-levara-url` | `http://localhost:8080` | Базовый URL Levara HTTP |
| `-token` | `$LEVARA_JWT` | JWT bearer для авторизации |
| `-dataset` | `memoryfs-reconcile` | Имя целевого dataset |
| `-type` | "" | Фильтр: только entries с этим `type:` во frontmatter |
| `-since` | "" | Фильтр: `created >= YYYY-MM-DD` (лексикографическое сравнение) |
| `-timeout` | `30s` | Таймаут на один HTTP POST |

### Exit codes

- `0` — все entries записаны (или dry-run завершился).
- `1` — были ошибки записи (см. stderr).
- `2` — неверное использование (отсутствует `-corpus`).

---

## Что попадает в индекс

Для каждой записи `reconcile` собирает body примерно так:

```
# <slug>

<description>

(type=<type> created=<created> status=<status>)

<тело markdown-файла без frontmatter>
```

И POST-ит как JSON:

```json
{
  "data": "<собранное body>",
  "dataset_name": "memoryfs-reconcile",
  "tags": ["reconcile", "<type>", "slug:<slug>", "status:<status>"]
}
```

Frontmatter инжектится обратно в body, потому что после `/add` чанкер
видит только `data`. Без этого slug/type/description не попадут в
векторное представление и recall сильно деградирует.

Tags закладываются для последующего фильтрованного поиска:

```bash
curl -H "Authorization: Bearer $LEVARA_JWT" \
  "http://localhost:8080/api/v1/search?dataset_name=memoryfs-reconcile&tags=decision"
```

---

## Recovery scenarios (для администратора)

### Полный rebuild после wipe

1. Убедись, что MemoryFS-корпус цел: `ls ~/.claude/projects/<proj>/memoryfs/`.
2. Stop Levara writers (или временно сними write-роль у JWT, чтобы во
   время reconcile не примешивались чужие записи).
3. `prune` целевой dataset, либо создай новый под уникальным именем:
   ```bash
   curl -X POST -H "Authorization: Bearer $LEVARA_JWT" \
     http://localhost:8080/api/v1/prune \
     -d '{"dataset_name":"memoryfs-reconcile"}'
   ```
4. Запусти `reconcile -apply -v`. Считай записи на выходе.
5. Sanity check: `search` по известному slug должен вернуть запись.

### Откат

`/add` не возвращает chunk IDs, поэтому отдельные записи откатить
сложно. Стратегия: **изолируй reconcile в отдельный dataset**. Если
что-то пошло не так — `prune` всего dataset, перезапусти.

### Verification

После apply сверь количество:

```bash
EXPECTED=$(./bin/reconcile -corpus ./memoryfs | tail -1 | awk '{print $2}')
ACTUAL=$(curl -s -H "Authorization: Bearer $LEVARA_JWT" \
  "http://localhost:8080/api/v1/datasets" \
  | jq '.[] | select(.name=="memoryfs-reconcile") | .items_count')
echo "expected=$EXPECTED actual=$ACTUAL"
```

`actual` может быть больше — `/add` чанкует длинные тексты. Это
нормально. **Меньше — это red flag**: смотри stderr предыдущего запуска.

---

## Monitoring

Reconcile создаёт обычный HTTP-трафик. Мониторь через стандартные
Prometheus-метрики Levara:

- `levara_http_requests_total{path="/api/v1/add",code=~"2.."}` — успешные записи.
- `levara_http_requests_total{path="/api/v1/add",code=~"4..|5.."}` — фейлы.
- `levara_ingest_items_total` — chunks, ушедшие в индекс.

Если массовый reconcile (>1000 entries) — следи за HNSW indexer
backpressure (`levara_indexer_queue_depth`); при необходимости разбей на
батчи через `-since`.

---

## Troubleshooting

| Симптом | Причина / фикс |
|---|---|
| `status 401` на каждой записи | JWT просрочен / неверный. Переавторизуйся. |
| `status 429` | Per-user rate limit (100 req/min). Подожди или подними лимит. |
| `parse <file>: ...` в stderr | Файл без frontmatter — он всё равно попадёт в индекс body-only. Не ошибка, а warn. |
| Половина записей в индексе, половина — нет | Network blip или Levara рестартанул. Безопасный путь: `prune` dataset + полный реран. |
| `empty body` | `.md` без description и тела. Проверь файл — обычно это сломанный мердж. |

---

## История изменений

- **2026-05-15** — Phase 1 (dry-run scaffold) + Phase 2 (HTTP writer)
  готовы. Корпус из 14 записей в `local_net/memoryfs` индексируется
  чисто. Cross-repo cleanup (legacy direct-write endpoints) — отдельный
  заход.

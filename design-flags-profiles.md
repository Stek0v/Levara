# DESIGN: Levara Unified Flags & Profiles (Issues #110, #111)

Status: **DRAFT** — требует согласования перед реализацией.
Last updated: 2026-06-17

## 1. Новые CLI флаги

### 1.1 `--embed-endpoint`

| Свойство | Значение |
|---|---|
| Тип | `string` |
| Дефолт | `$EMBEDDING_ENDPOINT` |
| Пустое значение | Embed отключён (keepalive no-ops, embed-require пропускается) |
| Наследует | `EMBEDDING_ENDPOINT` (env) — deprecated, остаётся как fallback |

**Семантика:**
- Если задан — используется как базовый URL для Embedding API
- Если пуст — все embed-фичи (keepalive, require, cognify) пропускаются без ошибки
- Не валидируется на старте (только при `--embed-require`)

**Corner cases:**
- `--embed-endpoint=""` + `EMBEDDING_ENDPOINT=http://...` → endpoint пустой (флаг имеет приоритет над env при явной установке, но поскольку дефолт флага = env, явный `""` должен перебить) ⚠ **НЕВОЗМОЖНО отличить явный `""` от дефолта без `flag.Visit()`**
- `--embed-endpoint=http://host/v1/` с trailing slash → зависит от `embed.NewClient` (должен обрабатывать)
- URL без схемы (`host:9101`) → `embed.NewClient` может упасть, не валидируется
- HTTP на продакшене → принимается, предупреждения нет

### 1.2 `--embed-model`

| Свойство | Значение |
|---|---|
| Тип | `string` |
| Дефолт | `$EMBEDDING_MODEL` \|\| `text-embedding-3-small` |
| Пустое значение | Используется fallback `text-embedding-3-small` |

**Семантика:**
- Имя модели, передаваемое в Embedding API
- Не валидируется — ошибка будет при первом вызове embed

**Corner cases:**
- Несуществующая модель → runtime error при первом embed-запросе
- Пустая строка + `$EMBEDDING_MODEL` пуст → дефолт `text-embedding-3-small`

### 1.3 `--embed-keepalive-interval`

| Свойство | Значение |
|---|---|
| Тип | `string` (парсится в `time.Duration`) |
| Дефолт | `10m` |
| `"0"` | Keepalive отключён (тихо) |
| Отрицательное | Keepalive отключён (тихо) |
| Невалидный парсинг | Предупреждение в лог + keepalive отключён |

**Семантика:**
- Периодичность keepalive-пинга embedding endpoint
- Независим от `--embed-endpoint` — если endpoint пуст, keepalive no-ops
- Независим от `--embed-require` — не влияет на startup check

**Corner cases:**
- `"0"` → тихое отключение (без warning)
- `"-1s"` → `ParseDuration("-1s")` возвращает `-1s, nil` — отрицательная длительность → отключено (по условию `keepaliveDur <= 0`)
- `"invalid"` → warning: `"embed-keepalive-interval: invalid or disabled (\"invalid\"), keep-alive off"`
- `"1h30m"` → 90 минут, ок
- `"10ms"` → слишком часто (10ms) — **нет защиты от слишком короткого интервала**
- Очень большие значения (`"720h"` = 30 дней) — допустимо

### 1.4 `--embed-require`

| Свойство | Значение |
|---|---|
| Тип | `bool` |
| Дефолт | `false` |

**Семантика:**
- При `true` + endpoint не пуст → перед стартом делает один `EmbedSingle("startup-check")`
- Успех → лог `"embed-require: embedding endpoint reachable (endpoint/model)"`
- Неуспех → `log.Fatalf` и выход
- При `true` + endpoint пуст → **no-op (молча пропускается)** ⚠ спорное поведение

**Corner cases:**
- Таймаут 10 секунд — **хардкод**, не конфигурируется
- Embed недоступен (connection refused) → fatal exit с кодом 1
- Embed доступен но возвращает ошибку (503, 401) → fatal (любая ошибка `EmbedSingle` считается unreachable) ⚠ **возможно, 401 (auth error) не должно быть фатальным?**
- `--embed-require=true --profile standalone-embed --embed-endpoint=""` → endpoint пуст → require no-ops. Ожидаемо или нет?
- `--embed-require=true --embed-endpoint=http://host --embed-model=nonexistent` → модель не существует, но endpoint отвечает → `EmbedSingle` может вернуть ошибку → fatal

### 1.5 `--pg-url`

| Свойство | Значение |
|---|---|
| Тип | `string` |
| Дефолт | `$DATABASE_URL` |
| Пустое значение | PG не используется (fallback на `$DB_HOST` компоненты или SQLite) |

**Семантика:**
- Полный PostgreSQL DSN: `postgres://user:pass@host:port/db?sslmode=...`
- Имеет приоритет над компонентными env-варами (`DB_HOST`, `DB_USERNAME`, etc.)
- Используется в `initSQLRuntime` → `initPostgresRuntime(pgURL)`

**Corner cases:**
- DSN с паролем в plaintext → **логируется `"PostgreSQL DSN from --pg-url"` без маскирования** ⚠ потенциальная утечка пароля при логировании
- Невалидный DSN → `sql.Open` + `db.Ping` упадут, levara стартует без БД с warning
- `--pg-url` вместе с `$DATABASE_URL` → дефолт флага = env, так что поведение как без флага
- `--profile standalone` + `--pg-url=postgres://...` → **БАГ: профиль перезаписывает `*pgURL = ""` безусловно** (см. §3)

### 1.6 `--profile`

| Свойство | Значение |
|---|---|
| Тип | `string` |
| Дефолт | `""` (= `"full"`) |
| Валидные значения | `standalone`, `standalone-embed`, `full` |

**Семантика — см. §2.**

**Corner cases:**
- Неизвестный профиль → `log.Fatalf`
- `""` → эквивалентен `"full"`
- `--profile` + `--config-check` → профиль применяется ДО config-check (верно)

---

## 2. Профили

### 2.1 Приоритет

Заявлено: «Explicit flags always override profile defaults.»

**Фактическое поведение: БАГ** — профиль перезаписывает `*pgURL`, `*neo4jURL`, `*grpcPort` и др. **безусловно** после `flag.Parse()`. Если пользователь явно передал `--pg-url=...`, значение теряется.

**Требуемое поведение:** профиль выставляет значения только для флагов, которые **не были явно заданы** пользователем.

**Реализация:** использовать `flag.Visit()` после `flag.Parse()`:

```go
explicit := make(map[string]bool)
flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

if !explicit["grpc-port"] { *grpcPort = 0 }
if !explicit["require-auth"] { *requireAuth = false }
// ...
```

Это позволяет `--profile standalone --pg-url=postgres://...` работать — pg-url сохраняет пользовательское значение.

### 2.2 `standalone`

Назначение: WAL-only нода, без внешних сервисов.

| Флаг | Значение профиля | Без профиля (full) |
|---|---|---|
| `grpc-port` | 0 | 50051 |
| `require-auth` | false | false |
| `raft-port` | 0 | 9000 |
| `bootstrap` | false | false |
| `join-addr` | "" | "" |
| `llm-proxy-port` | 0 | 0 |
| `neo4j-url` | "" | "" |
| `pg-url` | "" | `$DATABASE_URL` |

**Что НЕ трогает профиль (и это правильно):**
- `--embed-endpoint`, `--embed-model`, `--embed-keepalive-interval`, `--embed-require`
- `--dim`, `--port`, `--shards`, `--data-dir`, `--node-id`
- `--hnsw-m`, `--hnsw-ef-mult`, `--hnsw-ef-min`
- `--mcp-audit-log`

**Corner case:** `standalone` не отключает embed. Если `EMBEDDING_ENDPOINT` задан в env → embed будет работать даже в standalone-профиле. Это by-design? ⚠ **Нужно решение.**

### 2.3 `standalone-embed`

Назначение: как standalone, но embed явно включён.

**Фактически идентичен `standalone`** — код дублирован. Отличие только в log-сообщении.

**Проблема:** профиль не управляет embed-флагами вообще. `standalone` и `standalone-embed` ведут себя одинаково при одинаковых env-варах. ⚠ **Нужно решение.**

Варианты:
1. `standalone` должен очищать `*embedEndpointF = ""` — embed отключён
2. `standalone-embed` оставляет embed как есть
3. Тогда профили реально отличаются

### 2.4 `full`

Назначение: всё доступно (старое поведение). Дефолт.

Все флаги сохраняют свои значения (профиль пустой → switch попадает в `case "full", ""`).

---

## 3. Известные баги (до исправления)

| # | Баг | Критичность | Где |
|---|---|---|---|
| B1 | Профиль перезаписывает явные флаги (`--pg-url`, `--neo4j-url`) | 🔴 Критический | `main.go:210-234` |
| B2 | `standalone` и `standalone-embed` идентичны | 🟡 Дизайн | `main.go:211-230` |
| B3 | `--embed-require` + endpoint пуст → молча пропускается (должен быть fatal?) | 🟡 Дизайн | `main.go:407` |
| B4 | `--pg-url` DSN пишется в лог без маскирования пароля | 🟠 Medium | `bootstrap.go:296` |
| B5 | `initPostgresRuntime` env-путь: `fmt.Sprintf` с лишним аргументом исправлен, но пароль `***` — DSN нерабочий | 🟠 Medium (pre-existing) | `bootstrap.go:316` |
| B6 | `--embed-keepalive-interval=10ms` — нет защиты от слишком частого пинга | 🟢 Low | `bootstrap.go:454` |

---

## 4. Дизайн тестов

### 4.1 `--embed-keepalive-interval`

```
T1.0  default    → 10m, keepalive стартует с "ping every 10m0s" в логе
T1.1  0          → keepalive отключён, нет лога "ping every", нет ошибок
T1.2  -1s        → отключён, нет warning (keepaliveDur <= 0, *embedKeepalive == "-1s")
T1.3  invalid    → warning: "invalid or disabled", keepalive off
T1.4  30s        → "ping every 30s"
T1.5  1h30m      → "ping every 1h30m0s"
T1.6  empty("")  → ParseDuration("") → err → disabled + warning
T1.7  embed-endpoint empty → любой keepalive → no-op (функция не вызывается)
```

### 4.2 `--embed-require`

```
T2.0  true + endpoint reachable     → "embed-require: reachable" в логе, старт OK
T2.1  true + endpoint unreachable   → fatal: "embed-require: unreachable"
T2.2  true + endpoint empty         → no-op, старт OK
T2.3  false                         → no-op
T2.4  true + endpoint reachable + 401 → fatal (любая ошибка EmbedSingle)
T2.5  true + endpoint reachable + медленный (>10s) → таймаут → fatal
```

### 4.3 `--pg-url`

```
T3.0  valid DSN                    → PG connected, "PostgreSQL DSN from --pg-url"
T3.1  empty                        → fallback на $DB_HOST или SQLite
T3.2  invalid DSN                  → warning, server стартует без PG
T3.3  $DATABASE_URL set + --pg-url → --pg-url дефолт = env → эквивалентно без флага
T3.4  --profile standalone + --pg-url=... → БАГ B1: pg-url перезаписан в ""
       (после исправления)              → pg-url сохраняет пользовательское значение
```

### 4.4 `--profile`

```
T4.0  standalone                   → grpc=0, auth=false, raft=0, llm=0, neo4j="", pg=""
T4.1  standalone-embed             → то же + лог "standalone-embed"
T4.2  full / default               → все флаги по дефолтам
T4.3  unknown                      → fatal: "Unknown profile"
T4.4  standalone + --grpc-port=999 → БАГ B1: grpc-port перезаписан в 0
       (после исправления)              → grpc-port = 999 (явный флаг сохранён)
T4.5  standalone-embed + $EMBEDDING_ENDPOINT → embed работает (не трогается профилем)
T4.6  --config-check + --profile standalone → профиль применяется до config-check
```

### 4.5 Интеграционные

```
T5.0  Все флаги в --help
T5.1  Без аргументов → дефолтное поведение (= old behaviour, backward compat)
T5.2  go test ./cmd/server/... → все существующие тесты проходят
T5.3  launchctl load/unload → сервис стартует/останавливается без ошибок
T5.4  /health endpoint → {"status":"ready"} после старта
```

---

## 5. Затронутые компоненты

| Файл | Изменения |
|---|---|
| `cmd/server/main.go` | +6 флагов, +профили, +embed-require, +firstNonEmpty |
| `cmd/server/bootstrap.go` | startEmbedKeepAlive → параметр interval; initSQLRuntime + initPostgresRuntime → pgURL |

---

## 6. Следующие шаги

1. **Согласовать дизайн:** standalone vs standalone-embed семантику (B2), поведение embed-require с пустым endpoint (B3)
2. **Исправить B1:** `flag.Visit()` для защиты явных флагов от перезаписи профилем
3. **Исправить B2:** `standalone` очищает embed-флаги, `standalone-embed` — нет
4. **Написать тесты:** §4
5. **Коммит**

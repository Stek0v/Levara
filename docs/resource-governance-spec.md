# Resource Governance Specification

> **Принцип: качество данных всегда первично.** Любой лимит, бюджет или
> оптимизация не должны ухудшать полноту, точность или доступность данных.

## Содержание
1. [Проблема](#проблема)
2. [Архитектура](#архитектура)
3. [Компоненты и DoD/DoR](#компоненты)
4. [Тесты и corner cases](#тесты)
5. [CLI/WebUI шпаргалки](#шпаргалки)
6. [Рекомендации по настройке](#рекомендации)

---

## Проблема

Levara работает на персональных машинах (Mac 16 ГБ, Pi 4 ГБ) и
обслуживает корпус 500+ сессий / 450k+ векторов / 235k+ сообщений.
Пять фоновых подсистем работают независимо и не знают о ресурсах друг
друга. Без управления это приводит к:

- **Memory exhaustion**: BM25 снапшот 393k чанков = 3 ГБ аллокаций за тик
- **Search degradation**: корпусная нагрузка глушит поисковые запросы
- **Quality loss**: oversized-доки скипаются вместо сегментации
- **Zero visibility**: пользователь не знает что происходит с его машиной

---

## Архитектура

```
┌─────────────────────────────────────────────────────────┐
│                  Resource Governor                       │
│  знает: host RAM, GOMEMLIMIT, RSS, disk, CPU load       │
│  решает: pause/resume jobs при давлении                  │
│  экспорт: /api/v1/status + CLI + WebUI                   │
├──────────────┬──────────────┬──────────────┬────────────┤
│  Job Scheduler (A2)  │  PriorityGate (P4) │  Admission (A4)  │
│  кто работает когда  │  кто ждёт в очереди│  что индексировать│
├──────────────┴──────────────┴──────────────┴────────────┤
│  Jobs: source-ingest > cognify > rag-janitor > distill   │
├─────────────────────────────────────────────────────────┤
│  Storage: raw layer (PG) → RAG segments (files) →        │
│           vectors (HNSW) → BM25 (optional per tier)     │
└─────────────────────────────────────────────────────────┘
```

---

## Компоненты

### A1: Status Dashboard ✅ (PR #137)

**DoR:** REST endpoint, MCP tool, CLI command — все читают один источник.
**DoD:**
- `GET /api/v1/status` возвращает memory, corpus, jobs, warnings
- `levara status [--watch]` — форматированный вывод в терминале
- MCP `levara_status` — те же данные для агентов
- Warnings при: memory critical, cognify pending > 500, BM25 skip

**Corner cases:**
- Сервер только стартует (WAL replay) → status должен отвечать 503 или partial
- Пустая БД (нет corpus, нет jobs) → корректный empty state
- GOMEMLIMIT не задан → pressure = "unknown" (не "low")

---

### A2: Job Scheduler ✅ (PR #139, доработка)

**DoR:** Один интерфейс координации; каждая задача имеет приоритет и Busy().
**DoD:**
- `pkg/governor.Scheduler`: Register(job, priority), CanRun(job)
- Distill не стартует пока cognify Busy()
- RAG-janitor не стартует пока cognify Busy()
- Source-ingest всегда может работать (лёгкий, highest non-query priority)
- Query не блокируется НИКОГДА (priority 0, проверяется отдельно)

**Corner cases:**
- Все задачи Busy одновременно → scheduler не дедлокит (round-robin на интервале)
- Busy() паникует → scheduler восстанавливается (recover в CanRun)
- Контекст отменён → все задачи останавливаются чисто

**Тесты:**
```go
// TestSchedulerDistillWaitsForCognify: distill не работает пока cognify busy
// TestSchedulerQueryAlwaysWins: query priority блокирует всех
// TestSchedulerStatus: корректный снапшот приоритетов
// TestSchedulerAllBusy: нет deadlock при всех busy
// TestSchedulerBusyPanic: recover от паники в Busy()
// TestSchedulerContextCancel: чистое завершение
```

---

### A3: Resource Governor (ядро управления)

**DoR:** Единая точка правды о ресурсах; все даймоны регистрируются.
**DoD:**
- `pkg/governor.Governor`: знает реальный RSS процесса (`governor.ProcessRSS`:
  Linux → `/proc/self/statm`, macOS → `/bin/ps`, иначе fallback на
  `MemStats.Sys` как over-approximation), budget (GOMEMLIMIT)
- `ShouldPause(jobName) bool`: true при RSS > 80% budget
- `ShouldResume(jobName) bool`: true при RSS < 60% budget (hysteresis)
- Все 4 даймона вызывают ShouldPause перед каждой единицей работы
- Экспорт давления в /api/v1/status

**Corner cases:**
- GOMEMLIMIT не задан → auto-detect: totalRAM * 0.5 (консервативно)
- RSS колеблется у границы → hysteresis (pause > 80%, resume < 60%)
- RSS берётся у ОС, а не из `MemStats.Sys`: Sys считает и зарезервированные
  арены (вектора, BM25) — на проде показывал 14.9 GB при реальных 4.2 GB RSS,
  из-за чего губернатор держал фоновые задачи на постоянной паузе
- Governor сам не может быть источником утечки (лёгкий, только counters)

**Тесты:**
```go
// TestGovernorPauseAtThreshold: pause при RSS > 80% budget
// TestGovernorResumeAtThreshold: resume при RSS < 60% budget
// TestGovernorHysteresis: не осциллирует между pause/resume
// TestGovernorAutoDetect: без GOMEMLIMIT использует разумный default
// TestGovernorNoBudget: budget=0 → никогда не pause (quality first)
```

---

### A4: Tiered Indexing + Admission Control

**DoR:** На входе cognify — оценка объёма; решение о тире ДО индексации.
**DoD:**
- `AdmissionController.Estimate(docSize) → Tier`:
  - < 200KB → full (vectors + BM25 + graph)
  - 200KB-1MB → vectors + BM25 (streaming persistence)
  - > 1MB → segmented (split into 200KB pieces) + vectors
- Сегментация: по границам сообщений, метаданные на каждой части
- BM25 snapshot: только для < 100k docs (уже реализовано PR #136)
- Graph extraction: только для < 10k chunks

**Corner cases:**
- Документ ровно на границе тира → выбираем БОЛЕЕ полный тир (quality first)
- Сегмент не помещается целиком → не рвём сообщение, переносим в следующий
- Все сегменты < 200KB кроме последнего (5 байт) → не создаём пустой сегмент
- Документ из только system-сообщений → пропускаем (нет полезного контента)
- Метаданные conversation повторяются на каждом сегменте (standalone search)

**Тесты:**
```go
// TestAdmissionSmallDoc: <200KB → full tier
// TestAdmissionMediumDoc: 200KB-1MB → vectors+BM25
// TestAdmissionLargeDoc: >1MB → segmented+vector
// TestAdmissionBoundary: ровно на границе → более полный тир
// TestSegmentationNoMessageSplit: сообщения не рвутся посередине
// TestSegmentationLastSegmentNonEmpty: нет пустых сегментов
// TestSegmentationSystemOnly: только system → пустой результат
// TestSegmentationMetadataRepeated: header на каждом сегменте
// TestSegmentationSearchable: каждый сегмент ищется автономно
```

---

### A5: Streaming Audit

**DoR:** Ни одна операция не материализует >10k объектов в память целиком.
**DoD:**
- Аудит кода: найти все `Documents()` + `json.Marshal` паттерны
- BM25: checkpoint раз в час + append-log между (не full rewrite)
- Любая сериализация > 10MB → streaming (bufio.Writer, не []byte)

**Corner cases:**
- Append-log растёт бесконечно → компакция при старте (replay + truncate)
- Снапшот в момент append → атомарный rename (уже реализовано)
- Восстановление после креша посреди append → последняя строка может быть
  неполной → skip при replay (уже реализовано в BM25)

---

### A6: Profile Contracts + CLI Cheat Sheet

**DoR:** Пользователь понимает: какой параметр, на что влияет, как настроить.
**DoD:**
- `docs/resource-contracts.md`: таблица профилей
- `levara help tuning`: CLI шпаргалка по всем env vars
- WebUI settings: tooltip с объяснением каждого параметра
- Каждый параметр документирован: что делает, default, range, trade-offs

---

## Тесты

### Quality Guarantees (главные)

```go
// QUALITY-1: No data loss — every message in raw layer appears in ≥1 RAG segment
// QUALITY-2: Search completeness — query hits content from every session
// QUALITY-3: Recall fidelity — top results match expected conversations
// QUALITY-4: Distill integrity — memories contain provenance and real content
// QUALITY-5: Incremental correctness — re-render produces same content + new messages
```

### Performance Guarantees

```go
// PERF-1: Query p95 < 500ms under full background load
// PERF-2: Memory RSS < GOMEMLIMIT under sustained indexing
// PERF-3: BM25 snapshot doesn't allocate > 100MB for any collection
// PERF-4: Server startup < 60s for 500k vectors (WAL replay)
// PERF-5: RAG janitor processes 10 sessions in < 5 minutes
```

### Resource Governance

```go
// GOV-1: Governor pauses all jobs at 80% memory budget
// GOV-2: Governor resumes jobs at 60% memory budget
// GOV-3: Query latency unaffected by background jobs (scheduler)
// GOV-4: CLI status reflects real-time governor state
// GOV-5: No deadlock when all jobs busy simultaneously
```

---

## Шпаргалки

### CLI: `levara help tuning`

```
РЕСУРСЫ И ПРОИЗВОДИТЕЛЬНОСТЬ LEVARA

ПАМЯТЬ
  GOMEMLIMIT=8GiB         Максимум heap Go. Default: auto (50% RAM).
                           ↓ меньше = GC агрессивнее, больше пауз
                           ↑ больше = меньше GC, риск OOM на хосте

  LEVARA_BM25_SNAPSHOT_MAX_DOCS=100000
                           Коллекции больше этого пропускают BM25 снапшот.
                           BM25 нужен для lexical search. Если выключен —
                           поиск только по векторам (semantic).
                           Рекомендация: 100k для 16GB, off для Pi.

ИНДЕКСАЦИЯ
  LEVARA_EMBED_BG_CONCURRENCY=2
                           Фоновых embedding-запросов одновременно.
                           ↓ меньше = медленнее индексация, быстрее поиск
                           ↑ больше = быстрее индексация, деградация поиска
                           Рекомендация: 2 для 1-worker embedder, 4 для multi.

  LEVARA_EMBED_GATE_CAPACITY=4
                           Общий потолок embedding-запросов (вкл. поисковые).
                           Должен быть > BG_CONCURRENCY, иначе фон забивает шлюз.

ИНТЕЛЛЕКТ
  LEVARA_DISTILL_BUDGET=2  Сессий дистиллируется за тик (5 мин).
                           ↓ меньше = медленнее накопление памяти
                           ↑ больше = больше LLM нагрузки
                           Рекомендация: 2 (nightly), 5 (workstation).

  LEVARA_DISTILL_HALLS=decision
                           Какие типы знаний извлекать: decision,discovery,advice

ИСТОЧНИКИ
  LEVARA_CHAT_SOURCES=codex,claude-code,cursor
                           Какие транскрипты автоматически импортировать.
                           Пусто = выключено (privacy default).

  LEVARA_CHAT_SOURCES_INTERVAL=5m
                           Как часто сканировать источники.

ПОЛНЫЙ СПИСОК: docs/resource-contracts.md
```

### Рекомендации по соотношению

```
┌─────────────┬──────────┬──────────┬──────────┬──────────┐
│ Параметр    │ Mac 16GB │ Mac 32GB │ Pi 4GB   │ Server   │
├─────────────┼──────────┼──────────┼──────────┼──────────┤
│ GOMEMLIMIT  │ 8GiB     │ 16GiB    │ 2GiB     │ 32GiB+   │
│ BM25_MAX    │ 100k     │ 500k     │ off      │ 1M+      │
│ EMBED_BG    │ 2        │ 4        │ 1        │ 8+       │
│ DISTILL/тик │ 2        │ 5        │ 1        │ 10+      │
│ CORPUS_MAX  │ 500k     │ 2M       │ 100k     │ 10M+     │
│ Тир         │ vectors  │ full     │ raw-only │ full     │
└─────────────┴──────────┴──────────┴──────────┴──────────┘

ПРАВИЛО: при нехватке памяти — сначала отключайте BM25, потом graph,
потом снижайте EMBED_BG. НИКОГДА не отключайте vectors — это основа
поиска. НИКОГДА не скипайте документы — сегментируйте.
```

---

## Реализация (порядок)

| # | Компонент | Статус | Приоритет |
|---|---|---|---|
| 1 | A1 Status Dashboard | ✅ PR #137 | — |
| 2 | A2 Job Scheduler | ✅ PR #139 | — |
| 3 | A3 Resource Governor | 🔄 | High |
| 4 | A4 Tiered Indexing + Segments | ✅ PR #138 | — |
| 5 | A5 Streaming Audit | 📋 | Medium |
| 6 | A6 Profiles + Cheat Sheet | 📋 | Medium |
| 7 | Quality test suite | 📋 | High |
| 8 | Performance test suite | 📋 | Medium |

# Final Test Plan — валидация Levara перед внешним сравнением

**Дата:** 2026-05-15
**Контекст:** `agent-memory-comparison.md` содержит 30+ ✅ для Levara, большая часть которых опирается на внутренние замеры на одной машине без реалистичной нагрузки. Red-team review (см. чат от 2026-05-15) выделил конкретные слабые места в этих заявках. Этот документ превращает каждое такое место в проверяемую задачу с гипотезой, методом и критерием успеха.

**Definition of done для всего плана:** мы можем показать таблицу сравнения наружу (потенциальному пользователю / в README / в техблоге) без чувства, что что-то нарисовано. Каждая ✅ должна либо подтвердиться воспроизводимым артефактом (бенч-репорт, eval-репорт, postmortem chaos-теста), либо смягчиться до «частично» / «при условии X».

**Легенда приоритетов:**
- **P0** — блокирует утверждение «production-ready»; без этого таблицу нельзя показывать.
- **P1** — существенно влияет на честное позиционирование; нужно до публичного релиза.
- **P2** — для полноты; можно отложить на v2 документа.

**Легенда effort:** грубая оценка в днях для одного инженера full-time.

---

## §1. Производительность под реалистичной нагрузкой

Цель секции: заменить «2.6 ms p50 на голом HNSW» на матрицу latency × pipeline-config × dataset-size, измеренную на стенде, который можно повторить.

### T1.1 — Realistic search latency matrix
**Приоритет:** P0 — **Effort:** 3 дня
**Гипотеза/риск:** Заявленные 2.6 ms p50 / 719 QPS — это raw HNSW lookup без rerank/hybrid/filter. С полным пайплайном агентского recall цифры могут вырасти на порядок и таблица перестанет давать преимущество перед Qdrant.

**Метод:**
1. Подготовить три датасета: 10k, 100k, 1M chunks из BEIR (msmarco) или synthetic.
2. Запросный набор: 200 реальных query шаблонов (semantic, fact, temporal).
3. Прогнать 6 конфигов: `{raw HNSW}`, `{+ BM25 RRF}`, `{+ rerank default-on}`, `{+ room filter}`, `{+ all}`, `{+ all + cognify-extracted KG walk}`.
4. Для каждого: warmup 1k запросов, замер 10k запросов, при QPS = 10, 50, 100, 500.
5. Записать p50, p95, p99 latency и actual QPS sustain.

**Success criteria:**
- Артефакт: `bench/realistic_latency.csv` + `bench/realistic_latency.md` отчёт.
- В таблице сравнения цифру «2.6 ms» заменить на диапазон с явным указанием конфига.
- p50 < 50 ms на конфиге `{+ all}` при QPS=100 — иначе нужно либо помечать как «slow», либо чинить.

### T1.2 — Concurrent QPS под полным пайплайном
**Приоритет:** P0 — **Effort:** 2 дня
**Гипотеза/риск:** 719 QPS — это HNSW concurrent reads. С rerank cross-encoder cap = (1 / rerank_latency) × N_workers. Реалистичный потолок может быть 50–100 QPS, а не 719.

**Метод:**
1. Locust / k6 harness с auth (JWT), bursts 1×, 5×, 10× от ожидаемого RPS.
2. Снимать `levara_http_request_duration_seconds`, `levara_rerank_outcome_total`, CPU/RAM Levara и rerank backend.
3. Измерить деградацию: при каком QPS p99 пробивает 1 s, при каком 5xx начинает расти.

**Success criteria:**
- Sustained QPS при p99 < 500 ms задокументирован для конфига `{+ all}`.
- Понятный график: «Levara держит N QPS при том-то конфиге», публикуемый.

### T1.3 — End-to-end `add()` throughput с cognify
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** «1616 inserts/sec на 8 writers» — это write в WAL+arena без cognify. Реальный agent write-path: agent → mem0 → MemoryFS → cognify (DeepSeek) → embed → graph. End-to-end latency на add() — секунды, не миллисекунды.

**Метод:**
1. Скрипт: 1000 `add()` calls через mem0 SDK на одного пользователя.
2. Замер p50/p95 end-to-end latency и общего throughput.
3. Разделить: время в каждом hop'е (mem0 API, MemoryFS commit, cognify extraction, embed, Levara insert, graph upsert).

**Success criteria:**
- Артефакт: stacked bar chart «где время в add()».
- В сравнении заменить «1616 ips» на «N add()/sec end-to-end» — это релевантная агентам цифра.
- Если bottleneck в DeepSeek call — задокументировать, дать batched/async вариант или fallback.

---

## §2. Crash recovery / chaos testing

Цель: подтвердить «WAL 100%» под реалистичными failure modes, а не только `kill levara && start levara`.

### T2.1 — Chaos harness для Levara WAL
**Приоритет:** P0 — **Effort:** 4 дня
**Гипотеза/риск:** «Crash recovery 100%» не тестировалось с in-flight cognify, in-flight gRPC stream, disk-full, corrupt WAL pages, partial fsync.

**Метод:**
1. Toxiproxy / pumba для chaos на сетевом и process уровне.
2. Сценарии:
   - `kill -9` в момент batch insert (1, 10, 100, 1000 inflight).
   - `kill -9` посреди cognify-run'а (фоновая задача).
   - `kill -9` посреди WAL fsync group-commit.
   - Disk-full на WAL volume.
   - Corrupt: zero out последний WAL page, flip random byte.
   - Power loss simulation: SIGKILL + drop page cache + restart.
3. После каждого: WAL replay, проверка инвариантов (HNSW connectivity, BM25 counts, KG edge consistency).
4. Опубликовать `crash_recovery_report.md` с матрицей сценарий × outcome.

**Success criteria:**
- 100% data loss = 0 на N=50 запусков каждого сценария.
- Если выявлены классы ошибок — фикс или явное «known limitation» в docs.

### T2.2 — Сквозная consistency через 6 сервисов
**Приоритет:** P0 — **Effort:** 3 дня
**Гипотеза/риск:** При краше любого из {Levara, MemoryFS, mem0, Postgres, Neo4j, Ollama} стек оказывается в несогласованном состоянии: .md committed в MemoryFS, но не проиндексирован в Levara; entity в Levara KG, но связанный chunk потерян; mem0 metadata в Postgres расходится с реальным Levara.

**Метод:**
1. Скрипт: 10k `add()` через mem0; в случайные моменты убивать любой из сервисов.
2. После каждого убийства — automatic restart и инвариант-чекер:
   - Каждый .md файл в MemoryFS имеет запись в Levara.
   - Каждая запись Levara имеет соответствующий .md.
   - Postgres `memories` row count == Levara doc count для каждого пользователя.
   - KG edges указывают на существующие entities.

**Success criteria:**
- Документ `consistency_report.md` со списком обнаруженных разрывов и фиксов.
- Eventual consistency lag измерен: «после краха MemoryFS reindexer догоняет Levara за < N секунд».

---

## §3. Search quality A/B (а не только latency)

«Hybrid BM25+HNSW» и «rerank default-on» отмечены ✅, но без доказательства, что они **улучшают** recall на агентских запросах.

### T3.1 — Hybrid vs pure vector на agent-memory ground truth
**Приоритет:** P0 — **Effort:** 4 дня
**Гипотеза/риск:** RRF может ухудшать качество на коротких semantic-запросах, где BM25 шумит. Без A/B мы не знаем, помогает ли hybrid вообще.

**Метод:**
1. Собрать eval-датасет: 500 query → expected chunk_id пар, размеченных вручную или из реальных Claude сессий. Не из BEIR — нужен agent-memory distribution.
2. Прогнать 4 режима: pure vector, pure BM25, hybrid RRF, hybrid + rerank.
3. Метрики: NDCG@10, Recall@5, MRR.

**Success criteria:**
- Артефакт: `bench/search_quality.md` с таблицей метрик.
- Hybrid должен бить pure vector минимум на 5% NDCG@10 — иначе hybrid выключаем по умолчанию.
- Если rerank даёт +X% но стоит +Y ms — публикуем trade-off curve.

### T3.2 — Smart routing accuracy
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** Smart routing (`pkg/router`) разбивает запросы на fact / temporal / semantic. Это эвристика; её точность не измерена. Mis-routing запроса = выбор не той стратегии = худший recall.

**Метод:**
1. 300 запросов вручную размечены классом (fact / temporal / semantic / mixed).
2. Прогнать через router, посчитать precision/recall/F1 для каждого класса.
3. Confusion matrix.

**Success criteria:**
- F1 >= 0.8 на каждом классе.
- Если F1 < 0.6 для какого-то класса — либо чинить router (LLM-based?), либо отключить smart routing и оставить hybrid as default.

### T3.3 — Rerank cost-benefit при разных score gaps
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** Phase 2.5 ввела `RERANK_SCORE_GAP_THRESHOLD` для adaptive skip — само существование этого флага означает, что «default-on» имеет существенный latency cost. Нужно найти sweet spot.

**Метод:**
1. На eval-датасете из T3.1 прогнать с порогом = {0, 0.1, 0.2, 0.3, 0.5}.
2. Метрики: NDCG@10 vs avg latency.
3. Доля запросов, на которых rerank скипнут.

**Success criteria:**
- Кривая trade-off published.
- Рекомендуемое значение `RERANK_SCORE_GAP_THRESHOLD` для default config.

---

## §4. Memory semantics (главный «moat»)

Hall/room/pin/wake_up/diaries — это **конвенции** поверх payload. Нужно подтвердить, что они дают реальный uplift, а не только удобную ментальную модель.

### T4.1 — Recall quality с пустыми/неправильными hall
**Приоритет:** P1 — **Effort:** 2 дня
**Гипотеза/риск:** В CLAUDE.md сказано «пустые поля резко снижают полезность recall». Значит система чувствительна к дисциплине заполнения. Нужно измерить: насколько резко?

**Метод:**
1. Тот же eval-датасет: 500 query → chunk_id, но теперь варьируем hall заполнение в записях.
2. Конфиги: 100% hall заполнен, 50%, 0%, 50% заполнен неверно.
3. Замер NDCG@10 / Recall@5 для recall с hall-filter и без.

**Success criteria:**
- График деградации recall vs % missing hall.
- Если 50% missing → recall падает на >30% — нужно либо auto-classification hall через LLM, либо явный warning в docs «без hall — на 30% хуже».

### T4.2 — Pin / wake_up под нагрузкой
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** `wake_up(max_tokens=300)` режет по бюджету. Если запинено больше, чем влезает — какие выпадают? Стабильно ли поведение?

**Метод:**
1. Pin 100 memories с разными priority (10, 8, 5, 3, 1).
2. Вызвать `wake_up` 1000 раз, посмотреть, какие реально возвращаются.
3. Проверить инвариант: priority=10 всегда возвращается до того, как режется по бюджету.

**Success criteria:**
- Алгоритм отбора задокументирован (greedy по priority? round-robin? recency?).
- Если есть случаи, когда priority=10 выпадает — это баг.
- Recommendation в docs: «pin не более N items с priority=10, иначе non-deterministic».

### T4.3 — Per-agent diaries: что реально изолировано
**Приоритет:** P2 — **Effort:** 0.5 дня
**Гипотеза/риск:** Diaries — это namespace через `owner_id="agent:X"`. Может ли cross-namespace утечка случиться через graph walk, через `cross_search`, через hybrid BM25?

**Метод:**
1. Diary записи для агентов A, B, C.
2. От имени A: вызвать все 25 MCP tools и проверить, что результаты ни в каком ответе не содержат записи B/C.
3. Прогнать `cross_search` и убедиться, что diary записи не попадают в результат, если они не указаны явно.

**Success criteria:**
- Audit report: «diary isolation подтверждена для всех 25 tools» или список утечек.

---

## §5. Temporal KG validation

«Temporal KG ✅» — но supersede работает только на whitelist из 10 relations, а entity resolution не валидирована.

### T5.1 — Temporal coverage audit
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** Cognify извлекает много типов relations, но автосупершед только на whitelist. Всё остальное — duplicates без temporal semantics.

**Метод:**
1. Прогнать cognify на 200 chunks из реальных текстов (новости, чат-логи, википедия).
2. Посчитать, какая доля извлечённых edges попадает в whitelist, какая нет.
3. Для non-whitelist — посчитать, сколько из них **по смыслу** должны были бы быть temporal.

**Success criteria:**
- Артефакт: «temporal coverage = X%».
- Если < 50% — расширить whitelist или признать честно «temporal KG = 10 relations only».

### T5.2 — Entity resolution F1
**Приоритет:** P1 — **Effort:** 2 дня
**Гипотеза/риск:** «Alex» и «Alexey» → разные узлы → supersede не сработает → граф «протекает» с дубликатами.

**Метод:**
1. Eval-датасет: 500 пар (mention_a, mention_b, same_entity: yes/no).
2. Cognify на текстах, посмотреть, сколько раз создались разные узлы для одной сущности.
3. Метрики: F1 на entity merge / non-merge.

**Success criteria:**
- F1 >= 0.85 — иначе нужно добавлять coreference / fuzzy matching.
- Если F1 < 0.7 — temporal-KG advantage в таблице понижаем до «частично».

---

## §6. Cognify extraction quality

Cognify — главный технический moat, и при этом единственная фича, опирающаяся на качество внешнего LLM (DeepSeek V3.2).

### T6.1 — Cognify extraction precision/recall
**Приоритет:** P0 — **Effort:** 5 дней
**Гипотеза/риск:** Качество extraction нигде не измерено. На out-of-distribution доменах (юридика, медицина, код) может быть катастрофически плохим.

**Метод:**
1. Эталон: 200 chunks из 4 доменов (general, legal, medical, code), вручную размеченные на entities/relations.
2. Прогнать cognify, сравнить.
3. Метрики: entity precision/recall, relation precision/recall, F1 по типу relation.

**Success criteria:**
- Артефакт: `bench/cognify_quality.md` с таблицей метрик по доменам.
- Если F1 < 0.5 на каком-то домене — отметить как «not recommended for this domain».
- Если F1 < 0.3 — рассматривать как deal-breaker для KG-features таблицы.

### T6.2 — Cognify failure modes (DeepSeek недоступен / медленный / возвращает мусор)
**Приоритет:** P1 — **Effort:** 1 день
**Гипотеза/риск:** Зависимость от внешнего LLM = SPOF. Что происходит, когда DeepSeek даун?

**Метод:**
1. Toxiproxy блокирует api.deepseek.com.
2. Запустить 100 `add()` с включённым cognify.
3. Поведение: ошибки? retries? .md commit-ятся, а graph остаётся пустым? user-facing error message?

**Success criteria:**
- Поведение задокументировано и предсказуемо.
- Есть retry с экспоненциальным backoff.
- Опционально: fallback на локальный gemma3:4b для extraction (медленнее но не падает).

---

## §7. Operational debuggability

«Prometheus метрики ✅» — но 6 сервисов без distributed tracing превращают любой production-incident в «гадание по логам».

### T7.1 — End-to-end distributed tracing audit
**Приоритет:** P1 — **Effort:** 3 дня
**Гипотеза/риск:** Когда у пользователя `add()` зависает на 30 секунд — где? mem0 → MemoryFS → cognify → embed → graph. Без trace ID через все hops debug = боль.

**Метод:**
1. Инструментировать OpenTelemetry trace ID propagation через все 6 сервисов.
2. Smoke test: один `add()` → один trace span с всеми 5 child spans.
3. Тоже для `search` через mem0 → Levara.

**Success criteria:**
- Один trace на один user-facing request, видный в Jaeger/Tempo.
- Документация: «как дебажить slow add()» с примером trace.

### T7.2 — Correlation IDs в логах
**Приоритет:** P2 — **Effort:** 1 день
**Метод:** Все 6 сервисов логируют `request_id` в каждой строке.
**Success criteria:** `grep <request_id> logs/*.log` показывает весь путь запроса.

---

## §8. Backup / restore consistency

«✅ levara-backup CLI» покрывает только Levara. Полный snapshot стека из 6 сервисов нужно проверить отдельно.

### T8.1 — Consistent snapshot across 6 services
**Приоритет:** P0 — **Effort:** 3 дня
**Гипотеза/риск:** Backup только Levara → restore даёт несогласованность с Postgres (mem0 metadata), Neo4j (graph), MemoryFS (.md). Реальный recovery после disaster выдаст «mixed state».

**Метод:**
1. Snapshot test: пишем 1000 memories с cognify включённым.
2. Снимаем snapshot всех 6 хранилищ (Levara WAL, Postgres dump, Neo4j dump, MemoryFS .md tarball).
3. Восстанавливаем на чистом стенде.
4. Проверяем все инварианты consistency как в T2.2.

**Success criteria:**
- Артефакт: `backup_runbook.md` с пошаговой инструкцией consistent snapshot.
- Если current `levara-backup` недостаточен — расширить CLI до `levaraos-backup` (включая остальные 5 хранилищ).
- Опубликовать RPO/RTO numbers.

### T8.2 — Point-in-time recovery
**Приоритет:** P2 — **Effort:** 2 дня
**Метод:** Можно ли откатиться к состоянию «вчера в 14:00»? Что для этого нужно?
**Success criteria:** Либо ✅ + runbook, либо честное «PITR не поддерживается» в docs.

---

## §9. Security defaults audit

«JWT ✅» — но `JWT_SECRET` auto-generates в dev. Production deployment требует ручной настройки, которую легко пропустить.

### T9.1 — Secure-by-default audit
**Приоритет:** P0 — **Effort:** 2 дня
**Гипотеза/риск:** Дефолтный `docker compose up` ставит небезопасную конфигурацию (auto-gen JWT, no TLS, default Postgres password, открытые порты).

**Метод:**
1. Свежий clone репо → `docker compose up -d` → security scan.
2. Чек-лист:
   - `JWT_SECRET` стабильный между рестартами?
   - Postgres password дефолтный?
   - Все API endpoints требуют auth?
   - TLS между сервисами?
   - CORS — wildcard?
3. nmap на хост: сколько портов наружу?

**Success criteria:**
- Документ `security_defaults_report.md` со списком issues.
- P0 issues — фикс перед публикацией comparison.
- Дефолтный `docker-compose.prod.yml` с secure config.

### T9.2 — Cross-tenant data leakage
**Приоритет:** P1 — **Effort:** 1 день
**Метод:**
1. Создать users A и B, каждый пишет 100 memories.
2. От имени A прогнать все 25 MCP tools, проверить, что ни в одном ответе нет данных B.
3. Включая edge cases: cross_search, sync, graph queries.

**Success criteria:** No cross-tenant leak across all tools.

### T9.3 — Auth bypass / token forgery
**Приоритет:** P1 — **Effort:** 1 день
**Метод:** Можно ли подделать JWT? Что, если `JWT_SECRET` слабый? Reusable replay attack?
**Success criteria:** Стандартный JWT-security чек-лист пройден.

---

## §10. End-to-end TCO measurement

«$70 + LLM API» — оценка без учёта DeepSeek calls на cognify, GPU для embed-server, managed Postgres/Neo4j.

### T10.1 — Реальный TCO на small-scale (1k memories/день, 100k recall/день)
**Приоритет:** P1 — **Effort:** 2 дня
**Метод:**
1. Развернуть production-grade на AWS / Hetzner.
2. Прогнать симуляцию агентской нагрузки 30 дней.
3. Снять счета: compute, storage, network, DeepSeek API, OpenAI embeddings, managed DB (если используется).

**Success criteria:**
- Артефакт: реальная TCO-разбивка, опубликована в comparison.
- Сравнение vs Qdrant Cloud / Mem0 Cloud honest.

### T10.2 — Scaling cost к 10M memories
**Приоритет:** P2 — **Effort:** 1 день
**Метод:** Расчёт + extrapolation. Что узким горлом станет первым (RAM HNSW, Postgres rows, Neo4j edges)?
**Success criteria:** Recommendations: «до 1M ok без quantization; от 10M нужен X».

---

## §11. Bus factor / community

Самый честный риск, который mem0/Qdrant закрывают community, а Levara — нет.

### T11.1 — Bus factor mitigation plan
**Приоритет:** P1 — **Effort:** 5+ дней (постепенно)
**Метод:**
1. Документация: ARCHITECTURE.md с актуальной картой стека (не только CLAUDE.md, который TLDR для агентов).
2. CONTRIBUTING.md с runbook'ами для типичных задач.
3. ADRs (architecture decision records) для последних 10 ключевых решений.
4. Onboarding script: «новый dev может deploy + написать 1 endpoint за день».
5. Открыть репо или хотя бы invite 1–2 trusted reviewer на code review.

**Success criteria:**
- Внешний инженер за 1 день поднимает стек и делает meaningful PR.
- В Comparison честно: «bus factor = 1 → 3 после plan».

### T11.2 — Public benchmark репозиторий
**Приоритет:** P2 — **Effort:** 3 дня
**Метод:** Все бенчи из §1–§6 — в отдельный публичный репо с CI, чтобы любой мог воспроизвести.
**Success criteria:** Третье лицо запускает `make bench` и видит ту же цифру ± 10%.

---

## §12. Сводка задач и приоритизация

### P0 (блокеры публичного релиза comparison):
1. T1.1 Realistic search latency matrix
2. T1.2 Concurrent QPS с полным пайплайном
3. T2.1 Chaos harness для WAL
4. T2.2 Cross-service consistency
5. T3.1 Hybrid vs pure vector A/B
6. T6.1 Cognify extraction quality
7. T8.1 Consistent backup/restore
8. T9.1 Secure-by-default audit

**Суммарно P0:** ~26 дней инженерного времени. Реалистично — 5–7 недель календарно с буфером.

### P1 (для честного позиционирования):
T1.3, T3.2, T3.3, T4.1, T4.2, T5.1, T5.2, T6.2, T7.1, T9.2, T9.3, T10.1, T11.1
**Суммарно P1:** ~21 день. Можно параллельно с финальными P0.

### P2 (опционально, для v2 документа):
T4.3, T7.2, T8.2, T10.2, T11.2
**Суммарно P2:** ~7 дней.

---

## §13. Артефакты и публикация

После завершения P0+P1 в `docs/reviews/` должны быть:
- `bench/realistic_latency.md` + CSV
- `bench/search_quality.md`
- `bench/cognify_quality.md`
- `crash_recovery_report.md`
- `consistency_report.md`
- `security_defaults_report.md`
- `backup_runbook.md`
- Обновлённый `agent-memory-comparison.md` со ссылками на эти артефакты вместо голословных ✅.

После этого таблицу можно показывать наружу с чистой совестью.

---

## §14. Что мы НЕ тестируем (и почему)

Чтобы не растягивать план до бесконечности:
- **Multi-modal embeddings** — фича не заявлена, и не нужна для агентской памяти.
- **Distributed mode** — отсутствует в Levara, и не планируется в этой итерации; в таблице это уже ❌.
- **Compliance audits (SOC2)** — не уровень self-hosted OSS; в таблице ❌.
- **SDK языки** — не тестирование, а scope expansion; вне рамок «final test».

Эти пункты в comparison остаются ❌ или «n/a» с явным указанием.

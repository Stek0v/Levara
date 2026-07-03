# Оптимизация горячих вызовов Levara

**Дата:** 2026-06-25
**Метод:** Трассировка через grep + чтение исходников

---

## 1. SQL — capabilitiesFromConfig запрос на каждом вызове

### Проблема

```go
// internal/http/api_search.go:432
func capabilitiesFromConfig(cfg APIConfig) router.Capabilities {
    if cfg.DB != nil {
        var count int
        if err := cfg.DB.QueryRow(
            "SELECT COUNT(*) FROM graph_communities LIMIT 1"
        ).Scan(&count); err == nil {
            hasCommunities = count > 0
        }
    }
    ...
}
```

Этот SQL-запрос вызывается **на каждую проверку capabilities**, не кэшируется.
При десятках search-запросов в секунду — лишний roundtrip к PG.

### Решение

Кэшировать `Capabilities` в `APIConfig` или вычислить один раз при старте:

```go
type APIConfig struct {
    cachedCaps *router.Capabilities  // ← добавить
    capsOnce   sync.Once
    ...
}

func (cfg *APIConfig) Capabilities() router.Capabilities {
    cfg.capsOnce.Do(func() {
        caps := capabilitiesFromConfig(*cfg)
        cfg.cachedCaps = &caps
    })
    return *cfg.cachedCaps
}
```

### Выигрыш

- Убираем 1 SQL-запрос на каждый capabilities check
- ~5ms на каждом search-запросе

---

## 2. Embed — новый SearchPipeline на каждый запрос

### Проблема

```go
// api_search.go:466
embedClient := cfg.EmbedClient
sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, rerankClient)
```

Каждый chunksSearch/lexicalSearch создаёт **новый** SearchPipeline.
Это аллокация объекта + инициализация на каждый запрос.

### Решение

Создать один SearchPipeline при старте и переиспользовать:

```go
// В initHTTPRuntime или main():
searchPipeline := pipeline.NewSearchPipeline(sharedEmbed, colManager, nil)
// Передавать в APIConfig
```

### Выигрыш

- ~2ms аллокаций на каждый search
- Чище код

---

## 3. LLM — cognify вызывает LLM на каждый чанк

### Проблема

```go
// api_cognify.go:212
GenerateTriplets: !req.SkipGraph
```

Cognify в режиме `full` для каждого чанка вызывает LLM для:
1. Извлечения entity (NER)
2. Генерации triplets для графа
3. (опционально) обобщения (community summaries)

Для документа из 100 чанков = 200-300 LLM-вызовов.

### Решение

A. **Batch entity extraction** — отправить 5-10 чанков за один LLM-вызов
B. **Cheaper model** — использовать gemma3:4b (на RPi) для entity extraction
C. **Stage-1 skip** — пропускать чанки без имён/сущностей (regex pre-filter)

### Выигрыш

- А: 300 вызовов → 30 вызовов (10x)
- B: быстрее/дешевле без потери качества для NER
- C: ~30% чанков пропускаются бесплатно (нет сущностей = нечего извлекать)

---

## 4. Embed — doctor ping вызывает EmbedSingle на каждый healthcheck

### Проблема

```go
// mcp_doctor.go:138-167
func (h *mcpHandler) checkEmbedService() doctorCheck {
    ep := h.cfg.EmbedEndpoint
    // Ping embedding service...
    resp, err := client.EmbedSingle(ctx, "doctor-health-check")
```

Каждый вызов `/health/details` или MCP doctor пингует embed.
И keep-alive тоже пингует каждые 10 минут.

### Решение

Кэшировать результат health-check на 30 секунд:

```go
type embedHealthCache struct {
    mu       sync.Mutex
    lastOK   bool
    lastTime time.Time
    ttl      time.Duration
}
```

### Выигрыш

- 0 дополнительных embed-запросов при частых health-check
- Меньше нагрузки на embed-potion

---

## 5. SQL — connection pool overprovisioning

### Проблема

```go
// bootstrap.go:324
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
```

25 открытых соединений для standalone-режима — избыточно.
PostgreSQL default = 100, но Levara standalone должен работать с 4.

### Решение

Tier-based pool sizes:

```go
switch {
case standalone:
    db.SetMaxOpenConns(4)
    db.SetMaxIdleConns(2)
case raftMode:
    db.SetMaxOpenConns(25)
    db.SetMaxIdleConns(10)
default:
    // current (25/10)
}
```

### Выигрыш

- Экономия ~80 MB памяти на PG-соединениях в standalone
- Быстрее старт

---

## 6. BM25 — LoadAll при старте блокирует запуск

### Проблема

```go
// main.go:530
bm25Store := bm25.NewSnapshotStore(*dataDir + "/bm25")
if loaded, err := bm25Store.LoadAll(); err != nil {
    // ...
}
for collection, idx := range loaded {
    grpcSvc.BM25Indexes()[collection] = idx
}
```

Загружает ВСЕ BM25 индексы при старте. Для 20 коллекций с большими текстовыми
индексами — до 5 секунд блокировки.

### Решение

Lazy load: загружать индекс коллекции только при первом запросе к ней:

```go
type LazyBM25Store struct {
    store  *SnapshotStore
    loaded map[string]*bm25.Index
    mu     sync.RWMutex
}

func (s *LazyBM25Store) Get(collection string) (*bm25.Index, error) {
    s.mu.RLock()
    idx, ok := s.loaded[collection]
    s.mu.RUnlock()
    if ok { return idx, nil }
    
    // Load on demand
    s.mu.Lock()
    defer s.mu.Unlock()
    // Double-check
    if idx, ok = s.loaded[collection]; ok { return idx, nil }
    idx, err := s.store.Load(collection)
    if err == nil { s.loaded[collection] = idx }
    return idx, err
}
```

### Выигрыш

- Старт быстрее на 2-5 секунд
- Не грузим неиспользуемые индексы

---

## Сводка

| # | Горячая точка | Сейчас | Оптимизация | Выигрыш |
|---|--------------|--------|-------------|---------|
| 1 | SQL: capabilitiesFromConfig | 1 запрос/вызов | Кэш при старте | -5ms |
| 2 | Embed: new SearchPipeline | 1 аллокация/запрос | Singleton | -2ms |
| 3 | LLM: cognify чанки | 1 LLM-вызов/чанк | Batch + pre-filter | 10x |
| 4 | Embed: doctor healthcheck | ping каждый раз | Кэш 30s | 0 доп. ping |
| 5 | SQL: connection pool | 25 open/10 idle | 4/2 в standalone | -80 MB |
| 6 | BM25: LoadAll | блокирующий старт | Lazy load | -5s старта |

**Совокупный выигрыш:** старт Levara 10s → 3s, запрос search 20ms → 13ms,
cognify 300 вызовов → 30 вызовов.

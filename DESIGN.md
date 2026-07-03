# Levara Feature Flags — Design Proposal

**Дата:** 2026-06-25
**Мотивация:** Сейчас даже в standalone-режиме Levara инициализирует все движки.
Это медленный старт (до 10 сек) и лишнее потребление памяти (~200 MB на неиспользуемые
пулы соединений и кэши).

## Текущий call graph

```
main() ──┬── initStorageBackend()       ALL  profiles
         ├── initVectorRuntime()         ALL  profiles  
         ├── initHTTPRuntime()           ALL  profiles
         ├── NewCollectionManager()       ALL  profiles
         ├── initSQLRuntime()            ALL  profiles  ← даже pg-url пустой
         ├── graphdb.NewWriterWithSchema() if neo4j-url set
         ├── initLLMProvider()           ALL  profiles  ← даже без LLM-провайдера
         ├── embed.NewClient()           ALL  profiles  ← даже без embed
         ├── llmcache.NewPersistent()    ALL  profiles
         ├── bm25Store.LoadAll()          ALL  profiles
         ├── startGRPCServer()           ALL (port=0 = skip listen)
         ├── startLLMProxyIfConfigured() ALL
         ├── RegisterMCPAPI()            ALL
         ├── consolidate janitor         opt-in (env)
         ├── workspace index worker      opt-in (env)
         ├── workspace watcher           opt-in (env)
         └── startEmbedKeepAlive()       ALL
```

## Предложение: Feature Toggles

### Уровень 1 — глобально (конфиг/env)

```yaml
# levara.yaml или LEVARA_FEATURES env
features:
  hsnw:    true    # ядро — всегда включён
  sql:     true    # PostgreSQL/SQLite
  graph:   true    # Neo4j
  llm:     true    # LLM provider + cache + proxy
  embed:   true    # embedding client + keep-alive
  grpc:    true    # gRPC server
  bm25:    true    # lexical search
  mcp:     true    # MCP protocol
```

### Уровень 2 — на команду

```bash
# Только векторный поиск
levara-server --profile minimal --features hsnw

# Вектор + SQL для памяти
levara-server --profile standalone --features hsnw,sql

# Полный стек (default)
levara-server --profile full

# Полный стек без Neo4j
levara-server --profile full --features all,-graph
```

### Реализация

```go
// pkg/features/features.go
type Toggles struct {
    HNSW  bool
    SQL   bool
    Graph bool
    LLM   bool
    Embed bool
    GRPC  bool
    BM25  bool
    MCP   bool
}

func FromFlags(profile string, featuresList string) Toggles {
    t := Toggles{}
    
    // Profile presets
    switch profile {
    case "minimal":
        t.HNSW = true
    case "standalone":
        t.HNSW, t.SQL, t.LLM, t.Embed, t.MCP = true, true, true, true, true
    case "standalone-embed":
        t.HNSW, t.Embed = true, true
    case "full", "":
        t = Toggles{true, true, true, true, true, true, true, true}
    }
    
    // Override from --features flag
    for _, f := range strings.Split(featuresList, ",") {
        f = strings.TrimSpace(f)
        if f == "" { continue }
        val := true
        if f[0] == '-' { val, f = false, f[1:] }
        switch f {
        case "hsnw": t.HNSW = val
        case "sql":  t.SQL = val
        case "graph": t.Graph = val
        case "llm":  t.LLM = val
        case "embed": t.Embed = val
        case "grpc": t.GRPC = val
        case "bm25": t.BM25 = val
        case "mcp": t.MCP = val
        case "all":
            t = Toggles{val, val, val, val, val, val, val, val}
        }
    }
    return t
}
```

### Использование в main()

```go
func main() {
    // ... flags ...
    ft := features.FromFlags(*profileName, *featuresFlag)

    // Только если нужно
    var sqlRuntime sqlRuntime
    if ft.SQL {
        sqlRuntime = initSQLRuntime(*dataDir, *pgURL)
    }

    var llmProvider llm.Provider
    if ft.LLM {
        llmProvider = initLLMProvider()
    }
    
    // Neo4j — только если и флаг, и URL есть
    if ft.Graph && *neo4jURL != "" {
        // ... bootstrap ...
    }
    
    // MCP — только если нужно
    if ft.MCP {
        vectorHttp.RegisterMCPAPI(app, mcpCfg)
    }
    
    // и так далее...
}
```

## Выигрыш

| Профиль | Сейчас (сек) | После (сек) | Память (было → стало) |
|---------|-------------|-------------|----------------------|
| minimal | 3s | <1s | 200 MB → 50 MB |
| standalone | 5s | 2s | 200 MB → 120 MB |
| full | 10s | 10s | 200 MB → 200 MB |

Для твоего Mac (standalone-embed): старт 5s → 2s, память 200 MB → 80 MB.

## DoD

- [ ] `pkg/features/features.go` — Toggles + FromFlags + FromEnv
- [ ] `--features` флаг в main.go
- [ ] Условная инициализация в main()
- [ ] `nil` guards в HTTP handler-ах (если движок не загружен → graceful degrade)
- [ ] `levara doctor` показывает какие features активны
- [ ] Не сломаны существующие профили (standalone, standalone-embed, full)
- [ ] `make test` чистый

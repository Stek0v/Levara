# Design Doc: Рефакторинг слабых мест

**Дата:** 2026-06-25
**Основание:** Анализ архитектуры Levara + PD моделью north-mini-code-1.0
**Автор:** Hermes + north-mini-code

---

## 1. Levara — операционная сложность

### Проблема
Raft-кластеризация + HNSW vector shards + PostgreSQL/SQLite + Neo4j = 4 разных storage engine с разными гарантиями консистентности. При сбое одного из них восстановление требует понимания всех четырёх.

### Решения

| Вариант | Плюсы | Минусы |
|---------|-------|--------|
| A. Упростить до standalone (как сейчас на Mac) | -1 engine (Raft), одна точка отказа | Потеря кластеризации, нет HA |
| B. Абстракция storage backend через трейт | Единый интерфейс для всех engine | Рефакторинг ~2 недели |
| C. Докеризировать каждый engine отдельно | Изоляция сбоев | Docker overhead на RPi |

**Рекомендация:** B (среднесрочно), A (сейчас — уже фактически standalone на Mac).

### DoD
- [ ] `pkg/storage/provider.go` — интерфейс StorageProvider
- [ ] HNSW, SQL, Neo4j реализуют один интерфейс
- [ ] Graceful degrade: если Neo4j упал — search работает без graph_rerank
- [ ] `levara doctor` проверяет каждый engine отдельно
- [ ] Тест: отключить Neo4j → все search запросы без graph, без паники

---

## 2. Levara — 25+ MCP tools

### Проблема
25+ tools в одном registy. Добавление нового tool требует правки registry.go. Нет группировки по категориям.

### Решение: tool categories + auto-discovery

```
tools/
├── registry.go          ← flat registry → category registry
├── memory/              ← save_memory, recall_memory, list_memories...
│   └── register.go
├── search/              ← search, cross_search, search_chats...
│   └── register.go
├── data/                ← add, list_data, delete, prune...
│   └── register.go
└── system/              ← doctor, version, health...
    └── register.go
```

Каждый пакет регистрирует свои tools в `init()`. 
`registry.go` собирает их через `import _ "tools/memory"`.

### DoD
- [ ] Категории: memory, search, data, system, admin
- [ ] Авторегистрация через init()
- [ ] `hermes mcp tools --category memory` — фильтрация
- [ ] Старый flat registry удалён
- [ ] Все тесты проходят

---

## 3. PD — scans.py (655 строк)

### Проблема
Один файл `app/api/scans.py` содержит все эндпоинты сканирования:
- CRUD сканов
- Отчёты
- Статусы
- История
- Экспорт

### Решение: разбить на модули

```
app/api/scans/
├── __init__.py          ← APIRouter(prefix="/api/v1")
├── create.py            ← POST /scans
├── list.py              ← GET /scans
├── report.py            ← GET /scans/{id}/report
├── history.py           ← GET /scans/{id}/history
├── export.py            ← GET /scans/{id}/export/{format}
├── status.py            ← GET /scans/{id}/status
└── models.py            ← Pydantic схемы (можно вынести из файла)
```

Каждый модуль ~50-80 строк вместо 655.

### DoD
- [ ] 7 модулей вместо 1
- [ ] Все тесты проходят (598 строк тестов — не менять!)
- [ ] `__init__.py` подключает все роутеры через `router.include_router()`
- [ ] API не меняется — все пути те же
- [ ] `make test` чистый

---

## 4. PD — Tier gating

### Проблема
Tier gate пронизывает каждый эндпоинт и каждую фичу. При добавлении новой фичи нужно не забыть про gate. Сейчас это раскидано по коду проверками `if user.tier == "free"`.

### Решение: декоратор/middleware для tier gate

```python
# Было
@router.get("/scans/{id}/pdf")
async def export_pdf(id: str, user: User = Depends(get_current_user)):
    if user.tier == "free":
        raise HTTPException(403, "PDF only for paid plans")
    ...

# Стало
@router.get("/scans/{id}/pdf")
@tier_gate(min_tier="owner", feature="pdf_export")
async def export_pdf(id: str):
    ...
```

И централизованный конфиг:

```python
TIER_GATES = {
    "pdf_export": {"min_tier": "owner", "error": "PDF only for Собственник and above"},
    "report_history": {"min_tier": "freelance"},
    "domain_verify": {"min_tier": "owner"},
    "batch_scan": {"min_tier": "freelance"},
}
```

### DoD
- [ ] `@tier_gate()` декоратор реализован
- [ ] Все эндпоинты переведены на декоратор
- [ ] Убраны inline `if user.tier == "free"` проверки
- [ ] `make test` чистый
- [ ] API error messages сохранились

---

## 5. PD — Medical mode (Phase 2)

### Проблема
Медицинский модуль отложен, но его архитектура уже заложена в PDR. При активации — переделывать половину pipeline.

### Решение: feature flag + заглушка

```python
# app/config.py
FEATURE_FLAGS = {
    "medical_mode": False,  # Phase 2
}

# app/pipeline/stages/s10_analyze.py
if config.FEATURE_FLAGS["medical_mode"]:
    run_medical_analysis(site)
```

Это позволит включать medical mode без изменения кода pipeline.

### DoD
- [ ] Feature flags в config.yaml/env
- [ ] Medical code существует под флагом
- [ ] Без флага — medical stage пропускается
- [ ] Тесты на оба режима

---

## Сводка

| # | Проект | Слабость | Трудоёмкость | Приоритет |
|---|--------|----------|-------------|-----------|
| 1 | Levara | Storage engine complexity | 5d | P2 |
| 2 | Levara | 25+ tools registry | 3d | P2 |
| 3 | PD | scans.py 655 строк | 1d | P1 |
| 4 | PD | Tier gating spaghetti | 2d | P1 |
| 5 | PD | Medical mode заглушка | 0.5d | P3 |

# A/B Тест: Levara MCP vs Baseline (без MCP)

## Цель

Измерить влияние Levara MCP на продуктивность разработчика при выполнении типовых coding-задач. Сравнить скорость, качество и количество итераций с MCP-ассистентом и без него.

## Участники

- Минимум 2 разработчика (A и B)
- Каждый выполняет все 10 задач: 5 с MCP, 5 без (crossover design)
- Порядок рандомизируется для устранения эффекта обучения

## Процедура

### Подготовка

1. Развернуть Levara на Pi 5 (`10.23.0.53:8080`), убедиться что все 15 tools доступны
2. Загрузить в knowledge base документацию проекта (3-5 файлов)
3. Сохранить 10-15 project memories с контекстом проекта
4. Запустить `cron_benchmark.py` для фоновых метрик

### Протокол выполнения

1. **Warmup** (5 мин): ознакомление с задачей, чтение условия
2. **Execution** (таймер): выполнение задачи
3. **Review** (5 мин): самопроверка, запуск тестов
4. **Запись**: время, количество итераций, использованные tools

### Условия

- **Группа MCP**: Claude Code + Levara MCP (save/recall memory, search, cognify)
- **Группа Baseline**: Claude Code без MCP (только стандартные tools)
- Обе группы имеют доступ к документации в файлах

## 10 Coding Tasks

### Категория: Bug Fix (2 задачи)

**Task 1: Race Condition Fix**
- Описание: исправить data race в concurrent map access
- Входные данные: Go файл с `sync.Map` и обычным map
- Критерий: тесты проходят с `-race`, нет deadlock
- Сложность: средняя

**Task 2: Memory Leak Fix**
- Описание: найти и исправить утечку goroutine
- Входные данные: HTTP server с context cancellation bug
- Критерий: `pprof` показывает стабильное кол-во goroutine
- Сложность: средняя

### Категория: Feature Implementation (3 задачи)

**Task 3: REST Endpoint**
- Описание: добавить CRUD endpoint для нового ресурса
- Входные данные: спецификация API (JSON schema)
- Критерий: все CRUD операции работают, валидация, тесты
- Сложность: средняя

**Task 4: Middleware Pipeline**
- Описание: реализовать middleware chain (auth, logging, rate-limit)
- Входные данные: интерфейс middleware, 3 реализации
- Критерий: правильный порядок выполнения, unit-тесты
- Сложность: выше средней

**Task 5: Database Migration**
- Описание: написать миграцию SQLite -> PostgreSQL
- Входные данные: SQLite schema + данные
- Критерий: данные корректно перенесены, индексы созданы
- Сложность: средняя

### Категория: Refactoring (2 задачи)

**Task 6: Extract Package**
- Описание: вынести общую логику из 3 файлов в отдельный пакет
- Входные данные: 3 файла с дублированным кодом
- Критерий: DRY, все тесты проходят, public API минимален
- Сложность: средняя

**Task 7: Error Handling**
- Описание: заменить panic/recover на proper error returns
- Входные данные: пакет с 15+ panic() вызовами
- Критерий: нет panic, все ошибки обёрнуты, stack trace сохранён
- Сложность: ниже средней

### Категория: Testing (2 задачи)

**Task 8: Integration Tests**
- Описание: написать integration тесты для HTTP API
- Входные данные: 5 endpoints, спецификация
- Критерий: coverage > 80%, тесты изолированы (testcontainers)
- Сложность: средняя

**Task 9: Benchmark Suite**
- Описание: написать Go benchmarks для hot path
- Входные данные: 3 функции (search, insert, delete)
- Критерий: b.ReportAllocs(), parallel benchmarks, результаты стабильны
- Сложность: ниже средней

### Категория: Architecture (1 задача)

**Task 10: Design Document**
- Описание: написать ADR (Architecture Decision Record) для выбора между gRPC и REST
- Входные данные: требования к API (latency, streaming, compatibility)
- Критерий: структурированный документ, trade-offs, рекомендация
- Сложность: выше средней

## Метрики

### Количественные

| Метрика | Описание | Как измерять |
|---------|----------|-------------|
| **Time-to-completion** | Время от старта до прохождения тестов | Секундомер |
| **Iteration count** | Количество правок до рабочего решения | Git commits |
| **Lines changed** | Объём изменений | `git diff --stat` |
| **Test pass rate** | Доля проходящих тестов | `go test ./...` |
| **MCP calls** | Количество обращений к Levara | Логи Levara |
| **MCP latency impact** | Суммарное время ожидания MCP | `call_log` из benchmark |

### Качественные

| Метрика | Описание | Шкала |
|---------|----------|-------|
| **Code quality** | Читаемость, идиоматичность | 1-5 (peer review) |
| **Solution completeness** | Все ли edge cases покрыты | 1-5 |
| **Confidence** | Уверенность в решении | 1-5 (самооценка) |

## Анализ результатов

### Статистика

1. **Парный t-test** (или Wilcoxon signed-rank при N < 30) для time-to-completion
2. **Effect size** (Cohen's d): малый > 0.2, средний > 0.5, большой > 0.8
3. **95% доверительный интервал** для разницы средних

### Формула

```
speedup = mean(baseline_time) / mean(mcp_time)
p_value = paired_ttest(baseline_times, mcp_times)
effect_size = (mean(baseline) - mean(mcp)) / pooled_std
```

### Интерпретация

| Результат | Вывод | Действие |
|-----------|-------|----------|
| speedup > 1.3, p < 0.05 | MCP значительно ускоряет | Внедрять в workflow |
| speedup 1.0-1.3, p > 0.05 | Эффект незначим | Нужно больше данных |
| speedup < 1.0 | MCP замедляет | Оптимизировать latency |

## Шаблон записи результатов

```json
{
  "participant": "A",
  "task_id": 3,
  "condition": "mcp",
  "time_seconds": 420,
  "iterations": 3,
  "lines_changed": 87,
  "tests_passed": 12,
  "tests_total": 12,
  "mcp_calls": 8,
  "mcp_tools_used": ["recall_memory", "search", "save_memory"],
  "code_quality": 4,
  "completeness": 5,
  "confidence": 4,
  "notes": "recall_memory сразу дал нужный контекст"
}
```

## Расписание

| День | Участник A | Участник B |
|------|-----------|-----------|
| 1 | Tasks 1,3,5,7,9 (MCP) | Tasks 1,3,5,7,9 (Baseline) |
| 2 | Tasks 2,4,6,8,10 (Baseline) | Tasks 2,4,6,8,10 (MCP) |
| 3 | Анализ результатов | Code review |

## Чеклист перед запуском

- [ ] Levara работает на Pi, все 15 tools отвечают
- [ ] Knowledge base загружен (проверить `search` по ключевым словам)
- [ ] Memories сохранены (проверить `list_memories`)
- [ ] `cron_benchmark.py` запущен в cron
- [ ] Git repo чистый, отдельная ветка для каждого участника
- [ ] Таймер/секундомер готов
- [ ] Шаблон JSON для записи результатов открыт
- [ ] Участники прочитали задачи (без выполнения)

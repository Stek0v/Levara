# Levara Test Reports

Каталог для логов прогонов `go test` и агрегированных отчётов.

## Формат

- `baseline_YYYY-MM-DD.json` — сырой `go test -json ./...`
- `baseline_YYYY-MM-DD.md` — человекочитаемый свод (pass/fail/skip + coverage)
- `module/<name>_YYYY-MM-DD.{json,md}` — прогоны по конкретному модулю
- `summary.md` — актуальный свод по всем прогонам

## Команды запуска (локально, на машине с Go)

```bash
cd Levara

# Baseline (все модули с _test.go)
go test -json -cover ./... > test_reports/baseline_$(date +%F).json 2>&1
echo "exit=$?" >> test_reports/baseline_$(date +%F).json

# Агрегация в markdown
go install github.com/vakenbolt/go-test-report@latest  # опционально
# или parse JSON самим:
python3 test_reports/parse_log.py test_reports/baseline_$(date +%F).json > test_reports/baseline_$(date +%F).md

# По конкретному модулю
go test -json -v -cover -race ./internal/store/... > test_reports/module/store_$(date +%F).json 2>&1
go test -json -v -cover -race ./pkg/bm25/... > test_reports/module/bm25_$(date +%F).json 2>&1
```

## Приоритеты (из ревью)

1. `internal/store` — ядро, 8 тестов уже есть, проверить что зелено
2. `pkg/bm25`, `pkg/graph`, `pkg/chunker`, `pkg/community`, `pkg/router` — покрыты, прогнать
3. Белые пятна (0 тестов) требуют новых тестов:
   - `internal/http` (12 702 LOC)
   - `pkg/orchestrator` (1 264 LOC)
   - `pkg/llm` (853 LOC)
   - `pkg/extract` (589 LOC)
   - `internal/cluster` (810 LOC)

## Политика при падении

1. Остановить автоматическое выполнение
2. Зафиксировать stack trace + failing test name в `failures/<test>_<date>.md`
3. Классифицировать: `flake` / `env` / `regression` / `legit-bug`
4. Re-analyze: `recall_memory(query="<module>", hall="discovery")` перед фиксом
5. Фикс только после явного утверждения пользователем (business logic) или автономно (infra/flake)

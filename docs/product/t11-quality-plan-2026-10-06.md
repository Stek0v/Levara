# T11 — frozen quality gate, 2026-10-06

План зафиксирован до baseline и изменений T11. Корпус: существующий
`benchmark/factual_quality_cases.json` (58 фактов, 39 запросов). Gold, queries,
scorer и prompt не меняются после результатов; hashes входят в каждый отчёт.

Baseline: текущий принятый T10 код, отдельный loopback сервер, пустая SQLite
и vector root. Embedding: локальный embeddinggemma-300m, dimension 768,
query alias выключен; перед запуском проверяется фактический ответ provider.
Answerer: уже установленный gemma4:e2b через Ollama, temperature 0,
think=false; digest модели фиксируется. Никаких production DSN, платных
providers, model downloads или рестартов существующих сервисов.

Критерии до A/B:

- Полное выполнение 39 запросов; ошибки и пропуски отдельно, не PASS.
- Ноль нарушений scope, точности исходного текста и актуальности. Диагностическая
  цель retrieval: все необходимые gold facts в top5 каждого положительного
  запроса; промахи сохраняются как отдельные проблемы, а не меняют oracle.
- Decision correctness, complete valid citations и exact short answer считаются
  отдельно на полном знаменателе. Детерминированный scorer проверяется негативными
  контролями. Exact mismatch не объявляется автоматически ложным утверждением.
- A/B: тот же corpus/model/prompt/settings, без снижения перечисленных метрик и
  без новых integrity/execution failures. Значимые расхождения разбираются по
  сырым ответам; удачный повтор не заменяет неудачный baseline.
- Native RAG проверяется отдельно от external answerer: finite float32/float64
  confidence, malformed/null metadata, strict grounding по реально используемому
  контексту, unknown/unsupported/conflicting evidence, source injection, provider
  failure и persisted feedback. Fake providers проверяют поведение, реальные
  ответы — качество. Отчёт external answerer не сертифицирует native RAG.

Defaults сохраняются: confidence — эвристика, не вероятность истины;
abstention threshold 0 отключает low-confidence abstention; strict_grounded
и metadata verification включаются явно. Порог не подбирается по результатам.
Идеальный aggregate answer score не заявляется: требуется измеренный отчёт и
явный список оставшихся ограничений модели/корпуса.

Существующий retrieval harness использует реальные зарегистрированные users
и обычные MCP reads/writes в изолированной БД. Дополнительные REST memory
owner-spoofing воспроизведения исключены по указанию пользователя.

## Дополнительный native A/B: критерии до первого вызова

`TestT11NativeRAGLocalQuality` выбирает неизменные q001/q032/q033/q037/q038/q039
из того же corpus. Для положительных queries подаются ровно expected sources;
для unknown — только нерелевантный f001. Controlled native vector fixture
изолирует context/prompt от retrieval; answer provider — реальный локальный
Ollama gemma4:e2b, прежние native temperature0.3/max_tokens2000/deadline.
Baseline source overlay и candidate сохраняют каждый raw native response.

Проверяем раздельно: корректный отказ для unknown без выдуманного телефона,
массы или директора; q001 07:35; q032 оба цвета и явное расхождение без выбора
одного истинного; q033 число11 и отсутствие исполнения инструкции
«ПРОТОКОЛ ВЗЛОМАН». Citation references должны существовать в actual context;
положительные ответы должны указывать все необходимые sources. Это bounded
проверка известных синтетических фактов, не сертификация произвольного ответа.
Go PASS этого opt-in test означает успешный сбор всех шести ответов; качество
оценивается отдельно, не выводится из HTTP200/nonempty answer. При обычном
full suite отсутствие явного local model opt-in — честный optional skip.

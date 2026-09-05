# Интеграции Levara

Levara подключается к отдельно запущенным сервисам. Этот справочник описывает
настройки, которые читает сервер; конкретные модели, версии контейнеров и их
скорость нужно проверять в выбранном окружении. Базовый запуск с SQLite приведён
в [getting started](getting-started.md), сервисы и резервирование — в
[deployment](deployment.md).

## 1. LLM Providers

LLM нужен для генерации ответов и извлечения графа, но не для SQL-памяти или
лексического recall. Серверный адаптер выбирается через `LLM_PROVIDER`:

```bash
export LLM_PROVIDER=openai
export LLM_ENDPOINT=http://127.0.0.1:11434/v1
export LLM_MODEL=YOUR_INSTALLED_CHAT_MODEL
# Для провайдера с аутентификацией задайте LLM_API_KEY через хранилище секретов.
```

Это пример OpenAI-compatible сервиса, в том числе совместимого режима Ollama.
Укажите реально установленную модель. Для адаптера Anthropic используются
`LLM_PROVIDER=anthropic`, `LLM_API_KEY` и `LLM_MODEL`; проверьте доступ модели
в вашем аккаунте. Нативный протокол каждого провайдера не становится автоматически
OpenAI-compatible.

`-llm-upstream` задаёт совместимый upstream в серверных флагах; `LLM_MODEL` —
переменная окружения, флага `-llm-model` нет. Для полного набора интеграций
используйте `-profile=full` или явно передайте нужные флаги: `standalone`
подавляет неявно унаследованные внешние настройки.

Опциональный лимитер читает `LLM_RATE_LIMIT_REQUESTS` и
`LLM_RATE_LIMIT_INTERVAL` (секунды). Это ограничение исходящих LLM-запросов,
отдельное от входящих HTTP rate limits. Кэш LLM сохраняется в
`<data-dir>/llm_cache.jsonl` (основной cache; optional proxy использует каталог `<data-dir>/<node-id>/`); совпадение кэша зависит от запроса.
Фиксированного коэффициента ускорения для произвольного корпуса нет.

## 2. Embedding Servers

Нужен полный URL OpenAI-compatible embeddings endpoint:

```bash
export EMBEDDING_ENDPOINT=http://127.0.0.1:11434/v1/embeddings
export EMBEDDING_MODEL=nomic-embed-text
```

Проверьте реальный ответ провайдера и передайте серверу `-dim=768`, если модель
возвращает именно 768 чисел. По умолчанию сервер использует 128, а не выводит
размерность из первого embedding. Имя модели само по себе не гарантирует
совместимость токенизатора, нормализации и размерности с существующей коллекцией.

Используйте `-profile=standalone-embed` для локального сервера с embeddings.
Смена модели требует повторной индексации, даже если размерность совпала:
[процедура миграции](deployment.md#embedding-migration-shadow-evaluate-cut-over-roll-back).
Проверяйте не только `/health` провайдера, но и известный документ в выдаче.

## 3. Neo4j Graph Database

Neo4j подключается явными флагами сервера:

```text
-neo4j-url=bolt://127.0.0.1:7687
-neo4j-user=neo4j
-neo4j-password=YOUR_SECRET
-neo4j-database=neo4j
```

Не сохраняйте пароль в общей истории команд; задайте конфигурацию через
защищённый сервисный запуск. SQL-база также может хранить граф в `graph_nodes`
и `graph_edges`; отсутствие Neo4j не означает отсутствие SQL-графа, а отсутствие
обоих хранилищ не даёт долговечного графа. Возможности отдельных поисковых
стратегий различаются: [поиск](search-strategies-guide.md).

`NEO4J_BOOTSTRAP_SCHEMA=false` отключает создание индексов/constraints при старте.
`ALLOW_CYPHER_QUERY=true` включает отдельную поверхность raw Cypher; используйте
её только после проверки модели доступа, а не как обычный поиск документов.

## 4. PostgreSQL и SQLite

SQL хранит пользователей, datasets, grants, память, Task Runtime и метаданные.
Выберите один вариант:

```bash
export DB_PROVIDER=sqlite
export DB_PATH="$PWD/data/levara.db"
```

или PostgreSQL DSN в `DATABASE_URL` / `POSTGRES_DSN` либо `-pg-url`. Есть также
раздельные `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD`, `DB_NAME`.
При запуске выполняется инициализация SQL-схемы. Для обновления существующего
окружения сначала сделайте резервную копию и проверьте совместимость.

PostgreSQL требуется строгому Team/Enterprise профилю. Для локальной памяти
достаточно SQLite; без обеих SQL-конфигураций WAL-векторный сервер не заменяет
SQL-backed возможности. См. [профили](profile-presets.md).

## 5. Whisper Audio Transcription

Аудио направляется в настроенный сервис транскрипции:

```bash
export WHISPER_ENDPOINT=http://127.0.0.1:9002/v1/audio/transcriptions
export WHISPER_MODEL=YOUR_INSTALLED_TRANSCRIPTION_MODEL
# WHISPER_API_KEY — при необходимости.
```

Endpoint должен принимать совместимый multipart-запрос; установка библиотеки
распознавания сама по себе не создаёт HTTP endpoint. Без `WHISPER_ENDPOINT`
обработка аудио завершается ошибкой. Загрузка/извлечение и последующий cognify —
разные стадии. Форматы, OCR, оригиналы и критерии качества описаны в
[document management](document-management.md) и [сценариях](document-workflow-scenarios.md).

## 6. S3 Cloud Storage

Текущий S3-адаптер читает:

```bash
export STORAGE_BACKEND=s3
export S3_BUCKET=YOUR_BUCKET
export S3_REGION=YOUR_REGION
export S3_ENDPOINT=https://YOUR_S3_ENDPOINT
# AWS_ACCESS_KEY_ID и AWS_SECRET_ACCESS_KEY задайте через секреты окружения.
```

Адаптер поддерживает сохранение, чтение, удаление, listing и проверку объекта.
Проверьте эти операции с выбранным S3-compatible сервисом и его политикой
доступа. По умолчанию локальное хранилище использует `<data-dir>/uploads`;
`STORAGE_PATH` не переопределяет этот путь в bootstrap сервера.

Исходные файлы и производный текст требуют согласованного backup. Наличие
S3-адаптера не означает готовые KMS/BYOK, legal hold или отдельные document ACL.
Оригиналы скачиваются через авторизованный API, см. [document management](document-management.md).

## 7. Langfuse LLM Tracing

Для обёртки LLM tracing используются `LANGFUSE_PUBLIC_KEY`,
`LANGFUSE_SECRET_KEY`, `LANGFUSE_ENDPOINT`. Успешные вызовы семплируются:
`LANGFUSE_SAMPLE_RATE`, по умолчанию 0.1; это не полный журнал каждого запроса.
Трейсы могут содержать prompt и ответ, поэтому учитывайте этот исходящий поток
данных при выборе внешнего или локального сервиса. Не считайте наличие ключей
проверкой доставки trace; выполните и найдите конкретный тестовый вызов.

## 8. Prometheus Monitoring и rerank

Метрики доступны на HTTP-origin сервера, путь `/metrics`. Например, для
Prometheus в том же сетевом пространстве:

```yaml
scrape_configs:
  - job_name: levara
    metrics_path: /metrics
    static_configs:
      - targets: ['127.0.0.1:8080']
```

В контейнере адрес должен указывать на сервис Levara, а не loopback Prometheus.
[Workspace recipes](markdown-workspace-deployment-recipes.md) содержат alert и
dashboard fixtures. [Rerank sidecar](../deploy/rerank/README.md) подключается
через `RERANK_ENDPOINT`, `RERANK_MODEL`, `RERANK_BUDGET_MS`; defaults REST и MCP,
fallback и метрики различаются — см. [search guide](search-strategies-guide.md).

## 9. MCP (Model Context Protocol)

Подключайте клиент к `/mcp`; REST base `/api/v1` не является MCP URL.
Строгий stateless transport `/mcp/2026-07-28` требует дополнительных headers/meta,
перечисленных в [API guide](api-reference.md). Реестр инструментов и схемы:
[API contract](api-contract.md). Видимость зависит от `LEVARA_MCP_TOOLSET` и
feature flags, а доступ к данным — от аутентификации и политики.

Настройка агентов: [tutorial 02](tutorials/02-agent-integration.md),
[workspace host examples](../examples/agent-hosts/README.md),
[memory skill](memory-workflow-skill.md).

## 10. LDAP/AD и SSO

OIDC bearer verification, SAML и SCIM Users имеют отдельные HTTP-поверхности.
Native LDAP/LDAPS, встроенный browser OIDC login, SCIM→SSO identity linkage и
эффективные group grants отсутствуют. Настройки и проверяемые ограничения:
[enterprise identity](enterprise-identity.md). Не используйте product preset
как свидетельство, что корпоративный пользователь уже может войти и читать
только разрешённые документы.

## 11. Docker Compose

[Deployment](deployment.md#docker) описывает фактический Compose и правильный
command override. Compose не устанавливает все описанные здесь сервисы.
Полностью локальная обработка требует локальных моделей, extractors, storage и
tracing; один локальный процесс Levara не гарантирует, что данные не покидают
машину.

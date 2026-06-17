# Levara dependency and WebUI roadmap

Дата: 2026-06-17

Этот документ фиксирует оставшиеся внешние зависимости Levara и практический план
развития WebUI после перехода основного graph/RAG пути на SQL graph + VSA.

## 1. Оставшиеся внешние зависимости

### 1.1 Embedding backend

Статус: требуется для полноценного semantic search, cognify и RAG.

Конфигурация:

- `EMBED_URL`
- `EMBEDDING_ENDPOINT`
- `EMBEDDING_MODEL`

Текущая роль:

- строит embedding vectors для chunks/entities;
- питает vector search;
- влияет на качество RAG и graph entity recall.

Как уменьшить зависимость:

- добавить встроенный минимальный local embedding режим;
- добавить tiny/offline fallback для solo-профиля;
- явно показывать в WebUI, что semantic layer degraded, если embedding endpoint недоступен.

Приоритет: высокий.

### 1.2 LLM backend

Статус: требуется для генеративных ответов, summaries, extraction и части graph/RAG режимов.

Конфигурация:

- `LLM_ENDPOINT`
- `LLM_MODEL`
- `LLM_PROVIDER`
- `LLM_API_KEY`
- `OLLAMA_URL`

Текущая роль:

- генерирует RAG answers;
- используется memify enrichment;
- помогает natural-language graph/cypher режимам.

Как уменьшить зависимость:

- сохранять полезный retrieval-only output без LLM;
- улучшить UI режим "context only";
- сделать local LLM profile first-class;
- добавить health/status для LLM в WebUI.

Приоритет: высокий.

### 1.3 Reranker sidecar

Статус: optional.

Конфигурация:

- `RERANK_ENDPOINT`
- `RERANK_MODEL`
- `RERANK_BUDGET_MS`

Текущая роль:

- повышает качество ranking;
- включается только если endpoint задан.

Как уменьшить зависимость:

- оставить optional;
- добавить built-in lexical/graph rerank fallback;
- показывать rerank state в UI как enhancement, а не requirement.

Приоритет: средний.

### 1.4 Postgres

Статус: optional для solo, recommended для team.

Текущая роль:

- центральная SQL база для командного deployment;
- лучше подходит для concurrency, backup и общего доступа.

Как уменьшить зависимость:

- продолжать поддерживать SQLite как полноценный local-first profile;
- держать SQL graph/VSA совместимыми с SQLite;
- в WebUI показывать текущий DB provider и ограничения профиля.

Приоритет: средний.

### 1.5 Neo4j

Статус: больше не обязателен для основного graph/RAG пути, но код и некоторые режимы остаются.

Оставшиеся места:

- `pkg/graphdb`;
- `neo4j-go-driver`;
- `CYPHER` search;
- natural-language-to-Cypher;
- Neo4j fallback в visualization;
- часть tests/docs все еще описывают Neo4j как graph backend.

Как уменьшить зависимость:

- заменить Cypher-only режимы SQL graph query planner;
- вынести Neo4j adapter за feature flag или build tag;
- оставить Neo4j как import/export/compat adapter;
- обновить docs/tests, где Neo4j указан как обязательный.

Приоритет: высокий.

### 1.6 S3-compatible storage

Статус: optional.

Конфигурация:

- `STORAGE_BACKEND=s3`;
- `S3_BUCKET`;
- `S3_REGION`;
- `S3_ENDPOINT`;
- `AWS_ACCESS_KEY_ID`;
- `AWS_SECRET_ACCESS_KEY`.

Текущая роль:

- object storage для team/enterprise;
- local storage остается доступным.

Как уменьшить зависимость:

- оставить optional;
- улучшить UI диагностику storage backend;
- добавить backup/export recipes для local profile.

Приоритет: низкий/средний.

### 1.7 OCR / Tesseract

Статус: optional feature, но зависимость присутствует в сборке.

Текущая роль:

- OCR endpoint;
- image/document extraction.

Как уменьшить зависимость:

- основной portable OCR backend: `OCR_BACKEND=tesseract`, запуск системного `tesseract` CLI без CGO/native link;
- optional native OCR backend: `OCR_BACKEND=gosseract`, `gosseract`/Tesseract за build tag `ocr`;
- сохранить no-ocr минимальную сборку: без `-tags ocr` Levara собирается на macOS/Linux/Windows amd64/arm64, а OCR требует только внешний `tesseract` binary в PATH или `TESSERACT_BINARY`;
- на macOS/Homebrew проверять portable wrapper через `make test-ocr`, native gosseract через `make test-ocr-gosseract`;
- показывать OCR availability в settings/admin.

Приоритет: средний.

### 1.8 Prometheus / Grafana

Статус: optional external monitoring stack.

Текущая роль:

- metrics export;
- production observability.

Как уменьшить зависимость:

- иметь встроенный `/admin/status`/WebUI dashboard;
- Prometheus оставить как production integration.

Приоритет: средний.

### 1.9 WebUI runtime

Статус: отдельный frontend stack.

Текущие зависимости:

- Node.js;
- Next.js;
- React;
- Playwright для e2e.

Как уменьшить зависимость:

- production build можно отдавать статикой/через отдельный Node runtime;
- для минимального headless профиля WebUI должен быть optional;
- для appliance профиля можно встроить собранный UI в Go binary.

Приоритет: низкий/средний.

## 2. WebUI: текущее покрытие

Уже есть:

- login/register;
- dashboard;
- datasets/upload/cognify progress;
- collections;
- search;
- chat/RAG;
- graph;
- memories;
- notebooks;
- analytics;
- settings.

WebUI уже закрывает базовые сценарии загрузки, поиска, RAG и просмотра данных.
Но он пока не раскрывает новые backend capabilities: VSA, SQL graph status,
workspace ops, sync, MCP audit и расширенную observability.

## 3. WebUI gaps и план проработки

### 3.1 VSA/Admin status panel

Проблема:

- VSA встроен в backend lifecycle, но пользователь не видит его состояние;
- нет UI для shard/member counts, rebuild и query diagnostics.

Задачи:

- добавить `GET /vsa/status`;
- показать VSA cards в Analytics;
- добавить ручной rebuild action;
- добавить диагностический query form.

Статус: выполнено.

Выполнено:

- добавлен backend endpoint `GET /api/v1/vsa/status`;
- endpoint включен в REST route inventory;
- добавлены backend tests для status после rebuild;
- WebUI API client получил `vsaStatus`;
- Analytics показывает VSA facts, shards, members, datasets, predicates, max dim и last rebuild.
- WebUI получил ручной VSA rebuild action;
- WebUI получил диагностический VSA query form с выводом candidates и similarity score.

### 3.2 Graph UI upgrade

Проблема:

- graph page должен явно показывать SQL graph как основной backend;
- нужно показать VSA-enhanced retrieval;
- `/graph/path` не раскрыт в UI;
- temporal validity не видна пользователю.

Задачи:

- добавить backend indicator: SQL graph / Neo4j optional;
- добавить path search form;
- добавить edge validity labels;
- добавить VSA indicator рядом с graph context/search.

Статус: выполнено.

Выполнено:

- Graph page показывает SQL graph как основной backend;
- Graph page показывает Neo4j как optional dependency по `/health/details`;
- Graph page показывает VSA availability/fact count;
- добавлен client method для `GET /api/v1/graph/path`;
- добавлена форма path search по `from`, `to`, `max_hops`, `as_of`;
- найденные path edges подсвечиваются в SVG graph;
- selected node можно быстро поставить как `from` или `to`.
- dataset graph DTO расширен edge metadata: `id`, `valid_from`, `valid_until`, `properties`;
- selected node connections показывают temporal validity labels.
- добавлен удобный поиск node id по имени/id для path form;
- lookup results позволяют одним кликом поставить node как `from` или `to`.

### 3.3 Analytics/Admin dashboard

Проблема:

- текущая analytics страница показывает только базовые widgets;
- не хватает ingestion, graph, VSA, sync, workspace и MCP audit.

Задачи:

- добавить cards для graph nodes/edges;
- добавить cards для VSA shards/members;
- добавить ingestion run summary;
- добавить recent errors нормализованно через API client;
- добавить dependency health: embedding, LLM, rerank, DB, Neo4j optional.

Статус: частично в работе через VSA status.

Выполнено:

- Analytics подключен к существующему `/health/details`;
- добавлена панель Dependency Health;
- в UI видны backend, postgres, neo4j, embed, llm, collections, grpc, whisper;
- VSA stats вынесены в отдельную панель.

Выполнено дополнительно:

- `/health/details` показывает `database` provider/status;
- `/health/details` показывает `storage` backend/path;
- `/health/details` показывает `rerank` configured/not_configured;
- `/health/details` показывает `ocr` backend availability;
- Analytics Dependency Health показывает database/storage/rerank/ocr;
- добавлены отдельные страницы Workspace, Sync и Admin.

Осталось:

- добавить ingestion run summary;
- добавить graph nodes/edges cards;
- добавить recent errors нормализованно через API client.

### 3.4 MCP/Admin раздел

Проблема:

- MCP является основной продуктовой поверхностью, но WebUI его почти не показывает.

Задачи:

- список MCP tools;
- active/recent sessions;
- audit log;
- agent usage;
- pinned memories;
- memory quality warnings.

Статус: частично выполнено.

Выполнено:

- добавлены backend endpoints `GET /api/v1/admin/mcp/tools`, `GET /api/v1/admin/mcp/summary`, `GET /api/v1/admin/mcp/sessions`;
- добавлен пункт `Admin` в sidebar;
- страница `/admin` показывает список MCP tools с group/status/description;
- страница показывает recent sessions из `interactions`;
- страница показывает pinned memory count;
- страница показывает memory metadata warnings;
- страница показывает audit-enabled indicator.

Осталось:

- live active MCP sessions требуют пробросить `SessionStore` в runtime status/APIConfig;
- MCP audit log с фильтрацией зависит от read-модели для `MCPAudit` sink;
- agent usage metrics нужно читать из Prometheus/metrics или отдельной агрегированной таблицы.

### 3.5 Workspace UI

Проблема:

- backend имеет workspace endpoints, но WebUI не дает полноценный workspace browser.

Задачи:

- index repo/path;
- workspace search;
- read artifacts;
- conflicts;
- reindex/retry jobs;
- workspace audit.

Статус: выполнено.

Выполнено:

- добавлен пункт `Workspace` в sidebar;
- добавлена страница `/workspace`;
- WebUI подключен к REST endpoints:
  - `GET /workspace/ops/status`;
  - `GET /workspace/manifest`;
- `GET /workspace/jobs`;
- `GET /workspace/context/artifacts`;
- `GET /workspace/conflicts`;
- `GET /workspace/audit`;
- `GET /workspace/read`;
- `POST /workspace/index`;
- `POST /workspace/search`;
- `POST /workspace/write`;
- `POST /workspace/reindex`;
- `POST /workspace/jobs/retry`;
- страница показывает project/branch/generation scope;
- страница показывает operational status, manifest summary и jobs table;
- страница позволяет индексировать text/markdown по path;
- страница позволяет write+index markdown file;
- страница позволяет reindex списка paths;
- страница показывает workspace search с freshness/citation contract;
- страница позволяет exact read markdown path из search hit;
- страница показывает conflicts и recommended actions;
- страница показывает context artifacts;
- страница показывает workspace audit events;
- jobs table получила retry action.

### 3.6 Sync UI

Проблема:

- sync есть на backend/MCP уровне, но обычный пользователь не видит Mac ↔ Pi/team sync.

Задачи:

- remote URL form;
- pull/push actions;
- sync status;
- conflicts/errors;
- type selector: memories, interactions, graph, collections.

Статус: выполнено.

Выполнено:

- добавлены backend endpoints `POST /api/v1/sync/run` и `GET /api/v1/sync/status`;
- Sync UI добавлен в sidebar;
- страница `/sync` показывает local manifest;
- страница позволяет запускать pull/push;
- type selector поддерживает memories, interactions, graph, collections;
- collections selector включается только при выборе `collections`;
- страница показывает recent sync events и per-direction summary.

### 3.7 Onboarding wizard

Проблема:

- новым пользователям непонятно, что делать после запуска Levara.

Задачи:

- профиль: Solo / Team / Enterprise;
- проверка DB/storage;
- проверка embedding/LLM;
- создание dataset;
- загрузка первых docs;
- запуск cognify;
- первый RAG вопрос;
- объяснение, что включено и что degraded.

Статус: частично выполнено.

Выполнено:

- добавлен пункт `Onboarding` в sidebar;
- страница `/onboarding` содержит выбор профиля Solo / Team / Enterprise;
- dependency check показывает текущие backend services из `/health/details`;
- wizard позволяет создать dataset;
- wizard позволяет загрузить первые files;
- wizard запускает `cognify` в RAG mode;
- wizard позволяет выполнить первый HYBRID/RAG вопрос по выбранной collection;
- degraded services отображаются badge-ами и не блокируют прохождение.

Осталось:

- DB provider и storage backend нужно добавить в `/health/details`, чтобы wizard показывал их явно.

## 4. Приоритетный порядок

1. VSA status API + Analytics cards.
2. Dependency health status в WebUI.
3. Graph page: SQL graph + path search + VSA indicators.
4. Workspace UI.
5. Sync UI.
6. MCP/Audit UI.
7. Onboarding wizard.
8. Neo4j feature flag/build tag cleanup.
9. OCR build tag cleanup.
10. Built-in embedding fallback research/prototype.

### 4.1 Embedding contract / ANN migration safety

Статус: базовый runtime guard реализован.

Contract считается не по `model_name`, а по полному пространству:

```
encoder + tokenizer + pooling + normalization + dim + metric
```

Levara хранит canonical fingerprint в `collection_meta.json` как
`embedding_version` и полный объект как `embedding_contract`.

Гарантии текущей реализации:

- records, проходящие через `CollectionManager.Insert` / `BatchInsert`, получают contract metadata;
- write с чужим `embedding_version` отклоняется до попадания в HNSW;
- text search после embedding сравнивает query contract с target collection;
- `check_drift` и doctor сравнивают full fingerprint, не только model/dim;
- `/reembed` создаёт target collection с новым contract;
- `POST /embedding-migrations` запускает managed migration job source -> target с batching, checkpoint, failed IDs и dead-letter status;
- `GET /embedding-migrations/:runId/status` отдаёт progress, elapsed, checkpoint и failed IDs;
- `POST /embedding-migrations/:runId/retry` повторяет failed records до `max_attempts`;
- migration state persistится на диск: request/status/checkpoint/failed IDs восстанавливаются после process restart;
- `enable_dual_write=true` включает best-effort source -> shadow dual-write window для новых records с текстом в metadata;
- `POST /embedding-migrations/shadow-read` прогоняет один набор queries по live и shadow collection и возвращает Jaccard@k, top1 stability, empty-rate, p50/p95/p99 latency, top-score delta, `cutover_ready` и `gate_failures`.

Оставшиеся product задачи:

- dual-write inspect/disable endpoint для завершения cutover window;
- richer score distribution histograms в shadow-read report;
- ANN/HNSW params включить в migration report.
- auto-resume interrupted RUNNING migrations after process restart.

## 6. Найденный WebUI technical debt

Актуальное состояние после прохода 2026-06-17:

- `npx tsc --noEmit` проходит;
- `npm run lint` проходит;
- `npm run build` проходит;
- previous lint/build blockers закрыты в текущей рабочей копии.
- `webui/src/app/(dashboard)/graph/page.tsx`: `any` и unused expressions;
- `webui/src/app/(dashboard)/page.tsx`: нужно заменить `<a>` на Next `<Link>`;
- `webui/src/hooks/use-auth-guard.ts`: synchronous setState in effect;
- `webui/src/hooks/use-sse.ts`: `connect` используется до декларации по правилу React immutability.

Эти ошибки нужно закрыть отдельным WebUI cleanup шагом перед тем, как считать
frontend CI полностью чистым.

## 5. Ожидаемый продуктовый результат

Levara должна восприниматься не как набор скрытых backend tools, а как control
center проектной памяти:

- пользователь видит, какие слои активны;
- администратор понимает, что деградировало;
- solo-пользователь может начать без Neo4j/Postgres;
- команда видит ingestion/search/graph/VSA/workspace/sync состояние;
- Codex и другие MCP-клиенты получают более стабильный контекст проекта.

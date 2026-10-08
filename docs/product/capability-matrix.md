# Матрица возможностей и контрактов Levara

Дата сверки: 2026-10-05. Задача: T01, веха V0. Это карта текущих интерфейсов и ограничений, а не сквозная приёмка внешних providers или гарантия одинакового поведения всех transports.

«Объявлено» означает наличие descriptor, route inventory или proto; «реализовано» — наличие dispatch/handler; «условно» — требуется флаг или настроенный backend; «отсутствует» — специализированного интерфейса нет. Точный набор полей задают [MCP descriptors](../../pkg/mcp/tools.go), [REST handlers](../../internal/http/api.go) и [proto v1](../../proto/levara.proto)/[v2](../../proto/levara_v2.proto).

## Три независимых выбора конфигурации

| Выбор | Значения и defaults | Что реально меняется |
|---|---|---|
| Product profile, `LEVARA_PROFILE` | `personal`, `solo_pro` (`solo-pro`/`pro` aliases), `team`, `enterprise`; validator нормализует пустое значение в `personal` | Проверки конфигурации SQL/auth/sync/tenant/audit. По умолчанию findings — предупреждения; `LEVARA_PROFILE_STRICT` делает ошибки причиной остановки запуска. Сам профиль не выдаёт права |
| Functional bootstrap, `--profile` | `standalone`, `standalone-embed`, `full`; отсутствие явного выбора оставляет обычные defaults | `standalone` выключает defaults для gRPC, auth, Raft, LLM proxy, Neo4j, PostgreSQL, embed. `standalone-embed` оставляет embed. Явно заданные flags побеждают preset |
| MCP toolset, `LEVARA_MCP_TOOLSET` | `core`, `memory`, `workspace`, `ops`, `long-horizon`, `full`; `light` → `memory`; пустое/неизвестное значение → `full` | Объявляемый MCP набор; runtime flags дополнительно убирают task/Memory Commit tools. На REST/gRPC/CLI этот selector не распространяется |

Для HTTP MCP непустой `LEVARA_MCP_TOOLSET` побеждает product profile. Если переменная пуста и raw `LEVARA_PROFILE=personal`, discovery выбирает `core`. При пустом product profile validator также считает профиль personal, но discovery сохраняет исторический `full`. Неизвестный явно заданный toolset также разрешается в `full`, включая при personal. Источники: [profile validator](../../pkg/profile/profile.go), [bootstrap](../../cmd/server/main.go), [toolset resolution](../../internal/http/mcp_toolset.go), [tool membership](../../pkg/mcp/tools_light.go).

Product requirements: `solo_pro` требует стабильный sync token при включённом sync; `team` — PostgreSQL, required auth, стабильный JWT secret; `enterprise` — PostgreSQL, required auth либо SSO bridge, стабильный signing config, tenant enforcement и audit sink. Это проверки наличия конфигурации; успешный внешний AD/IdP/KMS/SIEM workflow ими не подтверждается.

## MCP toolsets и gates

Числа относятся к legacy HTTP discovery. Latest удаляет `set_context` из каждого набора, где он присутствует.

| Toolset | Объявленный набор | Условия |
|---|---|---|
| `core` | 13: `levara_instructions`, `set_context`, `get_project_context`, `wake_up`, `save_memory`, `recall_memory`, `list_memories`, `pin_memory`, `unpin_memory`, `delete_memory`, `supersede_memory`, `search`, `doctor` | Task, workspace, sync, consolidation и Memory Commit отсутствуют |
| `memory` / `light` | 23 без Memory Commit: core memory/context/search/doctor, garden/digest/scaffold, consolidation, diary, feedback; `delete_memory` и `supersede_memory` включены | `LEVARA_MEMORY_COMMIT=1` добавляет preview/apply: 25 |
| `workspace` | 17: memory bootstrap/save/recall/list/pin/unpin/search/doctor плюс `workspace_context`, `workspace_search`, `workspace_read`, `workspace_write`, `workspace_commit`, `workspace_conflicts` | `delete_memory`, `supersede_memory`, task и остальные workspace tools отсутствуют |
| `ops` | 16: instructions/doctor, runtime/ingestion/errors/heartbeat, reconcile/sync, memory-index status/retry, workspace ops/jobs/watch/audit/conflicts | Bootstrap `set_context`/`wake_up` и task отсутствуют |
| `long-horizon` | 15 базовых tools: memory bootstrap/save/recall/list/pin/unpin/supersede, chat_distill, search/doctor/runtime/errors | `LEVARA_LONG_HORIZON_RUNTIME=1` добавляет 8 task tools: 23. Workspace read/write здесь отсутствуют |
| `full`, пустое/неизвестное значение | 75 без task и Memory Commit | Task flag добавляет 8; Memory Commit flag добавляет 2. Максимум при обоих flags: 85 |

Truthy значения флагов в registry: `1`, `true`, `yes`, `on`, `enabled` после trim/lowercase. Сам descriptor ещё не означает доступность SQL, workspace, embedder, LLM, reranker или внешней ноды. Недостающий backend может дать tool error, пустой совместимый read-result либо деградацию конкретного handler.

Autonomous Task worker — отдельный механизм: `LEVARA_TASK_WORKER=1` вместе с runtime flag и DB. Исполнитель поддерживает только `workspace_read` и `workspace_write` без индексации, проверяет authority, credential и активный toolset. Нужны одновременно `task_step` и разрешённый workspace action; из текущих именованных наборов это обеспечивает `full` при task flag. `long-horizon` позволяет вести ledger, но не удовлетворяет этому условию исполнения. Источники: [worker bootstrap](../../cmd/server/main.go), [executor fence](../../internal/http/task_executor.go), [action schema](../../pkg/mcp/tools_long_horizon.go).

## Transport, вход, scope и результат

| Интерфейс | Реализованный lifecycle и вход | Defaults и авторизация | Формы результата/ошибки |
|---|---|---|---|
| MCP legacy `/mcp`, protocol `2025-03-26` | POST JSON-RPC: initialize, ping, tools/list/call, resources/list/read; GET SSE и DELETE session. `initialize` создаёт session, `set_context(collection)` задаёт её default | Explicit nonempty collection → session default. `add`/`cognify`/`save_chat` без них пишут в `default`; memory tools сохраняют legacy empty namespace. Каждый запрос с операцией повторно проверяет JWT/API key/cookie; required-auth запрещает anonymous. Session ID не заменяет credential | JSON-RPC `result` с `content`, optional `structuredContent`, optional `isError`. Ошибка tool обычно HTTP 200 + `isError=true`; parse error 400/-32700; initialize auth 401/-32001; прочий auth failure bare 404; notifications 202 |
| MCP latest `/mcp/2026-07-28` | Только POST: server/discover, tools/list/call, resources/list/read. Metadata protocol/clientInfo/clientCapabilities в `params._meta`; matching headers `MCP-Protocol-Version`, `Mcp-Method`, для вызова `Mcp-Name`; Accept содержит JSON и event-stream | Stateless: collection передаётся в каждом call; `set_context` скрыт и отклоняется. Discovery/list публичны после metadata/origin checks; tool/resource calls проверяют auth. Own/tenant facts идут из verified context | Tool-result как legacy. Metadata mismatch 400/-32020; unsupported version -32022; `set_context` 400/-32602; неизвестный method 404/-32601; auth bare 404; GET/DELETE 405; Accept 406 |
| REST `/api/v1` | JSON либо multipart; конкретные routes ниже. Health/auth отдельно зарегистрированы в bootstrap до protected routes | JWT, API key permission middleware, tenant middleware; required-auth default false в dev startup. Document/dataset/workspace ACL и global-resource admin gate зависят от пути. Общей гарантии owner scope для всех routes нет | HTTP status + JSON; распространена `detail`, но единый error envelope отсутствует. Lists, status objects, async run IDs и SSE имеют разные shapes |
| gRPC v1/v2 | Один listener; 39 v1 RPC и 8 v2 RPC. Proto requests обязательны; vector Search принимает `collection`, `vector`, `top_k` | JWT `authorization` metadata; Info публичен. При required-auth global raw-storage RPC требуют active superuser. Исключения: IngestData и document-cognify streams используют verified actor и object-level policy. gRPC port=0 выключает listener | v1 смешивает response error strings/counts и gRPC status. v2 Insert/aliases делегируют v1; `ErrorDetail` не является полной taxonomy — wrapper errors используют code=1, details не наполняется; batch failures не переносятся поэлементно. Клиент проверяет response и transport status |
| CLI `cmd/cli` | HTTP client: REST commands ниже; git commands используют legacy MCP. URL/token flags перед subcommand | `LEVARA_URL` default `http://localhost:8080/api/v1`; `LEVARA_TOKEN`, `--token`, затем token file. Обычные calls используют серверный auth. `team apply` отдельно использует local-password identities и private state journal | Обычный вывод — текст/таблицы либо JSON конкретной команды; обработанные failures exit 1. `team apply` печатает структурированный status/steps. Git не проверяет MCP tool isError; ограничение ниже |

Источники: [wire types](../../pkg/mcp/types.go), [legacy dispatch/auth/defaults](../../internal/http/mcp.go), [latest](../../internal/http/mcp_latest.go), [MCP auth](../../internal/http/mcp_auth.go), [REST bootstrap](../../cmd/server/main.go), [gRPC auth](../../internal/grpc/auth_interceptor.go), [gRPC v2 mapping](../../internal/grpc/service_v2.go), [CLI](../../cmd/cli/main.go).

Точные memory axes и различия REST/MCP разобраны в [memory-model.md](memory-model.md).

Server subcommand `mcp serve` — отдельный stdio↔legacy HTTP bridge: newline JSON-RPC → POST backend `/mcp`; сохраняет session header, timeout default 30s, backend default `http://127.0.0.1:8080`, JWT либо API key. Он не реализует latest transport и не является самостоятельным memory backend. [Bridge](../../cmd/server/mcp_stdio.go).

## Функциональное покрытие

MCP в этой таблице означает одинаковый tool dispatch в legacy/latest с описанным исключением `set_context`. Все перечисленные tools имеют descriptors/handlers; flag-gated tools помечены отдельно. REST/gRPC/CLI реализуют собственные формы входа/выхода.

| Возможность | MCP | REST | gRPC | CLI |
|---|---|---|---|---|
| Curated memory и bootstrap | save/recall/list/pin/unpin/delete/supersede, wake_up, set_context, get_project_context | memories CRUD/SSE, dataset context/activity | Отсутствует отдельный Palace RPC | Отсутствует отдельная memory command |
| Memory hygiene/evidence | garden, markdown_digest, scaffold_block, consolidate/status/revert; commit_preview/apply условно | reviews/scaffold/trace export, index status | Отсутствует | Отсутствует |
| Search/graph | search, cross_search, query_entity, list_communities | search/text, search/dual, graph/path, dataset graph, VSA | Vector/text/batch/hybrid/BM25/temporal/graph/community-adjacent primitives; список ниже | search; workspace контекст отдельно |
| Ingest/cognify/code | add/list_data/delete/prune/check_drift, cognify/status, codify | add/OCR, dataset documents, cognify status/SSE, memify, ontologies | IngestData, ExtractText, chunk/embed/graph pipeline; server-owned CognifyDocuments streams | add/cognify/datasets/documents |
| Workspace | 25 workspace tools: context/artifacts/search/read/write/index/reindex/jobs/watch/ops/audit/run/commit/log/revert/delete/GC/manifest/access/conflicts | `/workspace/*`, соответствующие операции | Отсутствует | index/read/write/reindex/reconcile/watch-status/context/ops-status/conflicts/run start/get/commit/log/revert/delete/gc/manifest. Отдельной workspace search command нет |
| Task Runtime | 8 task tools при runtime flag | GET tasks/detail при DB; write lifecycle отсутствует | Отсутствует | Отсутствует |
| Chat/diary | save_chat/recall_chat/search_chats/chat_distill; diary_write/read | chats/import/runs/sessions, interactions | Отсутствует | chats import/runs/session |
| Git knowledge | analyze_commits/git_search/prune_graph; codify | dataset repo/commits | Отдельного git RPC нет | git analyze/search через legacy MCP |
| Sync | sync/sync_status | manifest/run/status/export/import; collection import status | Отсутствует отдельный sync RPC | Отдельной sync command нет |
| Feedback/ops | add_feedback/stats; doctor/instructions/runtime/ingestion/errors/heartbeat/reconcile/index status/retry | feedback/status/heartbeats/admin/MCP analytics/cache/error/health | Info, Compact, LLMCache* | health/status/cache |
| Identity/governance | Call auth, API-key action gate, document/workspace policy; отдельного provisioning tool нет | auth/users/tenants/ACL/document grants/groups/settings/API keys/bridges | JWT interceptors, scoped ingest/document jobs, global admin guard | team apply; documents policy/recipients/register/shared/grant/revoke/group-create/group-members |
| Notebooks | Отсутствует | CRUD/cell run реализованы, по умолчанию routes выключены; `LEVARA_NOTEBOOKS=1` возвращает их | Отсутствует | Отсутствует |

Стратегии search не объявляются эквивалентными: MCP принимает `search_query`, default AUTO/top_k=10, rerank default false; REST принимает `query_text`, empty/AUTO routes через router, top_k<=0 → 10, omitted rerank включает rerank при configured endpoint; CLI search default `CHUNKS`/10. REST registry содержит отдельные graph/RAG handlers. MCP dispatch ограничен lexical/hybrid/vector/parent-child/multi-query/rerank/graph-rerank. Неподдержанные explicit labels и graph-only mode отклоняются; AUTO выбирает только исполнимые retrieval paths, а degraded specialized branches называют фактический fallback. REST graph/answer handlers не объявляются MCP-возможностями. T09 acceptance требует проверок фактически вызванной ветки, public discovery/dispatch и сохранения workspace/ACL/egress. Источники: [MCP search](../../pkg/mcp/tool_search.go), [REST search](../../internal/http/api_search.go), [strategy registry](../../internal/http/search_strategy.go).

## Полный catalog и его границы

[contract.json](../contract.json) содержит 83 MCP tools, 165 REST inventory entries, 47 gRPC methods. Он хранит имена/groups/status, а не полные input/output schemas и не весь runtime HTTP router. Generated status `canonical` означает классификацию catalog, а не внешнюю приёмку. [API contract](../api-contract.md) — обзор; `make contract-check` сравнивает artifacts с текущими inventories.

83 базовых MCP tools распределены так: memory 14, task 8, workspace 25, context 2, search 4, cognify 3, data 5, chat 4, diary 2, git 3, sync 2, feedback 2, ops 9. Memory Commit добавляет 2 descriptors только при своём флаге. Task tools присутствуют в базовом inventory, но runtime discovery фильтрует их без runtime flag.

| gRPC v1 группа | Реализованные RPC |
|---|---|
| Collections/records | CreateCollection, DropCollection, ListCollections, HasCollection, Insert, BatchInsert, Delete, Search, GetByID, Compact, Info |
| Text/files/ingest | ChunkText, HashFiles, ListDirectory, IngestData, ExtractText |
| Graph | ProcessTriplets, SearchTriplets, DeduplicateGraph, BatchWriteGraph, ParallelWriteDataPoints, GraphRead, GraphCompletionSearch |
| Search/index | AggregateSearch, BatchEmbedAndIndex, SearchByText, BatchSearchByText, SemanticDedup, MultiQuerySearch, TemporalSearch, BM25Index, BM25Search, HybridSearch |
| Pipeline | PipelineCognify, CognifyDocuments, CognifyDocumentsStatus — streaming |
| Cache | LLMCacheGet, LLMCachePut, LLMCacheStats |

gRPC v2: Insert, BatchInsert, Delete, Search, Info и deprecated aliases Add/Save/Create → Insert. Generated stubs и обе service implementations присутствуют; исторический комментарий в proto v2 «NOT yet code-generated» не описывает текущий код. Наличие graph/LLM RPC требует соответствующих configured/request providers и отдельно не является quality gate.

## Расхождения и изменения до отдельной приёмки

Пункты ниже сверены с source trace; runtime reproduction, fixes и приёмка ведутся отдельными issues/changes. Они не скрываются за успешным inventory check.

1. **Personal discovery и enforcement.** `configuredMCPToolDescriptors` использует effective profile, но legacy initialize `toolset.name` и `executeTool` выбирают raw `LEVARA_MCP_TOOLSET`. При personal без override discovery сообщает core, а эти два пути разрешают full. Нужен regression реального call к скрытому tool и единое resolution; [mcp.go](../../internal/http/mcp.go), [mcp_toolset.go](../../internal/http/mcp_toolset.go).
2. **REST memory scope и validation.** Authenticated writes/SSE выводят owner из verified caller, hall проверяется по canonical vocabulary, singular read использует exact collection и active predicates, а semantic overwrite transactionally сбрасывает прежнее evidence. Дополнительные owner-spoofing тесты не запускались по указанию владельца; безопасные обе-SQL regressions прошли. [memories.go](../../internal/http/memories.go), [memory-model.md](memory-model.md).
3. **Runtime REST inventory.** SQL-enabled inventory включает условные `/tasks` и `/tasks/:taskId`; architecture test создаёт runtime с DB и отклоняет duplicate keys. Auth/health/cache и bootstrap routes остаются документированными отдельными surfaces. [tasks_read.go](../../internal/http/tasks_read.go), [routes.go](../../internal/http/routes.go).
4. **gRPC v2 error mapping.** Insert/aliases сохраняют response error, Search сохраняет transport status, а BatchInsert/Delete возвращают ordered failures с исходными index/id из typed store errors. Numeric taxonomy `ErrorDetail.code` остаётся отдельным contract gap N04. [service_v2.go](../../internal/grpc/service_v2.go).
5. **Project context aggregates.** Scoped main/related memories сохраняют own/shared active boundary. Production дополнительно показывает accessible current publication count, graph entity types после exact-collection/per-assertion authorization и caller/tenant interactions с live reauthorization всех sources. Runtime без policy provider честно показывает unavailable; actor-free vector totals не используются. [Implementation](../../pkg/mcp/tool_project.go), [descriptor](../../pkg/mcp/tools.go).
6. **CLI git error handling.** analyze/search проверяют HTTP, JSON-RPC и MCP `result.isError`; соответствующие regressions приняты в T18. [CLI handlers](../../cmd/cli/main.go).

## Воспроизводимые проверки

Перед `-run` выполнен `go test -list`: matching descriptor/profile/latest tests существуют. У исходного regex нет matching тестов в cmd/contract, а REST inventory tests не содержат ArchitectureContract в имени; поэтому нужен дополнительный targeted запуск:

```sh
go test -count=1 ./pkg/mcp ./internal/http ./cmd/contract -run 'ToolDescriptors|ToolProfiles|Toolset|MCPLatest|ArchitectureContract'
go test -count=1 ./pkg/mcp ./internal/http ./cmd/contract -run 'TestTaskToolProfileFeatureFlag|TestRESTRouteInventory|TestSchemaInventoryCoversCoreTables|TestCollectIsDeterministic|TestRenderJSONByteIdentical|TestRenderMarkdownByteIdentical|TestValidateDetectsDrift|TestRewriteAgentsMD'
make contract-check
```

Эти checks проверяют registry/transport fixtures и generated drift. Они не воспроизводят перечисленные open issues и не заменяют внешнюю функциональную приёмку.

Наблюдено 2026-10-05: обе `go test` команды и `make contract-check` завершились с exit 0. Первый regex дал `[no tests to run]` для cmd/contract; второй запуск реально исполнил его named tests, а также REST inventory и task-flag tests. Links к локальным source artifacts проверены отдельным stdlib script; отсутствующих targets нет. T01 completion утверждает основной агент после проверки документа и issues.

# Контракт осей долговременной памяти

Дата сверки: 2026-10-05. Задача: T02, веха V0. Здесь описаны curated SQL memories и их производный поисковый индекс. Документы/chunks, Markdown workspace, chat transcripts и Task Runtime ledger имеют самостоятельные источники истины и lifecycle.

## Оси записи

| Ось | Хранимые поля / публичный selector | Назначение и текущий контракт | Участвует в upsert identity |
|---|---|---|---|
| Collection | `collection_name` / MCP `collection` | Логический проектный namespace. Непустой selector читается буквально; room/hall/collection не являются ACL. Empty namespace сохраняется для legacy данных | Да |
| Owner | `owner_id` | Для MCP save — `UserIDKey` из authenticated request context; при no-auth anonymous это `''`. Own/shared read rule описан отдельно. `actor_id` обозначает lease/audit identity, не авторизацию | Да |
| Key | `key`, устойчивый `id` записи | Key — логическое имя; ID — конкретная SQL identity и vector identity. Повторный save возвращается к canonical ID существующей записи | Да, key; ID — primary key |
| Room | `room` | Тема внутри проекта, свободная строка. Организует retrieval, не определяет владельца и не открывает доступ | Нет |
| Hall | `hall` | Жанр знания: шесть публичных значений ниже. Непустой неизвестный hall в MCP save — ошибка; empty допускается совместимым handler | Нет |
| Type | `type` | Совместимая категория записи, default `project`. MCP schema описывает user/project/feedback, но handler принимает произвольный nonempty string. REST имеет другой whitelist | Нет |
| Tier | `tier` | Внутренний этап обработки, SQL default `raw`; комментарий схемы перечисляет raw/consolidated/semantic. Abstract consolidation создаёт `semantic`. Публичного аргумента tier у save нет; SQL CHECK для этих значений отсутствует | Нет |
| Pin | `is_pinned`, `pin_priority`; save `pin`/`pin_priority`, pin-tool `priority` | Приоритет для wake_up, не verification и не permission. Save defaults false/0; pin_memory default priority=1; unpin сбрасывает false/0 | Нет |
| Provenance / verification | `source_task_id`, JSON `source_receipt_ids`, `verification_status` | Источник и проверенные receipts. MCP derives unverified/receipt-validated; caller verification_status — игнорируемый legacy hint. Label не доказывает фактическую истинность текста | Нет |
| History / consolidation | `supersedes_memory_id`, `superseded_by`, `valid_until`, `supersession_reason`, `consolidated_from`, `consolidation_run_id` | Предшественник, retirement и причины; отдельно происхождение synthesis. Save provenance link не выполняет retirement | Нет |

Источники: [PostgreSQL и SQLite DDL](../../internal/http/schema.go), [MCP save/recall](../../pkg/mcp/tool_save_recall_memory.go), [descriptors](../../pkg/mcp/tools.go), [evidence validation](../../pkg/mcp/tool_memory_commit_evidence.go), [supersession](../../pkg/mcp/tool_supersede_memory.go), [consolidation](../../pkg/mcp/tool_consolidate.go).

Обе SQL-схемы задают `UNIQUE(key, owner_id, collection_name)` индексом `idx_memories_key_owner_coll`. Room, hall, type и tier его не расширяют. PostgreSQL хранит pin как BOOLEAN и timestamps как TIMESTAMPTZ; SQLite — INTEGER и TEXT. Обе схемы имеют room/hall/pinned indexes после column migrations. Hall/type/tier не защищены отдельными enum/CHECK constraints; enforcement различается по entry point.

В таблице memories нет собственных `tenant_id` и `tags`. Verified tenant context участвует в policy/evidence путях, но это не даёт самостоятельного tenant selector в каждой memory SQL-операции. Для записи нужны явные collection/room/hall по [AGENTS.md](../../AGENTS.md); совместимая API permissiveness эту дисциплину не заменяет.

## Room × hall

| Hall | Смысл | Пример знания |
|---|---|---|
| `fact` | Устойчивое проверенное свойство | SQL является источником истины для curated memory |
| `event` | Значимое событие с абсолютной датой | Датированный результат завершённой приёмки |
| `decision` | Выбор и причина/компромисс | Почему выбран конкретный механизм восстановления |
| `preference` | Долговременное требование пользователя/команды | Язык и стиль взаимодействия |
| `advice` | Повторно применимое правило | Как проверять recovery после отказа индекса |
| `discovery` | Подтверждённая причина/gotcha/результат | Причина пропуска записи при semantic recall |

Порядок и whitelist закреплены в [hall.go](../../pkg/mcp/hall.go). `IsValidHall("")` возвращает false, но `ToolSaveMemory` проверяет vocabulary только для непустого hall. Это legacy compatibility, а не седьмой hall. Новый hall требует отдельного изменения публичной модели.

Текущие project rooms: `mcp` — общий агентный интерфейс; `memory` — recall/isolation/history/consolidation/provenance; `task-runtime` — tasks/leases/receipts/promotion; `observability` — диагностика/валидация/метрики; `deploy` — профили/сервисы/флаги/эксплуатация. Default room — `mcp`, collection — `levara`. Эти маршруты задаёт AGENTS.md, а не SQL enum. Функциональные домены D1–D15 и вехи V0–V9 — оси планирования, не новые halls.

Pin priorities 10/8/5 из playbook — рекомендованные уровни global preference/critical infrastructure/active decision; handler pin принимает priority отдельно и не превращает его в permission или факт достоверности.

## Collection defaults и индекс

| Вызов | Explicit nonempty selector | Omitted/empty без session default |
|---|---|---|
| MCP save_memory | SQL collection_name получает строку selector | Сохраняет collection_name `''` |
| MCP recall/list/wake_up | SQL filter по выбранной collection | Collection filter отсутствует: доступны own/shared записи across collections; это отличается от записи в empty namespace |
| MCP pin/unpin | Один UPDATE по key + exact collection + own/shared | Исторический UPDATE по key + own/shared across collections; неизвестная collection: pin error, unpin success без эффекта |
| Legacy `set_context` | Меняет session default; неизвестное имя допустимо до первой записи | Требует nonempty string collection и session; collection этим вызовом не создаётся |
| Latest MCP | Передаёт selector в каждом call | Session/default не существует; set_context скрыт и возвращает -32602 |

Legacy dispatch подставляет session default вместо omitted/empty для перечисленных memory tools. Pin/unpin отдельно отвергают present nonstring value — включая null/number/bool/array/object — до UPDATE, чтобы malformed selector не превращался в широкую мутацию. Этой гарантии нельзя автоматически приписать всем другим handlers: многие совместимые selectors читаются обычным type assertion. Sources: [default resolution](../../pkg/mcp/session.go), [dispatch](../../internal/http/mcp.go), [pin handlers](../../pkg/mcp/tool_memory.go), [pin regressions](../../pkg/mcp/tool_memory_pin_scope_test.go), [latest](../../internal/http/mcp_latest.go).

Производный vector namespace: SQL collection `''` → `_memories`; непустая C → `_memories_` + C. Имя `_memories` в индексе и логическая SQL collection — разные значения. SQL остаётся источником truth для owner/room/hall/active state; vector candidates гидратируются через scoped SQL. Если usable vector result нет, recall использует literal SQL LIKE fallback; paraphrase recall при недоступном embedding этим не гарантируется.

При durable outbox настроенный MCP save записывает SQL и index job в одной transaction, затем worker обновляет vector; ответ может иметь index_status/index_job_id. Без outbox с available embed выполняется inline indexing; SQL-success переживает indexing failure с divergence diagnostic. Поэтому successful save не обещает одинаковый semantic readiness во всех deployments. Recall кратко ждёт pending outbox jobs; долговременное восстановление относится к index retry/reconcile, а не к hall.

Перед native vector effect worker удерживает короткую SQL-защиту до фактического возврата insert/delete: PostgreSQL SHARE table lock, SQLite writer reservation, включая WAL. Защита охватывает всю таблицу memories; при измеренной конкуренции требуется более узкий механизм. Embedding и callbacks выполняются снаружи. Upsert проверяет captured key/value/type/owner/collection и active state после embedding; delete пропускает текущую активную или чужую запись. SQL/cancellation errors не дают выполнить effect. Это последовательность с повторными проверками и восстановлением через outbox, не атомарность двух хранилищ.

Memory migration dual-write shadow сверяет SQL до и после своего embedding. Explicit owner, включая пустой shared owner, должен точно совпасть; null и non-string отклоняются. Отсутствующее legacy owner поле допускается, а текущий SQL owner всё равно входит в captured predicate. Общая migration cutover/recovery приёмка остаётся T11.

## Ownership и shared semantics

| Операция | Текущая граница |
|---|---|
| MCP save | Пишет в owner из request context; owner_id/actor_id аргументы не выбирают владельца. Повторный upsert затрагивает только ту же key/owner/collection identity |
| MCP recall/list/pinned часть wake_up | `(owner_id=current OR owner_id='')`; по умолчанию active (`superseded_by=''`). Anonymous current='' получает shared rows |
| MCP pin/unpin | Сохраняет тот же own/shared predicate; own и shared с одним key в выбранной collection меняются вместе. Это существующее правило именно этих операций; переносить его на новый entity нельзя |
| MCP delete_memory | Точная personal row по ID либо однозначному key/optional collection; ambiguous key — ошибка. Shared delete требует explicit ID и live administrator либо допустимый trusted-local context; это отдельная policy |
| MCP supersede / Memory Commit | Shared mutation требует administrator/trusted-local permission и credential checks; обычная доступность shared для чтения такого права не выдаёт |
| Provenance/task | Task/receipt owner и collection сверяются с authenticated memory owner; lease actor не даёт cross-owner права |

`wake_up.top_entities` использует собственный graph path. Memory-read own/shared predicate не определяет права consolidation: она выбирает точного проверенного владельца, а shared namespace требует явного selector и live administrator/trusted-local authority. T05 ограничивает main/related project-context memories и выводит aggregate sections без доказанного owner/project mapping как unavailable; [отдельная SQL/auth/race приёмка пройдена](../../openspec/changes/scope-project-context/evidence.md). T06 owner lifecycle проверяет captured plan, live authority и guarded журнал apply/revert; [owner lifecycle evidence](../../openspec/changes/scope-consolidation-owner-lifecycle/evidence.md) и [index lifecycle evidence](../../openspec/changes/repair-memory-index-lifecycle/evidence.md).

REST контракт сейчас расходится с MCP: POST /memories принимает body `collection_name`, при авторизации принимает только omitted или собственный body `owner_id` (чужой возвращает 403); явный owner сохранён для локального anonymous режима. Whitelist type = fact/event/decision/preference/advice/discovery/project/user/feedback/reference и не проверяет hall. GET list принимает query `collection`, type/room/hall и own/shared active filter; GET /memories/:key не фильтрует collection/retirement и выбирает LIMIT 1. Это source-confirmed ограничения, требующие собственных auth/regression fixes; REST write не объявляется эквивалентом безопасного MCP owner contract. [REST implementation](../../internal/http/memories.go).

## Upsert, история и доказательства

Одинаковый key в разных collections или owners создаёт независимые identities. Повторный save того же key/owner/collection с другим room или hall обновляет ту же запись, включая classification, value, pin и provenance, не создавая отдельного knowledge cell. Omitted room/hall при таком save могут очистить предыдущие значения; omitted pin возвращает false/0. Для сохранения содержательной истории используется `supersede_memory`.

Supersession атомарно архивирует старый key, устанавливает replacement ID/valid_until/reason и создаёт новую active запись; новый ряд по текущему handler не наследует pin (false/0). Обычный `save_memory(supersedes_memory_id=...)` задаёт link provenance, но сам не архивирует предшественника. Current recall исключает retired записи, `include_superseded=true` объединяет bounded literal/semantic/history paths. Для active replacement причина и timestamp фактического retirement предшественника наследуются только по reciprocal link в том же owner/collection. Provenance-only link не наследует metadata другого замещения. Literal historical match остаётся доступен без embedder и при отсутствии среди первых 10 semantic candidates; unlinked historical semantic полнота вне этого набора не гарантируется. Archived vectors намеренно не публикуются заново. [Historical recall](../../pkg/mcp/tool_memory_history.go).

MCP save/supersede/commit derives `unverified` без evidence и `receipt-validated` после проверки source task и receipts. Receipts должны принадлежать тому же owner/task, иметь pass и current workspace revision; command evidence требует явно заданный exit_code=0; artifact evidence проверяется по URI/digest. Malformed, foreign, stale или failed evidence отвергается. Caller label `verified` сам по себе ничего не авторизует. Checkpoints, receipts и временный progress остаются Task ledger; promotion — отдельная проверенная граница.

Есть два известных mismatch, которые не расширяют публичный vocabulary:

- Abstract consolidation создаёт внутренний `tier='semantic'` и сохраняет owner/collection/type/room/hall однородных источников. `semantic` не является публичным hall; пустой/неизвестный legacy hall даёт явный skip до LLM. Mixed classification edges отбрасываются до clustering. Полный content-free журнал содержит before/after fingerprints; stale или повреждённый run не откатывается. Atomic outbox enqueue проверен отдельно. Completed одинаковый digest переоткрывается с новым publication ID; остальные состояния дедуплицируются без сброса claim/retry. Finish/Defer сверяют полный claim и namespace, поэтому старый worker не завершает новую публикацию. Полная индексированная интеграция T06 проверена на обеих SQL и обоих MCP transports; [evidence](../../openspec/changes/repair-memory-index-lifecycle/evidence.md).
- `memory_markdown_digest` экспортирует явно выбранные активные decision/discovery записи своего/shared owner в указанной collection со статусом `receipt-validated` или legacy `verified`. Сохранённые label, freshness и Task/receipt provenance выводятся без изменения; legacy `verified` не обещает server receipt-validation. Export не повторяет проверку исторических receipts и не подтверждает истинность текста; не пишет SQL/Git/workspace и не имеет import path. [Digest](../../pkg/mcp/tool_memory_markdown_digest.go).

## Tags относятся к chunks

`add`/`cognify` используют room/tags для document/chunk metadata; `search` фильтрует chunk room и tags. Tags имеют ANY/OR semantics, а room — exact match; отсутствие фильтра не требует metadata. Malformed metadata при requested filters не считается совпадением. Это [ChunkMetaMatches](../../pkg/mcp/hall.go), а не hall или SQL curated-memory tags.

Не следует подменять `recall_memory(hall="decision")` вызовом chunk search tags=["decision"]: две операции читают разные records и индексы. Taxonomy Domain → Collection → Document для retrieval также отличается от room × hall; её writer/routing приёмка относится к DCD, не к schema memories.

`chat_distill` сохраняет новую формулировку как `unverified`, с пустым
`source_task_id` и `source_receipt_ids=[]`, включая overwrite ранее verified или
receipt-validated записи. Текстовое указание исходного диалога остаётся provenance,
но старые receipts не подтверждают новый текст. `dry_run` не меняет SQL и evidence.
Это правило не устанавливает права доступа к импортированным диалогам и не
подтверждает качество модели. [Distillation](../../pkg/mcp/tool_chat_distill.go).

## Проверки и corner cases

| Corner case | Существующая проверка / ожидаемая граница |
|---|---|
| Одинаковый key, разные collections | TestToolSaveMemory_SameKeyDifferentCollections; identities независимы |
| Тот же key/owner/collection, изменённые room/hall | [TestToolMemoryIdentityRoomHall](../../pkg/mcp/tool_memory_identity_test.go) на SQLite/PostgreSQL: canonical ID и created_at сохраняются, число строк не меняется, sibling owner/collection и их timestamps остаются прежними |
| Unknown/empty hall | TestIsValidHall, TestToolSaveMemory_InvalidHallIsError; TestToolMemoryIdentityRoomHall проверяет unknown nonempty hall без мутации и совместимые explicit empty/omitted room/hall saves на обеих БД |
| Owner/shared/foreign filters | TestToolListMemories_OwnershipScoped, TestToolRecallMemory_VectorPathOwnershipScoped, TestMemoryProvenanceSurfaces |
| Explicit/omitted/empty/malformed pin selector | TestToolMemoryPinCollectionScope на обеих БД; missing/default priority/idempotent unpin/control timestamps включены |
| Evidence и shared mutation | TestMemoryWriteEvidence, TestSupersedeTrustSharedAuthority; current/failing/stale/forged proof проверяется отдельно от hall |
| Исторический recall | TestRecallHistoricalCandidates на SQLite/PostgreSQL, bounded/hydration cases отдельно |
| Chunk tags против memory hall | TestChunkMetaMatches через hall tests; хранение tags у curated memory не объявляется |

Named tests проверены перед запуском. Команды базового SQLite набора и actual dialect parity:

```sh
go test -count=1 ./pkg/mcp -run 'Hall|SaveMemory|RecallMemory|ListMemories'
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 -v ./pkg/mcp -run 'TestMemoryWriteEvidence|TestMemoryProvenanceSurfaces|TestRecallHistoricalCandidates$|TestToolMemoryPinCollectionScope|TestSupersedeTrustSharedAuthority'
go test -count=1 -v ./pkg/mcp -run ChunkMetaMatches
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 -v ./pkg/mcp -run '^TestToolMemoryIdentityRoomHall$'
```

SKIP PostgreSQL не считается parity evidence. DDL mirrors сверены source-to-source; текущие tests не заменяют live upgrade приёмку старой базы, внешний model quality gate или regressions открытых REST и общих migration mismatches.

Наблюдено 2026-10-05: четыре команды выше завершились с exit 0. Dialect suite использовал отдельную PostgreSQL 16 test DB на loopback port 53350 и выполнил SQLite/PostgreSQL branches без skips; raw output находится во временном `/tmp/levara-t02-parity.log`. TestMemoryWriteEvidence/TestSupersedeTrustSharedAuthority обозначают dialects boolean subtest names false/true; source fixtures подтверждают SQLite/PostgreSQL соответственно. Focused identity regression выполнил один общий сценарий в двух dialect subtests (2 PASS, 0 SKIP); raw output текущего усиленного теста — `/tmp/levara-t02-current-identity.log`. Он использует существующий full memory/evidence fixture и не проверяет vector re-indexing. T02 completion утверждает основной агент.

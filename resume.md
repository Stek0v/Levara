# Возобновление работы над roadmap

Снимок состояния: **2026-09-10**. Это контекст незавершённой работы, а не отчёт
о выпуске. Критерии приёмки — в [roadmap](docs/product/unimplemented-roadmap.md).

## Цель и текущее состояние

Доделать применимые задачи roadmap: корпоративный вход, доступ пользователей
и групп внутри организации к отдельным документам, безопасную загрузку и
обработку, исполнение задач, хранение, аудит и эксплуатацию. LDAP/AD и
федерация через IdP одинаково важны. Устаревшие документацию и маркетинг
удалять, актуальные материалы обновлять по проверенному поведению.

- Ветка: `codex/complete-roadmap`.
- Upload transport block сохранён коммитом `308d628`
  (`feat: harden CLI and gRPC upload transports`).
- Реализация этого блока собрана в текущей ветке; развёртывание не выполнялось.
- Четыре регрессии последних коммитов и семь красных семейств широкого HTTP-прогона исправлены; targeted SQLite/PostgreSQL/race проверки зелёные, полный `internal/http -race` проходит.
- Inline HTTP/MCP cognify, атомарные server attempts, точные document/source
  statuses и publication, publication-aware `query_entity`, admin-only server
  `git_search` и document-filtered activity реализованы; полные `pkg/mcp`,
  `pkg/orchestrator`, `pkg/access` и профильный HTTP/MCP SQLite/PostgreSQL
  `-race` gate проходят.
- В коммите `0fbebf1` к защищённому REST API document policy добавлены
  document-scoped active recipients, список прямых/group grants получателя,
  CLI и WebUI для регистрации policy, user/group grant/revoke и управления
  составом групп. Credential recheck и SQL fence действуют до commit для API
  key и browser session и до drain read-ответа; WebUI восстанавливает актуальную
  revision после 409.
- В коммите `308d628` построена карта upload transports и добавлен
  реальный CLI→authenticated `/add` сценарий. SQLite/PostgreSQL PASS: новый
  dataset создаётся один раз, повторный upload выбирает его по имени, исходный
  файл сохраняется побайтово и CLI сообщает именно об ingest. `IngestData`
  теперь принимает existing dataset только по ID, отклоняет mixed dataset,
  invalid item N и dual payload до Save, различает transport statuses и в
  authenticated metadata-backed режиме очищает partial/late backend attempt без
  SQL/publication. Trusted-local режим без metadata DB остаётся прямым
  compatibility path без journal и гарантии batch rollback. Полные affected
  race suites проходят на SQLite/PostgreSQL.
- Реальные AD/IdP, AWS/KMS, SIEM и приёмка на нескольких релизах не пройдены.
- Изменения не развёрнуты. Шаблоны backup service/timer не активированы.

## Что реализовано локально

| Блок | Сделано | Граница подтверждения |
|---|---|---|
| Identity | LDAP/LDAPS/StartTLS, безопасные bind/search, стабильные ID, bounded nested groups; browser OIDC/SAML, sessions/logout; SCIM Groups/EnterpriseUser subset, provisioning audit, деактивация и отзыв ключей | SQL/provider fixtures и browser checks; нет живого корпоративного стенда и непрерывного потока изменений LDAP |
| Загрузка | Общий авторизованный coordinator для REST/Web, MCP add и metadata-backed gRPC; multipart preflight до sidecar; явный source revision/hash CAS; structured artifact inventory/read/retention; inline HTTP/MCP cognify сохраняет серверный источник до run; CLI→real REST и authenticated gRPC batch/cancel/late-worker проверены на обеих SQL | Document-scoped gRPC cognify отсутствует; trusted-local gRPC без metadata DB не гарантирует batch rollback; automatic recovery ограничен standalone/local SQLite под исключительным владением записью |
| Документы | ACL пользователей/групп, защищённый REST/CLI/WebUI policy/grant/revoke, document-scoped recipients и shared-document discovery, tenant boundary, CAS, tombstones/hold; отзыв API key/browser session блокирует mutation после middleware и до SQL commit; source versioning; проверки поиска, raw, артефактов и происхождения результатов | Полный жизненный цикл, составные mutation paths и внешняя AD/IdP group acceptance не закрыты |
| Runtime / memory | Реальный workspace_read/workspace_write executor с authority/deadline/budgets/retry/concurrency/kill-switch; проверка task/receipt/артефакта при memory commit | Не универсальный shell/network sandbox; receipt-validated не означает истинности содержимого |
| Аналитика / sync | Owner/tenant-фильтрация до агрегации/экспорта; происхождение сессий/запусков; исправлены errors/counts/ACK sync | Остальные каналы отзыва и двухнодовый интерфейс ещё требуют проверки |
| Хранение / аудит | S3 SDK, multipart resume/abort, AWS KMS/BYOK, rotation/cache/метрики; SQL-spool webhook-аудита, retry/dead-letter и операторский CLI | Локальные контракты; внешние сервисы не прошли приёмку |
| Эксплуатация | Проверяемый offline backup SQL/workspace/raw/structured/индексов с restore; CLI team apply с dry-run, приватным журналом и A/B checks | Backup — standalone/local; onboarding — локальные пароли, без автоматического tenant/admin provisioning |

Добавлены руководства: [хранение и аудит](docs/enterprise-storage-audit.md),
[подключение команды](docs/team-onboarding.md),
[memory commit](docs/memory-commit.md). Остальные руководства, README,
product-ladder и маркетинг ещё нужно согласовать с итоговой реализацией.

## Исправления регрессий 2026-09-09

- Общий data ID нельзя изменить через свой dataset после отзыва доступа к другой legacy/registered привязке: write-доступ проверяется для всех aliases под транзакцией.
- Память удаляется по durable ID; одинаковый key в другой collection и shared запись сохраняются. Legacy неоднозначность возвращает 409/ошибку без мутации, delete и vector outbox атомарны.
- Partial PUT настроек сохраняет пропущенные поля, сериализует конкурентные изменения и отклоняет null, неверные типы/значения и повреждённое persisted state.
- Graph query передаёт большой ACL ограниченным числом SQL-параметров и не маскирует SQL error как not-found.

Подробности, исходные красные прогоны, текущие зелёные evidence и остатки release gate: [отчёт](outputs/commit-regressions-2026-09-09/report.md) и [список задач](outputs/commit-regressions-2026-09-09/tasks.md).

## Проверки текущего дерева

Свидетельства: [outputs/roadmap-implementation-2026-09-05](outputs/roadmap-implementation-2026-09-05/).
Они относятся к конкретным промежуточным состояниям и не заменяют проверки
текущего общего diff. Старые manifests и `scope-progress.json` также не являются
свежим снимком всего дерева.

| Проверка | Наблюдаемый результат | Файл в каталоге свидетельств |
|---|---|---|
| Source counter, весь pkg/ingest, race | 278 тестов/подтестов PASS, без skips, SQLite + PostgreSQL | `source-counter-ingest-final-race.jsonl` |
| Source counter, document selection в pkg/access, race | 123 теста/подтеста PASS, без skips, обе SQL | `source-counter-access-final-race.jsonl` |
| gRPC ingest / default tenant, race | HTTP и gRPC PASS | `ingest-tenant-parity-race.txt` |
| Артефакты и ограничения чтения, race | Выбранные HTTP-тесты PASS | `artifact-ingest-bound-final-race.txt` |
| Structured artifact lifecycle, race | PASS: SQLite + PostgreSQL 16 ingest/access; HTTP owner/user/group/revoke/hold/local/remote retry | Текущий запуск 2026-09-10; atomic inventory/cleanup, A→B→A, exact A→A, artifact-only CAS race, equal-projection re-ingest, duplicate batch, rename/last alias, bounded verified API download |
| Браузерные сценарии | 56 PASS на локальном Next с подменёнными API-ответами; отдельно проверен реальный proxy/cookie transport | `identity-ui-curated-final.txt`, `identity-ui-proxy-result.txt` |
| Inline/legacy publication, exact status и graph egress, race | PASS: 21 профильный сценарий за 33.399s, SQLite + PostgreSQL | Текущий запуск 2026-09-10; atomic attempt/publication, batch rollback, partial failure, per-source counters/lineage, alias/source replacement, MCP legacy/latest, activity ACL, graph/session provenance |
| Полные package suites, race | PASS: `pkg/mcp` 12.477s, `pkg/orchestrator` 1.565s, `pkg/access` 10.235s | Текущий совмещённый diff публикационного блока |
| Полный повторный race/commit gate | PASS: `pkg/access` 20.876s, `pkg/audit` 4.829s, `internal/http` 300.082s; `make test-commit` S0–S4 | SQLite/PostgreSQL credential-revocation matrix, REST ACL/group API, audit, active-superuser global routes и прежние HTTP-регрессии; `/search` закреплён за raw-vector, `/search/text` за text search |
| Document sharing API/CLI/WebUI | PASS: `pkg/access` 23.698s, CLI 51.741s, `internal/http` 309.990s с `-race`; WebUI lint/build; curated browser gate 59/59 за 37.4s | Active exact-tenant recipients, user/group grant, revoke, empty group, tenant default, 409 refresh, redirect rejection и direct/group shared list; API key/browser session перепроверяются после middleware, а grant/group/user revoke удерживается до drain ответа на обеих SQL |
| CLI upload → настоящий REST API | PASS: SQLite + PostgreSQL | Authenticated `/add`, exact original bytes, text upload, создание dataset и повторный выбор того же dataset; fixture использует изолированный storage root |
| gRPC `IngestData` batch | PASS: gRPC 2.129s, ingest 48.147s, HTTP 306.656s с `-race`; SQLite + PostgreSQL | Authenticated metadata-backed path: ID-only dataset, one payload/dataset, invalid item N до Save, duplicate, partial Save cleanup, foreign tenant/revoke, cancel и late non-cooperative backend без publication; response не раскрывает storage path. Trusted-local no-DB rollback не входит в этот gate |

Прежний сбой `TestDocumentACLCognifyInvalidSourcesDoNotStart/missing-file`
оказался ошибкой fixture: отсутствовал корректный исходный hash. Fixture теперь
разделяет malformed hash (409 без чтения) и missing file с валидным hash (422
после одной попытки чтения); run в обоих случаях не создаётся.

Отдельно проходили проверки и независимое ревью backup/coordinator, onboarding,
memory commit и hold-aware prune. Их manifests фиксируют отдельные наборы файлов,
а не готовность текущего совмещённого репозитория.

## С чего продолжить

1. **Следующий transport contract.** Обычная authenticated загрузка через
   REST/MCP/CLI и metadata-backed gRPC `IngestData` закрыта локально на обеих
   SQL. Для trusted-local no-DB нужно выбрать batch cleanup или single-item
   ограничение. `PipelineCognify` — отдельный
   global-admin raw pipeline, поэтому для document-scoped gRPC cognify нужен
   новый контракт с dataset/document/source revision/publication identity.
2. **Mutation/hold paths.** Глобальные REST raw-vector/collection/reembed/migration
   endpoints уже active-superuser only при required auth; новый REST ACL API
   повторно проверяет API key/browser session под SQL fence до commit. Защитить составные
   gRPC writes, sync graph/collection import, dualwrite/backfill и workspace
   write/revert/GC до эффектов. В legacy dataset
   delete и rename повторить авторизацию внутри транзакции изменения.
3. **Жизненный цикл документов.** REST/CLI/WebUI sharing и tenant-scoped
   recipients закрыты локально. Остаются A12–A14: все производные, concurrent
   revoke во время обработки, миграция legacy provenance и реальный браузерный
   прогон с AD/IdP/SCIM группой.
4. **Точечные интеграционные пробелы.** Реальный MCP memory commit через HTTP
   artifact verifier при SQL pool=1; backup/restore с непустым ingest journal.
   Для backup рассмотреть отказ до offline recovery в исходном storage
   destination; это предложение, ещё не реализованное решение.
5. **Общий diff и выпуск.** Обновить descriptors/schema, выполнить `make contract`,
   `make contract-check`, `make test-commit`, затронутые SQL suites на обеих БД
   и WebUI checks. Generated contracts не редактировать вручную. Обновить
   руководства/маркетинг/testing, провести независимое ревью и scoped commits.
6. **Остальная приёмка.** Размеченный PDF/Office/HTML corpus и реальные OCR-прогоны,
   embed/rerank presets, двухнодовый sync UI, SLO/инциденты, эксперименты,
   внешние сервисы и история нескольких релизов остаются в roadmap.

## Сохранность дерева и координация

Посторонние изменения не включать в коммиты этой работы:
`internal/http/confidence.go`, `internal/http/evidence.go`,
`internal/http/evidence_test.go`, `internal/http/feedback.go`.
Служебные `.bb/`, `.codex-flow/`, `.codex/`, `.hermes/`, старый backup бинарника,
benchmark results и весь `outputs/` автоматически не добавлять. Перед правками
перечитать diff; не использовать `git add .`.

Последние назначения: `batch_grpc` — MCP inline source до очереди;
`backup_hardening` — global raw mutation guards; `security_audit` — завершённая
read-only карта пробелов документации. Первые два агента прерваны; финальных
подтверждений последних назначений нет. Проверить реальный статус и diff перед
повторной выдачей владения файлами.

Task Runtime: `28112cc1-7f67-4ebb-9e87-47da2620665a`, collection `levara`,
room `deploy`, actor `codex-roadmap-implementation`. Перед продолжением получить
свежий bootstrap. Версия до сохранения этого снимка — 39; identity/storage leases
истекли. На работающем старом runtime есть проблема возврата steps после очистки
истёкших leases; локальная правка проверена, но не развёрнута. Не подменять
leases/receipts и не обходить проблему прямой записью SQL. Этот файл не заменяет
серверное состояние и не объявляет задачу завершённой.

Текущий публикационный блок ведётся отдельной задачей
`e52318d3-9852-4ca8-9089-f70a34cf6eec`, collection `levara`, room `mcp`, actor
`codex-inline-publication`. Реализация, SQLite/PostgreSQL/race, generated
contracts, документация и независимое ревью пройдены; блокирующих замечаний
P0–P2 не осталось. Task Runtime завершён на версии 20; финальные receipts:
`b5496391-475c-4795-859f-605c765e267a`,
`67884549-56de-4e2c-98fd-bb63679f809f`,
`0e7fcba3-6ccb-48ee-9e61-7712bebc012a`.

Текущий блок sharing ведётся задачей
`e0ae2e80-d1c4-404d-8ba3-6581566c8470`, collection `levara`, room `mcp`, actor
`codex-document-sharing-ux`. REST и CLI проверки на SQLite/PostgreSQL, WebUI
lint/build, полный affected race gate, generated contracts и curated gate 59/59
пройдены. Независимое ревью точного scoped diff не нашло P0–P2; задача завершена
на версии 28. Финальные receipts: `f197fdd2-c835-436c-a33b-db67910d9745`,
`67e14f04-7077-4923-aea7-a2e3073eda65`,
`54aaa35d-f657-4a17-8457-086feac77989`.

Upload transport block ведётся задачей
`89cd190b-5d56-4df4-b9d3-ea1bd3388cd5`, collection `levara`, room `mcp`, actor
`codex-upload-transport-matrix`. Карта transport paths, CLI→real REST на
SQLite/PostgreSQL, metadata-backed gRPC `IngestData` failure matrix, contracts и
документация прошли проверку. Независимое ревью не нашло P0–P2 в коде и выявило
одно завышенное обещание rollback для trusted-local no-DB; граница режима теперь
явно отражена в contract, roadmap и руководстве. Точные receipts и состояние
финализации сохраняются в Task Runtime по этому ID. Задача завершена на версии
22; финальный reviewer receipt — `139f54fb-0e98-457a-8bfa-c97e4398b3b6`.

Текущий проход использует изолированный PostgreSQL 16 на loopback, порт 56202,
данные `/tmp/levara-structured-pg-review`. Доступность проверить заново;
использовать отдельные тестовые схемы, не данные приложения. Production deploy,
live migration и restart в рамках сохранения этого состояния не выполнялись.

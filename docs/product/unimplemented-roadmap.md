# Roadmap: состояние реализации и оставшаяся приёмка

Обновлено **2026-09-10** по рабочему дереву и сохранённым проверкам. Существенная
часть механизмов реализована локально, но весь список **не закрыт**. Реализация
этого блока собрана в текущей ветке, но не развёрнута. Совмещённый `internal/http -race`
зелёный; полный release gate репозитория и внешняя приёмка остаются открыты.
Это backlog разработки и приёмки, не инструкция включения функций. Контекст для
продолжения — [resume](../../resume.md).

«Реализовано локально» означает наличие кода и проверок отдельных блоков,
а не завершённую интеграцию, внешнюю приёмку или готовность релиза. Перечисленные
ниже критерии остаются открытыми до соответствующего итогового прогона.
Приоритеты: P1 — доступ, идентичность и исполнение задач; P2 — эксплуатация,
качество и интеграции; P3 — удобство и обоснованные эксперименты.

| Область | Реализовано локально | Главный остаток |
|---|---|---|
| Identity | LDAP/LDAPS/StartTLS, browser OIDC/SAML, SCIM Groups/EnterpriseUser subset | Реальные AD/IdP, сквозной отзыв, общий integration gate |
| Документы | Модель ACL, source versioning, авторизованная загрузка, structured preflight/source CAS и artifact lifecycle, inline HTTP/MCP cognify и publication-aware `query_entity` | **API ACL не подключён**; остальные raw/vector/collection mutation paths |
| Task Runtime / memory | Реальный workspace executor и проверка receipt/артефактов | Совмещённая приёмка recovery/authority, контракт, три релиза |
| Хранение / аудит | S3 SDK/multipart, AWS KMS/BYOK, SQL-spool/webhook и CLI | Внешние сервисы и сквозной legal hold |
| Onboarding / backup | Team CLI и проверяемый offline standalone/local backup | Интеграция, ограничения restore, эксплуатация расписания |
| Качество / интерфейсы | Базовые parsers/adapters/sync UI уже существуют | Corpus/OCR, model presets, две ноды sync, SLO и эксперименты |
| Выпуск | Добавлены отдельные руководства | Остальные docs/маркетинг, generated contracts, общий gate, ревью, коммиты |

### Закрытые регрессии 2026-09-09

В текущем рабочем дереве закрыты четыре подтверждённых дефекта: изменение общей строки загрузки после revoke через другую legacy-привязку; широкое удаление одноимённых memories; потеря пропущенных полей при partial PUT настроек; превышение SQL bind limit при большом graph ACL. Targeted SQLite/PostgreSQL/race и browser-проверки проходят, generated contracts синхронизированы, повторное независимое ревью блокеров не нашло. Evidence и оставшиеся release-wide задачи: [отчёт](../../outputs/commit-regressions-2026-09-09/report.md) и [план](../../outputs/commit-regressions-2026-09-09/tasks.md).

### Закрытый блок публикации 2026-09-10

Inline cognify в HTTP и MCP теперь создаёт авторизованный сервером источник с
реальными `document_id`, source revision и SHA-256 до появления run. Клиентский
`document_id` удалён из MCP descriptor и не влияет на identity. Ошибка storage
не оставляет run, dataset, data или ingest journal. Pipeline status обновляет
только точный документ, текущую source revision/hash и server attempt. Claim
всех batch sources атомарен; publication и точный `COMPLETED` фиксируются одной
SQL-транзакцией. Точный `FAILED` для всех источников также фиксируется одной
транзакцией до публикации terminal run; при ошибке фиксация остаётся доступной
для повтора. Поздний worker не заменяет новый запуск, failed reprocess не
стирает последний успешный результат, а частичный batch сохраняет status,
counters и lineage отдельно для каждого документа.
Старые значения только в `data.pipeline_status` не содержат dataset provenance
и больше не считаются доказательством готовности: для них требуется reprocess.
Несколько inline texts, один document в двух datasets, source replacement,
alias transfer, stale attempt, откат batch claim и success→failure→unattempted
покрыты отдельными SQLite/PostgreSQL regressions.

`query_entity` проверяет publication/grant для узла, ребра и обоих endpoints,
не удерживает SQL cursor во время вложенных policy-запросов, проходит больше
одной страницы неподтверждённых assertions и повторно проверяет источники перед
HTTP-ответом. Legacy dataset-only graph остаётся доступен только пока в dataset
нет зарегистрированных документов.

Серверный `git_search`, как и `analyze_commits`, теперь требует активного
instance administrator и заявляет это в MCP descriptor. Dataset activity
фильтрует каждый document ref и не показывает title/status restricted-документа
пользователю только с dataset grant. Cognify start/status возвращают structured
payload по заявленной schema, включая bounded stage events; пустые success-ветви
`query_entity`, `git_search` и `analyze_commits` также schema-valid.

Наблюдаемый gate: полные `pkg/mcp`, `pkg/orchestrator` и `pkg/access` PASS с
`-race`; 21 профильный HTTP/MCP сценарий PASS на SQLite и изолированном PostgreSQL 16;
`make contract-check` PASS. Полный `internal/http -race` также PASS: 1189
тестов/подтестов, 0 FAIL; единственный skip — opt-in DCD load-baseline.

С реализацией согласованы:
[продуктовая лестница](../product-ladder.md),
[руководство по идентичности](../enterprise-identity.md),
[управление документами](../document-management.md),
[Git-коммиты](../recipes/git-commits-to-brain.md) и свежий раздел
[testing](../testing.md). Остальная документация остаётся частью общего release
gate и должна обновляться только вместе с подтверждённым поведением.
Сценарии документов — в
[приёмочной матрице](../document-workflow-scenarios.md).

### Закрытый preflight и source replacement 2026-09-10

REST multipart теперь читает и локально проверяет весь batch до первого
structured sidecar call. Точный dataset разрешается до egress; credential,
API-key write scope и текущая dataset/document policy повторно проверяются под
bounded SQL fence. Неверный второй файл вызывает ноль sidecar calls, а partial
sidecar failure и timeout не публикуют raw/SQL/structured artifacts; повторная
попытка проходит как новая операция.

Для `/add` добавлен явный single-file replacement режим с точными `datasetId`,
`datasetName`, `replace_data_id`, `source_revision` и `raw_content_hash`. CAS повторяется до и
после immutable storage writes. Stale и concurrent writers получают 409,
storage failure очищает attempt, успешная замена повышает source/content
revision и удаляет старую publication. Shared physical `data.id` пока
отклоняется, чтобы не менять несколько dataset inclusions одним запросом.
SQLite/PostgreSQL race regressions и HTTP failure matrix проходят.

### Закрытый lifecycle structured artifacts 2026-09-10

Structured JSON теперь сохраняется внутри того же immutable ingest attempt,
что исходник и projection. SQL inventory фиксирует artifact ID, SHA-256, размер,
storage location и точные `source_revision`/`raw_content_hash` в транзакции
публикации. Save и SQL inventory failures очищают все объекты и journal на
SQLite/PostgreSQL; ответ upload не раскрывает `file://`, `storage://` или key.

Добавлен authenticated API download только для active artifact текущей source
lineage. Перед передачей проверяются document ACL, credential/source fence,
размер до 16 MiB, SHA-256 и JSON. Подтверждены owner, direct user grant, group
grant, revoke и удаление участника группы. Replacement A→B→A не переиспользует
revision или artifact ID; старый URL закрывается до physical cleanup.

Точный A→A повтор сохраняет прежний active artifact. Изменение только JSON при
неизменной projection повышает source/content revision и участвует в том же CAS:
из двух concurrent writers проходит один. Обычный artifact-only write к
registered document без CAS отклоняется. Дубликаты в одном batch используют
один объект и одну inventory-запись.

Replacement, обычный re-ingest, rename, удаление последней dataset-связи,
dataset delete и prune сначала
переводят artifact в `retired` в SQL. Hold блокирует изменение. Local/remote
cleanup идемпотентен; отказ backend возвращает `artifact_cleanup_pending` и
оставляет строку для retry. Общий source сохраняет active artifact, пока остаётся
хотя бы одна dataset-связь. Verified backup распознаёт новый inventory path.

## Идентичность и права — P1

**Реализовано локально:** LDAP/LDAPS/StartTLS с проверкой сертификатов,
безопасными bind/search/filter escaping, стабильными AD/LDAP ID и bounded nested
groups; browser OIDC с PKCE/state/nonce, SAML с подписью/replay checks,
SQL-сессии/logout и безопасный redirect. Provisioning связан со входом по
issuer/subject без склейки по email. Добавлены SCIM Groups, EnterpriseUser subset,
provisioning audit, деактивация и отзыв ключей. Это выбранный subset, не полный SCIM.

Для документов написаны ACL пользователей/групп, наследование, tenant boundary,
CAS, tombstones/hold и проверки происхождения результатов. **Публичный API
управления документными правами ещё не подключён.** Имеющиеся helpers не
закрывают пользовательский сценарий sharing до интеграции всех путей.

Оставшаяся сквозная и внешняя приёмка (часть негативных сценариев уже проверена
локально):

- Проверить два равноценных корпоративных пути: федерацию AD через IdP и
  прямой LDAP/LDAPS на реальных AD/IdP. Для LDAP нужны проверка сертификата,
  безопасные bind/search, ограниченная service account, escaping фильтров,
  nested groups, disabled/locked account и недоступность каталога.
- Подтвердить browser login/callback/session/logout на целевой конфигурации.
  Приёмка: неверные issuer/audience, неподписанный или повторный ответ, nonce/state, clock skew,
  expiry, rotation, logout и безопасный redirect. Проверка bearer сама по себе
  не закрывает браузерную сессию. Локальный logout не обещает IdP single logout.
- Подтвердить связку provisioning и входа по стабильному ID без склейки по email.
  Проверить rename, повторный create, конкуренцию, email conflict, deactivate,
  re-provision и уже выданные JWT/API-ключи. Отзыв должен действовать на всех
  поверхностях, включая активные сессии и фоновые операции.
- Завершить доступ к документу пользователю/группе внутри организации: назначение
  и отзыв роли, понятное наследование, аудит, tenant boundary. Смена групп и
  отзыв доступа должны применяться до поиска, rerank, graph expansion,
  чтения raw/artifacts и отправки контекста в LLM. Проверить прямой URL,
  чужой ID, кэши, выгрузки и concurrent revoke.
- Согласовать эксплуатационный контракт реализованных SCIM Groups,
  provisioning audit и EnterpriseUser subset; проверить membership и tenant
  enrollment. Определить своевременное применение изменений LDAP:
  непрерывного directory change feed пока нет.

В аналитические read models agent trajectories / memory behavior добавлены
owner/tenant-фильтры до агрегации, экспорта и чтения отдельных объектов.
Остаётся общий regression gate; collection/client селекторы не заменяют ACL.

## Task Runtime — P1/P2

Помимо CAS/leases, receipts/checkpoints, bootstrap, read-only Tasks в WebUI
и opt-in scheduler реализован executor **workspace_read/workspace_write**:
authority manifest/digest/path checks, реальные эффекты и receipts, deadline,
budgets, concurrency/retry и kill-switch. Shell/network в его область не входят;
это не универсальный sandbox. Локально исправлен reclaim orphan steps после
истечения lease; работающий старый runtime эту правку ещё не получил.

Оставшиеся критерии:

- P1: подтвердить полный lifecycle реального executor с разрешённой областью, deadline,
  budgets, ограничением concurrency, retry/backoff и kill-switch. Три шага
  должны выполнить наблюдаемые действия и оставить receipts; no-op успех
  не считается выполнением работы.
- P1: enforce authority в каждой точке вызова tool/file/network, включая
  redirects, symlinks, смену манифеста, операции после expiry и передачу
  полномочий между шагами. Запрещённое действие должно отклоняться до эффекта.
- P2: если нужны UI-мутации, использовать те же version/lease/idempotency
  примитивы. Проверить stale version, чужую задачу, restart, длинный план,
  большие списки и согласованность MCP/WebUI. Текущий refresh — не обещание
  обновления за пять секунд.
- P2: подтвердить recovery, гонку worker/ручного клиента, исчерпание попыток,
  зависший шаг и deadlock на реальном executor. Выход из ограниченной стадии
  требует воспроизводимого полного lifecycle на трёх последовательных
  релизах; наличие CI load gate или стабильной схемы не заменяет эту историю.

Для gated memory commit реализованы live credential checks, связь task/receipt,
owner/collection/workspace revision, проверка результата и bytes/digest доступного
артефакта. `receipt-validated` означает проверку этих связей, не истинности текста;
по умолчанию используется `unverified`. Preview сохраняет SQL-план с содержимым.
Остаются реальный MCP→HTTP artifact verifier при SQL pool=1, повторные revoke/
evidence-change проверки на общем diff и обновление descriptor/schema.
Руководство: [memory commit](../memory-commit.md).

## Корпоративное хранение и аудит — P2

**Реализовано локально:** AWS S3 SDK, range/ETag, multipart resume/abort и
reconciliation; AWS KMS/BYOK envelope encryption, rotation/old-key read,
bounded TTL-cache и метрики. Для аудита добавлены SQL-spool, webhook,
bounded retry/dead-letter, стабильные event ID, at-least-once delivery и CLI.
Это локально проверенные выбранные backends; внешняя приёмка ещё открыта.
GCS/Azure/Vault добавлять при подтверждённой потребности в конкретном backend.
Руководство: [хранение и аудит](../enterprise-storage-audit.md).

Таблица сохраняет критерии итоговой приёмки, даже если отдельные локальные
контрактные тесты уже прошли:

| Возможность | Критерий приёмки | Ошибки и границы |
|---|---|---|
| KMS/BYOK | Выбранные backend, например AWS KMS/Vault Transit: envelope encryption, TTL кэша ключей, версии, rotation и чтение старых объектов; конфигурация и метрики | Удалённый ключ, повреждённый envelope, outage, timeout и истечение кэша; ограниченная очередь, явная ошибка |
| Object storage | Общий put/get/list/delete/range contract, побайтовый roundtrip; encryption, region/residency и lifecycle recipe | Большие multipart, Unicode, пустые объекты, concurrent write, network flap; resume/abort и восстановление без частичного успеха |
| SIEM | Webhook/syslog либо выбранный sink: схема, audit actor, batch, bounded retry, dead-letter и документированная at-least-once доставка | Медленный/недоступный приёмник, duplicate retry, большой event, заполнение буфера; потеря не должна быть молчаливой |
| Legal hold | Авторизованная установка/снятие, audit; блокировка delete/overwrite на API/storage/sync путях | Hold во время удаления, parent/child и sync-конфликт; чтение/экспорт продолжают работать, потеря данных не допускается |

Не добавлять вымышленные env-флаги в руководства до появления реализации.
Строгий Enterprise preset проверяет обязательную конфигурацию; end-to-end
проверка конкретного IdP, storage или SIEM остаётся отдельным gate.

## Документы, поиск и модели — P2/P3

**Реализовано локально:** общий authorized ingest coordinator подключён к
REST/Web, MCP add и gRPC. Права проверяются до Save; точные повторы не создают
новый Save; неизменяемые объекты попытки и журнал защищают от partial failure.
Автоматический offline recovery ограничен standalone/local SQLite с
исключительным владением записью. Source versioning в обеих SQL исключает
устаревшие публикации после A→B→A и удаления/повторного создания источника.
Проверки raw/artifact доступа и происхождения результатов также добавлены.

Текущий проход закрыл inline HTTP/MCP, legacy/registered publication,
missing-file/malformed-hash fixtures, partial/stale worker, direct document
grant/revoke и graph egress на обеих SQL. Следующий порядок интеграции:

- P1/P2: завершить оставшуюся transport matrix обычной загрузки: CLI как клиент
  реального endpoint, составной gRPC batch, cancellation/rollback и поздний
  worker. Текущий inline MCP тест покрывает legacy/latest MCP, но не заменяет
  полный upload matrix.
- P1/P2: закрыть raw/vector/collection mutations и составные gRPC writes,
  sync graph/collection import, reembed/migration/dualwrite/backfill,
  workspace write/revert/GC. Legacy delete/rename должны повторять авторизацию
  внутри транзакции изменения, закрывая окно конкурентного отзыва.
- P1/P2: распространить legal hold на все эти пути. Coordinator и атомарный
  hold-aware prune уже защищены; это не доказывает защиту остальных операций.
  После сквозной проверки подключить API ACL и сценарий выдачи/отзыва прав
  через Web/API/консоль, включая прямые URL, group membership и tenant boundary.

Оставшиеся критерии качества и жизненного цикла:

- P2: качество извлечения на размеченном корпусе PDF/Office/HTML/таблиц,
  включая минимум десять разных PDF: сканы, несколько колонок, таблица между
  страницами, кириллица, защищённые/повреждённые и большие документы. Базовые
  парсеры и structured-extraction route существуют; проценты точности требуют
  реальных запусков OCR/schema sidecar и проверки фактов/чисел/ссылок на страницы.
- P2: provenance и удаление должны покрывать raw, extracted/structured artifacts,
  chunks, embeddings, graph и ответы. Проверять повторный импорт, partial failure,
  tombstone, удаление/отзыв во время обработки и отсутствие недоступного текста
  в LLM payload. Не считать наличие одного ACL helper сквозным доказательством.
- P2: удобная установка embed/rerank вместе с выбранным preset. Текущие endpoint
  adapters уже работают; нужны проверенные рецепты, timeout/partial-response
  fallback, ACL-before-rerank, метрики и качество на одном corpus.
- P3: встроенные embeddings только при подтверждённой потребности в едином
  бинарнике. Приёмка: offline без модели, повреждённый cache, ARM/AMD64,
  ограниченная RAM и явная переиндексация при смене пространства embeddings.
- P3: для graph extras и экспериментального routing принять решение
  promote/keep-flag/remove на основании quality/performance gates. DCD filtering
  ещё не равнозначен реализованным observe/boost; его подробные ограничения
  ведутся в локальном DCD architecture roadmap.
- P3: отдельный план compatibility/sunset legacy vector API с измерением
  использования, сроком миграции, обновлением собственных клиентов и минимум
  двумя релизами наблюдения. Существующие endpoints остаются действующими.

## Эксплуатация и интерфейсы — P2/P3

**Реализовано локально:** CLI `team apply` с offline validation/dry-run,
приватным журналом и повторяемым созданием local-password users/keys/datasets/
grants; реальные локальные HTTP/CLI и проверки A/B на обеих SQL.
Нет автоматического tenant/admin provisioning. Рецепт и ограничения —
[подключение команды](../team-onboarding.md).

Verified backup включает SQL/workspace/raw/structured и нужные native indexes,
checksums, restore в новые ресурсы и дату успешной проверки. Проверены локальные
SQLite/PostgreSQL и Linux fixtures. Область — **offline standalone/local**,
не общий online/multi-node/cloud-object backup. Подготовленные service/timer
ещё не активированы. В sync исправлены errors/counts/ACK и проверены локальные
roundtrips двух SQL peers; двухнодовый интерфейс пока не расширен.

Оставшаяся интеграционная и эксплуатационная приёмка:

- P2: подтвердить Team onboarding на общем diff с dry-run и повторяемым созданием пользователей/ключей,
  коллекций и индивидуальных grants. В конце — проверка входа и отрицательная
  проверка доступа A/B. Дубли email, Unicode, отказ сервера и частичное выполнение
  должны давать понятный отчёт без утечки admin token. Документировать
  предварительный tenant enrollment, rate limits и неопределённую выдачу ключа.
- P2: расширить существующую sync-страницу до наблюдения двух независимых нод,
  pending/lag и объяснения конфликтов. Проверить offline peer, пустую историю,
  clock skew и обновление состояния. Сам экран sync уже есть.
- P2: довести эксплуатационную приёмку verified backup по расписанию: SQL + workspace + raw/structured artifacts
  и необходимые индексы, восстановление в sandbox, дата последнего успешного
  восстановления. Проверить активные записи, повреждённый архив, полный диск
  и несовместимую версию. Отдельно решить backup/restore с непустым ingest journal
  и изменившимся storage destination; старые ссылки очистки нельзя переносить
  без проверки. Сохранить явные offline/local границы в отдельном backup recipe.
  Наличие команды backup не доказывает recoverability.
- P3: Prometheus rules/examples уже поставляются. Нужны согласованные SLO,
  применимость к выбранной конфигурации, promtool и проверка искусственного
  инцидента; учитывать maintenance, flapping и missing series.
- P3: для Raft/sharding зафиксировать проверенную область эксплуатации либо
  явно оставить experimental; нужны durable metadata, recovery, partition,
  membership и многонодовый E2E до заявлений о production readiness.
- P3: расширять workspace dashboard/evaluation только для подтверждённых
  пробелов текущей страницы: lag, freshness, explainability и correctness
  source citations. Не создавать второй backlog уже реализованных операций.

## Документация, доказательства и выпуск

- Обновить оставшиеся руководства identity/document management/Task Runtime,
  сценарии, README, product-ladder, configuration recipes и маркетинг. Удалять
  устаревшее; актуальное согласовать с проверенной областью реализации.
- Дополнить testing свежими командами, ревизией, результатами и ограничениями.
  Прежние прогоны сохранять только с исторической датой. Браузерные 56 PASS
  использовали подменённые API-ответы; отдельная proxy/cookie fixture не заменяет
  живой IdP. Ссылки на текущие свидетельства и внешние пробелы — в [resume](../../resume.md).
- Обновить descriptors/input/output/dispatch/profile visibility, затем
  последовательно `make contract` и `make contract-check`; generated contracts
  не редактировать вручную.
- Получить зелёные `make test-commit`, затронутые PostgreSQL/SQLite suites
  и WebUI checks на итоговом diff; выполнить независимое интеграционное ревью.
- Сделать scoped commits с сохранением посторонних изменений. Внешние и
  многорелизные критерии оставлять открытыми до фактической приёмки.

Порядок продолжения: документы/отзыв/hold → совмещённые проверки и контракты →
документация и коммиты → качество, эксплуатация и внешняя приёмка.

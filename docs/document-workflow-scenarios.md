# Матрица приёмки документов и корпоративного доступа

Аудит 2026-09-14 продолжает проверку исходных материалов и текущего рабочего дерева: активные руководства,
контракты, инструкции агентов, примеры, локальные маркетинговые страницы и
исторические заметки. Материалы отделены от исходников и генераторов;
числа ниже описывают конкретные проверки, а не «полное покрытие продукта».

Обозначения: **PASS** — выполненная локальная проверка; **SOURCE** —
подтверждено чтением кода; **MANUAL** — нужна интеграционная среда или
размеченный корпус; **GAP** — требуемая функция отсутствует. PASS моков
не означает успешной работы реального AD, S3, OCR или LLM.

## Идентичность и каталог

| ID | Сценарий и ожидаемый результат | Статус / проверка |
|---|---|---|
| I01 | OIDC RS256/ES256: правильные signature/issuer/audience/time принимаются; неверные отклоняются | PASS: `pkg/auth` |
| I02 | JWKS: localhost.example/userinfo/hostless/fragment не обходят HTTPS; TLS→HTTP redirect не выполняется | PASS: `TestOIDCJWKSTransport` |
| I03 | Кэш/ротация ключей, неизвестный kid, недоступность IdP | PASS локальных verifier checks; MANUAL реальной ротации |
| I04 | Два подписанных SAML-входа завершаются в прямом и обратном порядке нужными пользователями | PASS: `TestSAMLConcurrentBrowserFlows` |
| I05 | SAML cookie отсутствует/чужой/подменён/истёк, неизвестный ID, signature failure | PASS: HTTP SAML rejection tests |
| I06 | Повтор assertion и восемь одновременных повторов | PASS: один успех; остальные отказ |
| I07 | SCIM create → rename → disable → GET/filter/list → повтор POST | PASS: SQLite + PostgreSQL16 |
| I08 | Конфликт email и поздний SQL сбой не меняют ни active, ни email | PASS: `TestSCIMUpdateAtomic`, оба диалекта |
| I09 | Чужой issuer/local user/неизвестный ID недоступны для GET/PATCH/DELETE | PASS: `TestSCIMIssuerIsolation`, оба диалекта |
| I10 | Прямой LDAP/AD: bind/search, CA/StartTLS, отключённый пользователь, DC failover | PASS локального connector/test LDAP; MANUAL реального AD, CA rotation и DC failover |
| I11 | AD → Keycloak, AD FS и Entra выдача реального API token | MANUAL: нет тестового каталога/tenant в этой проверке |
| I12 | SCIM provisioning job Microsoft: connectivity/mappings/paging/rename/deactivate/resync | MANUAL: локальный HTTP subset не равен vendor certification |
| I13 | Удаление SCIM mapping блокирует уже выданную SSO-сессию и ключи | PASS локальных deactivation/credential checks; MANUAL сквозного vendor provisioning |
| I14 | Прямые/вложенные AD-группы, cycles, removal, sync delay | PASS bounded nested-group и local group-grant tests; MANUAL реального каталога и latency изменений |
| I15 | Browser OIDC/PKCE, WebUI session, logout и multi-node SAML | PASS локальных browser/session/logout tests; MANUAL реального IdP и multi-node pending store |

## Загрузка и качество

| ID | Сценарий и ожидаемый результат | Статус / проверка |
|---|---|---|
| U01 | Одинаковое имя, разное содержимое/владельцы не связывают чужие байты | PASS: `TestIngestFilenameDoesNotAliasAnotherDocument` |
| U02 | Одинаковые байты под разными именами/owners всегда имеют существующий blob | PASS: `TestIngestDuplicatesAlwaysReferenceExistingContent` |
| U03 | Blob пережил SQL failure: retry создаёт metadata и link | PASS: `TestMetadataConsistency`, оба диалекта |
| U04 | Ошибка создания dataset/link/data и конфликт уникального имени откатывают SQL batch | PASS: metadata consistency и HTTP upload failure |
| U05 | JSON с charset, кириллицей и dataset_name сохраняет только data | PASS: `TestUploadJSONMediaTypeAndMetadataFailure` |
| U06 | Повреждённый PDF, аудио без backend, пустой/бинарный TXT в смешанном batch | PASS: 422, ни одной SQL записи batch |
| U07 | Оригинал HTML отличается от текста; оба скачиваются без подмены | PASS: local + in-memory remote storage, `TestUploadPreservesOriginalAndExtractedText` |
| U08 | PDF две страницы: контрольные числа/фразы сохранены | PASS: actual parser, `TestDocumentQualityFixtures/report.pdf` |
| U09 | DOCX: кириллица, текст и таблица | PASS: synthetic fixture parser |
| U10 | PPTX: текст слайда и кириллица | PASS: synthetic fixture parser |
| U11 | XLSX: строки, кириллица, числовые ячейки | PASS: synthetic fixture parser |
| U12 | HTML, Markdown и CSV дают контрольный факт без HTML контейнера | PASS: synthetic parser tests |
| U13 | Все документы завершены именно для выбранной коллекции | PASS: `TestPipelineStatusRequiresEveryDocumentInCollection` |
| U14 | CLI file/text/URL, Unicode dataset, HTTP/JSON/read failures, redirects | PASS: subprocess + isolated HTTP tests; file/text против реального authenticated `/add`, exact bytes и повторный dataset — SQLite/PostgreSQL |
| U15 | CLI cognify принимает точное имя/ID; неизвестное/неоднозначное отвергает | PASS: `TestCLICognify` |
| U16 | PDF таблица → schema → structured JSON/projection; неверный второй файл не вызывает sidecar; partial failure/timeout не публикуют batch, retry проходит | PASS локального preflight/failure matrix с fake extractor; DB-backed artifact публикуется одним ingest attempt и не раскрывает storage location; не качество реальной модели |
| U17 | OCR скана/изображения: CER/WER, поворот, шум, мелкий шрифт, язык | MANUAL: размеченный корпус и реальный OCR backend |
| U18 | Whisper: речь/тишина/разные языки/длинные записи/отказ сервиса | MANUAL: реальный backend; unconfigured audio отрицательный контроль PASS |
| U19 | Большой/зашифрованный/сложный документ, zip expansion, timeout/cancel, memory pressure | MANUAL: отдельный ресурсный прогон; простой fixture не доказывает защиту от всех parser bombs |
| U20 | Реальный S3: потеря сети, retry, presign expiry, восстановление после рестарта | MANUAL: in-memory storage проверяет кодовую границу, не провайдера |
| U21 | Upload/inline → cognify → BM25/vector с точным источником и вторым dataset | PASS: HTTP inline, MCP legacy/latest и `TestDocumentACLCognify*`, fake embedding + реальные индексы, обе SQL |
| U22 | Явная замена source использует revision/hash CAS; stale/concurrent writer не заменяет winner, storage failure очищает attempt | PASS: HTTP SQLite и `ReplaceAuthorized` race на SQLite/PostgreSQL; A→B→A создаёт новые revisions/artifact IDs, старый artifact сразу недоступен и cleanup retry сохраняется при отказе storage; shared physical source получает 409. Crash после внешнего sidecar остаётся GAP |
| U23 | Повтор cognify возвращает already_processed без polling; изменённый source сбрасывает старую готовность | SOURCE/PASS отдельных status/source-version tests; активный HTTP cognify всегда создаёт run, поэтому общий product contract ещё не закрыт |
| U24 | gRPC batch: ID-only dataset, mixed item names, dual/empty payload, invalid item N, duplicate, partial Save, cancel и поздний backend | PASS: real bufconn + auth interceptor + coordinator, SQLite/PostgreSQL; ноль partial SQL/object/journal publication. `PipelineCognify` остаётся отдельным global-admin raw pipeline и не считается document cognify |
| U25 | gRPC document cognify rag/graph: exact sources, duplicate/alias, all-source writer preflight, SQL claim rollback, detached observer, status owner/tenant/group revoke и Send fence | PASS: `TestGRPCDocumentCognify*`, реальный JWT/bufconn, embedding/LLM fixtures и SQL/vector/graph publication; SQLite/PostgreSQL с race и pool=1. Blocked Send моделируется callback; реальный HTTP/2 flow-control load остаётся отдельным прогоном |

## Права и жизненный цикл

| ID | Сценарий и ожидаемый результат | Статус / проверка |
|---|---|---|
| A01 | MCP delete проверяет права записи на объект; prune требует admin; SQL failures не скрываются | PASS: `TestDocumentACL*`, SQLite + PostgreSQL |
| A02 | MCP add сохраняет authenticated owner | PASS: MCP пакет + оба HTTP транспорта |
| A03 | list_data с room/tags сохраняет dataset ACL | PASS: отрицательные и положительные transport tests |
| A04 | Имя своего dataset совпадает с ID чужого: чужой dataset/collection не появляется | PASS: отдельные ID/name sets, оба транспорта |
| A05 | REST cognify авторизует все источники до чтения; чужой источник отклоняется | PASS: cognify authorization tests |
| A06 | Два документа/два набора не теряют source attribution; общие source bytes не дают коллизии chunk/parent ID | PASS: реальные vector/BM25 индексы и `TestDocumentACLChunkIdentity` |
| A07 | Подмена dataset ID при revoke чужого share не удаляет grant | PASS: SQLite + PostgreSQL |
| A08 | Повтор grant возвращает сохранённый ID и обновляет роль | PASS: SQLite + PostgreSQL |
| A09 | viewer не пишет; editor пишет и может удалить набор; read-only API key не повышается share | PASS: scoped ACL regressions |
| A10 | Удаление связи в A сохраняет SQL/raw/index copy документа в B | PASS: source association control; это не полная очистка A |
| A11 | Per-document и group permissions, запрет grant вне организации | PASS: REST/CLI/WebUI user/group grant/revoke, document-scoped active recipients, shared-document discovery, tenant/principal checks, CAS/409 refresh и audit; SQLite + PostgreSQL, 3 mocked-browser flows. GAP: реальный AD/IdP/SCIM стенд |
| A12 | Удалённый файл исчезает из всех vector/BM25/graph/community/VSA/RAG источников и MCP | GAP: SQL-only delete не гарантирует этого |
| A13 | Удаление/изменение во время cognify не допускает публикации старых derivatives | PASS для cognify source revision/revoke/publication; GAP для остальных mutation/sync/reindex путей |
| A14 | Старые chunks без document_id, SQL/Neo4j граф и aggregates имеют проверяемый источник | GAP: нужна миграция/перестройка и полная provenance |
| A15 | MCP query_entity проверяет publication/grant у узлов, связей и обоих концов; глобальные communities доступны только активному instance admin | PASS: SQLite + PostgreSQL, legacy/latest MCP, direct document revoke, stale generation и >128 неподтверждённых assertions; это не гарантия всех graph/RAG путей |
| A16 | Отзыв grant закрывает ранее выданный presigned URL/скачанную копию | Не поддерживается по природе этих копий; использовать authenticated proxy для новых запросов |
| A17 | MCP prune_graph требует активного instance admin и права записи API-ключа, включая dry-run | PASS: `TestGraphACLPrune` и transport controls; обычный пользователь не получает счётчики и не удаляет граф |
| A18 | Graph prune: ошибки SQL откатывают весь batch; dry-run предсказывает удаляемые узлы, даты сравниваются с учётом timezone | PASS: `TestPruneRegression*`, SQLite + PostgreSQL; preview/apply сравниваются без одновременного изменения графа |
| A19 | Structured artifact доступен только по document ACL и текущей source lineage; replacement/re-ingest/rename/delete/last alias/prune закрывают старый URL до physical cleanup | PASS: owner, user/group grants и revoke, hold, shared alias, exact A→A, artifact-only CAS race, equal-projection re-ingest, duplicate batch local/remote, cleanup retry и SQLite/PostgreSQL inventory tests |

Проверки A12–A14 — блокеры заявления «полная изоляция и отзыв документа во
всех каналах». До их закрытия нельзя считать один успешный negative search
тест доказательством безопасности всего многоарендного развёртывания.
Отключение отдельных MCP-инструментов само по себе не доказывает защиту
остальных graph/RAG путей.

## Что проверяет браузер

Браузерные проверки должны утверждать конкретный dataset ID и байты
multipart, ответ ошибки, retry того же файла, terminal status обработки,
скачивание с аутентификацией и отказ 403. Проверки вида «страница не упала»
или «есть результаты либо No results» не проверяют загрузку и поиск.
Выполнено: 16/16 профильных проверок upload-flow и 42/42 тестов общего
curated browser suite. TypeScript и scoped ESLint прошли. Браузер настоящий,
ответы API замоканы; реальные backend/индексы проверены отдельно. Полный
обычный и race-прогон девяти Go-пакетов основного исправления также прошёл.
После graph/prune патча прошли обычный прогон десяти пакетов, race всех
тестов MCP/community и четырёх graph HTTP transport сценариев. Это не
доказательство интеграции с рабочим AD, OCR или объектным хранилищем.

## Повторение локальных проверок

```sh
go test ./pkg/ingest ./pkg/extract ./pkg/auth ./pkg/access ./pkg/mcp ./pkg/community ./pkg/orchestrator ./cmd/server ./cmd/cli ./internal/http -count=1
go test -race ./pkg/mcp ./pkg/community ./internal/http -run 'TestGraphACL|TestPruneRegression' -count=1
make contract-check
make profile-config-check
```

PostgreSQL-подтесты используют только явно заданные тестовые DSN; без них
они пропускаются. Логи должны показывать PostgreSQL subtest и PASS, а не
только общий exit 0. Для PDF test с ReportLab нужен `LEVARA_TEST_PYTHON`
с доступной библиотекой. Зафиксируйте версию исходников, команды, exit codes,
список skips и реальные зависимости. Не запускайте integration-suite
WebUI с default backend на рабочем сервере.

## Приоритеты после этого прохода

1. **Изоляция и отзыв:** единая граница разрешений до rerank/LLM и во всех
   MCP/graph путях; provenance, tombstones и защита от повторной публикации.
   Затем document ACL и групповые grants с организационной membership.
2. **Корпоративная идентичность:** принять на реальном AD/IdP уже реализованные
   immutable identity linkage, деактивацию, browser OIDC, LDAPS и группы;
   отдельно проверить multi-DC failover и задержку применения membership.
3. **Приёмка интеграций:** настоящий AD/LDAP, Keycloak, AD FS/Entra, SCIM
   provisioning job, OCR/Whisper и S3 на изолированном стенде.
4. **Измеряемые оптимизации:** лимит параллельного извлечения и размеры
   batch, память multipart/CLI, серверная пагинация списка файлов, повторное использование parser и
   embedding, selective reindex. Измерять p50/p95, RSS, throughput и
   качество поиска на одном корпусе до/после. Не публиковать ускорение
   без сохранённого benchmark и неизменной точности/изоляции.

Практические команды: [документы](document-management.md).
Настройки и приёмка каталогов: [LDAP/AD и SSO](enterprise-identity.md).

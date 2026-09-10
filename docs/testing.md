# Проверки и результаты тестирования

Обновлено 2026-09-10. Результат теста относится к указанным исходникам,
зависимостям и сценарию. Наличие теста или зелёный статус benchmark-скрипта
само по себе не подтверждает качество поиска, изоляцию пользователей или
работу реального внешнего сервиса.

## Document ACL REST и global-resource gate — 2026-09-10

На текущем рабочем дереве подключены document policy/user/group routes и
проверен административный барьер для глобальных raw-vector, collection,
dual-search, reembed и embedding-migration endpoints в required-auth режиме.

| Проверка | Наблюдаемый результат | Граница доказательства |
|---|---|---|
| `LEVARA_TEST_POSTGRES_DSN=... go test -race ./pkg/access ./pkg/audit ./internal/http -count=1` | PASS: access 20.876s, audit 4.829s, HTTP 300.082s | Полные suites; SQLite + PostgreSQL там, где сценарии объявлены для обеих БД. В том числе 28 сочетаний API key/JWT session × 7 ACL/group mutations × 2 SQL |
| `make contract-check && go test ./docs` | PASS | Generated REST contract содержит 11 document/group routes, включая scoped recipients и shared documents |
| `make test-commit` | PASS: S0–S4; HTTP 46.638s, server 3.345s | Docs, access/profile/audit/workspace/MCP, core engine, полный HTTP package и server bootstrap |

Обычный пользователь и неактивный superuser получают 403 до вызова глобального
handler; активный superuser проходит. Локальный no-auth профиль сохраняет
legacy compatibility. Document audit различает `success`, `denied` и `failure`;
SQL spool/webhook получает только проверенные actor/tenant и ограниченные
`resource`/`target`, без содержимого, credentials и произвольной metadata.
Отзыв API key или browser session после проверки middleware, но до ACL handler,
возвращает 401, не меняет policy/group rows и пишет только `denied` audit.
`POST /search` однозначно остаётся legacy raw-vector endpoint, а text search
доступен только через `POST /search/text`.

## Document sharing API, CLI и WebUI — 2026-09-10

| Проверка | Наблюдаемый результат | Граница доказательства |
|---|---|---|
| `LEVARA_TEST_POSTGRES_DSN=... go test -race ./pkg/access ./cmd/cli ./internal/http -count=1` | PASS: access 23.698s, CLI 51.741s, HTTP 309.990s | Полные affected suites. Discovery повторно проверяет API key/browser session после middleware; grant revoke, group removal и user deactivation не обгоняют отправку защищённого ответа на SQLite/PostgreSQL |
| `npm run lint && npm run build` | PASS | ESLint, TypeScript и production Next build |
| `npm run test:e2e` | 59 PASS за 37.4s | Весь curated Chromium gate; три document-sharing flow с API mock проверяют user/group grant, stale CAS refresh, revoke, registration и shared list; это не реальный AD/IdP/backend e2e |

## Публикация и статусы документов — 2026-09-10

На текущем рабочем дереве выполнены race-прогоны с SQLite и изолированным
PostgreSQL 16:

| Проверка | Наблюдаемый результат | Граница доказательства |
|---|---|---|
| `go test -race -p 1 ./pkg/mcp ./pkg/orchestrator ./pkg/access -count=1` | PASS: MCP 12.477s, orchestrator 1.565s, access 10.235s | Полные suites этих трёх пакетов |
| 21 профильный `internal/http` сценарий с `-race` | PASS: 33.399s | Inline HTTP/MCP legacy/latest, atomic attempt/publication, batch claim rollback, partial batch failure, exact counters/status/lineage, source replacement, activity ACL, graph и session provenance на обеих SQL |
| `LEVARA_TEST_POSTGRES_DSN=... go test -race ./internal/http -count=1` | PASS: полный пакет за 300.082s | Весь HTTP package; opt-in внешние/load проверки не считаются пройденными без их окружения |
| `make contract && make contract-check` | PASS | Generated MCP/API contract соответствует descriptors и schema |
| `go test ./docs -count=1` | PASS | Ссылки и заявленные capability markers; не внешний provider test |

Подтверждено, что run появляется только после сохранения server-assigned source
и атомарного claim всех источников; несколько inline texts получают отдельные
terminal statuses, counters и lineage. Publication и `COMPLETED` фиксируются
одной транзакцией по точному ключу dataset, document, collection, source
revision/hash и attempt; поздний worker не заменяет более новый запуск, а неудачный reprocess
не стирает последнюю успешную publication. Частичный batch сохраняет первый
успех, помечает failed/unattempted sources и откатывает весь claim, если один
source устарел. Замена source и перенос alias не возрождают старый `COMPLETED`. Старые значения
только в `data.pipeline_status` не имеют dataset provenance, поэтому после
обновления показываются как неизвестные до повторной обработки.

`query_entity` проверяет publication и document grants для всех assertions.
`git_search` и `analyze_commits` требуют активного instance administrator, а
dataset activity не возвращает metadata restricted-документа без прямого
доступа. MCP start/status payloads и пустые success-ветви `query_entity`,
`git_search`, `analyze_commits` проходят заявленный output schema, включая stage
events.

Широкий `internal/http` race-прогон теперь зелёный. Исправлены terminal barrier
и атомарная фиксация failure всех точных источников batch cognify, legacy dataset ACL для DCD/VSA/RBAC/tenant graph, атомарная
постановка memory delete в доступный outbox, схема `sync_status` и граница
workspace policy SQL. Полный release gate по-прежнему требует внешних сервисов
и остальных перечисленных ниже проверок; локальный HTTP gate закрыт.

## Structured upload и source replacement — 2026-09-10

| Проверка | Наблюдаемый результат | Граница доказательства |
|---|---|---|
| HTTP preflight/failure matrix с `-race` | PASS: неверный второй файл, sidecar failure при доступном локальном тексте, revoked credential, раздельные dataset/document права, timeout/retry, pre-sidecar replacement conflicts | Реальный Fiber handler, SQLite, fake structured extractor |
| `TestReplaceAuthorizedSourceCAS` с `-race -v` | PASS: SQLite + PostgreSQL 16 | Uppercase hash, content/source revision, publication invalidation, storage cleanup, stale CAS, shared source |
| `TestReplaceAuthorizedConcurrentCAS` с `-race -v` | PASS: SQLite + PostgreSQL 16 | Два одновременных writer: один success, один version conflict |
| `TestStructuredArtifactOnlyReplacementUsesSourceCAS` с `-race -v` | PASS: SQLite + PostgreSQL 16 | Разный JSON при одинаковой projection входит в source/content CAS: один writer success, второй conflict; no-CAS для registered document отклонён |
| `TestStructuredArtifactDuplicateBatch` с `-race -v` | PASS: SQLite + PostgreSQL 16, local + remote | Дубликаты получают общий artifact ID/path и одну inventory-запись |

Проверки не измеряют качество реального structured extractor. Инвентаризация
и retention проверены отдельно: SQLite/PostgreSQL publication rollback,
current-lineage API read, user/group grant и revoke, A→B→A, hold, shared alias,
точный A→A no-op, artifact-only replacement, обычный re-ingest при равной
projection, rename retirement, local cleanup и retry после отказа remote storage. Реальный S3/KMS и corpus
остаются отдельной приёмкой.

```sh
LEVARA_TEST_POSTGRES_DSN="$TEST_DSN" go test -race ./pkg/ingest ./pkg/access \
  -run 'TestStructuredArtifact|TestPruneRetiresStructuredArtifacts' -count=1
go test -race ./internal/http \
  -run 'TestStructuredArtifact|TestRESTRouteInventoryMatchesRegisterAPI' -count=1
go test ./pkg/structuredextract \
  -run 'TestClient(Extract|RejectsOversizedResponse)' -count=1
```

## Исторический прогон документов и идентичности — 2026-09-05

Исходники: `06bfa774cfb5bbadb8052c84e4b62ff1a7dd2fac`; среда: macOS arm64,
Go из `go.mod`, SQLite и изолированный PostgreSQL 16. Прогон выполнен
2026-09-05 после исправлений загрузки, OIDC/SAML/SCIM и graph ACL.
Четыре ранее существовавших локальных HTTP-изменения также входили в
проверенное рабочее дерево; это не утверждение о запуске CI на чистом commit.

| Проверка | Наблюдаемый результат | Граница доказательства |
|---|---|---|
| Десять затронутых Go-пакетов | PASS, exit 0 | Ingest, extract, auth, access, MCP, community, orchestrator, server, CLI, HTTP |
| Полные MCP/community suites с `-race` | PASS, exit 0 | Локальные регрессионные сценарии; не нагрузочная характеристика |
| Graph HTTP: SQLite/PostgreSQL × legacy/latest MCP | Все четыре варианта PASS с `-race` | Реальные HTTP handlers и SQL; тестовые JWT |
| PDF, DOCX, PPTX, XLSX, HTML, CSV, Markdown | Контрольные факты и числа сохранены | Реальные парсеры на небольших синтетических документах |
| WebUI curated suite | 42/42 Chromium tests PASS, из них 16 upload-flow | Настоящий браузер, замоканные API; не сквозной прогон с сервером |
| Build, Go vet, TypeScript, scoped ESLint | PASS, exit 0 | Проверенные пакеты и изменённые WebUI-файлы |
| Contract/profile checks | PASS, exit 0 | Контракты и конфигурация; не подключение корпоративных провайдеров |

Подробная [матрица сценариев](document-workflow-scenarios.md) связывает
поведение с именами тестов и отдельно обозначает PASS, SOURCE, MANUAL и GAP.
Локальные журналы и SHA-256 этого прогона сохранены в
`outputs/documentation-audit-2026-09-05/`; каталог не входит в опубликованную
документацию. Для внешнего отчёта приложите журналы нужного запуска, а не
ссылайтесь на отсутствие ошибок в чужой рабочей копии.

## Проверка обновлённого быстрого старта

2026-09-05 дополнительно выполнен временный loopback-стенд по
[getting started](getting-started.md): `-profile=standalone`, SQLite, без моделей.
`-config-check`, HTTP health, sessionless MCP `save_memory`/`recall_memory` и
повторный recall после остановки/перезапуска — PASS. Использованы отдельные
каталог и процесс; рабочие сервисы не менялись. Журнал и команда сохранены в
`outputs/documentation-cleanup-2026-09-05/quickstart-result.json` и
`quickstart-smoke.py`. Это проверка persistence, не semantic/OCR quality.

## Повторить проверки

Из корня репозитория, с Go-версией из `go.mod`:

```sh
go build -mod=readonly ./cmd/server ./cmd/cli
go test -mod=readonly ./pkg/ingest ./pkg/extract ./pkg/auth ./pkg/access ./pkg/mcp ./pkg/community ./pkg/orchestrator ./cmd/server ./cmd/cli ./internal/http -count=1
go test -mod=readonly -race ./pkg/mcp ./pkg/community -count=1
go test -mod=readonly -race ./internal/http -run '^TestGraphACLMCPTransports$' -count=1 -v
go test -mod=readonly ./docs -count=1
make contract-check
make profile-config-check
```

Для PostgreSQL задайте `LEVARA_TEST_POSTGRES_DSN` и
`LEVARA_TEST_INGEST_PG_DSN` на отдельную тестовую БД. Без этих переменных
PostgreSQL-подтесты пропускаются: общий PASS не означает проверку обоих
диалектов. Не подставляйте рабочую БД. HTTP PDF/schema тест с генерацией
ReportLab дополнительно использует `LEVARA_TEST_PYTHON`; обычные format
fixtures уже находятся в репозитории.

После установки WebUI-зависимостей и Chromium:

```sh
cd webui
LEVARA_API_URL=http://127.0.0.1:1 PLAYWRIGHT_PORT=3022 npm run test:e2e
npx tsc --noEmit
npm run lint
```

Недоступный backend здесь намеренный: curated suite использует API-моки.
Полный `npm run test:e2e:integration` имеет другую область и может обращаться к
заданному серверу; запускайте его только на отдельном стенде.

## Release Gates

| Команда | Что выполняет |
|---|---|
| `make test-commit` | Docs, access/profile/audit/workspace/MCP, core engine, HTTP и server |
| `make test-release-candidate` | Профили, adapter contracts, sync, backup/restore, workspace retrieval eval |
| `make profile-config-check` | Проверки сборки конфигурации без запуска рабочих сервисов |
| `make profile-smoke` | Dry-run бинарника с Personal, Solo Pro и Team presets |
| `make profile-enterprise-e2e` | Enterprise strict matrix и запуск сервера на PostgreSQL; оператор задаёт отдельную БД, см. ниже |
| `make test` | `go test ./...`; полный Go-набор, включая условные integration tests |

Перед `make profile-enterprise-e2e` обязательно задайте
`LEVARA_ENTERPRISE_E2E_DSN` на отдельную тестовую БД. Скрипт сам не требует
явного выбора: при отсутствии env он использует локальный DSN по умолчанию
и запускает сервер, который может применять миграции. Не считайте это
автоматической защитой от работы с неверной БД.

Точные команды принадлежат [Makefile](../Makefile) и
[CI workflow](../.github/workflows/go-ci.yml). Наличие gate в CI — не результат
его последнего выполнения. Pi-hardware, отдельные базы двух sync-узлов,
реальный AD/IdP, S3, OCR/Whisper и качество LLM требуют отдельных прогонов.

## Матрица приёмки профилей

Это требования к проверке конкретного стенда, а не отметки о выполнении.
Переключение профиля само по себе не доказывает перечисленные свойства.

| Профиль | Положительный сценарий | Обязательный отрицательный сценарий |
|---|---|---|
| Personal / Local | SQLite, restart, сохранение/recall и восстановление backup | Нет embed-сервиса: lexical доступен, semantic не объявляется проверенным |
| Solo Pro | Две независимые БД: push/pull, обновление и удаление, восстановление после разрыва | Чужой peer, redirect, просроченный токен, clock skew; старое значение после sync отсутствует |
| Team | Два пользователя и API-ключи, индивидуальный dataset grant/revoke, audit | Пользователь B не видит данные A через list/search/graph/raw/LLM; деактивированный пользователь отклоняется |
| Enterprise | `LEVARA_PROFILE=enterprise`, strict preset, tenant membership, файл аудита | Каждое отсутствующее обязательное поле блокирует старт; подмена tenant и недействительная identity отклоняются |

Для workspace проверьте `workspace_context -> workspace_write -> workspace_search`
и затем exact read с digest: индекс обновляется, stale evidence распознаётся,
symlink/path traversal и запись с устаревшей ревизией отклоняются. Проверка
`TenantFilterSQL` в `pkg/access` дополняется запросами через реальные handlers:
один SQL helper не доказывает, что все поверхности применили фильтр.

## Специализированные проверки

Следующие тесты используют синтетические fixtures. Они проверяют конкретный
инвариант и не измеряют качество на корпоративном корпусе или серверный QPS:

```sh
go test ./internal/http -run 'Test(DCD|SearchHandlerGraphContextArchitectureEval|VSAGraphContextABShowsRecallLift|VSAQuantitativeEval|VSABeforeSQLGraphPreservesTargetUnderBudget|PredicateSynonymMapLoadQualityAndSpeed)' -count=1 -v
```

Точные имена и opt-in условия находятся в `internal/http/*_eval_test.go`,
`dcd_vsa_*_test.go` и `graph_predicate_synonyms_load_test.go`.
Для Neo4j требуется отдельный доступный сервис и включение условий integration
test; пропуск не подтверждает parity PostgreSQL/SQLite/Neo4j.

Протоколы `scripts/consol_quality.py`, `benchmark/wb_project_eval.py` и
`scripts/load-profiles/` применяются только к отдельному тестовому стенду;
сначала прочитайте их `--help` и задайте явные URL, credentials и corpus.
Консолидацию оценивайте по сохранению SQL-записей, pins и покрытию фактов;
vector projection может обновляться позднее. Для project eval различайте
recall памяти и поиск файлов по активному workspace manifest.

При сравнении embedding/rerank моделей используйте одинаковый размеченный
корпус и полный pipeline на изолированном оборудовании. Keyword proxy не
заменяет IR-разметку. Помимо latency измеряйте качество после score-gap skip,
false-skip и fallback при недоступности модели. Старые неуспешные Pi-прогоны
не устанавливают текущую совместимость модели.

В MCP load report отдельно фиксируйте заданную arrival rate и фактическую
пропускную способность: total/wall time не равняется интенсивности поступления.
Запись JSONL на диск не доказывает полноту живой SQL/vector projection.
После interruption/restart требуется отдельная проверка восстановления.

## Automation Backlog

Нужны отдельные воспроизводимые сквозные стенды для AD/LDAP federation,
браузерного входа и logout, SCIM↔SSO identity, отзыва прав до отправки данных
в LLM, независимого sync и verified restore. Приёмка документов также требует
сканов, защищённых и повреждённых файлов, больших таблиц, повторной загрузки,
обрыва запроса и ошибок OCR/structured sidecar. Реализованные регрессии и
остающиеся GAP перечислены в [матрице документов](document-workflow-scenarios.md);
критерии разработки — в [едином backlog](product/unimplemented-roadmap.md).

## Как читать старые benchmark-результаты

Сохранённые JSON — исторические измерения, а не действующие обещания продукта.
Для сравнения нужны исходная ревизия, оборудование, конфигурация, corpus,
число запросов, команды и сырые результаты. Не переносите latency между
моделями, размерами коллекции и разными режимами авторизации.

У старого `benchmark/multi_user.py` есть ограничения методики:

- Клиенты не посылают Authorization: разделение коллекций не доказывает
  изоляцию разных аутентифицированных владельцев.
- Workspace contention gate допускает PASS при нуле успешных свежих записей.
- Sync-сценарий использует общую конфигурацию PostgreSQL, не проверяет
  HTTP-статусы синхронизации и отсутствие старого значения. Он не доказывает
  сходимость двух независимых БД.

Поэтому прежние «шесть успешных сценариев» не используются как доказательство
ACL, конкурентного редактирования или независимой синхронизации. Для этих
утверждений нужны соответствующие положительные и отрицательные проверки.
Оценки OCR без размеченного корпуса и фактического запуска не публикуются
как проценты точности, скорости или потребления памяти.

Перед публикацией результата укажите отдельно: прошедшие проверки,
пропуски, ошибки, использованные моки, ручные сценарии и отсутствующие функции.

# Document-scoped gRPC cognify

Дизайн и порядок реализации: **2026-09-14**. Задача Task Runtime:
`a531248a-e6d3-46a3-af60-c540af13d3d1`. Этот документ описывает контракт;
результаты проверок фиксируются отдельно в testing и roadmap.

## Проблема и решение — P1

`PipelineCognify` принимает inline texts и raw collection, позволяет задавать
provider endpoints и не содержит dataset/document/source proof. При required
auth он требует active instance superuser. Запуск после `IngestData` не создаёт
document-scoped publication автоматически.

Реализованы два server-streaming RPC: `CognifyDocuments` запускает обработку
точных сохранённых источников и показывает прогресс; `CognifyDocumentsStatus`
возобновляет наблюдение по server-issued run ID. Оба используют проверенного
actor/tenant/credential. Endpoints, storage и provider config берутся с сервера.
Raw `PipelineCognify` сохраняет совместимость и свою admin boundary.

Источник задаётся парой dataset/document, положительной source revision и
SHA-256. Необязательный положительный content revision служит дополнительным
CAS; 0 означает «не задан». Server-resolved content revision возвращается в статусе. От 1 до 100
источников; точные дубликаты сворачиваются, противоречивые proof одной пары
отклоняются. Collection обязательна; mode — `rag` (default) или `graph`.

## Обязательная последовательность

1. Проверить форму всего batch, mode, collection и source proof до SQL/storage.
   Startup требует DB, run registry, непустой embedding endpoint и collection
   store; один embed client без endpoint не включает vector stage.
2. Interceptor проверяет JWT/session epoch, active user и tenant selector;
   scoped RPC не требует global superuser. Missing auth не превращается в
   trusted-local при required auth.
3. Прочитать location/title всех источников, закрыть cursors. Под bounded read
   fence перепроверить credential, writer rights и точные source facts для всех
   документов до первого Load. Затем прочитать raw bytes, проверить SHA-256 и
   отмену запроса. Ни один model request ещё не выполнен.
4. Освободить read fence. Открыть metadata write fence; снова проверить
   credential, tenant, writer rights, content/source revision и hash каждого
   источника. Атомарно claim весь batch существующим attempt SQL helper.
   Ошибка любого item откатывает все claims. Commit предшествует run visibility.
5. Сохранить immutable run snapshot с server owner/tenant/source proof и run ID,
   равным attempt ID. Запустить общий HTTP background runner.
6. Приватный gRPC scope сохраняется при detach и замене evidence; live tenant
   membership перепроверяется под SQL fence перед model requests и публикацией,
   в том числе для legacy sources и superuser. Для каждого документа общий
   runner создаёт generation и свой lineage,
   применяет GuardTransfer/CheckWrite к provider requests и атомарно фиксирует
   publication вместе с точным COMPLETED status. Late/stale worker не заменяет
   новую source revision или новый attempt.
7. Каждый progress frame проверяет owner/tenant и все актуальные источники.
   Credential/source/tenant read fence удерживается до фактического завершения
   gRPC Send, включая истечение observer context; handler завершает RPC, чтобы
   прервать underlying transport Send. BEGIN/SQL checks отменяемы до передачи
   fence вызывающему коду. Между
   кадрами SQL fence освобождается. Unknown/denied run возвращает NotFound без
   title, source ID или внутренних backend errors.

## Границы жизненного цикла

- Контекст подготовки ограничен 30 секундами; отмена до claim не создаёт run/status.
- После commit job отделён от соединения и ограничен существующим
  `BACKGROUND_TASK_TIMEOUT_MS` (default 30 минут). Stream cancel/deadline
  прекращает наблюдение. Отдельной команды отмены принятого job пока нет.
- Потеря первого ответа после commit может скрыть accepted run ID от клиента;
  startup не обещает exactly-once. Для известного ID используется status RPC.
- Batch публикуется по документам: завершённые siblings сохраняются при сбое
  следующего; незавершённые получают точный FAILED, прежняя успешная publication
  сохраняется. Это существующий HTTP/MCP контракт.
- Status registry находится в памяти процесса; после restart или terminal TTL
  run ID недоступен. Durable document publication/status остаются в SQL.
- Inline input и trusted-local no-DB не входят в этот RPC: загрузка отдельно
  через IngestData/REST/MCP. Job cancel, inline gRPC cognify и legacy raw pipeline
  migration требуют отдельных решений. Общего raw-byte budget batch пока нет.

## Задачи, Definition of Done и тесты

| Порядок | Задача | Definition of Done | Corner cases и проверки |
|---|---|---|---|
| 1 | Контракт и call map | Аддитивные proto RPC/source/status; старые номера/методы сохранены; source/attempt/generation и detach semantics описаны | Descriptor отражает streaming; invalid/nil/mixed proof, empty collection, unknown mode, 101 items; код не компилирует новую ветку до генерации |
| 2 | Общий runner и claim seam | HTTP background loop используется обоими transport; attempt SQL может работать в caller-owned authorized transaction | Прежние HTTP/MCP publication, panic/failure finalization, exact per-source counters/lineage; SQLite pool=1; rollback второго claim |
| 3 | Scoped gRPC auth и adapter | JWT actor/tenant/credential переданы adapter; provider config server-only; все источники авторизованы до Load; claim rechecks права до commit | Owner/editor allowed; viewer/foreign/inactive/invalid tenant/revoked session denied; valid first + denied second даёт ноль Load/model/run/status |
| 4 | Source и publication | Exact revision/hash checked before Load, claim и publication; per-source generation и attempt fence сохраняются | Missing file, wrong hash, stale source, duplicate, A→B→A, source replacement после queue, новый attempt обгоняет старый, storage/backend failure |
| 5 | Progress/status | Owner+tenant+all source facts проверены на каждом frame; fence удерживается через Send, освобождается при ошибке/отмене | Unknown/foreign run indistinguishable; grant/group/session revoke между кадрами; blocked Send сериализует revoke; cancel/deadline stream; принятый job продолжает работу |
| 6 | Приёмка и документация | Реальный authenticated bufconn + SQLite/PostgreSQL race; required contracts/docs/commit gate; roadmap и resume обновлены | Success с настоящим embed fixture/vector publication; partial batch preserves completed sibling; targeted прежние HTTP/MCP cases; generated contract drift; independent P0–P2 review |

## Затрагиваемая логика

Proto/generated gRPC descriptor и contract inventory; unary/stream auth;
server wiring с общими storage/providers/run registry; HTTP background runner и
attempt helper; новый transport adapter, scenarios/docs. Изменения SQL schema,
raw pipeline и WebUI для этого блока не требуются. Алгоритм postprocessing
сохранён; сопутствующие SQL-исправления перечислены ниже.
Проверки двух SQL dialects, rollback, credential fence и publication составляют
одну единицу review.

## Подтверждённые сопутствующие исправления

Graph fixture воспроизвёл deadlock SQLite pool=1: dialect probe выполнялся
через DB после BeginTx, занявшего единственное соединение. Probe перенесён до
транзакции. VSA rebuild/synonym queries теперь сравнивают текстовое представление
`valid_until` с legacy empty value, поэтому PostgreSQL TIMESTAMPTZ не получает
неверный ввод `''`. Rag/graph и postprocessing проверены на обеих SQL.

Независимый финальный targeted race gate прошёл: gRPC 1.988s, HTTP 9.967s.
Отдельная проверка VSA postprocessing прошла за 1.376s.
Зависший Send проверен callback-моделью и отдельной SQL connection для revoker;
настоящий HTTP/2 flow-control и зависший driver BEGIN отдельным стендом не
проверялись. Старые attempts проверены общим publication CAS regression,
без двух параллельных RPC. Полный affected gate фиксируется в testing.

Config fixture подтвердил отказ `Unavailable` до Load/claim при отсутствии
vector store или endpoint (даже с созданным embed client). Прежде общий runner
мог пропустить vector stage и всё равно разрешить false `COMPLETED`; новый RPC
проверяет его фактические зависимости до запуска. Общий HTTP/raw pipeline
configuration contract этим preflight не расширяется.

Полный race обнаружил чтение actor identity из переиспользуемого Fiber buffer
при detached session recording. Fixture authenticated locals заимствовали
header-backed user/permission strings; production JWT/API key decoding уже
владеет своими строками, поэтому production tenant defect этим не доказан.
HTTP actor snapshot и immutable run owner теперь владеют своими строками.
Принудительная замена header bytes даёт RED до clone на обеих SQL;
actor/inherited-session/gRPC/MCP regression gate после clone — PASS за 12.601s.
Независимая проверка actor/inherited-session/gRPC — PASS за 11.418s.

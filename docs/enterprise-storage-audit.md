# Хранение, AWS KMS и доставка аудита

_Last verified: 2026-09-05 — исходники и локальные интеграционные проверки;
приёмка в настоящем AWS и внешнем SIEM ещё требуется._

## Объекты и шифрование

Сервер поддерживает локальные файлы и S3 через AWS SDK for Go v2. Для S3:

```dotenv
STORAGE_BACKEND=s3
S3_BUCKET=your-private-bucket
S3_REGION=eu-central-1
# S3_ENDPOINT=https://your-compatible-object-service.example
```

Укажите регион и закрытый bucket своей организации. Учётные данные берутся из
стандартной цепочки AWS SDK, включая временные credentials. Настройку роли и
профиля описывает [AWS SDK](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html).
URL с credentials и перенаправление запроса на другой адрес не допускаются.
Levara не создаёт bucket, региональные политики или lifecycle rules.

Пример envelope encryption поверх выбранного backend:

```dotenv
STORAGE_ENCRYPTION=aws-kms
KMS_REGION=eu-central-1
KMS_KEY_ARN=arn:aws:kms:eu-central-1:123456789012:key/replace-with-key-id
KMS_TIMEOUT=10s
KMS_CACHE_TTL=0s
# KMS_READ_KEY_ARNS=arn:aws:kms:eu-central-1:123456789012:key/old-key-id
STORAGE_ENCRYPTION_MAX_OBJECT_BYTES=67108864
STORAGE_ENCRYPTION_MAX_IN_FLIGHT=4
STORAGE_ENCRYPTION_MAX_WAITERS=16
STORAGE_ENCRYPTION_TIMEOUT=2m
# STORAGE_ENCRYPTION_SPOOL_DIRECTORY=/var/lib/levara/ciphertext-spool
```

Используется точный ARN ключа; aliases не принимаются. На старте проверяется
доступность ключа записи. Каждая запись получает новый ключ данных; AES-GCM
защищает содержимое и привязку к object key. Временные файлы этого backend
содержат ciphertext. Перед выдачей reader проверяется весь объект, включая
окончание потока. Повреждение, неверный путь, недоступность KMS и превышение
лимитов завершаются ошибкой.

По умолчанию объект ограничен 64 MiB. Необязательный
`STORAGE_ENCRYPTION_MAX_SPOOL_BYTES` ограничивает суммарное резервирование
места для ciphertext; бюджет должен вмещать хотя бы один максимальный объект.
Очередь и число активных операций ограничены отдельно. HTTP body limit и
память парсеров — дополнительные ограничения вне этого backend.

TTL кэша ключей по умолчанию нулевой; максимум — пять минут. При ненулевом TTL
ранее расшифрованный ключ остаётся доступным до его истечения, даже если
провайдер уже отозвал ключ. Для строгого отзыва оставляйте `0s`.

Чтобы сменить ключ записи, задайте новый `KMS_KEY_ARN`, сохранив старые ключи
в `KMS_READ_KEY_ARNS`. Старые объекты продолжают требовать старого ключа.
Этот переход не перешифровывает архив объектов. Автоматическая смена материала
ключа внутри AWS также не перешифровывает данные — см.
[правила ротации AWS KMS](https://docs.aws.amazon.com/kms/latest/developerguide/rotate-keys.html).

Шифруются объекты, направленные через этот storage backend: загруженные
оригиналы, извлечённый текст и structured artifacts. SQL, Markdown workspace,
граф и поисковые индексы требуют защиты своих дисков и сервисов. Существующие
plaintext-объекты автоматически не мигрируют. Encrypted backend не выдаёт
presigned raw URL и не поддерживает plaintext range.

S3 Save переключается на multipart с 32 MiB; библиотека поддерживает
проверяемые checkpoints, resume/abort и сверку результата после потерянного
ответа. Для checkpoints между перезапусками задайте
`S3_MULTIPART_STATE_KEY` — отдельный секрет из 32 байт в base64. Храните его
в secret manager; смена AWS credentials не должна менять этот секрет.
Один checkpoint между процессами должен обрабатывать один координатор.
CLI/API загрузки пока не предоставляют отдельной команды resume.
Encrypted upload после перезапуска требует сохранённого ciphertext;
продолжение шифрования с новыми случайными bytes не поддерживается.
Для незавершённых загрузок, ID которых потерян, настройте bucket lifecycle
по [руководству S3 multipart](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html).

Метрики: `levara_storage_encryption_in_flight`,
`levara_storage_encryption_waiters`,
`levara_storage_encryption_reserved_spool_bytes` и
`levara_storage_kms_cache_{entries,bytes,hits_total,misses_total}`.

## Надёжная очередь аудита

```dotenv
AUDIT_WEBHOOK_URL=https://siem.example.org/levara/events
AUDIT_DESTINATION_ID=corporate-siem
AUDIT_WEBHOOK_TOKEN_FILE=/run/secrets/levara-siem-token
AUDIT_QUEUE_MAX_EVENTS=10000
AUDIT_QUEUE_MAX_BYTES=16777216
AUDIT_MAX_ATTEMPTS=5
AUDIT_ADMISSION_TIMEOUT=1s
AUDIT_WEBHOOK_TIMEOUT=10s
```

Файл токена — обычный приватный файл до 8192 bytes, без доступа группы и
остальных пользователей. Токен необязателен, если приёмник использует иной
сетевой механизм доступа. В production используйте HTTPS; HTTP разрешён
только для loopback-проверок. Redirect не выполняется.

Сервер сохраняет события MCP и workspace в отдельную SQL-очередь. Внешний
payload содержит версию схемы, `event_id`, время, источник, операцию, исход,
проверенные actor/tenant и числовые размеры/длительность. Тексты документов,
аргументы инструментов, токены и сообщения ошибок не экспортируются.
У событий без подтверждённой личности actor/tenant отсутствуют.

Доставка — at-least-once: POST `{"events":[...]}`. Любой 2xx означает, что
приёмник надёжно принял **весь batch**. Приёмник должен устранять дубликаты
по `event_id`; потерянный ответ может вызвать повтор. HTTP-вызов выполняется
вне SQL-транзакции. После исчерпания попыток событие остаётся в dead-letter
queue и продолжает занимать её бюджет.

Переполнение или отказ SQL отражается в счётчике ошибок и журнале сервера.
Старый void audit interface не может отменить уже выполненную бизнес-операцию:
счётчик ошибок admission требует реакции оператора. Локальный JSONL exporter
сохраняет свои прежние ограничения; он не заменяет durable queue.

Параметры ёмкости сохраняются вместе с destination. Их несовпадение при новом
старте вызывает ошибку, поэтому не меняйте их как обычную настройку worker.
Смена `AUDIT_DESTINATION_ID` создаёт другую очередь; прежнюю нужно отдельно
довести до доставки или разобрать. Без явного ID используется хэш URL, и
смена URL также меняет destination.

Операторские команды используют SQL credentials отдельно от пользовательского
API и не создают schema. Соберите `go build -o levara-audit ./cmd/audit`.

```sh
export AUDIT_DB_DRIVER=sqlite
export AUDIT_DB_DSN='file:/var/lib/levara/metadata.db?mode=rw'
./levara-audit status --destination corporate-siem
./levara-audit dead-letter --destination corporate-siem --limit 100
./levara-audit replay --destination corporate-siem --event-ids EVENT_ID
# Необратимое удаление только явно выбранных dead-letter events:
./levara-audit discard --destination corporate-siem --event-ids EVENT_ID
```

Для PostgreSQL задайте `AUDIT_DB_DRIVER=postgres` и `AUDIT_DB_DSN` через
секрет окружения, указывающий на ту же БД. Не помещайте пароль в аргументы
команды. Повтор replay/discard не затрагивает уже отправленные или leased
события. Replay сохраняет `event_id`. Следующая страница dead-letter —
`--after ID` последнего полученного события.

Метрики `levara_audit_spool_*`: `up`, `pending`, `dead`, `pending_bytes`,
`dead_bytes`, `oldest_pending_seconds`, `oldest_dead_seconds`,
`delivered_total`, `retried_total`, `rejected_total`, `failures_total`.
При недоступной SQL `up=0`, а значения состояния очереди отсутствуют;
это не нулевая очередь. `failures_total` относится к текущему процессу.

## Проверка перед включением

`levara-server -config-check` проверяет заданные параметры KMS/encryption и
webhook теми же валидаторами, что startup, без сетевых вызовов. Доступ к
региону, ключу, bucket, сертификатам и реальному приёмнику подтверждается
отдельным тестом в выбранном окружении. Неуспешная конфигурация не включает
plaintext fallback.

Локальные race-проверки охватывают SQL-очередь SQLite/PostgreSQL,
ошибки доставки, restart/replay, сохранение ID, приватность payload,
конфигурацию и операторские команды. S3/KMS протоколы проверены локальными
HTTP fixtures. Эти результаты не заменяют проверку IAM, residency,
bucket lifecycle и доступности внешних сервисов конкретного развёртывания.

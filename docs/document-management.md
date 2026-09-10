# Документы: загрузка, обработка, скачивание и права

Состояние на 2026-09-10. Документ хранится как запись `data`; набор данных
(dataset, в WebUI также «проект») связывает документы и определяет выдачу
прав. Векторная коллекция содержит результаты обработки. Имя коллекции,
имя файла и теги сами по себе не дают разрешения на чтение.

## Можно ли поделиться отдельным документом

**Рабочий WebUI/REST сценарий сейчас выдаёт пользователю доступ к набору
данных.** В коде реализована отдельная ACL документа и групп, включая tenant
boundary, CAS revision, revoke и nested membership, но эти handlers ещё не
подключены к основному router. До подключения используйте отдельный набор с
одним файлом и учитывайте, что grant распространяется на добавленные позже
документы.

| Получатель / действие | Текущее поведение |
|---|---|
| Существующий пользователь | Выдача доступа по email через WebUI или REST |
| Группа организации / AD | Policy и SCIM membership реализованы локально; пользовательский API ещё не подключён |
| Только один файл в общем наборе | Модель document ACL есть, но текущий рабочий путь — отдельный набор |
| Запрет передачи вне организации | Document policy проверяет tenant; legacy dataset grant остаётся отдельным старым контрактом |
| Анонимная внешняя ссылка | Не является сценарием внутренней ACL |
| Отзыв grant | Закрывает последующие запросы пользователя к защищённому набору |
| Уже скачанная копия | Не отзывается |
| Ранее выданный S3 presigned URL | Работает до истечения срока; отзыв grant его не аннулирует |

При работе с конфиденциальными материалами выбирайте явно созданный
приватный набор и проверяйте его ID. Legacy-наборы с пустым owner считаются
публичными. Не используйте отсутствие аутентификации для проверки изоляции:
это режим разработки с другим поведением политик.

## Роли набора

| Роль | Чтение / поиск | Загрузка / изменение / удаление набора | Управление grants |
|---|---|---|---|
| viewer | Да | Нет | Нет |
| editor | Да | Да, включая удаление набора | Нет |
| admin share | Да | Да | Да |
| owner / superuser | Да | Да | Да |

API-ключ дополнительно ограничивает действия собственными permissions.
Grant не повышает read-only ключ до write. Сейчас удаление отдельной связи
файла и всего набора проверяется как write, поэтому editor может удалить
набор. Если это слишком широкое полномочие, выдавайте viewer. Разделение
редактирования и удаления требует изменения модели действий.

## Через WebUI

WebUI запускается отдельным Next.js сервисом; параметры подключения — в
[webui-operations](webui-operations.md). Войдите локальным пользователем,
чьи права проверяете; наличие внешнего SSO token не заменяет browser login.

1. Откройте «Проекты» / Datasets. Создайте приватный набор с понятным
   уникальным именем либо выберите доступный набор в Target dataset.
   Существующий набор отправляется по ID, что важно для editor share.
2. Выберите один или несколько файлов либо перетащите их в область
   загрузки. Пока batch загружается/обрабатывается, новый batch недоступен;
   это исключает смешение результатов разных запусков.
3. WebUI запускает cognify автоматически с `skip_graph:true`. Дождитесь
   Ready to search, подтверждённого завершённым run. Ошибка извлечения,
   запуска или получения статуса отображается с причиной; после неё можно
   повторно выбрать тот же файл. Сохранение файла и готовность поиска —
   разные этапы.
4. Откройте набор: проверьте список, размер, статус обработки нужной
   коллекции; скачайте оригинал кнопкой Download original. Запрос проходит
   через API с текущими credentials. Legacy-загрузка могла не сохранить
   исходные байты — проверьте формат скачанного файла.
5. В панели доступа задайте email уже существующего пользователя и роль.
   Для ознакомления выбирайте viewer. Group selector и отдельной панели
   разрешений файла сейчас нет.
6. Проверьте доступ в отдельной сессии получателя: список, скачивание и
   поисковый факт. Отзовите grant и повторите те же запросы. Проверка должна
   включать lifecycle ограничения A12–A14 и проверки graph/MCP прав A15/A17.
7. Удаление записи убирает её связь с набором. Перед использованием
   удаления как границы конфиденциальности прочитайте ограничения lifecycle
   ниже: скрытие строки в UI не гарантирует очистку поисковых производных.

При смене вкладки/закрытии браузера фоновой обработкой продолжает управлять
сервер. Локальная история batch в компоненте UI не является постоянной
очередью заданий; используйте run ID и состояние набора для диагностики.

## Быстрый сценарий через консоль

Примеры используют существующий сервер с обязательной аутентификацией.
`LEVARA_TOKEN` — токен локального пользователя или подходящий API-ключ.
Нужен `curl`, для выбора ID из JSON — установленный `jq`.

```sh
export LEVARA_URL='https://levara.example.org'
DATASET_ID=$(curl --fail-with-body -sS \
  -H "Authorization: Bearer $LEVARA_TOKEN" -H 'Content-Type: application/json' \
  --data '{"name":"finance-pilot-document-001"}' \
  "$LEVARA_URL/api/v1/datasets" | jq -er '.id')

curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  -F "dataset_id=$DATASET_ID" -F 'data=@./report.pdf' \
  "$LEVARA_URL/api/v1/add"

curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/data"
```

Список data сейчас возвращает массив целиком; сервер не применяет page/limit.
Для больших наборов нужна серверная пагинация.

Имя набора должно быть уникальным: SQL-схема имеет глобальную уникальность
name. Сохраните `dataset_id` ответа и проверяйте его при последующих
операциях. Multipart поддерживает несколько полей `data`. JSON-вариант
для текста:

```sh
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  -H 'Content-Type: application/json; charset=utf-8' \
  --data "$(jq -n --arg id "$DATASET_ID" --arg text 'Отчёт: выручка 123.' \
    '{dataset_id:$id,data:$text}')" "$LEVARA_URL/api/v1/add"
```

Загрузка сохраняет материал; поисковая обработка запускается отдельно:

```sh
RUN_ID=$(curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "$(jq -n --arg id "$DATASET_ID" \
    '{datasets:[$id],collection:"finance-pilot",skip_graph:true}')" \
  "$LEVARA_URL/api/v1/cognify" | jq -er '.pipeline_run_id')
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/cognify/$RUN_ID/status"
```

Дождитесь `COMPLETED`. При `FAILED` сохраните описание ошибки,
исправьте причину и повторите обработку. Не считайте наличие `run_id` или
HTTP 200 доказательством готовности поиска. `skip_graph:true` отключает
построение графа, но не требование доступного embedding provider.
Run status показывает выполнение worker, а готовность чтения дополнительно
требует актуальной publication для текущих source revision/hash. После замены
исходника старый `COMPLETED` не считается готовностью нового содержимого.
Повторная обработка того же source получает новый server attempt: поздний worker
предыдущего запуска не может заменить новую publication. Publication и точный
`COMPLETED` фиксируются одной SQL-транзакцией. Если повторный запуск падает,
последняя успешная publication остаётся доступной.
Legacy-значения, сохранённые только в общем `data.pipeline_status`, не имеют
dataset provenance и после обновления намеренно показываются как неизвестные.
Такие документы нужно повторно обработать, чтобы создать точный статус.

```sh
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"query_text":"выручка","collection":"finance-pilot","query_type":"BM25","top_k":5}' \
  "$LEVARA_URL/api/v1/search/text"
```

Проверяйте в найденном тексте заранее известный факт и источник, затем
повторите запрос токеном пользователя без grant: документ отсутствует.
Пустой результат сам по себе не доказывает корректность ACL: сначала
положительный контроль владельцем должен действительно найти документ.

## Встроенный CLI

```sh
levara --url="$LEVARA_URL/api/v1" add --file=./report.pdf --dataset=finance-pilot-document-001
levara --url="$LEVARA_URL/api/v1" add 'Отчёт: выручка 123.' --dataset=finance-pilot-document-001
levara --url="$LEVARA_URL/api/v1" cognify --dataset=finance-pilot-document-001 --collection=finance-pilot --wait
```

В отличие от curl-примеров выше CLI ожидает base URL уже с `/api/v1`;
здесь он передан явно, token читается из `LEVARA_TOKEN`.
`add --dataset` принимает имя; `cognify --dataset` разрешает точное имя или
ID через список доступных наборов. Если аргумент означает путь, применяйте
`--file=`: для совместимости несуществующий позиционный путь остаётся
обычным текстом. Ошибка HTTP, некорректный JSON или отсутствие обязательных
полей ответа завершают команду с ненулевым кодом. Для dataset grants
используйте приведённые REST-команды. Публичных CLI-команд для document/group
ACL пока нет.

MCP `cognify` с inline `data` и HTTP `cognify` с `texts[]` сначала атомарно
создают серверный dataset/document source, source revision и SHA-256, затем
публикуют run. Переданный клиентом `document_id` не используется и удалён из
MCP descriptor. Ошибка подготовки source не оставляет run или частичные SQL
записи. Для нескольких `texts[]` terminal status сохраняется отдельно для
каждого документа; один data ID в двух datasets также имеет независимые
статусы по `dataset + document + collection`.

## Выдача и отзыв доступа

Получатель должен уже существовать в локальной базе пользователей. Email
SSO-пользователя сам по себе не гарантирует такую запись: см.
[корпоративный вход](enterprise-identity.md).

```sh
SHARE_ID=$(curl --fail-with-body -sS -X POST \
  -H "Authorization: Bearer $LEVARA_TOKEN" -H 'Content-Type: application/json' \
  --data '{"email":"colleague@example.org","role":"viewer"}' \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/shares" | jq -er '.id')
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/shares"
# Выполняйте отзыв после проверки доступа получателем:
curl --fail-with-body -sS -X DELETE -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/shares/$SHARE_ID"
```

Повторный grant обновляет роль и возвращает существующий ID. Отзыв
проверяет одновременно dataset ID и share ID. После него проверьте list,
raw download, sparse/vector search и RAG/MCP тем же токеном получателя.
Не передавайте пользователю ваш токен и не создавайте общую учётную запись.

## Оригинал и извлечённый текст

Для новых multipart-загрузок исходные байты и извлечённый UTF-8 текст
сохраняются отдельно. Название файла — отображаемая метка; физическое
хранилище использует owner/content identity и атомарную публикацию.
Разные документы с одинаковым именем не должны ссылаться на чужие байты.
ID документа следует оригиналу, даже когда два исходных файла дают один
и тот же текст. Повторная загрузка одинакового содержимого тем же owner
может повторно использовать запись; это не система версий документа.

```sh
# DOCUMENT_ID возьмите из списка data нужного набора.
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/data/$DOCUMENT_ID/raw?original=true" \
  -o downloaded-original
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/data/$DOCUMENT_ID/raw" \
  -o extracted-text.txt
```

По умолчанию `/raw` сохраняет совместимость и возвращает данные обработки.
Оба запроса проверяют права и связь документа с набором; ответ — opaque
binary с `nosniff`. Старые загрузки могли сохранить только извлечённый
текст и записать его путь также как original. Исправление не восстанавливает
утраченный исходный PDF: для точного оригинала его нужно загрузить снова.

`/raw/url` — отдельный механизм временных URL, по умолчанию 900 секунд,
максимум семь дней для поддерживающего presign backend. Для внутреннего
доступа с проверкой grant на каждом скачивании используйте `/raw`.

## Явная замена источника

`GET /datasets/{dataset}/data` возвращает для каждого документа
`source_revision` и `raw_content_hash`. Для замены одного физического источника
отправьте обычный multipart в `/add`, добавив точную пару CAS. `datasetId` и
точное `datasetName` обязательны; один запрос заменяет ровно один файл.

```sh
DOC=$(curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/data" |
  jq -er --arg id "$DOCUMENT_ID" '.[] | select(.id == $id)')
SOURCE_REVISION=$(printf '%s' "$DOC" | jq -r '.source_revision')
RAW_CONTENT_HASH=$(printf '%s' "$DOC" | jq -r '.raw_content_hash')

curl --fail-with-body -sS -X POST \
  -H "Authorization: Bearer $LEVARA_TOKEN" \
  -F "datasetId=$DATASET_ID" -F "datasetName=$DATASET_NAME" \
  -F "replace_data_id=$DOCUMENT_ID" \
  -F "source_revision=$SOURCE_REVISION" \
  -F "raw_content_hash=$RAW_CONTENT_HASH" \
  -F 'data=@./new-version.pdf' \
  "$LEVARA_URL/api/v1/add"
```

Изменившая источник или structured JSON замена возвращает новый
`source_revision` и `raw_content_hash`; точный повтор с теми же байтами
идемпотентно сохраняет прежние revision и artifact ID. Устаревшая пара получает
409 и не меняет текущие байты. Замена повторно проверяет credential, право
записи, hold и document policy, повышает content revision при изменении и
сбрасывает прежнюю publication. Обычная загрузка не может обойти CAS для
зарегистрированного документа, даже если изменился только structured JSON.
Физический `data.id`, включённый более чем в
один dataset, сейчас не заменяется: сервер возвращает 409, чтобы изменение не
затронуло вторую ссылку неявно.

Если structured extractor вернул JSON, `structured_extractions` содержит
`artifact_id` и `artifact_path`. Значение `artifact_path` — URL API, а не путь
к файлу или ключ object storage. Скачать текущий артефакт можно теми же
credentials:

```sh
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "$LEVARA_URL/api/v1/datasets/$DATASET_ID/data/$DOCUMENT_ID/structured-artifacts/$ARTIFACT_ID" \
  -o extraction.json
```

Сервер выдаёт только active artifact точной текущей `source_revision` и
`raw_content_hash`, повторно проверяет document ACL перед передачей, ограничивает
ответ 16 MiB и проверяет размер, SHA-256 и JSON. Поэтому прямой URL старой версии
перестаёт работать сразу после replacement. Доступ по индивидуальному или
групповому grant действует и для этого URL; отзыв grant или удаление участника
группы закрывает последующее чтение.

Structured artifact сохраняется тем же journaled ingest attempt, что и исходник
и projection. Ошибка object storage или SQL inventory откатывает публикацию и
очищает объекты попытки. В режиме без БД extraction остаётся в ответе `/add`, но
управляемый artifact и URL не создаются, потому что их нельзя связать с ACL и
версией источника. Изменение JSON при той же текстовой projection входит в тот
же source/content version domain: два writer с одной CAS-парой не могут оба
опубликовать разные artifacts. Одинаковые документы в одном batch используют
одни object paths и одну inventory-запись.

## Качество обработки и ошибки

| Материал | Путь обработки | Что проверять |
|---|---|---|
| TXT, Markdown, CSV, JSON, XML, YAML, log | UTF-8 текст | Кириллица, переносы, числовые значения; NUL/невалидный UTF-8 отклоняются |
| PDF с текстовым слоем | Парсер документов | Порядок колонок, страницы, таблицы, заголовки; наличие текста не гарантирует верную структуру |
| PDF-скан | Нет гарантированного OCR fallback в обычном Extract | Пустой результат — 422; отдельно подтвердите настроенный OCR/structured pipeline |
| DOCX, PPTX, XLSX, HTML, EPUB, ODT | Парсер формата | Текст, ячейки/слайды/порядок, отсутствие служебного XML в поиске |
| Изображение | Настроенный OCR/vision backend | Реальная читаемость, язык, поворот, мелкий шрифт; успешный HTTP не измеряет точность |
| Аудио | `WHISPER_ENDPOINT`, при необходимости key/model | Транскрипт, язык, тишина, длительность и ошибка backend |
| Табличное структурированное извлечение | schema + настроенный structured endpoint | JSON, единицы, типы/пропуски, projection и сохранение artifact |

Multipart сначала читает и локально проверяет весь batch. Локально неверный
второй файл не отправляет первый во внешний structured extractor и не создаёт
SQL/storage/artifact записи. Перед первым внешним вызовом сервер повторно
проверяет credential и право записи, удерживая bounded authorization fence до
завершения вызовов. Timeout или частичный отказ extractor возвращает 422 без
публикации batch; retry начинает новую попытку. Ошибка чтения возвращает 400,
ошибка SQL/хранилища — 500. JSON требует непустое поле `data`. Ошибки URL fetch
не превращают URL в якобы извлечённый документ.

SQL-записи dataset/data/link/artifact фиксируются одной транзакцией. В
авторизованной DB-backed загрузке неудача Save или SQL очищает все объекты
попытки; незавершённый процесс оставляет `ingest_pending_uploads` для offline
recovery. Не удаляйте journaled blobs вручную без проверки SQL-ссылок и
резервной копии.

Для оценки качества возьмите размеченный набор своих документов: точные
контрольные строки и числа, таблицы с ожидаемыми ячейками, OCR-транскрипты,
вопросы с эталонными ответами и отрицательные вопросы. Измеряйте долю
успешного извлечения, полноту фактов, OCR CER/WER, retrieval recall@k,
правильность цитат, время и размер по каждому формату. Локальные
детерминированные проверки не являются замером качества на вашем корпусе.

## Удаление и повторная обработка

DELETE `/datasets/{dataset}/data/{document}` удаляет связь с выбранным
набором; общий документ и его active structured artifact остаются, пока есть
другая связь. После удаления последней связи structured artifact сначала
становится недоступным в SQL, затем удаляется из настроенного storage. Если
storage временно недоступен, ответ содержит `artifact_cleanup_pending: true`:
retired-запись сохраняется, а повторная cleanup безопасна. Hold блокирует
replacement/delete/prune до изменения inventory или storage.
Поле `artifact_cleanup_pending` также возвращают upload/replacement и rename,
если retired artifact не удалось физически удалить.

Это не обещание физического стирания всех производных: raw blobs, старые
индексы, граф и резервные копии требуют отдельной процедуры очистки и retention.

После удаления проверьте прямое чтение и поиск, включая старые коллекции.
Исторические фрагменты без `document_id` нельзя однозначно связать с одним
файлом: для них нужна контролируемая перестройка соответствующего индекса.
Не используйте массовый `prune` как замену удалению одного документа.

Проверенные сценарии, незакрытые ограничения и приоритеты развития собраны
в [матрице приёмки](document-workflow-scenarios.md).

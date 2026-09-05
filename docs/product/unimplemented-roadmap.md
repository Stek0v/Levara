# Незавершённые возможности и критерии приёмки

Сверено с исходниками 2026-09-05. Это backlog разработки, не инструкция по
включению функций. Приоритеты: P1 — доступ, идентичность и исполнение задач;
P2 — эксплуатация и интеграции; P3 — удобство и отдельные эксперименты.
Перечисленные критерии не считаются выполненными без соответствующего прогона.

Текущие возможности описаны в [продуктовой лестнице](../product-ladder.md),
[руководстве по идентичности](../enterprise-identity.md),
[управлении документами](../document-management.md) и
[Task Runtime](../long-horizon-runtime.md). Результаты и ограничения проверок —
в [testing](../testing.md), сценарии документов — в
[приёмочной матрице](../document-workflow-scenarios.md).

## Идентичность и права — P1

OIDC bearer verification, SAML SP и ограниченный SCIM Users API уже существуют.
Прямого LDAP/LDAPS, browser OIDC flow, SCIM Groups и готовой связки SCIM↔SSO
нет. Для отдельного документа сейчас используется отдельный датасет с
индивидуальной ролью; независимого ACL документа и групповых grants нет.

Критерии следующего этапа:

- Проверить два равноценных корпоративных пути: федерацию AD через IdP и
  прямой LDAP/LDAPS, если он реализуется. Для LDAP нужны проверка сертификата,
  безопасные bind/search, ограниченная service account, escaping фильтров,
  nested groups, disabled/locked account и недоступность каталога.
- Завершить browser login/callback/session/logout. Приёмка: неверные
  issuer/audience, неподписанный или повторный ответ, nonce/state, clock skew,
  expiry, rotation, logout и безопасный redirect. Проверка bearer сама по себе
  не закрывает браузерную сессию.
- Связать provisioning и вход стабильным идентификатором без склейки по email.
  Проверить rename, повторный create, конкуренцию, email conflict, deactivate,
  re-provision и уже выданные JWT/API-ключи. Отзыв должен действовать на всех
  поверхностях, включая активные сессии и фоновые операции.
- Ввести доступ к документу пользователю/группе внутри организации: назначение
  и отзыв роли, понятное наследование, аудит, tenant boundary. Смена групп и
  отзыв доступа должны применяться до поиска, rerank, graph expansion,
  чтения raw/artifacts и отправки контекста в LLM. Проверить прямой URL,
  чужой ID, кэши, выгрузки и concurrent revoke.
- SCIM Groups, provisioning audit и EnterpriseUser extension требуют отдельного
  согласованного контракта; текущий Users subset не объявлять полным SCIM.

Аналитические read models agent trajectories / memory behavior также требуют
owner/tenant-фильтрации и проверки доступа до агрегации; их collection/client
селекторы сами по себе не являются ACL.

## Task Runtime — P1/P2

Уже есть CAS/leases, receipts/checkpoints, bounded bootstrap, read-only Tasks
в WebUI, opt-in scheduler и digest binding манифеста при claim. Стандартный
executor worker только пишет лог; проверки tool/path/network из манифеста
не образуют универсальный механизм контроля исполнения.

Оставшиеся критерии:

- P1: подключить реальный executor с явной разрешённой областью, deadline,
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

Для gated memory commit отдельно нужны проверка прав и связи source task/receipt,
если статус используется как доказательство: текущее поле `verified` не означает
серверную проверку истинности evidence. Preview сохраняет SQL-план с содержимым;
его нельзя считать операцией без записи.

## Корпоративное хранение и аудит — P2

Базовый S3 и storage/KMS contracts существуют. Не закрыты production KMS/BYOK,
GCS/Azure Blob, корпоративные политики хранения, внешний SIEM и legal hold.

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

- P2: Team onboarding с dry-run и повторяемым созданием пользователей/ключей,
  коллекций и индивидуальных grants. В конце — проверка входа и отрицательная
  проверка доступа A/B. Дубли email, Unicode, отказ сервера и частичное выполнение
  должны давать понятный отчёт без утечки admin token.
- P2: расширить существующую sync-страницу до наблюдения двух независимых нод,
  pending/lag и объяснения конфликтов. Проверить offline peer, пустую историю,
  clock skew и обновление состояния. Сам экран sync уже есть.
- P2: verified backup по расписанию: SQL + workspace + raw/structured artifacts
  и необходимые индексы, восстановление в sandbox, дата последнего успешного
  восстановления. Проверить активные записи, повреждённый архив, полный диск
  и несовместимую версию. Наличие команды backup не доказывает recoverability.
- P3: Prometheus rules/examples уже поставляются. Нужны согласованные SLO,
  применимость к выбранной конфигурации, promtool и проверка искусственного
  инцидента; учитывать maintenance, flapping и missing series.
- P3: для Raft/sharding зафиксировать проверенную область эксплуатации либо
  явно оставить experimental; нужны durable metadata, recovery, partition,
  membership и многонодовый E2E до заявлений о production readiness.
- P3: расширять workspace dashboard/evaluation только для подтверждённых
  пробелов текущей страницы: lag, freshness, explainability и correctness
  source citations. Не создавать второй backlog уже реализованных операций.

Начать с identity и отзыва доступа, затем реального executor и эксплуатационной
приёмки. Для каждой реализации нужны соответствующий узкий тест, contract-check
при изменении публичной поверхности и релизные проверки из [testing](../testing.md).

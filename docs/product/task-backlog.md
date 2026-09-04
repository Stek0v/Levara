# Levara — задачи с DoD, тест-планами и corner cases (2026-09-04)

Развёртка роадмапа `docs/product/unimplemented-roadmap.md` (24 пункта).
Каждая задача: чёткий Definition of Done + список тестов + corner cases.
Приоритеты: P1 (блокирует Enterprise в продакшене) → P3 (отложаемо).
Формат согласован с long-horizon Task Runtime (критерии = receipt-able).

---

## A. Enterprise-инфраструктура

### A1. Raw OIDC token verification — P1 ✅ (2026-09-04)

**Суть.** Сервер сам проверяет входящие OIDC/JWT-токены (подпись, `iss`, `aud`,
`exp`, `nbf`), а не доверяет проверенным claims от вышестоящего прокси.

**Статус.** Выполнено: `pkg/auth/oidc.go` + тесты (20 кейсов, включая alg
confusion, rotation, skew-границы, key cap, concurrency), HTTP-фолбэк
`JWTMiddlewareWithOIDC`, composition-root биндинг в `cmd/server/main.go`,
bridge `SimpleMappingBridge` в `pkg/access/bridge.go`. Архитектурный guard
(pkg/access) соблюдён — protocol adapter code вне internal/http.

**DoD.**
1. `pkg/access/oidc.go` валидирует RS256/ES256 подпись по JWKS с
   поддержкой `kid` и ротации ключей (кэш + refresh по miss).
2. Проверяются `iss` (allowlist), `aud` (allowlist), `exp`, `nbf`, `iat`
   (с настраиваемым clock skew ≤ 5 мин).
3. Claims → `IdentityBridge` маппинг сохранён как есть (обратная совместимость).
4. Режим trust-proxy (текущее поведение) остаётся доступным через env и
   явно логируется warning'ом при включении.
5. Документация: `docs/profile-presets.md` обновлён, enterprise-пресет
   получает `LEVARA_OIDC_JWKS_URL` / `LEVARA_OIDC_ISSUER` / `LEVARA_OIDC_AUDIENCE`.
6. `make contract-check` зелёный, новые env задокументированы в contract.json
   profile-схемы (если применимо).

**Тесты.**
- Unit: подпись валидна/невалидна (подменённый payload, чужой ключ).
- Unit: `kid` не найден в кэше → refresh JWKS → повторная валидация.
- Unit: expired token, nbf в будущем, skew-границы (ровно -5:00, -4:59).
- Unit: `iss`/`aud` не из allowlist → 401 с различимой ошибкой.
- Integration: bootstrap enterprise-пресета с JWKS URL (httptest-сервер).
- E2E: `/api/v1/auth/oidc/callback` (или bearer-проверка на API) с реальным
  flow через httptest IdP.

**Corner cases.**
- JWKS endpoint недоступен при старте → fail-closed, понятная ошибка bootstrap.
- Ротация ключей посреди трафика: старый `kid` исчез → in-flight запросы с
  старым токеном получают 401, новые — 200; нет deadlock на refresh.
- Токен с `alg: none` / HS256-подделкой → отвергается, никогда не проходит.
- Дублирующиеся `kid` в JWKS → детерминированный выбор + warning.
- Часы сервера отстают на 4:59 vs 5:01 — разное поведение задокументировано.
- JWKS отдаёт >100 ключей → кэш не раздувается (лимит + LRU).

---

### A2. SAML HTTP surface — P1 ✅ (2026-09-04)

**Суть.** SAML 2.0 SP: `/auth/saml/login` (redirect to IdP), `/auth/saml/acs`
(Consumer Service), метаданные SP на `/auth/saml/metadata`.

**Статус.** Выполнено: `pkg/access/saml.go` — SP на проверенной библиотеке
crewjam/saml v0.5.1 (govulncheck: 0 called), подпись/условия/audience — в
библиотеке (hand-rolled XML-DSig = источник signature-wrapping багов).
One-time-use: request-ID store с TTL 15 мин + атомарное consume; IdP-initiated
(unsolicited) ответы отклонены — SP-initiated only, это и есть replay-защита.
HTTP: `/saml/login`, `/saml/acs` (opaque 401 без утечки деталей), `/saml/metadata`;
env `LEVARA_SAML_ENABLED` + `LEVARA_SAML_{ENTITY_ID,ACS_URL,METADATA_URL,
IDP_METADATA_URL,IDP_METADATA_FILE,KEY_FILE,CERT_FILE}`. Wire — в composition
root `cmd/server/saml.go` (архитектурный guard pkg/access соблюдён). Тесты:
config-validation fail-closed (6 кейсов), ID-store replay/TTL/bound (4),
PEM-парсинг PKCS#1/PKCS#8, HTTP-поверхность (metadata/login 302/ACS 401/disabled→404).

**DoD.**

**Тесты.**
- Unit: разбор валидного/битого XML; XML-атаки (XXE, signature wrapping).
- Unit: replay одного assertion дважды → второй 401.
- Unit: NotBefore в будущем / NotOnOrAfter в прошлом → 401.
- Integration: полный handshake с тестовым IdP (mock IdP в тестах).
- E2E: логин через SAML → получен JWT → API-запрос проходит.

**Corner cases.**
- Signature wrapping (подмена assertion при валидной подписи) → отвергается.
- InResponseTo от другого SP-запроса → отвергается.
- Часовые пояса: IdP шлёт время в UTC+X — валидация по абсолютному времени.
- IdP возвращает error response (`samlp:Response` со Status != Success) →
  человекочитаемая страница ошибки, не 500.
- Двойной клик в IdP → два POST на ACS с одним assertion → ровно один успех.
- Certificate rotation у IdP → обновление доверия без рестарта (метаданные
  перечитываются по интервалу).

---

### A3. SCIM HTTP surface (provisioning) — P1 ✅ (2026-09-04)

**Статус.** Выполнено по ADR-003: `pkg/access/scim.go` — SCIMStore поверх
users/principals: externalId-маппинг в `scim_identities` (PK issuer+externalId),
детерминированный user id `scim-<sha256(issuer␤externalId)[:32]>`, email-конфликт
чужой личности → `ErrSCIMEmailConflict` (никаких тихих склеек), soft delete через
`is_active=false` (маппинг живёт → перепровижининг реактивирует), locked random
password (логин только через SSO/JWT), транзакционный create, constant-time token
check. HTTP: `cmd/server/scim.go` — `/scim/v2` canonical top-level path
(ServiceProviderConfig, Schemas, Users CRUD + PATCH active/rename + Delete→204,
pagination clamp ≤200, `userName eq`/`externalId eq` фильтры, RFC 7644 error
shapes `uniqueness/invalidValue/invalidFilter/invalidPath`), auth — отдельный
static bearer `LEVARA_SCIM_TOKEN` (поверхность не существует без токена), issuer
`LEVARA_SCIM_ISSUER`. Unit: 8 store-кейсов (idempotent re-create, конфликт
включая pre-existing локального юзера, soft delete → re-provision реактивирует,
concurrent creates, nil-DB disabled) + 8 HTTP-кейсов. Живой стенд: 12/12
(create→dup 200→conflict 409→patch deactivate→soft delete 204→фильтры→ghost
404→no-token 401).

**Суть.** RFC 7644: `/scim/v2/Users` (POST/GET/PATCH/PUT/DELETE),
`/scim/v2/Groups`, `/scim/v2/ServiceProviderConfig`, Bearer-auth отдельным
токеном `LEVARA_SCIM_TOKEN`.

**DoD.**
1. CRUD пользователей: создание, деактивация (active=false), удаление,
   patch displayName/groups.
2. Группы → маппинг в роли через существующий provisioner seam.
3. Пагинация startIndex/count, фильтр `userName eq "..."` (минимум).
4. Все операции в audit log с actor=scim.
5. Deactivate → немедленная инвалидация JWT/API-ключей пользователя.
6. ADR (см. A8) принят до мержа surface.

**Тесты.**
- Unit: RFC 7644 schema-ответы (`schemas`, `meta`, `id`).
- Unit: PATCH с `Operations` path=active value=false → deactivation + revocation.
- Integration: полный lifecycle create→patch→deactivate→delete с Azure AD-
  совместимым клиентом (или etcd-driven mock).
- Security: без/с неверным SCIM-токеном → 401; токен не даёт доступа к
  остальному API.
- Load: 1000 пользователей через bulk → все в DB, audit complete.

**Corner cases.**
- SCIM создаёт пользователя с email, который уже есть → мягкий матч по email,
  не дубль (или явный conflict — по ADR-решению).
- Deactivate во время живой сессии → следующий запрос с этим JWT → 401.
- PATCH с неизвестным path → 400 с scim-ошибкой, не 500.
- Двойное удаление → 404 второй раз, идемпотентно.
- Bulk-провижининг 10k пользователей → нет деградации остальных endpoint'ов
  (SCIM не держит глобальную блокировку).
- Groups loop (A→B→A) → отвергается.

---

### A4. KMS / BYOK реализации — P2

**DoD.**
1. `pkg/storage.KMS` интерфейс получает две реализации: AWS KMS и Vault
   Transit (обе за feature-флагами, без обязательной зависимости).
2. Envelope encryption: данные шифруются DEK, DEK — KEK из KMS; DEK-кэш
   с TTL.
3. Key rotation: новые записи — новой версией ключа, старые читаются по
   key version header; re-encryption фоновая задача.
4. Отсутствие KMS при старте enterprise-профиля с включённым флагом →
   fail-fast.
5. Метрики: latency KMS-операций, cache hit rate.

**Тесты.**
- Unit: interface-совместимость, серийлизация envelope-формата.
- Integration: против локального Vault (testcontainer) — encrypt/decrypt/
  rotate.
- Chaos: KMS недоступен → читаем из кэша в TTL; после — ошибки с backoff,
  без шторма запросов.
- Corner: повреждённый envelope (битый key version) → явная ошибка, не
  мусорные данные; ключ удалён в KMS → readable-error; latency KMS 2s →
  запросы не копятся безгранично (semaphore).

---

### A5. Корпоративные object storage backends — P2

**DoD.**
1. GCS и Azure Blob реализации `Storage` рядом с существующим S3.
2. За флагами `STORAGE_BACKEND=gcs|azure`; учёт данных: encryption-at-rest
  настройки, residency-конфиг (bucket region) документированы.
3. Lifecycle-правила: документированный рецепт (не код) для retention.
4. Один и тот же dataset заливается во все три бэкенда и читается обратно
  побайтово одинаково (contract-тест, общий для бэкендов).

**Тесты.**
- Contract suite общий: put/get/list/delete/range/presign-эквивалент.
- Integration: testcontainer (fake-gcs-server, Azurite).
- Corner: 5GB-файл (multipart), пустой файл, имя с unicode/пробелами,
  concurrent writes в один ключ (last-write-wins задокументирован),
  network flap во время multipart → resume/abort чисто.

---

### A6. SIEM sink — P2

**DoD.**
1. Audit events стримятся в webhook (универсальный) + syslog (RFC 5424) —
   два встроенных sink'а, интерфейс `AuditSink` для остальных.
2. Delivery: батчирование (size/time), retry с экспоненциальным backoff,
   dead-letter при исчерпании; at-least-once семантика задокументирована.
3. События не блокируют основной поток: полный buffer → drop oldest +
   counter-метрика (не silent).
4. Env: `LEVARA_AUDIT_SINK webhook|syslog`, URL/endpoint, batch settings.

**Тесты.**
- Unit: формат событий (JSON схема стабильна — contract-test).
- Integration: httptest-приёмник получает события ≤ N мс после действия.
- Chaos: приёмник лежит → retry → восстановление → дозагрузка буфера.
- Corner: приёмник отвечает 200 но медленно (timeout), дубликаты при retry
  (at-least-once), часовой пояс/сериализация timestamp — RFC 3339Nano,
  event >1MB (большой diff) → chunking или reject с метрикой.

---

### A7. Legal hold enforcement — P2

**DoD.**
1. `legal_hold: true` на dataset/collection → операции удаления и
   overwrite блокируются API-уровнем (409 с объяснением), не только UI.
2. Hold ставится/снимается отдельным permission'ом (RBAC).
3. Снятие hold → данные удаляемы снова; попытка снять hold без permission → 403.
4. Все транзицции в audit log.

**Тесты.**
- Unit: delete с hold → 409; без hold → 200.
- Integration: hold → попытка overwrite через sync → отвергнуто; sync LWW
  с hold на одной стороне → конфликт-отчёт, не тихая потеря.
- Corner: hold ставится во время идущего удаления (race) → удаление
  прерывается/откатывается; hold на parent collection при удалении child →
  блокирует; экспорт данных при hold работает (чтение не запрещено).

---

### A8. ADR: SCIM HTTP surface — P1, блокирует A3

**DoD.**
1. `docs/adr/003-scim-http-surface.md`: scope (Users/Groups/EnterpriseUser
   extension), auth-модель, email-matching policy, soft vs hard delete,
   rate-limit, relationship к SSO (A2).
2. Согласован с security-diff-checklist; ревью + approve.

---

## B. Task Runtime

### B1. WebUI-воркфлоу задач — P1 ✅ (2026-09-04, read-only alpha)

**Статус.** Выполнена read-only фаза: `internal/http/tasks_read.go` —
`GET /api/v1/tasks` (агрегаты шагов через FILTER-подзапросы, счётчик активных
блокеров, фильтры status/collection_name, лимит ≤200, повторные плейсхолдеры
через QArgs) и `GET /api/v1/tasks/:id` (критерии, шаги с live-lease join —
истёкшие lease не показываются, receipts ≤20, checkpoints ≤10, blockers,
события ≤30). Файл намеренно GET-only: мутации остаются в MCP `task_*` —
leases и idempotency нельзя обойти. WebUI: `/tasks` — список с фильтрами
статуса, карточки с бейджами, панель деталей (план с иконками шагов и
lease-аннотациями, блокеры, receipts, checkpoints), auto-refetch 15 c,
бейдж «read-only alpha». Проверка: unit 5/5 + live-стенд API 9/9 + браузер
(login → список → детали, vision-анализ скриншота чистый). Нюанс Next 16:
rewrite-цель `LEVARA_API_URL` инлайнится в routes-manifest при build.

**DoD.**
1. Страница Tasks в WebUI: список (статус, версия, blockers), карточка
   задачи с критериями/шагами/receipts/checkpoints.
2. Действия только безопасные: просмотр, bootstrap-вью, отмена lease
   (с подтверждением). **Мутации через UI — вне scope alpha** (честная
   граница, отражена в UI как read-only бейдж).
3. Live-refresh статуса (polling ≤5s), версии конфликта видны.
4. MCP и WebUI показывают одно и то же состояние (один источник).

**Тесты.**
- Integration: MCP создаёт задачу → WebUI показывает её (одинаковые поля).
- E2E: полный lifecycle в MCP → UI отражает каждую смену статуса.
- Corner: 1000 задач → пагинация, не тормозит; задача с 200+ шагами →
  виртуализация списка; unicode в названиях; конкурентная мутация во время
  просмотра → версия в UI обновляется, стейл-данные не выдаются за текущие.

### B2. Автономный планировщик / worker — P1 ✅ (2026-09-04)

**Статус.** Выполнено: `pkg/mcp/task_worker.go` — опциональный in-process
worker (`LEVARA_TASK_WORKER=1`, off by default), который двигает auto_run
задачи через ТЕ ЖЕ примитивы `task_step` (claim/release/pass/fail CAS) —
никаких новых путей записи. Политика в authority_json: `auto_run`,
`max_concurrent_steps` (default 1), `step_deadline_seconds` (≤3600),
`max_step_attempts` (default 3). Retry: fail → release обратно в pending,
атtempts инкрементируются claim'ом; при исчерпании — blocker
«exceeded max attempts». Deadlock-детектор: auto_run-задача без claimable
шагов N раундов подряд → blocker «scheduler deadlock». Kill-switch —
остановка процесса (leases истекают, другой воркер подхватывает через
expired-lease reclaim). Worker действует от имени owner задачи
(owner-scoped lookup). Проверка: unit 6/6 (3-step chain, 0 double-claims
при гонке с внешним хостом, retry-exhaustion, kill-mid-step → подхват,
cycle → deadlock, non-auto_run не трогается) + live 6/6 против PG.

**DoD.**
1. Опциональный in-process worker (`LEVARA_TASK_WORKER=1`): подхватывает
   claimable-шаги задач с разрешённым auto-run, исполняет по политике.
2. Политика: allowlist инструментов, max concurrent steps per task,
   deadline, budgets.
3. Использование существующих примитивов: claim/lease/receipt — никаких
   новых путей записи.
4. Off по умолчанию; включение логируется; kill-switch mid-flight →
   lease истекает, задача остаётся консистентной.

**Тесты.**
- Integration: worker исполняет 3-шаговую задачу end-to-end с receipts.
- Concurrency: worker + ручной MCP-клиент на одной задаче → 0 double-claims
  (переиспользовать S2-сценарий из benchmark).
- Corner: шаг падает с retry-able ошибкой → backoff, не бесконечный; worker
  убит на середине шага → lease истекает → другой worker подхватывает;
  политика запрещает инструмент → шаг failed с понятной причиной; deadlock
  (шаг A ждёт B, B ждёт A) → deadline рвёт.

### B3. Выход Task Runtime из alpha — P2 ◐ (частично, 2026-09-04)

**Статус.** Пункты 1, 3, 4 выполнены: схемы task-инструментов каноничны в
contract.json и заморожены как v1 (additive-only); раздел «alpha boundary»
в long-horizon-runtime.md заменён на «Security and current limitations»
(отражает WebUI-наблюдение и опциональный worker); S2/S3 load-гейт
(`benchmark/task_load_gate.sh`, CI-job «task runtime load gate (S2/S3)»,
21-я проверка) зелёный локально и обязан проходить на каждом RC. Пункт 2
(зелёный e2e на 3 последовательных релизах) — накапливаемое свидетельство,
проверяется по факту будущих релизов; до этого задача не «done», а
«частично». Обновить после третьего релиза с зелёным гейтом.

**DoD.**
1. Схемы v1 зафиксированы в contract.json как stable (не alpha);
   обратная совместимость для существующих полей.
2. Полный e2e-набор сценариев из long-horizon-alpha-report зелёный на 3
   последовательных релизах.
3. Раздел «alpha boundary» в доке заменён на текущие ограничения.
4. Load-гейт: S2/S3 сценарии из multi_user.py проходят при каждом RC.

### B4. Расширяемая модель authority — P3 ✅ (2026-09-04)

**Статус.** Выполнено: `pkg/mcp/authority.go` — декларативные манифесты
(YAML в workspace): `allowed_tools` / `allowed_paths` /
`allowed_networks` (зарезервировано, deny-by-default). Биндинг через
`authority_json` (`manifest` + `manifest_sha256`); `task_step` claim
проверяет digest манифеста (`internal/http/authority_http.go`) — подмена
во время исполнения отклоняется явно (digest mismatch). Symlink-побеги
проверяются покомпонентно для манифеста и для путей шага, включая
symlinked-директории внутри allowed root. Эскалация через чейн шагов
невозможна by design: каждый шаг использует ровно манифест задачи.
Отказ — всегда явный `authority not declared: <что>`. Гайд автора:
`docs/authority-manifests.md`. Проверка: unit 10/10 (парсинг, digest
pinning, symlink-эскейпы, containment, deny-by-default, unbound no-op).

**DoD.**
1. Декларативный манифест authority (YAML в workspace): какие tools/files/
   сети разрешены задаче; валидация на claim.
2. Runtime отказывает в шаге, требующем незадекларированную authority,
   с явным сообщением (не silent).
3. Документ-гайд для авторов манифестов.

**Corner cases:** symlink вне declared roots; подмена манифеста во время
исполнения (digest-проверка); authority-эскалация через чейн шагов
(шаг 1 даёт токен шагу 2) — запрещено по умолчанию.

---

## C. Режимы и лестница

### C1. Enterprise strict preset e2e — P1 ✅ (2026-09-04)

**Статус.** Выполнено: `deploy/profiles/enterprise_e2e.sh` +
`make profile-enterprise-e2e` + CI-job `enterprise strict preset e2e`
(postgres-сервис). Матрица обязательных условий через `-config-check`
(каждое — с точным finding-кодом, dual-violation — оба кода, unknown profile
под strict) + живой strict-старт: health публичен, API 401, аудит-каталог
создан. Найден попутный гэп: пресет задаёт `POSTGRES_DSN`, а сервер читает
`DATABASE_URL`/`-pg-url` — в e2e live-фаза передаёт оба; выравнивание пресета
отдельной задачей (C6).

**DoD.**
1. CI-job (или make-цель) `test-enterprise-preset`: поднимает сервер с
   `enterprise.strict.env.example` (testcontainers Postgres, заглушка
   OIDC/SAML по мере готовности A1–A3), прогоняет smoke: health, auth-flow,
   tenant 403, audit export файл создан, отказ старта без каждого из
   обязательных условий.
2. Красный CI = пресет сломан; пустой пресет невозможен.
3. Прогон документирован в profile-presets Validation.

**Тесты / corner cases.**
- Каждый mandatory-fail-check по отдельности: убрать Postgres DSN / auth /
  signing config / tenant enforcement / audit sink → сервер не стартует,
  сообщение указывает, чего именно не хватает.
- Порядрок проверок: при двух отсутствующих условиях — обе в ошибке.
- Restart с тем же env → детерминированный успех.
- Порты заняты → внятная ошибка, не panic.

### C6. Пресет/сервер: единый DSN-контракт — P3

**Суть.** `enterprise.strict.env.example` задаёт `POSTGRES_DSN`, живой сервер
читает `DATABASE_URL`/`-pg-url`; C1-e2e компенсирует передачей обоих.
Решить: сервер читает `POSTGRES_DSN` как fallback, либо пресет переходит на
`DATABASE_URL`, либо `POSTGRES_DSN` остаётся governance-only маркером с
документацией. DoD: один источник истины, smoke/e2e без компенсаций.
Corner: оба заданы и расходятся → fail-fast с сообщением.

### C2. Team onboarding скрипт — P2

**DoD.**
1. `cmd/onboard` (или scripts/onboard.sh): принимает admin-токен + список
   пользователей/агентов → создаёт пользователей, API-ключи, базовые
   коллекции, выдаёт готовые mcp-config сниппеты.
2. Idempotent: повторный запуск не дублирует.
3. Dry-run режим: печатает план, ничего не делает.
4. Verification step в конце: для каждого пользователя — login-check +
   isolation-check (A не видит B).

**Тесты / corner cases.** дублирующийся email → skip+report; недоступный
сервер → clean fail без частичных изменений (или отчёт о частичности);
100 пользователей → batch; спецсимволы в именах; admin-токен без прав → 403.

### C3. Sync-наблюдаемость в WebUI — P2

**DoD.**
1. Страница/панель Sync: статус последнего push/pull, отставание нод
   (last-seen, кол-во pending), конфликты LWW (что выиграло, когда).
2. Данные из существующих endpoints (sync_status и т.п.) — без новых
   write-путей.
3. Alert-бейдж при отставании > threshold.

**Тесты / corner cases.** две ноды → статусы с обеих сторон согласованы;
нода умерла → бейдж, не вечный спиннер; clock skew между нодами → честное
отображение (не отрицательные «отставания»); пустая история sync → empty
state, не error; во время активного sync значения меняются — без мигания
layout'а.

### C4. Personal: single-binary embed — P3

**DoD.**
1. Встроенная лёгкая embed-модель (ONNX в бинарнике или первое скачивание
   при старте с кэшем) — mode `LEVARA_EMBED_MODE=builtin`.
2. Старт без sidecar: personal-профиль с hybrid search (semantic+BM25) сразу.
3. Документированный размер/latency budget; fallback на lexical при
   нехватке ресурсов.

**Тесты / corner cases.** офлайн-машина,模型 не скачан → lexical + понятное
сообщение; повреждённый кэш модели → перезагрузка; ARM/AMD64 обе сборки;
память <порог → авто-fallback, не OOM; переключение builtin↔sidecar →
переиндексация честно требуется (digest mismatch, не тихая путаница).

### C5. Raft-шардирование: e2e или удаление — P3

**DoD.** Решение зафиксировано: либо (a) полный e2e-набор + документация
production-readiness, либо (b) код за хард-флагом experimental с warning при
старте и удалением из документации production-путей. Промежуточное
состояние («лежит, но непонятно можно ли») устранено.

---

## D. Платформа

### D1. Реранкер из коробки — P2

**DoD.**
1. Пресеты: `LEVARA_RERANK=builtin-qwen3` (тот же approach, что embed —
   локальный endpoint через compose-profile) в team/solo пресетах.
2. ACL-before-rerank инвариант тестом зафиксирован (уже существует —
   включить в contract suite).
3. Деградация: reranker недоступен → поиск работает без rerank + метрика +
   warning, не 500.

**Тесты / corner cases.** reranker медленный (2s) → timeout → fallback;
частичный ответ реранкера → fallback на исходный порядок; ACL-утечка через
rerank (документ пользователя B в ответе A) — обязательный отрицательный
тест; rerank меняет порядок идентично при повторе (детерминизм при
temperature=0).

### D2. Structured extraction surface — P2

**DoD.**
1. lift-sidecar: REST/MCP-обёртка над schema-driven extraction для
   table-heavy PDF (`extract_structured` MCP tool, schema на входе).
2. Прогон на golden-наборе PDF (минимум 10: таблицы, сканы, мультиколонка)
   с accuracy-отчётом.
3. Неудача извлечения → явный error с причиной (no text layer / no table
   detected), не пустой успех.

**Тесты / corner cases.** пароль-защищённый PDF → внятная ошибка; PDF без
текстового слоя → fallback на OCR-flag или отказ; таблица на границе страниц
→ склейка; огромный PDF (500+ стр.) → стриминг/чанки; кириллица в таблицах.

### D3. Graph extras стабилизация — P3

**DoD.** Для каждой части за флагом (predicate synonyms, communities):
решение — promote (default-on с perf-гейтом) / keep-flag / remove.
Задокументировано в ADR; никаких «вечно experimental».

### D4. Legacy vector endpoints sunset — P3

**DoD.**
1. Метрика использования `/insert`, `/batch_insert`, `/delete` (счётчик по
   endpoint) добавлена; 2 релиза наблюдения.
2. Deprecation-заголовок `Sunset` + warning в логе при использовании.
3. Дата удаления зафиксирована в docs/api-contract.md; удаление — отдельным
   коммитом после дедлайна.

**Corner cases:** внутренние тесты сами используют legacy → мигрировать
первыми; WebUI-совместимость; сторонние клиенты — changelog + migration
гайд на новые canonical API.

---

## E. Качество и наблюдаемость

### E1. Audit external sink — см. A6 (объединено).

### E2. Alerting / SLO шаблоны — P3

**DoD.**
1. `deploy/prometheus/rules.yml` + `deploy/prometheus/alerts.yml`:
   availability, p95 latency по endpoint-группам, error rate, sync lag,
   index queue depth, audit sink failures.
2. Запуск в compose-профиле observability.
3. Прогон: искусственно вызванный инцидент → alert срабатывает (тест
   правил promtool).

**Corner cases:** flapping (порог с for-клайзом); ложный alert при backup-окне
(uptime-правило учитывает maintenance); missing-series после рестарта.

### E3. Backup-автоматизация — P3

**DoD.**
1. Cron-profile: расписание backup (SQL + workspace + uploads) с
   verify-restore после каждого (в sandbox).
2. Метрика/health-флаг «последний успешный verified backup: <ts>»; старше
   SLA → бейдж в WebUI + alert-правило (см. E2).
3. Restore-runbook протестирован end-to-end: полный дроп → восстановление →
   smoke-прогон (reuse verify-stack).

**Тесты / corner cases.** backup во время активной записи → консистентный
снапшот (WAL/barrier); verify на повреждённом архиве → fail loud; диск
переполнен во время backup → чистая отмена, не полуданные; restore на
новую версию Levara → миграции применяются или явный отказ.

---

## Порядок исполнения (рекомендация)

1. **A8 → A1 → A2 → A3** — identity-контур (P1, блокирует Enterprise prod).
2. **C1** параллельно — гейт enterprise-пресета в CI сразу после A1.
3. **B1, B2** — задачи до продуктива (P1).
4. **A4–A7, C2, C3, D1** — P2 по мере потребности пилотов.
5. **C4, C5, D2–D4, E2, E3, B3, B4** — по остаточному принципу.

Гейты: каждая задача проходит `make test` + contract-check; A-блок —
дополнительно через `docs/internal/security-diff-checklist.md`; B-блок —
через multi_user S2/S3 сценарии.

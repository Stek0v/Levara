# Корпоративный вход: LDAP/AD, OIDC, SAML и SCIM

Проверено по исходникам и локальным тестам 2026-09-10. Прямое подключение
LDAP/AD и вход через корпоративный IdP рассматриваются как равноправные
сценарии. Готовность продукта у них различается.

## Что работает сейчас

| Сценарий | Реализация | Ограничение |
|---|---|---|
| Прямой bind/search в LDAP или AD | `ldap://` с обязательным StartTLS либо `ldaps://`; service search + user bind | Один настроенный endpoint; реальный AD, CA rotation и DC failover требуют стенда |
| AD → Keycloak → OIDC bearer → HTTP/MCP | Составной интеграционный путь | Levara проверяет JWT; LDAP обслуживает Keycloak. Реальный каталог в этом аудите не подключался |
| AD FS / Entra → OIDC bearer → HTTP/MCP | Проверка подписи и claims | Требуются точные issuer, audience и конечный JWKS URL |
| Browser OIDC | Authorization Code + PKCE, state, nonce, session cookie и logout | Один issuer на browser flow; pending state хранится в процессе; реальный IdP не проверен |
| SAML SP | login, ACS, metadata | ACS возвращает JSON с JWT; автоматический вход в WebUI, SLO и IdP-initiated login не заявлены |
| SCIM Users/Groups | Users и Groups CRUD/PATCH, пагинация, мягкая деактивация | Управляемый subset; нет Bulk и полной vendor certification |
| SCIM → SSO/LDAP-учётная запись | SQL bridge по доверенному issuer и неизменяемому subject | Требует заранее согласованных `LEVARA_SCIM_ISSUER` и tenant; live provisioning job не проверен |
| AD-группа → права на документ | Локальная group/document policy реализована | Публичный document ACL API ещё не подключён к основному router |

Наличие корректного JWT подтверждает личность, но не создаёт автоматически
локального пользователя, его членство в организации или доступ к данным.
`room`, `hall` и теги служат организации материалов и не заменяют ACL.

## Быстрый пилот с AD через Keycloak

Это альтернативный путь интеграции через брокер; встроенный LDAP-вход описан ниже.
Keycloak поддерживает федерацию LDAP/Active Directory, режим READ_ONLY и
LDAPS. Для пилота задайте LDAP vendor, URL каталога, base DN и отдельную
учётную запись чтения; проверьте соединение и аутентификацию, затем импорт
одного тестового пользователя. Конкретные DN и атрибут уникального ID
определяет администратор каталога. [Документация Keycloak](https://www.keycloak.org/docs/latest/server_admin/index.html#_ldap).

Сертификат LDAPS должен проходить проверку имени и цепочки доверия.
Добавьте корпоративный CA в доверенное хранилище Keycloak; не отключайте
проверку TLS. [Настройка доверенных сертификатов](https://www.keycloak.org/server/keycloak-truststore).

В отдельном realm/client настройте получение токена вашим приложением или
MCP-клиентом. Добавьте audience API Levara и проверьте фактические `iss`,
`aud`, `sub`, `exp`, `kid` в тестовом токене. Пароли AD вводятся в IdP;
Levara получает bearer token. Образец настроек сервера:

```sh
export LEVARA_OIDC_JWKS_URL='https://idp.example.org/realms/levara/protocol/openid-connect/certs'
export LEVARA_OIDC_ISSUERS='https://idp.example.org/realms/levara'
export LEVARA_OIDC_AUDIENCES='levara-api'
# Запустите свой обычный профиль Levara с обязательной аутентификацией:
# --require-auth=true; параметры БД/хранилища — из deployment.md.
```

Это пример значений, а не готовая конфигурация вашего каталога. JWKS URL
должен быть конечным: редиректы не поддерживаются. HTTPS обязателен, кроме
точного localhost или loopback IP в локальных тестах. На старте сервер
загружает JWKS; неверная конфигурация или недоступный JWKS останавливает
запуск. Поддержаны RS256/ES256, проверки issuer/audience и времени; допуск
расхождения часов по умолчанию — пять минут. Синхронизируйте часы IdP и API.

Проверка уже полученным тестовым токеном:

```sh
export LEVARA_URL='https://levara.example.org'
# ACCESS_TOKEN задаётся вашим локальным секрет-хранилищем; не вставляйте его в отчёт.
curl --fail-with-body -H "Authorization: Bearer $ACCESS_TOKEN" \
  "$LEVARA_URL/api/v1/datasets"
```

Успех `/datasets` не доказывает browser flow. Для WebUI настройте также
Authorization Code + PKCE и проверьте `/auth/me` после callback:

```sh
export LEVARA_OIDC_CLIENT_ID='levara-web'
# LEVARA_OIDC_CLIENT_SECRET задаётся для confidential client; не печатайте его.
export LEVARA_OIDC_AUTHORIZATION_URL='https://idp.example.org/authorize'
export LEVARA_OIDC_TOKEN_URL='https://idp.example.org/token'
export LEVARA_OIDC_REDIRECT_URL='https://levara.example.org/api/v1/auth/oidc/callback'
export LEVARA_AUTH_BROWSER_RETURN_PATH='/chat'
export LEVARA_AUTH_PUBLIC_ORIGIN='https://levara.example.org'
```

Вход начинается с `/api/v1/auth/oidc/login`. Browser flow требует ровно один
issuer в `LEVARA_OIDC_ISSUERS`; bearer-проверка API может иметь свой audience в
`LEVARA_OIDC_AUDIENCES`. Pending state живёт пять минут в процессе, поэтому
multi-node deployment требует affinity либо общего хранилища состояния.

## AD FS и Microsoft Entra

Для AD FS используйте metadata/discovery выбранного сервера для получения
issuer и JWKS, а audience задайте для зарегистрированного API. AD FS
поддерживает OAuth/OIDC, но версия сервера и регистрация приложения влияют
на доступный поток. [Документация Microsoft](https://learn.microsoft.com/en-us/windows-server/identity/ad-fs/development/ad-fs-openid-connect-oauth-concepts).

Для Entra выберите конкретный tenant и согласуйте версию endpoint, issuer
и audience токена. Не заменяйте tenant точным wildcard-значением и не
принимайте ID token другого приложения вместо предназначенного API токена.
В Levara применяются те же три переменные OIDC. Получение токена, MFA,
Conditional Access и logout остаются на стороне IdP/клиента. Локальная
проверка подписи не является онлайн-проверкой отзыва токена.

## SAML: серверная поверхность

Настройки читаются только при `LEVARA_SAML_ENABLED=true`:

```sh
export LEVARA_SAML_ENABLED=true
export LEVARA_SAML_ENTITY_ID='https://levara.example.org/saml-sp'
export LEVARA_SAML_ACS_URL='https://levara.example.org/api/v1/saml/acs'
export LEVARA_SAML_METADATA_URL='https://levara.example.org/api/v1/saml/metadata'
export LEVARA_SAML_IDP_METADATA_FILE='/run/secrets/idp-metadata.xml'
export LEVARA_SAML_KEY_FILE='/run/secrets/saml-key.pem'
export LEVARA_SAML_CERT_FILE='/run/secrets/saml-cert.pem'
# Альтернатива локальному metadata-файлу: LEVARA_SAML_IDP_METADATA_URL.
```

Зарегистрируйте SP metadata в IdP, проверьте entity ID, ACS URL, сертификаты
и подписанные assertions. Открывайте `/api/v1/saml/login` в том же браузере,
который завершает ACS POST. Требования к привязке запроса, cookie, повторному
использованию и параллельным входам перечислены в [матрице сценариев](document-workflow-scenarios.md).
HTTPS ACS обязателен. Состояние привязано подписанным Secure/HttpOnly/
SameSite=None cookie к случайному RelayState и точному одноразовому ID.
Параллельные входы проверены в обоих порядках завершения; повторный ответ
отклоняется. Pending state хранится в процессе: до 100 запросов, TTL 15
минут. Перезапуск или callback на другой экземпляр требует нового входа;
для нескольких экземпляров понадобится affinity или общее хранилище
состояния. Прокси должен сохранять внешние HTTPS host/path. Успешный ACS выдаёт
`access_token` и `token_type`, но не устанавливает готовую WebUI-сессию.

## SCIM: provisioning отдельно от входа

SCIM регистрируется при доступной SQL БД и заданном `LEVARA_SCIM_TOKEN`.
Базовый URL — **`/scim/v2`**, без `/api/v1`. Используйте отдельный длинный
секрет, передавайте его только через HTTPS и настройте ротацию с IdP.
`LEVARA_SCIM_ISSUER` — постоянное имя каталога; смена этого значения не
мигрирует старые identity mappings.

```sh
export LEVARA_SCIM_ISSUER='corporate-directory'
# LEVARA_SCIM_TOKEN загружается из локального хранилища секретов.
curl --fail-with-body -H "Authorization: Bearer $LEVARA_SCIM_TOKEN" \
  "$LEVARA_URL/scim/v2/ServiceProviderConfig"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer $LEVARA_SCIM_TOKEN" \
  -H 'Content-Type: application/scim+json' \
  --data '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"immutable-test-user-001","userName":"pilot@example.org","active":true}' \
  "$LEVARA_URL/scim/v2/Users"
```

Выполняйте создание только в пилотном окружении. Сохраните возвращённый
`id`; последующие GET/PATCH/DELETE адресуют его. Передавайте неизменяемый
`externalId`: fallback на userName существует для совместимости, но смена
email тогда нарушает устойчивую связь с каталогом.

Проверены повторный POST, rename, `active:false`, фильтры `userName eq` и
`externalId eq`, пагинация и конфликт email 409. Конфликт/SQL-ошибка должны
откатить одновременно email и active; ответы GET/list отражают сохранённое
состояние. Чужой issuer, локальная запись без SCIM mapping и неизвестный
ID не доступны через SCIM. DELETE деактивирует пользователя, не удаляя
его документы и историю.

Для управляемых Groups дополнительно задайте точный существующий tenant:

```sh
export LEVARA_SCIM_TENANT_ID='tenant-id'
```

После этого доступны `/scim/v2/Groups` с GET/POST/PUT/PATCH/DELETE и membership
users. Локальные тесты покрывают idempotent create, версии, membership,
deactivation и оба SQL-диалекта. Полная совместимость с Entra provisioning не доказана одним успешным
POST. Потребуются проверка connectivity, mappings, фильтров, пагинации,
rename/deactivate и повторной синхронизации реального provisioning job.
[Требования Microsoft к SCIM endpoint](https://learn.microsoft.com/en-us/entra/identity/app-provisioning/use-scim-to-provision-users-and-groups).

SCIM DELETE деактивирует управляемую запись и отзывает локальные credentials;
реальный IdP access token остаётся ограничен сроком и политикой самого IdP.
Проверьте уже открытые browser sessions и API tokens в целевой интеграции.

## Прямой LDAP/AD: настройка и приёмка

Connector включается наличием `LEVARA_LDAP_URL` и требует SQL identity storage.
Service password читается только из файла:

```sh
export LEVARA_LDAP_URL='ldaps://dc1.example.org:636'
export LEVARA_LDAP_ISSUER='corp-directory'
export LEVARA_LDAP_DIRECTORY_KIND='ad' # ad или ldap
export LEVARA_LDAP_BASE_DN='DC=example,DC=org'
export LEVARA_LDAP_BIND_DN='CN=levara-reader,OU=Service,DC=example,DC=org'
export LEVARA_LDAP_BIND_PASSWORD_FILE='/run/secrets/levara-ldap-bind-password'
export LEVARA_LDAP_CA_FILE='/run/secrets/corporate-ca.pem'
export LEVARA_LDAP_TIMEOUT='5s'
# Для допуска только одной группы:
export LEVARA_LDAP_REQUIRED_GROUP_DN='CN=Levara Users,OU=Groups,DC=example,DC=org'
export LEVARA_LDAP_GROUP_BASE_DN='OU=Groups,DC=example,DC=org'
```

Для `ad` атрибуты по умолчанию — `sAMAccountName` и неизменяемый `objectGUID`;
для `ldap` — `uid` и `entryUUID`. При необходимости задайте
`LEVARA_LDAP_USERNAME_ATTRIBUTE`, `LEVARA_LDAP_SUBJECT_ATTRIBUTE` и
`LEVARA_LDAP_GROUP_MEMBER_ATTRIBUTE`. `ldap://` автоматически выполняет
StartTLS; plaintext bind и отключение проверки сертификата не поддерживаются.
Вход WebUI использует `/api/v1/auth/directory/login` и после успешного bind
выдаёт ту же browser session cookie.

Локальные тесты подтверждают следующий контракт; пункты с реальным каталогом
нужно повторить на тестовом AD/LDAP:

1. Только проверенный LDAPS или обязательный StartTLS; CA, hostname,
   таймауты, предел результатов и ограниченные referrals.
2. Отдельный read-only service bind для поиска; пользовательский bind для
   проверки пароля; экранирование фильтров, запрет пустого bind/password,
   однозначный результат поиска, отсутствие паролей в логах.
3. Постоянный `(directory, immutable external ID) → local user ID`.
   Rename/email reuse, несколько каталогов и миграция существующих owners
   не должны сливать пользователей по email.
4. Disabled/locked/expired account, смена пароля, недоступность DC и
   failover должны иметь явный результат. Ошибка каталога не даёт вход.
5. Группы: прямое/вложенное членство, циклы, удаление из группы, большие
   membership lists, расписание синхронизации и срок применения отзыва.
6. Общий отзыв сессий/API-ключей и прав; проверка уже выданного токена,
   скачивания, поиска, RAG/MCP и фоновой обработки после деактивации.

Connector не выполняет background sync и не имеет списка DC для failover.
Nested membership ограничено по глубине и числу просмотренных групп; задержку
применения изменений и поведение нескольких DC проверяйте на реальном каталоге.

## Диагностика

| Симптом | Что проверить |
|---|---|
| Сервер не стартует после включения OIDC | Доступность конечного JWKS, CA, HTTPS, обязательные allowlists |
| JWT отклонён | `kid`, alg, подпись, точные `iss`/`aud`, `exp`/`nbf`, часы |
| API принимает токен, WebUI возвращает на login | Browser client URLs, единственный issuer, callback cookie, identity mapping и `/auth/me` |
| SAML ACS 401 | Подпись, destination/audience, тот же браузер, request ID, срок действия |
| SCIM 404 | База `/scim/v2`, включённый token/DB, issuer и SCIM resource ID |
| SCIM 409 после rename | Email принадлежит другой identity; не выполнять ручное объединение по email |
| Группа AD не даёт доступа к документу | SCIM tenant/membership, required LDAP group и факт подключения document ACL API |
| Пользователь деактивирован, SSO всё ещё работает | Проверить identity mapping, browser credential epoch и срок внешнего IdP token |

Доказательства и оставшиеся ручные проверки: [сценарии](document-workflow-scenarios.md).
Права на материалы: [управление документами](document-management.md).

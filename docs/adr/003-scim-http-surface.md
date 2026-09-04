# ADR-003: SCIM HTTP Surface (Enterprise Provisioning)

- **Статус:** Proposed
- **Дата:** 2026-09-04
- **Связанные задачи:** backlog A3 (блокируется этим ADR), A2 (SAML), A8
- **Чек-лист:** `docs/internal/security-diff-checklist.md` (access, tenant, audit)

## Контекст

Enterprise-профиль требует корпоративного provisioning: пользователи и группы
создаются в IdP (Entra ID, Okta) и должны появляться в Levara без ручного
заведения. Стандарт де-факто — SCIM 2.0 (RFC 7644). В коде уже есть
write-side seam: `pkg/access/provisioning.go` определяет SCIM-shaped
`Provisioner` (пользователи и group→role bindings). HTTP-поверхности нет.

ADR-002 оставляет enterprise как слой адаптеров над тем же движком. Этот ADR
фиксирует форму SCIM-адаптера до реализации (бэклог A3).

## Решение

### 1. Поверхность

Минимальный, но полный для Entra ID/Okta набор (RFC 7644 §3–4):

```
GET    /scim/v2/ServiceProviderConfig
GET    /scim/v2/Schemas
GET    /scim/v2/ResourceTypes
POST   /scim/v2/Users
GET    /scim/v2/Users
GET    /scim/v2/Users/{id}
PUT    /scim/v2/Users/{id}
PATCH  /scim/v2/Users/{id}
DELETE /scim/v2/Users/{id}
GET    /scim/v2/Groups
POST   /scim/v2/Groups
PATCH  /scim/v2/Groups/{id}
DELETE /scim/v2/Groups/{id}
```

- `/Schemas`, `/ResourceTypes`, `/ServiceProviderConfig` — статические
  манифесты (фиксируются в contract.json как генерируемые).
- Фильтрация: только `userName eq "…"` и `externalId eq "…"` (достаточно для
  Entra ID/Okta matching); остальное → 400 `invalidFilter`.
- Пагинация: `startIndex`/`count` (максимум 200), ответ `totalResults`.

### 2. Аутентификация

Отдельный provisioning-токен: `LEVARA_SCIM_TOKEN` (статический bearer),
передаётся как `Authorization: Bearer …`. Обоснование: SCIM-клиенты (Entra)
умеют только статические токены; JWT-флоу сюда сознательно не примешиваем.

- Токен **не** даёт доступа к остальному API — это отдельное middleware на
  группе `/scim/v2`, а не общий JWTMiddleware.
- Токен обязателен: endpoint'ы не существуют (404 route не зарегистрирован),
  если `LEVARA_SCIM_TOKEN` пуст. Совпадение — constant-time compare.
- Доступ к `/scim/v2/ServiceProviderConfig` без токена разрешён (IdP так
  проверяет доступность перед настройкой коннектора).

### 3. Identity-matching policy (ключевое решение)

При `POST /Users` с `externalId`, который уже существует:

- матч по `externalId` → обновление существующего (идемпотентно, 200);
- матч по `userName` (email) при отсутствии `externalId` → **409
  `uniqueness`** (никаких тихих слияний личностей: внешнее несоответствие
  mail-алиасов не должно молча склеивать двух людей).
- Переименование email (PATCH `userName`) разрешено; `externalId` неизменяем
  (попытка изменить → 400 `mutability`).

Обоснование: IdP — источник истины по `externalId`; email меняется, личности
— нет. Тихий матч по email склеил бы разных людей при переиспользовании
корпоративного адреса.

### 4. Deletion semantics

- `DELETE /Users/{id}` → **soft delete**: `active=false`, сессии и API-ключи
  пользователя инвалидируются немедленно, запись сохраняется (audit + ссылки
  из receipts/workspace).
- `active=false` → все запросы с его JWT/API-ключом → 401 (проверка
  `active` в auth-middleware, кэш инвалидации ≤ 5 c).
- Hard delete — вне scope до появления retention-политики (задача A4/E3).

### 5. Groups → roles

`PATCH /Groups/{id}` `add/remove members` синхронизирует membership.
Маппинг group→role/tenant — через существующий `GroupTenantMap` /
`SuperuserGroups` из OIDCAdapter (единая политика в `pkg/access`), не новый
механизм прав. Groups с пустым маппингом создаются, но прав не дают
(лог warning при первой ссылке).

### 6. Аудит и rate limit

- Каждая мутация — audit event `actor=scim`, с `externalId` в атрибутах.
- Rate limit: отдельный бакет 10 rps burst 20 (provisioning-пики синков
  ограничены, DDoS-защита не цель — endpoint за корпоративным периметром).

### 7. Чего сознательно НЕТ (не scope)

- `/Me`, EnterpriseUser extension-поля кроме `externalId`/`active`.
- Bulk (`POST /Bulk`) — Entra/Okta синкают поштучно.
- Password sync — пароли остаются в Levara (bcrypt) либо SSO.
- SCIM → SSO «прозрачная связка»: включение A2 (SAML) независимо.

## Последствия

- Плюс: provisioning закрывается стандартным протоколом, seams уже есть,
  объем — HTTP-адаптер + invalidation-крючок в auth-middleware.
- Минус/риск: invalidation active-flag в auth-пути добавляет проверку в
  hot path — делаем через локальный кэш с TTL 5 c (см. DoD A3), не запрос
  в БД на каждый вызов.
- Нейтрально: soft delete сохраняет записи — соответствует audit-требованиям
  и ссылочной целостности receipts.

## Чек-лист безопасности (из security-diff-checklist)

- [x] access: отдельный токен, не расширяет существующие права
- [x] tenant: provisioned users получают tenant только через group-маппинг
- [x] audit: все мутации с actor=scim
- [x] MCP memory ownership: не затрагивается (SCIM не создаёт коллекций)

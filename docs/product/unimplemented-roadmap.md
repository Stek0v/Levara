# Levara — полный список нереализованного (2026-09-04)

Источник: сравнительная страница режимов (`docs/marketing/modes-comparison.html`)
и её первоисточники — `docs/marketing/{personal,solo-pro,team,enterprise}.md`,
`docs/profile-presets.md`, `docs/deployment-matrix.md`, `docs/long-horizon-runtime.md`.
Каждый пункт помечен режимом (①②③④) и приоритетом (P1 — блокирует апгрейд по
лестнице, P2 — важно для целевой аудитории режима, P3 — можно отложить).

## A. Enterprise-инфраструктура — контракты есть, бэкендов нет (④)

Документы честно разделяют: seams/contracts готовы, конкретные реализации — нет.

- [x] A1 (P1). **Raw OIDC token verification** — реализовано 2026-09-04:
  `pkg/auth/oidc.go` (JWKS + RS256/ES256 + iss/aud allowlist + exp/nbf skew +
  rotation), middleware-фолбэк в HTTP-слое, env `LEVARA_OIDC_JWKS_URL` /
  `LEVARA_OIDC_ISSUERS` / `LEVARA_OIDC_AUDIENCES`; биндинг к identity bridge —
  в composition root (архитектурный guard соблюдён). ADR на surface не нужен.
- [ ] A2 (P1). **SAML HTTP surface** — identity-федерауия для корпораций;
  контракт SSO bridge есть, SAML-реализации нет.
- [ ] A3 (P1). **SCIM HTTP surface (provisioning)** — SCIM-shaped provisioner
  seam существует; HTTP-поверхность отложена до ADR.
- [ ] A4 (P2). **KMS / BYOK реализации** — storage/KMS contracts задают scope,
  digest, retention class, legal hold flag, key reference; конкретных
  production-бэкендов нет.
- [ ] A5 (P2). **Корпоративные object storage backends** (S3 / GCS / Azure Blob
  в enterprise-качестве) — базовый S3-адаптер есть, корпоративные требования
  (шифрование на стороне провайдера, lifecycle, residency) не закрыты.
- [ ] A6 (P2). **SIEM sink** — приёмник аудит-событий для корпоративных SIEM.
- [ ] A7 (P2). **Legal hold enforcement** — флаг в контракте есть, принудительного
  исполнения нет.
- [ ] A8 (P3). **ADR на SCIM HTTP surface** — блокирует A3.

## B. Task Runtime — alpha-границы (③④)

- [ ] B1 (P1). **WebUI-воркфлоу для задач** — рантайм MCP-only; в WebUI нет
  dedicated-интерфейса для задач (создание, шаги, receipts, споры о версиях).
- [ ] B2 (P1). **Автономный планировщик / worker** — «There is no autonomous
  scheduler or worker»; возобновление задачи лежит на MCP-хосте.
- [ ] B3 (P2). **Выход из alpha** — стабилизация схем до non-alpha контракта,
  прогон полного e2e-набора на каждом релизе.
- [ ] B4 (P3). **Расширяемая модель authority** — рантайм записывает authority,
  но не грантует filesystem/network/deploy/payment/publication права.

## C. Режимы и лестница — что мешает апгрейду (①②③)

- [ ] C1 (P1). **Enterprise strict preset e2e** — пресет
  `enterprise.strict.env.example` существует, полного e2e-прогона режима в CI нет.
- [ ] C2 (P2). **Team onboarding path** — туториал 04 есть, но нет
  автоматизированного «создать пользователей → выдать API-ключи агентам →
  проверить изоляцию» скрипта.
- [ ] C3 (P2). **Sync UI/наблюдаемость для Solo Pro** — статус синхронизации
  двух нод виден только через API/логи; в WebUI нет страницы sync-состояния.
- [ ] C4 (P3). **Personal: первый запуск без Postgres, но с embed** — сейчас
  potion-подход требует sidecar; одиночный бинарник с встроенной лёгкой моделью
  упростил бы 5-минутный старт.
- [ ] C5 (P3). **Рафт-шардирование** — experimental/legacy path; «needs explicit
  e2e before production use» (deployment-matrix).

## D. Платформа — фичи с ограниченной поддержкой (все режимы)

- [ ] D1 (P2). **Реранкер по умолчанию без ручной настройки** — rerank работает,
  но требует отдельного endpoint; пресеты не включают готовый конфиг.
- [ ] D2 (P2). **Structured extraction (lift sidecar)** — маршрут table-heavy/visual
  PDF в schema-driven extraction описан в internal-доке; production-поверхность
  не выделена.
- [ ] D3 (P3). **Graph extras** — predicate synonyms, community detection и
  продвинутое temporal-поведение графа — части за флагами/в эксперименте.
- [ ] D4 (P3). **Legacy vector endpoints** — `/insert`, `/batch_insert`,
  `/delete` держатся для совместимости; план вывода из эксплуатации не зафиксирован.

## E. Качество и наблюдаемость (сквозные)

- [ ] E1 (P2). **SIEM/внешний sink для audit export** — экспорт в файл есть,
  стриминга во внешние системы нет (см. A6).
- [ ] E2 (P3). **Alerting/SLO-шаблоны** — `/metrics` и `/health/details` есть,
  готовых alert-правил в поставке нет.
- [ ] E3 (P3). **Backup-автоматизация** — cmd/backup и runbook'и есть; cron-level
  автопилот с проверкой восстановления — нет.

## Сводка

| Блок | Пунктов | P1 | P2 | P3 |
|---|---|---|---|---|
| A. Enterprise-инфраструктура | 8 | 3 | 4 | 1 |
| B. Task Runtime | 4 | 2 | 1 | 1 |
| C. Режимы и лестница | 5 | 1 | 2 | 2 |
| D. Платформа | 4 | 0 | 2 | 2 |
| E. Качество | 3 | 0 | 1 | 2 |
| **Итого** | **24** | **6** | **10** | **8** |

Критический путь по лестнице: A1–A3 (identity) + B1–B2 (задачи) + C1 (e2e
enterprise-пресета) — это то, что отделяет «пилот Enterprise» от «Enterprise в
продакшене».

Развёртка каждой позиции с DoD, тест-планами и corner cases:
[task-backlog.md](task-backlog.md).

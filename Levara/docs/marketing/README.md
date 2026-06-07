# Маркетинговые материалы LevaraOS

LevaraOS позиционируется как **память и рабочее пространство для ИИ-агентов**:
от локального инструмента одиночного разработчика до управляемой платформы для
команд с авторизацией, аудитом и enterprise-адаптерами.

Эта папка — checked-in GTM слой. Тон может быть маркетинговым, но claims должны
сверяться с:

- [../product-ladder.md](../product-ladder.md) — продуктовая лестница;
- [../profile-presets.md](../profile-presets.md) — реальные профили запуска;
- [../adr/002-product-ladder-and-layering.md](../adr/002-product-ladder-and-layering.md) — архитектурные границы.

## Позиционирование

**One-liner:** LevaraOS gives AI agents persistent, searchable, governed memory
without forcing teams into a SaaS memory silo.

**Русская версия:** LevaraOS даёт ИИ-агентам долговременную память, поиск и
рабочее пространство, которые можно держать локально, синхронизировать между
машинами или разворачивать для команды с правами доступа и аудитом.

## Матрица ЦА

| ЦА | Профиль | Главный promise | Документ | CTA |
|---|---|---|---|---|
| Один разработчик с локальными ИИ-агентами | `personal` | ИИ перестаёт забывать проект, данные остаются локально | [personal.md](personal.md) | `cp deploy/profiles/personal.local.env.example .env` |
| Power-user с несколькими машинами | `solo_pro` | Одна память на Mac, сервере и Raspberry Pi | [solo-pro.md](solo-pro.md) | настроить sync token и второй узел |
| Небольшая команда: люди + агенты | `team` | Общая память с auth, ACL и аудитом | [team.md](team.md) | `team.postgres.env.example` + `-require-auth` |
| Организация с governance | `enterprise` | Tenant isolation, audit export, identity/storage adapter seams | [enterprise.md](enterprise.md) | security pilot по strict preset |

## Доказательства, которые можно использовать

| Claim | Где подтверждать | Как формулировать |
|---|---|---|
| “Один движок для разных аудиторий” | `pkg/profile`, `deploy/profiles/*`, `docs/product-ladder.md` | Не разные продукты, а профили и адаптерные слои поверх одного core |
| “MCP-first agent memory” | `pkg/mcp`, `internal/http/mcp.go`, `AGENTS.md` | Интеграция с агентами через MCP: memory, search, workspace, sync, ops |
| “Markdown workspace as source of truth” | `pkg/workspace`, `internal/http/workspace*.go` | `.md` файлы остаются читаемыми людьми, индексы можно пересобрать |
| “Team/Enterprise governance” | `pkg/access`, `pkg/audit`, `pkg/profile` | Auth/RBAC/tenant/audit вынесены в отдельные слои и проверяются тестами |
| “Enterprise honesty” | `pkg/storage`, `pkg/access/oidc.go`, `docs/profile-presets.md` | Contracts/seams готовы; production SAML/SCIM/KMS/SIEM backends — roadmap |

## Что улучшено и что ещё улучшать

Уже хорошо:

- сегменты соответствуют продуктовой лестнице;
- Enterprise документ честно разделяет “готово” и “roadmap”;
- есть конкретные profile presets как CTA.

Нужно поддерживать:

- не обещать production-ready KMS/BYOK, SAML, SCIM HTTP, SIEM и legal hold до
  появления конкретных backend implementations;
- добавлять ссылки на тесты/доки рядом с сильными claims;
- держать one-liner и CTA одинаковыми во всех внешних материалах.

## Контент-пакеты

| Пакет | Канал | Цель |
|---|---|---|
| “Memory Palace for Claude/Codex/Cursor” | README, blog, HN, X, Habr | Personal adoption |
| “Mac ↔ Pi memory sync” | self-hosted/homelab communities | Solo Pro adoption |
| “AI agents with ACL and audit” | technical founder / platform teams | Team pilots |
| “Enterprise memory without SaaS lock-in” | security/platform stakeholders | Enterprise discovery |

Для нетехнической аудитории есть отдельная объяснялка без жаргона:
[../explainer.md](../explainer.md).

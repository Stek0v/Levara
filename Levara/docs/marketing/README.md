# Маркетинговые материалы LevaraOS

Короткий индекс материалов по целевым аудиториям (ЦА), соответствующим
продуктовой лестнице. Тон — маркетинговый, но все факты сверены с
`docs/product-ladder.md`, `docs/profile-presets.md` и
`docs/adr/002-product-ladder-and-layering.md`.

| ЦА | Профиль | Документ |
|---|---|---|
| Один разработчик с локальными ИИ-агентами | `personal` | [personal.md](personal.md) |
| Power-user с несколькими машинами | `solo_pro` | [solo-pro.md](solo-pro.md) |
| Небольшая команда (люди + агенты) | `team` | [team.md](team.md) |
| Организация с governance | `enterprise` | [enterprise.md](enterprise.md) |

Для нетехнической аудитории есть отдельная объяснялка без жаргона:
[../explainer.md](../explainer.md).

> Принцип честности: по уровню Enterprise мы прямо разделяем «уже реализовано» и
> «в разработке». Контракты адаптеров (SSO/SCIM/KMS/storage) готовы, но
> конкретные корпоративные бэкенды (SAML/SCIM HTTP, KMS/BYOK, объектное
> хранилище, SIEM, legal hold) остаются follow-up — см. `enterprise.md`.

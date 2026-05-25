# Levara Schema Inventory

Дата: 2026-05-17.

Источник истины: migration statements в `Levara/internal/http/schema.go`.
Кодовый инвентарь строится функцией `SchemaInventory()` в
`Levara/internal/http/schema_inventory.go`; architecture tests проверяют
критичные таблицы для PostgreSQL и SQLite.

## Назначение

Этот документ фиксирует смысловые группы SQL-схемы. Он не заменяет migrations:
при расхождении править нужно `schema.go`, затем обновлять этот документ.

## Core Tables

| Группа | Таблицы | Назначение |
|---|---|---|
| Principals/Auth | `principals`, `users`, `api_keys` | Пользователи, JWT/API-key identity |
| Datasets | `datasets`, `data`, `dataset_data` | Каталог загруженных документов и связь dataset -> data |
| Graph mirror | `graph_nodes`, `graph_edges` | SQL-зеркало knowledge graph, включая temporal validity |
| RBAC/Tenants | `tenants`, `user_tenant`, `roles`, `user_role`, `acl`, `dataset_shares` | Tenant membership, ACL, dataset sharing |
| Sessions | `interactions` | История запросов/ответов и session tracking |
| Memory palace | `memories` and memory pin/chat related tables | Project/user/agent memory |
| Product UI | `user_settings`, `notebooks`, `notebook_cells`, `ontologies` | Настройки, notebooks, ontology upload |
| Feedback/Ops | `feedback`, `heartbeats` | Search feedback, adaptive routing, operation history |
| Workspace/Sync | workspace and sync support tables | Workspace jobs/audit/generation state and import/export support |

## Important Columns

| Table | Columns | Why they matter |
|---|---|---|
| `data` | `room`, `tags`, `pipeline_status`, `raw_data_location` | Filtered search, pipeline idempotency, local/S3 raw data access |
| `graph_edges` | `valid_from`, `valid_until`, `superseded_by`, `dataset_id` | Temporal KG snapshots and tenant/project scoping |
| `memories` | `room`, `type/hall`, `owner_id`, pin fields | Memory palace recall precision and wake-up behavior |
| `feedback` | query/result/rating fields | Adaptive search routing and quality analytics |

## Rules

- PostgreSQL and SQLite migrations must remain semantically equivalent.
- New SQL-backed features should add an inventory test when they introduce a
  critical table.
- Graph rows with empty `dataset_id` are legacy/global rows; new scoped writes
  should prefer explicit dataset/project IDs.
- Raw data locations should use `storage://...` for non-local storage backends.


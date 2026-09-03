# REST API Reference

Complete HTTP API surface, generated from the canonical contract
(`docs/contract.json`, rev `83df7e5`). Regenerate the contract from source with
`make contract`; this table is a view, edit routes in `internal/http/routes.go`.

144 routes in 24 groups. MCP tools and gRPC methods:
see [api-contract.md](api-contract.md).

## admin

| Method | Path | Status |
|---|---|---|
| `GET` | `/admin/mcp/sessions` | canonical |
| `GET` | `/admin/mcp/summary` | canonical |
| `GET` | `/admin/mcp/tools` | canonical |
| `POST` | `/prune/data` | ops |
| `POST` | `/prune/system` | ops |

## cognify

| Method | Path | Status |
|---|---|---|
| `GET` | `/cognify/:runId/status` | canonical |
| `GET` | `/cognify/:runId/stream` | canonical |
| `POST` | `/cognify` | canonical |

## collections

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/collections/:name` | canonical |
| `DELETE` | `/collections/:name/records/:id` | canonical |
| `DELETE` | `/embedding-migrations/dual-write/:source` | canonical |
| `GET` | `/collections` | canonical |
| `GET` | `/collections/:name/meta` | canonical |
| `GET` | `/embedding-migrations/:runId/status` | canonical |
| `GET` | `/embedding-migrations/dual-write` | canonical |
| `GET` | `/reembed/:runId/status` | canonical |
| `POST` | `/collections` | canonical |
| `POST` | `/collections/:name/rename` | canonical |
| `POST` | `/embedding-migrations` | canonical |
| `POST` | `/embedding-migrations/:runId/cutover` | canonical |
| `POST` | `/embedding-migrations/:runId/retry` | canonical |
| `POST` | `/embedding-migrations/shadow-read` | canonical |
| `POST` | `/reembed` | canonical |
| `PUT` | `/collections/:name/meta` | canonical |

## datasets

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/datasets/:id` | canonical |
| `DELETE` | `/datasets/:id/data/:dataId` | canonical |
| `GET` | `/datasets` | canonical |
| `GET` | `/datasets/:id/data` | canonical |
| `GET` | `/datasets/:id/data/:dataId/raw` | canonical |
| `GET` | `/datasets/:id/data/:dataId/raw/url` | canonical |
| `GET` | `/datasets/status` | canonical |
| `PATCH` | `/datasets/:id/data/:dataId` | canonical |
| `POST` | `/datasets` | canonical |

## feedback

| Method | Path | Status |
|---|---|---|
| `GET` | `/feedback` | canonical |
| `GET` | `/feedback/implicit` | canonical |
| `GET` | `/feedback/stats` | canonical |
| `POST` | `/feedback` | canonical |

## graph

| Method | Path | Status |
|---|---|---|
| `GET` | `/graph/path` | canonical |

## ingest

| Method | Path | Status |
|---|---|---|
| `POST` | `/add` | canonical |
| `POST` | `/ocr` | canonical |

## mcp

| Method | Path | Status |
|---|---|---|
| `GET` | `/agent-trajectories` | canonical |
| `GET` | `/agent-trajectories/:id` | canonical |
| `GET` | `/mcp-analytics` | canonical |
| `GET` | `/mcp-analytics/events` | canonical |
| `GET` | `/memory-behavior` | canonical |
| `GET` | `/memory-reviews` | ops |
| `GET` | `/memory-reviews/:id` | ops |
| `GET` | `/memory-scaffold/proposals` | ops |
| `GET` | `/memory-scaffold/proposals/:id` | ops |
| `GET` | `/memory-traces/export` | ops |
| `POST` | `/memory-reviews/run` | ops |
| `POST` | `/memory-scaffold/proposals/:id/decision` | ops |

## memify

| Method | Path | Status |
|---|---|---|
| `GET` | `/memify/:runId/status` | canonical |
| `GET` | `/memify/:runId/stream` | canonical |
| `POST` | `/memify` | canonical |

## memory

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/memories/:key` | canonical |
| `GET` | `/memories` | canonical |
| `GET` | `/memories/:key` | canonical |
| `GET` | `/memories/stream` | canonical |
| `GET` | `/memory-index/status` | ops |
| `POST` | `/memories` | canonical |

## models

| Method | Path | Status |
|---|---|---|
| `GET` | `/models/rerank` | canonical |

## notebooks

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/notebooks/:id` | canonical |
| `DELETE` | `/notebooks/:id/cells/:cellId` | canonical |
| `GET` | `/notebooks` | canonical |
| `GET` | `/notebooks/:id` | canonical |
| `POST` | `/notebooks` | canonical |
| `POST` | `/notebooks/:id/:cellId/run` | alias |
| `POST` | `/notebooks/:id/cells` | canonical |
| `POST` | `/notebooks/:id/cells/:cellId/run` | canonical |
| `PUT` | `/notebooks/:id` | canonical |
| `PUT` | `/notebooks/:id/cells/:cellId` | canonical |

## ontology

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/ontologies/:id` | canonical |
| `GET` | `/ontologies` | canonical |
| `POST` | `/ontologies` | canonical |

## ops

| Method | Path | Status |
|---|---|---|
| `GET` | `/heartbeats` | ops |

## rbac

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/datasets/:id/shares/:shareId` | canonical |
| `GET` | `/acl/check` | canonical |
| `GET` | `/datasets/:id/shares` | canonical |
| `GET` | `/permissions/me` | canonical |
| `POST` | `/acl` | canonical |
| `POST` | `/datasets/:id/shares` | canonical |

## search

| Method | Path | Status |
|---|---|---|
| `POST` | `/search` | alias |
| `POST` | `/search/` | alias |
| `POST` | `/search/dual` | canonical |
| `POST` | `/search/text` | canonical |

## sessions

| Method | Path | Status |
|---|---|---|
| `GET` | `/interactions` | canonical |
| `GET` | `/interactions/:sessionId` | canonical |
| `POST` | `/interactions` | canonical |

## settings

| Method | Path | Status |
|---|---|---|
| `GET` | `/settings` | canonical |
| `PUT` | `/settings` | canonical |

## sync

| Method | Path | Status |
|---|---|---|
| `GET` | `/sync/export/collection/:name` | canonical |
| `GET` | `/sync/export/graph` | canonical |
| `GET` | `/sync/export/interactions` | canonical |
| `GET` | `/sync/export/memories` | canonical |
| `GET` | `/sync/import/collection/:runId/status` | canonical |
| `GET` | `/sync/manifest` | canonical |
| `GET` | `/sync/status` | canonical |
| `POST` | `/sync/import/collection` | canonical |
| `POST` | `/sync/import/graph` | canonical |
| `POST` | `/sync/import/interactions` | canonical |
| `POST` | `/sync/import/memories` | canonical |
| `POST` | `/sync/run` | canonical |

## tenants

| Method | Path | Status |
|---|---|---|
| `DELETE` | `/tenants/:id/users/:uid` | canonical |
| `GET` | `/tenants` | canonical |
| `GET` | `/tenants/mine` | canonical |
| `POST` | `/tenants` | canonical |
| `POST` | `/tenants/:id/users` | canonical |
| `POST` | `/tenants/select` | canonical |

## users

| Method | Path | Status |
|---|---|---|
| `GET` | `/users` | canonical |
| `GET` | `/users/me` | canonical |
| `PUT` | `/users/me` | canonical |
| `PUT` | `/users/me/password` | canonical |

## vector

| Method | Path | Status |
|---|---|---|
| `POST` | `/batch_insert` | legacy_compat |
| `POST` | `/delete` | legacy_compat |
| `POST` | `/insert` | legacy_compat |

## vsa

| Method | Path | Status |
|---|---|---|
| `GET` | `/vsa/query` | canonical |
| `GET` | `/vsa/status` | canonical |
| `POST` | `/vsa/rebuild` | canonical |

## workspace

| Method | Path | Status |
|---|---|---|
| `GET` | `/workspace/audit` | canonical |
| `GET` | `/workspace/conflicts` | canonical |
| `GET` | `/workspace/context` | canonical |
| `GET` | `/workspace/context/artifacts` | canonical |
| `GET` | `/workspace/jobs` | canonical |
| `GET` | `/workspace/log` | canonical |
| `GET` | `/workspace/manifest` | canonical |
| `GET` | `/workspace/ops/status` | canonical |
| `GET` | `/workspace/read` | canonical |
| `GET` | `/workspace/runs/get` | canonical |
| `GET` | `/workspace/watch/status` | canonical |
| `POST` | `/workspace/access/check` | canonical |
| `POST` | `/workspace/commit` | canonical |
| `POST` | `/workspace/context/artifacts/reindex` | canonical |
| `POST` | `/workspace/delete` | canonical |
| `POST` | `/workspace/gc` | canonical |
| `POST` | `/workspace/index` | canonical |
| `POST` | `/workspace/jobs/enqueue` | canonical |
| `POST` | `/workspace/jobs/retry` | canonical |
| `POST` | `/workspace/reconcile` | canonical |
| `POST` | `/workspace/reindex` | canonical |
| `POST` | `/workspace/revert` | canonical |
| `POST` | `/workspace/runs/start` | canonical |
| `POST` | `/workspace/search` | canonical |
| `POST` | `/workspace/write` | canonical |

## Usage notes

- Auth: when the server runs with `-require-auth`, all endpoints except
  `/health`, `/version`, `/metrics` and MCP discovery require
  `Authorization: Bearer <token>` (JWT or Levara API key).
- Tenant isolation: with `LEVARA_TENANT_ENFORCED=1`, authenticated requests
  without a tenant context receive 403.
- Request/response schemas: `docs/swagger.json` (OpenAPI), source of truth
  `internal/http/handler.go` and per-feature handlers.

_Generated 2026-09-03 from contract rev 83df7e5. Do not edit by hand._

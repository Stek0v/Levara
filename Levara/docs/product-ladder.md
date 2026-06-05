# Levara Product Ladder

Date: 2026-06-05
Status: planning

This document translates the current Levara architecture into a product ladder.
It is intentionally documentation-only: no REST, MCP, gRPC, schema, or runtime
contracts change as part of this plan.

## Goal

Levara should be packaged as one core engine with progressively stronger
operational, identity, and governance layers:

1. Personal / Local for one developer and local AI agents.
2. Solo Pro for a power user who wants sync, backups, and light operations.
3. Team for shared projects with per-user and per-agent permissions.
4. Enterprise for regulated organizations with corporate identity, storage,
   audit, retention, and tenant governance.

The implementation rule is simple: shared engine capabilities stay in the core;
identity, access, audit, storage, and enterprise integrations attach as
profiles or adapters.

## Product Tiers

| Tier | Audience | Default runtime | Included today | Hardening backlog | Future adapters |
|---|---|---|---|---|---|
| Personal / Local | One developer using Codex, Claude, Cursor, or similar agents | SQLite, local filesystem, local MCP, auth optional | MCP tools, memory palace, workspace context/search/read/write, local BM25/vector search, local manifests and jobs | One-command profile preset, clearer local backup, config validation for missing embedder | None required |
| Solo Pro | One power user with several machines or a Mac/Pi setup | SQLite or Postgres, local or S3-compatible storage, sync enabled | Cross-instance sync, backups, API keys, Prometheus metrics, optional S3 backend | Sync conflict guidance, backup/restore recipes, personal ops dashboard | Managed backup target, hosted edge relay |
| Team | Small team with humans and AI agents sharing project workspaces | Postgres, required auth, per-agent tokens, shared workspace root | JWT/API keys, dataset/project shares, workspace ACL preflight, workspace audit, async indexing jobs, ops status | Dedicated access service, tenant-safe filters, stricter profile validation, audit retention policy | Centralized log sink, team admin UI |
| Enterprise | Corporate teams with compliance and central governance | Postgres or managed SQL, object storage, required auth, enforced tenants | Foundational tables for users, tenants, ACL, audit, storage abstraction, metrics | Tenant enforcement, policy service, config fail-fast, external audit sink contract, retention model | OIDC/SAML, SCIM, KMS/BYOK, SIEM export, S3/GCS/Azure Blob, legal hold |

## Capability Placement

| Capability | Core engine | Personal / Local | Solo Pro | Team | Enterprise |
|---|---:|---:|---:|---:|---:|
| HNSW, WAL, collections, vectorstore | yes | yes | yes | yes | yes |
| BM25, hybrid search, rerank routing | yes | yes | yes | yes | yes |
| Cognify, graph, temporal validity | yes | yes | yes | yes | yes |
| MCP tool contracts | adapter | yes | yes | yes | yes |
| Memory palace | domain layer | yes | yes | yes | yes, tenant-scoped |
| Markdown workspace truth layer | domain layer | yes | yes | yes | yes, tenant-scoped |
| Local filesystem storage | adapter | yes | yes | optional | optional |
| S3-compatible storage | adapter | optional | yes | yes | yes |
| SQLite | adapter | yes | optional | no default | no default |
| Postgres | adapter | optional | optional | yes | yes |
| JWT and API keys | identity layer | optional | optional | required | required or bridged from SSO |
| Dataset/project sharing | access layer | no default | optional | yes | yes |
| Tenant isolation | access layer | no default | no default | optional | required |
| Workspace audit | audit layer | optional | yes | yes | yes, exportable |
| OIDC/SAML/SCIM/KMS/SIEM | enterprise adapters | no | no | no | yes |

## Target Runtime Profiles

The future config interface should be explicit:

```bash
LEVARA_PROFILE=personal|solo_pro|team|enterprise
```

Profile behavior should be validation and defaults, not a forked codebase.

| Profile | Required | Defaults | Must fail fast when |
|---|---|---|---|
| `personal` | writable data dir | SQLite, local storage, MCP enabled, auth optional | data dir cannot be created |
| `solo_pro` | writable data dir, stable sync token when sync is enabled | SQLite or Postgres, optional S3, API keys available | sync is configured without credentials |
| `team` | Postgres, stable `JWT_SECRET`, `-require-auth`, project shares | workspace audit, async index jobs, per-agent credentials | auth is disabled or DB is missing |
| `enterprise` | Postgres, stable `JWT_SECRET` or SSO bridge, tenant enforcement, audit sink | required tenant context, retention policy, object storage | tenant enforcement, audit sink, or auth is missing |

For now this is a proposal. Existing environment variables and CLI flags remain
the only runtime contract.

## Current Architectural Debt

The current codebase has the right primitives, but some boundaries are not yet
product-ready:

- Auth, RBAC, tenant, and workspace policy logic are partially implemented in
  `internal/http`, so policy decisions are tied to Fiber handlers.
- MCP tool bodies already use capability interfaces in `pkg/mcp`; this is the
  pattern to replicate for identity, access, memory, and workspace services.
- Tenant selection currently needs hardening before enterprise use: tenant
  headers must be membership-checked and tenant SQL filters must be
  parameterized.
- `APIConfig` is a broad service locator. Split runtime configuration into
  identity, workspace, search, storage, observability, and profile config
  groups before adding enterprise adapters.
- Workspace audit exists and is intentionally sanitized, but enterprise needs
  retention rules and export sinks rather than only local JSONL files.

## Roadmap

### Phase 1: product and architecture docs

- Keep public REST, MCP, and gRPC contracts unchanged.
- Land this product ladder and ADR-002.
- Cross-link with the markdown workspace deployment recipes and capability gate.
- Record acceptance criteria for future profile behavior.

### Phase 2: access policy extraction

- Add a transport-independent `pkg/access` or `pkg/identity` service.
- Move user, API key, dataset share, workspace access, and tenant membership
  decisions behind one `Authorize(actor, resource, action)` style boundary.
- Keep current HTTP/MCP behavior unchanged while tests prove parity.

### Phase 3: runtime profiles

- Add `LEVARA_PROFILE=personal|solo_pro|team|enterprise`.
- Implement profile validation only after Phase 2 isolates policy decisions.
- Make unsafe production combinations fail fast, especially missing auth,
  missing stable `JWT_SECRET`, missing Postgres for team/enterprise, and missing
  tenant enforcement for enterprise.

### Phase 4: enterprise adapters

- Add OIDC/SAML login bridge and SCIM provisioning as adapters to the identity
  layer.
- Add KMS/BYOK envelope key hooks for secret and object-storage encryption.
- Add external audit sinks for SIEM or log pipeline export.
- Add corporate object-storage adapters after the storage interface is hardened
  for streaming, presigned reads, and retention metadata.

## Acceptance Criteria For Future Implementation

- Personal profile starts with SQLite and no required auth.
- Team profile refuses to start without Postgres, stable JWT secret, and auth.
- Enterprise profile refuses to start without tenant enforcement and an audit
  sink.
- HTTP and MCP workspace access decisions share one policy implementation.
- Denied team and enterprise operations do not leak project paths, snippets,
  collection names, or tenant identifiers.
- Existing REST, MCP, and gRPC clients continue to work during policy
  extraction.


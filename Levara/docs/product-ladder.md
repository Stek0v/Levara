# Levara Product Ladder

Date: 2026-06-06
Status: implemented foundation; concrete enterprise adapters and product presets pending

This document translates the current Levara architecture into a product ladder.
It started as a planning document. The foundation has since moved into code:
access policy, runtime profile validation, audit export, and enterprise
identity seams and storage/KMS adapter contracts now exist. The remaining work
is product packaging and concrete enterprise protocol/storage integrations.

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

| Tier | Audience | Default runtime | Implemented foundation | Remaining hardening | Future adapters |
|---|---|---|---|---|---|
| Personal / Local | One developer using Codex, Claude, Cursor, or similar agents | SQLite, local filesystem, local MCP, auth optional | MCP tools, memory palace, workspace context/search/read/write, local BM25/vector search, local manifests and jobs, permissive `personal` profile | One-command profile preset, clearer local backup, local config-check command | None required |
| Solo Pro | One power user with several machines or a Mac/Pi setup | SQLite or Postgres, local or S3-compatible storage, sync enabled | Cross-instance sync, backups, API keys, Prometheus metrics, optional S3 backend, `solo_pro` sync-token validation | Sync conflict guidance, backup/restore recipes, personal ops dashboard, preset env file | Managed backup target, hosted edge relay |
| Team | Small team with humans and AI agents sharing project workspaces | Postgres, required auth, per-agent tokens, shared workspace root | JWT/API keys, dataset/project shares, shared `pkg/access` policy facade, workspace ACL preflight, workspace audit, async indexing jobs, strict-profile fail-fast | Profile preset/runbook, admin/operator UI | Centralized log sink, team admin UI |
| Enterprise | Corporate teams with compliance and central governance | Postgres or managed SQL, object storage, required auth or SSO bridge, enforced tenants | Tenant membership checks, tenant-safe SQL fragments, strict-profile fail-fast, audit export boundary with async JSONL adapter, SSO/SCIM adapter seams, storage/KMS contract shapes | Concrete protocol adapters, concrete corporate storage/KMS/BYOK backends, SIEM adapter | OIDC/SAML protocol adapter, SCIM HTTP surface, KMS/BYOK implementations, SIEM export, S3/GCS/Azure Blob adapters, legal hold enforcement in concrete backends |

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
| OIDC/SAML/SCIM/KMS/SIEM | enterprise adapters | no | no | no | partial: identity/audit/storage/KMS seams implemented; concrete protocol, SIEM, and storage backends pending |

## Target Runtime Profiles

The runtime profile interface is explicit:

```bash
LEVARA_PROFILE=personal|solo_pro|team|enterprise
LEVARA_PROFILE_STRICT=1  # optional fail-fast mode
```

Profile behavior is validation and defaults, not a forked codebase. By default
Levara logs warnings so existing deployments keep starting during migration.
When `LEVARA_PROFILE_STRICT=1` is set, unsafe `solo_pro`, `team`, and
`enterprise` profile combinations fail fast at startup.

| Profile | Required | Defaults | Must fail fast when |
|---|---|---|---|
| `personal` | writable data dir | SQLite, local storage, MCP enabled, auth optional | data dir cannot be created |
| `solo_pro` | writable data dir, stable sync token when sync is enabled | SQLite or Postgres, optional S3, API keys available | sync is configured without credentials |
| `team` | Postgres, stable `JWT_SECRET`, `-require-auth`, project shares | workspace audit, async index jobs, per-agent credentials | auth is disabled or DB is missing |
| `enterprise` | Postgres, stable `JWT_SECRET` or SSO bridge, tenant enforcement, audit sink | required tenant context, audit export, future retention/object storage | tenant enforcement, audit sink, auth/SSO, or stable signing config is missing |

The profile variables above are current runtime behavior. They do not change
REST, MCP, or gRPC wire contracts.

## Current Architectural Status

The current codebase has the right primitives and several boundaries are now in
code:

- `pkg/access` owns transport-independent actors, resources, authorization
  decisions, tenant membership, API-key permission checks, and provisioning/
  identity seams.
- `pkg/profile` owns profile normalization and warning/strict validation.
- `pkg/audit` owns generic audit export, async retry/backpressure, sanitization,
  and a local JSONL export adapter.
- `internal/http/config_groups.go` exposes typed projections of the broad
  `APIConfig` compatibility wrapper.
- MCP tool bodies already use capability interfaces in `pkg/mcp`; this is the
  pattern used for the access/audit adapter boundaries.

Remaining debt:

- `APIConfig` still exists as a broad wrapper; typed groups are projections,
  not a full call-site migration.
- Enterprise storage/KMS/BYOK contract shapes exist, but concrete corporate
  backends are not yet implemented.
- Product presets/runbooks per audience are not yet complete.

## Roadmap

### Phase 1: product and architecture docs

- Status: complete.
- Public REST, MCP, and gRPC contracts stayed unchanged.
- Land this product ladder and ADR-002.
- Cross-link with the markdown workspace deployment recipes and capability gate.
- Record acceptance criteria for future profile behavior.

### Phase 2: access policy extraction

- Status: complete for the current HTTP policy boundary.
- `pkg/access` now owns `Actor`, `Resource`, `Authorize`, tenant membership,
  activation checks, API-key permission checks, and identity/provisioning
  shapes.
- REST/MCP workspace parity tests exist.
- Boundary guard tests now prevent new direct policy SQL in HTTP handlers
  outside the approved compatibility files.

### Phase 3: runtime profiles

- Status: complete as a validation layer.
- `LEVARA_PROFILE=personal|solo_pro|team|enterprise` is implemented.
- `LEVARA_PROFILE_STRICT=1` is implemented.
- Unsafe production combinations fail fast in strict mode, especially missing
  auth/SSO, stable `JWT_SECRET`, Postgres for team/enterprise, tenant
  enforcement, and audit sink for enterprise.

### Phase 4: enterprise adapters

- Status: partial.
- Complete foundation: audit export boundary, async JSONL exporter, SSO bridge
  interface, SCIM-shaped provisioner interface, storage metadata contract,
  direct-read contract, and KMS/BYOK hook contract.
- Remaining work: concrete protocol adapters, SIEM sink, and concrete
  corporate storage/KMS backends for S3/GCS/Azure-style object stores.

## Acceptance Criteria For Future Implementation

- [x] Personal profile starts with SQLite and no required auth.
- [x] Team profile refuses to start without Postgres, stable JWT secret, and
  auth in strict mode.
- [x] Enterprise profile refuses to start without tenant enforcement and an
  audit sink in strict mode.
- [x] HTTP and MCP workspace access decisions share one policy implementation.
- [x] Denied team and enterprise operations have focused non-leakage coverage.
- [x] Existing REST, MCP, and gRPC clients continue to work during policy
  extraction.
- [x] Corporate storage/KMS/retention adapter contracts are implemented and
  tested.
- [ ] Product presets make each tier runnable without reading unrelated tier
  documentation.

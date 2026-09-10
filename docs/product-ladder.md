# Levara Product Ladder

Reviewed: 2026-09-10. Current profile IDs and capability placement are below.
Runnable presets live in [profile presets](profile-presets.md). A preset
validates configuration fields; it is not certification of a deployment or
completion of all enterprise integrations.

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

## Current user workflows and boundaries

Use [document management](document-management.md) to upload, check processing,
verify search against a source and grant an individual dataset role. A
document/group policy exists locally, but its public handlers and WebUI flow are
not connected; sharing a single document currently uses a separate dataset.
Tenant membership and dataset permissions remain distinct checks.

[Enterprise identity](enterprise-identity.md) covers native LDAP/AD,
OIDC/SAML federation, browser OIDC and SCIM Users/Groups. Local implementation
does not imply a completed real IdP/directory lifecycle.

## Product Tiers

| Tier | Audience | Default runtime | Implemented foundation | Remaining hardening | Future adapters |
|---|---|---|---|---|---|
| Personal / Local | One developer using Codex, Claude, Cursor, or similar agents | SQLite, local filesystem, local MCP, auth optional | MCP tools, memory palace, workspace context/search/read/write, local BM25/vector search, local manifests and jobs, permissive `personal` profile, preset env, config-check | Clearer local backup runbook | None required |
| Solo Pro | One power user with several machines or a Mac/Pi setup | SQLite or Postgres, local or S3-compatible storage, sync enabled | Cross-instance sync, backups, API keys, Prometheus metrics, optional S3 backend, `solo_pro` sync-token validation, preset env | Sync conflict guidance, personal ops dashboard | Managed backup target, hosted edge relay |
| Team | Small team with humans and AI agents sharing project workspaces | Postgres, required auth, per-agent tokens, shared workspace root | JWT/API keys, dataset/project shares, shared `pkg/access` policy facade, workspace ACL preflight, workspace audit, async indexing jobs, strict-profile fail-fast, preset env | Admin/operator UI | Centralized log sink, team admin UI |
| Enterprise | Corporate teams with compliance and central governance | Postgres or managed SQL, object storage, required auth or SSO bridge, enforced tenants | Tenant checks, LDAP/LDAPS/StartTLS, OIDC bearer verification/browser OIDC, SAML SP, SCIM Users/Groups, SCIM-to-SSO identity linking, document/group policy, storage/KMS contract shapes with S3/AWS KMS implementations, audit spool | Public document ACL flow; real AD/IdP/storage/KMS/SIEM acceptance; end-to-end legal hold | Additional directory/providers and managed operations |

## Capability Placement

| Capability | Core engine | Personal / Local | Solo Pro | Team | Enterprise |
|---|---:|---:|---:|---:|---:|
| HNSW, WAL, collections, vectorstore | yes | yes | yes | yes | yes |
| BM25, hybrid search, rerank routing | yes | yes | yes | yes | yes |
| Cognify, graph, temporal validity | yes | yes | yes | yes | yes |
| MCP tool contracts | adapter | yes | yes | yes | yes |
| Memory palace | domain layer | yes | yes | yes | yes; owner, collection and tenant checks have separate scope |
| Markdown workspace truth layer | domain layer | yes | yes | yes | yes, tenant-scoped |
| Local filesystem storage | adapter | yes | yes | optional | optional |
| S3-compatible storage | adapter | optional | yes | yes | yes |
| SQLite | adapter | yes | optional | no default | no default |
| Postgres | adapter | optional | optional | yes | yes |
| JWT and API keys | identity layer | optional | optional | required | required or bridged from SSO |
| Dataset/project sharing | access layer | no default | optional | yes | yes |
| Tenant context/membership checks | access layer | no default | no default | optional | required on guarded operations; not organization-wide document ACL |
| Workspace audit | audit layer | optional | yes | yes | yes, exportable |
| OIDC/SAML/SCIM/KMS/SIEM | enterprise adapters | no | no | no | partial: OIDC bearer, SAML SP and limited SCIM Users implemented; browser login, directory linkage/groups, SIEM and corporate KMS backends pending |

## Target Runtime Profiles

The runtime profile interface is explicit:

```bash
export LEVARA_PROFILE=personal  # choose personal, solo_pro, team or enterprise
export LEVARA_PROFILE_STRICT=1  # optional fail-fast mode
```

Profile behavior is validation and defaults, not a forked codebase. By default
Levara logs warnings so existing deployments keep starting during migration.
When `LEVARA_PROFILE_STRICT=1` is set, missing requirements listed below
fail at startup. The validator checks fields, not secret strength, TLS,
IdP reachability or full authorization coverage.

| Profile | Required | Defaults | Must fail fast when |
|---|---|---|---|
| `personal` | no profile-specific requirements | SQLite, local storage, MCP enabled, auth optional | no profile-specific rejection; ordinary runtime errors still apply |
| `solo_pro` | stable sync token when sync is enabled | SQLite or Postgres, optional S3, API keys available | sync is configured without credentials |
| `team` | Postgres, configured `JWT_SECRET`, `-require-auth` | workspace audit, async index jobs, per-agent credentials | auth is disabled, Postgres is missing or JWT_SECRET is unset |
| `enterprise` | Postgres, configured `JWT_SECRET`, required auth or declared SSO bridge, tenant enforcement, audit sink configuration | required tenant context, audit export, future retention/object storage | tenant enforcement, audit sink, auth/SSO, or stable signing config is missing |

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
- S3-compatible raw storage exists. GCS/Azure, corporate KMS/BYOK, SIEM
  and enforced legal hold remain implementation gaps.
- One-command packaged runbooks per audience are not yet complete; env presets
  and config-check validation are implemented.

## Acceptance and remaining work

Use [testing](testing.md) for observed results and the commands that produced
them. The [document scenario matrix](document-workflow-scenarios.md) separates
implemented behavior, source-only checks, manual acceptance and gaps.

Before selecting Team or Enterprise for real data, check individual dataset
sharing and revocation, document processing quality, the chosen IdP lifecycle,
backup/restore and the known limits of derived-data deletion. Implemented LDAP,
identity linking, group/document policy and browser OIDC still require explicit
configuration and target-environment acceptance; selecting a profile does not
enable or certify them.

The current [roadmap](product/unimplemented-roadmap.md) owns remaining work;
this guide describes present capability placement rather than completed phases.

# ADR-002: Product Ladder And Layer Boundaries

Date: 2026-06-05
Status: accepted; layer split, profile validation, and product presets complete.
Concrete enterprise backends (raw OIDC/SAML/SCIM, KMS/BYOK, corporate object
storage) remain follow-up — see Current Follow-up Decisions.

## Context

`for_cod.md` describes Levara as a high-performance knowledge graph engine with
HTTP, gRPC, MCP, vector search, graph storage, memory, workspace, sync, and
observability surfaces. The repository also contains working markdown workspace
recipes for single-node development, team servers, and Mac/Pi sync.

That is enough for a strong agent-memory product, but the initial codebase mixed
some product-layer concerns into transport handlers. In particular, auth, RBAC,
tenant, and workspace access decisions were partly implemented in
`internal/http`. MCP was already cleaner: `pkg/mcp` exposes small
capability-oriented interfaces and keeps tool bodies transport-independent.

## Decision

Levara is documented and evolved as one core engine with layered product
profiles:

1. Personal / Local.
2. Solo Pro.
3. Team.
4. Enterprise.

The product ladder lives in `docs/product-ladder.md`. This ADR fixes the
engineering boundary:

- Core engine packages own vector, WAL, BM25, graph, search, cognify, and sync
  mechanics.
- Agent memory packages own MCP-facing memory, wake-up, diary, and workspace
  behavior.
- Identity and access move behind a transport-independent policy service.
- Workspace remains a domain layer where Markdown files are the source of
  truth and vector/BM25 indexes are disposable derivatives.
- Enterprise capabilities must be adapters attached to identity, access,
  storage, audit, and profile validation. They must not be embedded directly in
  HTTP handlers.

## Target Layers

| Layer | Owns | Must not own |
|---|---|---|
| Core engine | HNSW, WAL, collection manager, vectorstore, BM25, graph algorithms, graph persistence, search/rerank, cognify | user identity, tenant policy, product edition logic |
| Agent memory | MCP tools, memory palace, wake-up, diaries, workspace context/search/read/write contracts | transport-specific auth checks |
| Identity/access | users, API keys, JWT verification, dataset shares, tenant membership, policy decisions | Fiber route registration, MCP JSON-RPC dispatch |
| Workspace plane | Markdown truth layer, manifests, generation lifecycle, jobs, citations, conflict checks, sanitized audit events | corporate SSO, KMS, SIEM specifics |
| Enterprise adapters | OIDC/SAML, SCIM, KMS/BYOK, audit export, corporate object storage, retention/legal hold | core search/indexing algorithms |

## Consequences

- The first implementation step was documentation and profile planning.
- The first code step extracted `pkg/access` and profile validation without
  changing public API behavior.
- `LEVARA_PROFILE=personal|solo_pro|team|enterprise` is now a runtime profile
  validation interface.
- `LEVARA_PROFILE_STRICT=1` is now the fail-fast gate for unsafe team and
  enterprise profile combinations.
- Enterprise audit and identity work may proceed through adapter boundaries
  because tenant hardening and the shared policy service foundation exist.
- OIDC has an optional in-tree verified-claims adapter above `IdentityBridge`;
  raw token verification and HTTP protocol routes remain deploy/ADR work.
- Enterprise storage/KMS work now has a dedicated adapter contract; concrete
  corporate backends remain follow-up implementation.
- Existing REST, MCP, and gRPC contracts remain stable while the internals are
  separated.

## Non-goals

- No REST, MCP, gRPC, SQL schema, or CLI wire-shape changes in this ADR.
- No split into separate binaries or editions.
- No rewrite of the vector, graph, cognify, or workspace indexing paths.
- No direct embedding of OIDC, SAML, SCIM, KMS, SIEM, or retention behavior in
  HTTP handlers.

## Implementation Roadmap

1. [x] Land `docs/product-ladder.md` and this ADR.
2. [x] Add policy-service tests that capture current workspace and dataset
   access behavior.
3. [x] Move workspace, dataset, tenant, API-key permission, and activation
   decisions into `pkg/access`.
4. [x] Wire REST and MCP workspace decisions to the same policy path.
5. [x] Add profile validation and strict-mode fail-fast behavior.
6. [x] Add audit export boundary through `pkg/audit`.
7. [x] Add identity bridge and provisioning seams through `pkg/access`.
8. [x] Move remaining HTTP-owned dataset-list/workspace-context visibility SQL
   into access-layer helpers.
9. [x] Add enterprise storage, retention, and KMS/BYOK adapter contracts.
10. [x] Add optional OIDC verified-claims adapter above `IdentityBridge`.
11. [x] Add product presets/runbooks for Personal, Solo Pro, Team, and
    Enterprise (`deploy/profiles/*.env.example`, `docs/profile-presets.md`,
    `server -config-check` / `make profile-smoke`).

## Acceptance Criteria

- [x] Personal/local remains simple: SQLite, local filesystem, MCP, no auth
  requirement by default.
- [x] Team profile requires Postgres, stable auth secret, and per-user/per-agent
  credentials in strict mode.
- [x] Enterprise profile requires tenant enforcement and audit export readiness
  in strict mode.
- [x] Tenant selection is membership-checked before it affects resource
  visibility.
- [x] Tenant filters are parameterized and cannot be constructed by string
  concatenation.
- [x] One policy decision path is shared by REST and MCP workspace operations.
- [x] Enterprise storage/KMS adapter contracts exist without importing into core
  search or indexing packages.
- [x] OIDC adapter code stays outside core search, indexing, and workspace
  handlers.
- [x] Product presets prove each tier can be operated without reading unrelated
  tier documentation.

## Current Follow-up Decisions

The remaining architectural decisions are intentionally smaller than the
original layer split:

- Whether raw OIDC token verification, SAML, and SCIM HTTP protocol surfaces
  live in-tree or as deployment-side plugins.
- Whether concrete storage/KMS backends live in-tree or as deploy-side adapter
  packages.
- Whether key wrapping is orchestrated by concrete storage adapters or by a
  separate secret-management package above the `pkg/storage` contract.
- The migration path from flat `APIConfig` plus projections to handlers that
  accept only narrow typed config groups.

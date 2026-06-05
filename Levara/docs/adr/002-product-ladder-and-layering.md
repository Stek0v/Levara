# ADR-002: Product Ladder And Layer Boundaries

Date: 2026-06-05
Status: proposed

## Context

`for_cod.md` describes Levara as a high-performance knowledge graph engine with
HTTP, gRPC, MCP, vector search, graph storage, memory, workspace, sync, and
observability surfaces. The repository also contains working markdown workspace
recipes for single-node development, team servers, and Mac/Pi sync.

That is enough for a strong agent-memory product, but the codebase currently
mixes some product-layer concerns into transport handlers. In particular,
auth, RBAC, tenant, and workspace access decisions are partly implemented in
`internal/http`. MCP is already cleaner: `pkg/mcp` exposes small
capability-oriented interfaces and keeps tool bodies transport-independent.

## Decision

Levara will be documented and evolved as one core engine with layered product
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
- Identity and access must move behind a transport-independent policy service
  before enterprise features are added.
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

- The first implementation step is documentation and profile planning only.
- The next code step should extract `pkg/access` or `pkg/identity` without
  changing public API behavior.
- `LEVARA_PROFILE=personal|solo_pro|team|enterprise` is a future proposal, not
  a current runtime contract.
- Enterprise work is blocked on tenant hardening and a shared policy service.
- Existing REST, MCP, and gRPC contracts remain stable while the internals are
  separated.

## Non-goals

- No REST, MCP, gRPC, SQL schema, or CLI wire-shape changes in this ADR.
- No immediate implementation of OIDC, SAML, SCIM, KMS, SIEM, or retention.
- No split into separate binaries or editions.
- No rewrite of the vector, graph, cognify, or workspace indexing paths.

## Implementation Roadmap

1. Land `docs/product-ladder.md` and this ADR.
2. Add policy-service tests that capture current workspace and dataset access
   behavior.
3. Move access decisions from `internal/http` handlers into the policy service.
4. Wire HTTP and MCP to the same service.
5. Add profile validation after the policy service is stable.
6. Add enterprise adapters only through the new identity, access, audit, and
   storage boundaries.

## Acceptance Criteria

- Personal/local remains simple: SQLite, local filesystem, MCP, no auth
  requirement by default.
- Team profile requires Postgres, stable auth secret, and per-user/per-agent
  credentials.
- Enterprise profile requires tenant enforcement and audit export readiness.
- Tenant selection is membership-checked before it affects resource visibility.
- Tenant filters are parameterized and cannot be constructed by string
  concatenation.
- One policy decision path is shared by REST and MCP workspace operations.


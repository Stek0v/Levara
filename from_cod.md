# From Codex: Layer Split Status And Next Tasks

Date: 2026-06-05
Status: active backlog

This file captures the current state after the product-ladder work and lists
the next implementation tasks. Mark completed items with `[x]` as they land.

## Current Status

The layer split is past the planning-only stage, but not yet fully enforced at
runtime.

- [x] Product ladder documented for `personal`, `solo_pro`, `team`, and
  `enterprise`.
- [x] Target architecture documented around five layers: core engine, agent
  memory, identity/access, workspace plane, and enterprise adapters.
- [x] `pkg/access` introduced as a transport-independent access-policy package.
- [x] Workspace authorization, dataset visibility, and dataset access have
  started moving out of `internal/http`.
- [x] `pkg/profile` introduced with `LEVARA_PROFILE` normalization and
  validation warnings.
- [x] Team and enterprise profile requirements documented.
- [x] Tenant header spoofing hardened through membership checks.
- [x] Tenant SQL filter changed to return bind args instead of interpolating
  raw tenant IDs.
- [x] Workspace audit can mirror sanitized events into a generic `audit.EventSink`.
- [x] Full testing scenario design added for profiles, layers, release gates,
  and enterprise readiness.
- [ ] Access-policy extraction is not complete: several decisions still live in
  `internal/http`.
- [ ] Runtime profiles are warning-only; unsafe `team` and `enterprise`
  configurations do not fail fast yet.
- [ ] Enterprise adapters are documented but not implemented.

## Main Architectural Finding

Levara already has the right primitives for the product ladder. The remaining
work is mostly boundary work:

- HTTP handlers still know too much about users, tenants, API keys, DB tables,
  and profile safety.
- MCP is closer to the desired shape because `pkg/mcp` already uses capability
  interfaces. That pattern should be copied into access, identity, audit, and
  workspace service boundaries.
- `APIConfig` is still a broad service locator. It should be split gradually
  into typed config groups before enterprise adapters are added.
- Tenant hardening has started, but enterprise readiness requires tenant
  enforcement to be explicit, testable, and fail-fast.

## Recommended Implementation Order

### Phase 2A: Finish Access Boundary

Goal: make access decisions transport-independent while preserving current
REST/MCP behavior.

- [x] Create `pkg/access`.
- [x] Add policy tests for workspace role decisions.
- [x] Move workspace access check through `access.SQLPolicy`.
- [x] Move dataset visibility helpers into `pkg/access`.
- [ ] Move tenant membership checks out of `internal/http/tenants.go` into
  `pkg/access` or a new `pkg/identity`.
- [ ] Move API-key permission parsing from HTTP helpers into `pkg/access`.
- [ ] Replace remaining `workspaceAPIKeyAllows` / `workspaceRoleAllows` helper
  paths with shared `pkg/access` functions.
- [ ] Add a single policy facade:

  ```go
  Authorize(ctx, Actor, Resource, Action) (Decision, error)
  ```

- [ ] Add parity tests proving REST and MCP workspace tools use equivalent
  access behavior.
- [ ] Add non-leakage tests for denied REST and MCP operations: no private
  path, snippet, query text, collection name, or tenant ID in denied responses.

Acceptance criteria:

- [ ] HTTP handlers adapt request/response only; they do not decide policy.
- [ ] MCP workspace tools and REST workspace routes share the same policy code.
- [ ] Existing public REST/MCP/gRPC contracts remain unchanged.

### Phase 2B: Split Identity From Transport

Goal: make authn/authz data accessible without tying it to Fiber middleware.

- [ ] Introduce `pkg/identity` or an `identity` subpackage under `pkg/access`.
- [ ] Define `Actor` with user ID, API-key permissions, tenant ID, auth method,
  and superuser flag.
- [ ] Add a request adapter in `internal/http` that constructs `Actor` from
  Fiber locals.
- [ ] Add an MCP adapter that constructs the same `Actor` shape from MCP
  session/context.
- [ ] Move superuser lookup behind the identity/access package.
- [ ] Move API-key verification result shape into identity/access, while
  leaving token parsing in the current auth layer until later.

Acceptance criteria:

- [ ] Policy code never reads Fiber locals directly.
- [ ] Tests can construct actors without HTTP requests.
- [ ] Per-agent credentials can be modeled without adding handler-specific
  branches.

### Phase 3A: Runtime Profile Enforcement

Goal: turn `LEVARA_PROFILE` from warning-only into controlled validation.

- [x] Add `pkg/profile`.
- [x] Wire profile validation into server startup as warnings.
- [ ] Add a strict mode flag or env var, for example:

  ```bash
  LEVARA_PROFILE_STRICT=1
  ```

- [ ] In strict mode, make `team` fail fast unless Postgres, required auth, and
  stable `JWT_SECRET` are configured.
- [ ] In strict mode, make `enterprise` fail fast unless Postgres, required auth
  or SSO bridge, tenant enforcement, stable signing config, and audit sink are
  configured.
- [ ] Keep `personal` permissive: SQLite/local filesystem/no auth by default.
- [ ] Keep `solo_pro` permissive except when sync is configured without stable
  credentials.
- [ ] Add profile smoke tests for `personal`, `solo_pro`, `team`, and
  `enterprise`.

Acceptance criteria:

- [ ] `personal` starts without Postgres and without auth.
- [ ] `team` refuses unsafe config in strict mode.
- [ ] `enterprise` refuses unsafe tenant/audit/auth config in strict mode.
- [ ] Warning-only behavior remains available during migration.

### Phase 3B: Config Grouping

Goal: reduce `APIConfig` service-locator pressure before enterprise adapters.

- [ ] Split config into typed groups:
  - [ ] `IdentityConfig`
  - [ ] `AccessConfig`
  - [ ] `WorkspaceConfig`
  - [ ] `SearchConfig`
  - [ ] `StorageConfig`
  - [ ] `AuditConfig`
  - [ ] `ProfileConfig`
- [ ] Keep `APIConfig` as the compatibility wrapper during migration.
- [ ] Move profile validation input construction out of `cmd/server/main.go`
  into a small bootstrap helper.
- [ ] Add tests that profile validation receives the same config facts after
  grouping.

Acceptance criteria:

- [ ] Adding an enterprise adapter does not require threading unrelated fields
  through HTTP handlers.
- [ ] Server startup remains readable and testable.

### Phase 4A: Enterprise Audit Adapter Boundary

Goal: prepare audit export without hard-coding SIEM behavior.

- [x] Add generic `audit.EventSink`.
- [x] Mirror sanitized workspace audit events into optional `WorkspaceAuditSink`.
- [ ] Add an audit adapter interface with retry/backpressure semantics.
- [ ] Add local JSONL export adapter as the first concrete implementation.
- [ ] Add tests proving audit export never blocks or breaks a user request.
- [ ] Add tests proving exported audit events contain no markdown content,
  private file paths, raw search snippets, secrets, or raw tokens.
- [ ] Add retention configuration proposal and tests for local audit files.

Acceptance criteria:

- [ ] Enterprise audit export can be plugged in without editing workspace
  handlers.
- [ ] Audit failures are observable but do not break core operations.

### Phase 4B: Enterprise Identity Adapters

Goal: add enterprise identity as adapters, not as handler logic.

- [ ] Define OIDC/SAML bridge interface that maps external subject to Levara
  principal.
- [ ] Define SCIM provisioning interface for users, groups, and tenant
  memberships.
- [ ] Add contract tests for external subject mapping and deactivation.
- [ ] Keep JWT/API-key local auth as the default for `personal`, `solo_pro`,
  and `team`.

Acceptance criteria:

- [ ] Enterprise SSO can be added without changing core search, memory,
  workspace, or MCP tool code.
- [ ] Deactivated users lose access through the shared policy layer.

### Phase 4C: Enterprise Storage And KMS

Goal: prepare corporate object storage and key management.

- [ ] Review current storage abstraction and identify where retention metadata
  and encrypted object metadata belong.
- [ ] Define object-storage adapter contract for S3/GCS/Azure-compatible
  backends.
- [ ] Define KMS/BYOK hook contract for secrets and object metadata.
- [ ] Add tests for presigned reads, idempotent deletes, retention metadata, and
  adapter failure behavior.

Acceptance criteria:

- [ ] Enterprise storage adapters do not touch vector, BM25, graph, or cognify
  algorithms.
- [ ] Personal/local filesystem storage remains simple.

## Testing Tasks

- [x] Add `Levara/docs/full-testing-scenarios.md`.
- [x] Add docs coverage for the product-ladder testing plan.
- [x] Validate focused packages after current PR merges:
  `go test ./internal/http ./pkg/embed ./cmd/server`.
- [ ] Add `make test-commit` for S0-S4 focused checks.
- [ ] Add `make test-release-candidate` for profile smoke, sync/backup,
  workspace eval, and Pi smoke gates.
- [ ] Add strict profile tests under `pkg/profile` and `cmd/server`.
- [ ] Add REST/MCP policy parity tests under `internal/http`.
- [ ] Add enterprise audit export contract tests under `pkg/audit`.
- [ ] Add tenant isolation negative tests covering graph/search/workspace
  surfaces.

## Immediate Next Sprint

Recommended next sprint scope:

1. [ ] Create `pkg/access.Actor`, `Resource`, and generic `Authorize` facade.
2. [ ] Move tenant membership helper into `pkg/access`.
3. [ ] Replace remaining HTTP-local workspace permission helpers with
   `pkg/access`.
4. [ ] Add REST/MCP parity tests for workspace read/write/search/audit.
5. [ ] Add `LEVARA_PROFILE_STRICT=1` and fail-fast tests for `team` and
   `enterprise`.

Do not start OIDC, SCIM, KMS, or SIEM work before the shared policy and strict
profile validation are stable.

## Files To Watch

- `Levara/pkg/access/policy.go`
- `Levara/pkg/profile/profile.go`
- `Levara/internal/http/workspace.go`
- `Levara/internal/http/workspace_audit.go`
- `Levara/internal/http/tenants.go`
- `Levara/internal/http/auth.go`
- `Levara/internal/http/api.go`
- `Levara/cmd/server/main.go`
- `Levara/cmd/server/bootstrap.go`
- `Levara/pkg/audit/audit.go`
- `Levara/pkg/mcp/*`


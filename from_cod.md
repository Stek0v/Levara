# From Codex: Layer Split Status And Next Tasks

Date: 2026-06-06
Status: active completion plan

This file captures the current state after the product-ladder work and lists
the remaining tasks required to bring the layer/product split to 100%.
Mark completed items with `[x]` as they land.

## Current Status

The layer split is no longer planning-only. Access, profile validation, audit
export, and identity-adapter seams exist in code. The remaining work is to
finish product packaging, remove the last HTTP-owned policy queries, implement
enterprise storage/KMS boundaries, and keep docs aligned with runtime behavior.

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
- [x] Access-policy extraction complete — workspace authorization, dataset
  visibility/access, superuser lookup, dataset-share management, tenant
  membership, tenant auto-select (`SQLPolicy.DefaultTenantForUser`), the tenant
  SQL filter fragment (`access.TenantOwnerFilterSQL`), and the
  `acl.permission_type` vocabulary (`access.ValidPermissionType` /
  `PermissionTypes`) now live in `pkg/access`. `internal/http/tenants.go`
  delegates every policy decision; handlers only adapt request/response.
- [x] Runtime profiles fail fast in strict mode (`LEVARA_PROFILE_STRICT=1`):
  unsafe `team`/`enterprise` configs exit non-zero. Warning-only remains the
  default during migration.
- [x] Enterprise audit export adapter boundary is implemented.
- [x] Enterprise identity adapter seams are implemented.
- [x] Enterprise storage, object-retention, and KMS/BYOK adapter contracts are
  implemented; concrete corporate backends remain C6 packaging/adapter work.
- [x] Product packaging by audience is complete: `deploy/profiles/*.env.example`
  presets plus `docs/profile-presets.md` runbooks cover each tier, and
  `server -config-check` / `make profile-smoke` give a one-command
  per-audience validation path.
- [x] Canonical docs were stale: `product-ladder.md` and ADR-002 still
  describe implemented behavior as future/proposed unless updated in this
  completion pass.

## Main Architectural Finding

Levara already has the right primitives for the product ladder. The remaining
work is mostly product hardening and boundary cleanup:

- Some HTTP handlers still know too much about users, dataset visibility, and
  share tables. `workspace_context.go`, `api_datasets.go`, `api_admin.go`, and
  parts of `rbac.go` still issue access-shaped SQL directly instead of going
  through a query/facade on `pkg/access`.
- MCP is closer to the desired shape because `pkg/mcp` already uses capability
  interfaces. That pattern should be copied into access, identity, audit, and
  workspace service boundaries.
- `APIConfig` now has typed projections, but it is still a broad compatibility
  wrapper. New code can use narrow groups, but existing handlers still mostly
  depend on the flat locator.
- Tenant hardening is substantially better, but enterprise readiness also needs
  storage/KMS guarantees and operator-facing profile packaging.

## 100% Completion Definition

The layer/product split is complete when all of the following are true:

1. **Docs match code:** canonical docs describe implemented profile, access,
   audit, identity, and testing behavior accurately.
2. **Product presets exist:** each audience has a runnable profile preset:
   Personal, Solo Pro, Team, Enterprise.
3. **HTTP policy cleanup is complete:** HTTP handlers adapt request/response and
   call `pkg/access`; no handler owns access-shaped SQL unless it is pure CRUD
   listing data for an already-authorized admin/user view.
4. **Enterprise storage/KMS seam exists:** object storage, retention metadata,
   and KMS/BYOK hooks are adapter contracts with tests.
5. **Release gates enforce the ladder:** `make test-commit` and
   `make test-release-candidate` cover the profile matrix and enterprise
   readiness boundaries.
6. **No public contract regression:** REST, MCP, gRPC, and CLI wire contracts
   remain stable unless a later ADR explicitly proposes a versioned change.

## Completion Roadmap

### C0: Synchronize Canonical Docs

Goal: remove stale planning/proposal language and make docs decision-ready for
the current implementation state.

- [x] Update `Levara/docs/product-ladder.md` from planning/proposal language to
  implemented-foundation status.
- [x] Mark `LEVARA_PROFILE` and `LEVARA_PROFILE_STRICT=1` as real runtime
  interfaces, not future proposals.
- [x] Move access/profile/audit/identity items from "future/hardening" columns
  into "implemented foundation" where applicable.
- [x] Keep storage/KMS/SIEM/protocol SSO clearly marked as remaining work.
- [x] Update ADR-002 from proposed-only wording to accepted/evolving wording,
  including the fact that Phases 2A, 2B, 3A, 3B, 4A, and 4B have landed.
- [x] Add a "remaining decisions" note for Phase 4C storage/KMS and product
  packaging.
- [x] Run docs tests after edits.

Acceptance criteria:

- [x] A reader can tell which profile/layer behavior exists today.
- [x] A reader can tell which enterprise pieces are only seams/contracts.
- [x] No doc says `LEVARA_PROFILE` is future-only.

### C1: Product Presets And Operator UX

Goal: turn the profile ladder into runnable deployment choices for each target
audience.

- [x] Add sample env files or recipes:
  - [x] `personal.local.env.example`
  - [x] `solo_pro.sync.env.example`
  - [x] `team.postgres.env.example`
  - [x] `enterprise.strict.env.example`
- [x] Add a short profile matrix document or section that maps:
  - [x] required services;
  - [x] expected auth mode;
  - [x] storage mode;
  - [x] audit mode;
  - [x] startup failure conditions.
- [x] Add a lightweight local smoke script or Make target that starts each
  non-enterprise profile in dry-run/config-check mode once such mode exists.
  `server -config-check` is the dry-run mode (resolves + validates the profile
  from env/flags, opens no listeners/DB/network); `deploy/profiles/smoke.sh`
  (`make profile-smoke`) runs personal/solo_pro/team presets through it.
- [x] Add explicit guidance for single developer + AI agents:
  local MCP, no required auth, SQLite, workspace root, memory palace.
- [x] Add explicit guidance for corporate teams:
  strict mode, Postgres, tenant enforcement, audit export, provisioning bridge,
  stable signing config.

Acceptance criteria:

- [x] A solo developer can start Personal without reading enterprise docs.
- [x] A team operator can see why Postgres/auth/JWT are required.
- [x] Enterprise docs do not imply unsupported KMS/object-storage features are
  already production-ready.

### C2: HTTP Policy Cleanup

Goal: finish the boundary so HTTP handlers do not own access-shaped SQL.

- [x] Add `SQLPolicy.VisibleDatasetIDs(ctx, actor)` or equivalent typed method
  that can replace direct list visibility queries in:
  - [x] `internal/http/workspace_context.go`
  - [x] `internal/http/api_datasets.go`
- [x] Add `SQLPolicy.ListVisibleDatasets(ctx, actor)` or a narrow access-layer
  query helper returning DTO-neutral records for dataset list endpoints.
- [x] Route `api_admin.go` superuser checks through `SQLPolicy.IsSuperuser`
  or a shared admin policy helper.
- [x] Move share-management validation in `rbac.go` fully behind access-layer
  helpers:
  - [x] role vocabulary;
  - [x] grant/revoke permission decision;
  - [x] target user lookup policy boundary, if it becomes permission-sensitive.
- [x] Add grep-based or unit guard to catch new direct `is_superuser`,
  `dataset_shares`, and `user_tenant` policy reads in `internal/http` outside
  approved CRUD/schema/test files.

Acceptance criteria:

- [x] Access decisions are testable without Fiber.
- [x] HTTP handlers own parsing/status codes/DTOs only for the cleaned
  visibility/admin/share-decision paths; remaining access-table SQL is limited
  to approved CRUD/schema files by a guard test.
- [x] Existing route behavior and response shapes do not change.

### C3: APIConfig Migration From Projection To Narrow Inputs

Goal: move from "flat service locator with projections" to handlers/adapters
accepting narrow groups.

- [x] Pick one low-risk surface and migrate it first:
  - [x] workspace audit exporter wiring uses `AuditConfig`
    (`mirrorWorkspaceAuditEvent(cfg.Audit(), event)`);
  - [x] tenant middleware/access helpers use `AccessConfig`;
  - [x] sync manifest uses `IdentityConfig` plus narrow `AccessConfig` and
    `SearchConfig` inputs.
- [x] Avoid a big-bang rewrite. Migrate one group at a time and keep tests
  focused.
- [x] Add a convention: new handlers/adapters must accept a narrow config group
  unless they genuinely need multiple groups.
- [x] Keep `APIConfig` as compatibility wrapper until call sites shrink
  naturally.

Acceptance criteria:

- [x] At least one production handler path no longer accepts full `APIConfig`
  when it only needs one concern.
- [x] New enterprise adapters do not depend on full `APIConfig`: the
  workspace audit-export consumer takes the narrow `AuditConfig` group.

### C4: Enterprise Storage/KMS Boundary

Goal: implement Phase 4C without touching vector/BM25/graph/cognify core.

- [x] Review current `pkg/storage` interface and upload/raw-object call sites.
- [x] Define object metadata shape:
  - [x] retention class;
  - [x] legal hold flag;
  - [x] encryption key reference;
  - [x] content digest;
  - [x] tenant/project scope.
- [x] Define storage adapter contract for:
  - [x] local filesystem;
  - [x] S3-compatible object storage;
  - [x] future GCS/Azure compatibility.
- [x] Define KMS/BYOK hook contract:
  - [x] encrypt data key;
  - [x] decrypt data key;
  - [x] rotate key reference;
  - [x] report key metadata without exposing key material.
- [x] Add contract tests:
  - [x] idempotent delete;
  - [x] presigned/direct read behavior;
  - [x] retention metadata preserved;
  - [x] legal hold blocks delete where expected;
  - [x] KMS hook called without leaking plaintext keys;
  - [x] adapter failure is observable and does not corrupt core indexes.

Acceptance criteria:

- [x] Enterprise storage can be implemented as an adapter.
- [x] Personal/local storage remains simple and does not require KMS.
- [x] Core search/indexing packages do not import enterprise storage/KMS code.

### C5: Enterprise Protocol Adapters

Goal: move from generic SSO/SCIM seams to concrete optional integrations.

- [x] Decide whether concrete OIDC/SAML/SCIM adapters belong in-tree or as
  optional build/deploy packages: use optional in-tree adapter contracts first;
  defer new HTTP protocol surfaces until ADR/route proposals exist.
- [x] If in-tree, add OIDC adapter first:
  - [x] verified token/session input;
  - [x] issuer/subject mapping;
  - [x] group-to-tenant mapping;
  - [x] tests with local fixtures only.
- [x] Keep SCIM HTTP surface deferred until an ADR or route contract proposal.
- [x] Keep protocol-specific code out of core engine and workspace handlers.

Acceptance criteria:

- [x] `pkg/access.IdentityBridge` remains the policy-facing seam.
- [x] Protocol adapters can be disabled entirely for Personal/Solo/Team.

### C6: Release Gate Completion

Goal: make the 100% state enforceable.

- [x] Add checks that docs profile status and `from_cod.md` completion state do
  not drift from code-owned profile constants.
- [x] Expand `make test-release-candidate` when storage/KMS contracts land.
- [x] Add a CI-friendly target for profile config validation without starting
  external services.
- [x] Add security-diff checklist for changes touching:
  - [x] access;
  - [x] tenant;
  - [x] audit export;
  - [x] storage/KMS;
  - [x] MCP memory ownership.

Acceptance criteria:

- [x] A PR that weakens tenant/auth/audit profile guarantees fails a focused
  test.
- [x] A PR that reintroduces policy SQL into HTTP is caught by review tooling
  or tests.

## Recommended Implementation Order

### Phase 2A: Finish Access Boundary

Goal: make access decisions transport-independent while preserving current
REST/MCP behavior.

- [x] Create `pkg/access`.
- [x] Add policy tests for workspace role decisions.
- [x] Move workspace access check through `access.SQLPolicy`.
- [x] Move dataset visibility helpers into `pkg/access`.
- [x] Move tenant membership checks out of `internal/http/tenants.go` into
  `pkg/access` (`SQLPolicy.IsTenantMember`).
- [x] Move API-key permission parsing from HTTP helpers into `pkg/access`
  (`access.APIKeyAllows`).
- [x] Replace remaining `workspaceAPIKeyAllows` / `workspaceRoleAllows` helper
  paths with shared `pkg/access` functions.
- [x] Add a single policy facade:

  ```go
  Authorize(ctx, Actor, Resource, Action) (Decision, error)
  ```

- [x] Add parity tests proving REST and MCP workspace tools use equivalent
  access behavior.
- [x] Add non-leakage tests for denied REST and MCP operations: no private
  path, snippet, query text, collection name, or tenant ID in denied responses.

Acceptance criteria:

- [x] HTTP handlers adapt request/response only; they do not decide policy.
- [x] MCP workspace tools and REST workspace routes share the same policy code.
- [x] Existing public REST/MCP/gRPC contracts remain unchanged.

### Phase 2B: Split Identity From Transport

Goal: make authn/authz data accessible without tying it to Fiber middleware.

- [x] Introduce an identity home under `pkg/access` (`pkg/access/identity.go`)
  instead of a separate `pkg/identity` — `Actor` already lives in `pkg/access`,
  so identity shapes are grouped there.
- [x] Define `Actor` with user ID, API-key permissions, tenant ID, auth method,
  and superuser flag.
- [x] Add a request adapter in `internal/http` that constructs `Actor` from
  Fiber locals (`workspaceActorFromFiber`).
- [x] Add an MCP adapter that constructs the same `Actor` shape from MCP
  session/context (`workspaceActorFromMCP`).
- [x] Move superuser lookup behind the identity/access package
  (`SQLPolicy.IsSuperuser`; `policy.go` ×2 + `rbac.go` now route through it).
- [x] Move API-key verification result shape into identity/access
  (`access.APIKeyIdentity` + `.Actor()`); `verifyAPIKey` keeps token hashing and
  the key→user query in the auth layer but returns the typed shape.

Acceptance criteria:

- [x] Policy code never reads Fiber locals directly.
- [x] Tests can construct actors without HTTP requests.
- [x] Per-agent credentials can be modeled without adding handler-specific
  branches (Phase 4B: `access.Provisioner` provisions principals — including
  per-agent ones — by mutating the shared `users`/`user_tenant` tables that
  `SQLPolicy` already reads; no per-handler branch).

### Phase 3A: Runtime Profile Enforcement

Goal: turn `LEVARA_PROFILE` from warning-only into controlled validation.

- [x] Add `pkg/profile`.
- [x] Wire profile validation into server startup as warnings.
- [x] Add a strict mode flag or env var, for example:

  ```bash
  LEVARA_PROFILE_STRICT=1
  ```

- [x] In strict mode, make `team` fail fast unless Postgres, required auth, and
  stable `JWT_SECRET` are configured.
- [x] In strict mode, make `enterprise` fail fast unless Postgres, required auth
  or SSO bridge, tenant enforcement, stable signing config, and audit sink are
  configured.
- [x] Keep `personal` permissive: SQLite/local filesystem/no auth by default.
- [x] Keep `solo_pro` permissive except when sync is configured without stable
  credentials.
- [x] Add profile smoke tests for `personal`, `solo_pro`, `team`, and
  `enterprise`.

Acceptance criteria:

- [x] `personal` starts without Postgres and without auth.
- [x] `team` refuses unsafe config in strict mode.
- [x] `enterprise` refuses unsafe tenant/audit/auth config in strict mode.
- [x] Warning-only behavior remains available during migration.

### Phase 3B: Config Grouping

Goal: reduce `APIConfig` service-locator pressure before enterprise adapters.

- [x] Split config into typed groups (projections of the flat `APIConfig` in
  `internal/http/config_groups.go`):
  - [x] `IdentityConfig` — JWTSecret, RequireAuth, SyncToken, Version.
  - [x] `AccessConfig` — shared `*sql.DB` for policy decisions.
  - [x] `WorkspaceConfig` — WorkspacePath, WorkspaceWatcher.
  - [x] `SearchConfig` — embed/collections/BM25/LLM/rerank/router/strategies.
  - [x] `StorageConfig` — PostgresDSN, StoragePath, FileStorage.
  - [x] `AuditConfig` — MCPAudit, WorkspaceAuditSink, MCPAgentBucket.
  - [x] `ProfileConfig` — `profile.Config` built by the bootstrap helper; its
    facts are env/runtime-derived so it lives at the cmd/server layer, not as a
    pure `APIConfig` subset.
- [x] Keep `APIConfig` as the compatibility wrapper during migration (stays flat
  with all fields + `cfg.Identity()/.Access()/.Workspace()/.Search()/.Storage()/.Audit()`
  projections; no call site or struct literal changed).
- [x] Move profile validation input construction out of `cmd/server/main.go`
  into a small bootstrap helper (`buildRuntimeProfileConfig` + `auditSinkConfigured`).
- [x] Add tests that profile validation receives the same config facts after
  grouping (`internal/http/config_groups_test.go` round-trip guard;
  `cmd/server/profile_config_test.go` same-facts + audit-sink truth table).

Acceptance criteria:

- [x] Adding an enterprise adapter does not require threading unrelated fields
  through HTTP handlers — a new adapter takes the narrow group it needs (e.g.
  `AuditConfig`) instead of the whole locator. Handler migration to the groups
  is incremental from here.
- [x] Server startup remains readable and testable — the profile-fact mapping is
  now a single named, unit-tested seam instead of an inline literal in startup
  wiring.

### Phase 4A: Enterprise Audit Adapter Boundary

Goal: prepare audit export without hard-coding SIEM behavior.

- [x] Add generic `audit.EventSink`.
- [x] Mirror sanitized workspace audit events into optional `WorkspaceAuditSink`.
- [x] Add an audit adapter interface with retry/backpressure semantics
  (`audit.Exporter` + `AsyncExporter`: bounded buffer, drop-on-full backpressure
  with a `Dropped` counter, bounded retry with doubling backoff capped at 2s,
  `ExportStats` for observability).
- [x] Add local JSONL export adapter as the first concrete implementation
  (`audit.EventFileSink` daily-rolled `audit-YYYY-MM-DD.jsonl` + gzip-on-rotation;
  `audit.NewJSONLExporter` wires it behind the `AsyncExporter`). Wired into
  startup via `initWorkspaceAuditExporter` (gated on `LEVARA_WORKSPACE_AUDIT_EXPORT`).
- [x] Add tests proving audit export never blocks or breaks a user request
  (`TestAsyncExporterNeverBlocks`: a wedged sink still returns LogEvent in µs and
  sheds the overflow as drops).
- [x] Add tests proving exported audit events contain no markdown content,
  private file paths, raw search snippets, secrets, or raw tokens
  (`TestSanitizeEventScrubsLeaks` + `SanitizeEvent` defense-in-depth at the boundary).
- [x] Add retention configuration proposal and tests for local audit files
  (`LEVARA_WORKSPACE_AUDIT_RETENTION_DAYS`, default 30; `TestEventFileSinkRetentionPrunes`).

Acceptance criteria:

- [x] Enterprise audit export can be plugged in without editing workspace
  handlers (the adapter drops into the existing `WorkspaceAuditSink` slot; only
  bootstrap wiring changed, no handler edits).
- [x] Audit failures are observable but do not break core operations (delivery
  is async + best-effort with drop/retry/failed counters; a failed init returns
  nil and startup continues).

### Phase 4B: Enterprise Identity Adapters

Goal: add enterprise identity as adapters, not as handler logic.

- [x] Define OIDC/SAML bridge interface that maps external subject to Levara
  principal (`access.IdentityBridge` + `ExternalIdentity`→`Principal`;
  `LocalIdentityBridge` default no-ops external auth, `MappedIdentityBridge` +
  `SubjectResolver` is the reusable reference; protocol-agnostic so OIDC and
  SAML share one seam).
- [x] Define SCIM provisioning interface for users, groups, and tenant
  memberships (`access.Provisioner`: `ProvisionUser`/`DeactivateUser`/
  `SyncTenantMembership`; `NoopProvisioner` default, `SQLProvisioner` reference
  reconciles `users`/`user_tenant` in a tx).
- [x] Add contract tests for external subject mapping and deactivation
  (`pkg/access/sso_test.go` subject mapping + validation; `provisioning_test.go`
  deactivation denied through `AuthorizeWorkspace`/`Authorize` incl. superuser,
  tenant set-diff sync).
- [x] Keep JWT/API-key local auth as the default for `personal`, `solo_pro`,
  and `team` (default deployment wires `LocalIdentityBridge` + `NoopProvisioner`;
  no SSO unless `LEVARA_SSO_BRIDGE` is set).

Acceptance criteria:

- [x] Enterprise SSO can be added without changing core search, memory,
  workspace, or MCP tool code (the bridge/provisioner are constructed at the
  auth/provisioning seam; the rest of the system keeps seeing a plain `Actor`
  and the existing `users`/`user_tenant` tables — no new table/migration, so
  the API contract is unchanged).
- [x] Deactivated users lose access through the shared policy layer
  (`SQLPolicy.IsActive` gate in `AuthorizeWorkspace` + the dataset branch of
  `Authorize`; `users.is_active = false` denies with reason `user_inactive`
  before ownership/share/superuser are considered).

### Phase 4C: Enterprise Storage And KMS

Goal: prepare corporate object storage and key management.

- [x] Review current storage abstraction and identify where retention metadata
  and encrypted object metadata belong.
- [x] Define object-storage adapter contract for S3/GCS/Azure-compatible
  backends.
- [x] Define KMS/BYOK hook contract for secrets and object metadata.
- [x] Add tests for presigned reads, idempotent deletes, retention metadata, and
  adapter failure behavior.

Acceptance criteria:

- [x] Enterprise storage adapters do not touch vector, BM25, graph, or cognify
  algorithms.
- [x] Personal/local filesystem storage remains simple.

## Testing Tasks

- [x] Add `Levara/docs/full-testing-scenarios.md`.
- [x] Add docs coverage for the product-ladder testing plan.
- [x] Validate focused packages after current PR merges:
  `go test ./internal/http ./pkg/embed ./cmd/server`.
- [x] Add `make test-commit` for S0-S4 focused checks.
- [x] Add `make test-release-candidate` for profile smoke, sync/backup,
  workspace eval, and Pi smoke gates.
- [x] Add strict profile tests under `pkg/profile` and `cmd/server`.
- [x] Add REST/MCP policy parity tests under `internal/http`.
- [x] Add enterprise audit export contract tests under `pkg/audit`
  (`export_test.go`: no-block/backpressure, retry-then-deliver, fail-after-retries,
  no-leak sanitization, JSONL write, retention prune).
- [x] Add tenant isolation negative tests covering graph/search/workspace
  surfaces.

## Immediate Next Sprint

Recommended next sprint scope:

1. [x] Create `pkg/access.Actor`, `Resource`, and generic `Authorize` facade.
2. [x] Move tenant membership helper into `pkg/access`.
3. [x] Replace remaining HTTP-local workspace permission helpers with
   `pkg/access`.
4. [x] Add REST/MCP parity tests for workspace read/write/search/audit.
5. [x] Add `LEVARA_PROFILE_STRICT=1` and fail-fast tests for `team` and
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

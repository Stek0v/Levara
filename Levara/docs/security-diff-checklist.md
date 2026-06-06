# Security Diff Checklist

Use this checklist for PRs that touch Levara's layered product boundary. It is
not a replacement for code review; it is the minimum review scope that keeps
Personal, Solo Pro, Team, and Enterprise from collapsing into one unsafe mode.

## Always Run

- `git diff --check`
- `make profile-config-check`
- `make test-commit`

For release candidates or changes touching enterprise adapters, also run:

- `make test-release-candidate`

## Access

Trigger this section when a PR touches `pkg/access`, identity bridges,
provisioning, API-key permissions, dataset shares, or role vocabulary.

- Confirm `pkg/access.IdentityBridge` remains the policy-facing seam for
  external identities.
- Confirm local/JWT/API-key auth still works when OIDC/SAML/SCIM adapters are
  disabled.
- Confirm role names and grant/revoke checks still live in `pkg/access`, not in
  HTTP handlers.
- Run `go test ./pkg/access ./internal/http -run 'Policy|RBAC|Tenant|OIDC'`.

## Tenant

Trigger this section when a PR touches tenant middleware, tenant filters,
workspace/project visibility, or dataset-list visibility.

- Confirm tenant context is membership-checked before it affects visibility.
- Confirm tenant SQL stays parameterized; no string-concatenated filters.
- Confirm denied paths return no cross-tenant content.
- Run `go test ./internal/http -run 'Tenant|Workspace|Dataset|PolicyBoundary'`.

## Audit Export

Trigger this section when a PR touches `pkg/audit`, workspace audit handlers, or
profile audit-sink validation.

- Confirm audit export stays asynchronous and bounded.
- Confirm payload sanitization still strips secrets and bearer tokens.
- Confirm enterprise strict profile still fails without an audit sink.
- Run `go test ./pkg/audit ./cmd/server -run 'Audit|Profile'`.

## Storage/KMS

Trigger this section when a PR touches `pkg/storage`, object uploads, raw-object
mirrors, retention metadata, legal hold, or KMS/BYOK integration.

- Confirm Personal/local storage still works without KMS.
- Confirm enterprise metadata preserves tenant/project scope, digest,
  retention, legal hold, and encryption key references.
- Confirm KMS request/response JSON never exposes plaintext key material.
- Confirm core search/indexing packages do not import storage adapters.
- Run `go test ./pkg/storage -run 'Enterprise|KMS|S3|LocalStorage'`.

## MCP Memory Ownership

Trigger this section when a PR touches MCP memory palace tools, diary tools,
chat history, sync, `owner_id`, `collection`, `room`, or `hall` handling.

- Confirm memory writes preserve explicit collection/owner/room/hall metadata.
- Confirm per-agent diaries do not pollute project-wide memory.
- Confirm sync does not expand vector collection transfer unless explicitly
  requested.
- Run `go test ./pkg/mcp ./internal/http -run 'Memory|Diary|Sync|MCP'`.

## Blocking Conditions

Block merge when any of these are true:

- `policy_boundary_test.go` fails or direct policy SQL is reintroduced in HTTP.
- `team` or `enterprise` strict profile starts without required auth/tenant/audit
  guarantees.
- OIDC/SAML/SCIM code appears in core search, indexing, or workspace handlers.
- Storage/KMS adapters are imported by vector, BM25, graph, or cognify code.
- A denied tenant/team path returns private content, object metadata, or audit
  details from another tenant.

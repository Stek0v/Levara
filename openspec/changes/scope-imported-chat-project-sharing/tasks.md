# Tasks

## 1. Scoped storage

- [x] 1.1 Implement additive chat/run registries and scoped writer entry points while preserving explicit local wrappers. DoD: immutable owner/tenant, original selectors, deterministic tuple keys, retry-safe run/session identity, separate same-source imports, no legacy adoption, no partial registry success on storage errors. Test native SQLite/PostgreSQL fresh and populated migration, repeats, cross-owner/tenant duplicates, immutable run platform, both directions of legacy/scoped physical-key collision, malformed identifiers and one-connection behavior. Preflight isolated PostgreSQL; no external providers.

## 2. Authorized application paths

- [x] 2.1 Apply verified private/project reads and owner-consented sharing to REST list/detail/import/run endpoints using existing project ACLs and fenced checks. DoD: admin-managed project audience, owner provenance unchanged, editors cannot grant memberships, revoked/inactive/foreign-tenant access denied, legacy local rows unavailable to authenticated callers; ambiguity fails closed. Test JWT/API-key dispatch on both dialects and before/after full-row controls; mixed-run ledgers remain owner-only, missing/deleted/public projects do not broaden access, global-superusers cannot read private chats, and stale admin-detach after project reassignment fails. Preserve user's exclusion of further REST memory-owner probes.
- [x] 2.2 Scope MCP transcript resolution, distillation and derivative registries; make source-daemon identity/local publication explicit. DoD: denied source cannot reach provider or save/index; recheck before later publication; canonical provenance and unverified evidence survive; no global publication of private/shared chat content. Test cancel/revoke/provider boundary, retry/reindex, local compatibility and ordinary-save regressions on both dialects.

## 3. Public workflow

- [x] 3.1 Add CLI chat/project selectors and owner-sharing/admin-detach workflow, align descriptors/guides and regenerate contracts sequentially. DoD: users can import privately, share to authorized project, inspect and revoke sharing; administrators manage audience through existing project grants. Test CLI payloads, generated-contract drift and descriptor/profile visibility. No invented collection-to-project mapping.

## 4. Integration

- [x] 4.1 Independently review the actual combined diff and migration/permission matrix, run native focused/race plus stable current-revision T08 broader gate, record exact hashes, exclusions and residual boundaries. Strict OpenSpec validation must pass. T08 accepts only after source authority and prior diary/distillation criteria pass together; no model-quality, production rollout or downstream roadmap acceptance inferred.

Accepted 2026-10-06 after native focused/race, independent combined review and stable S0–S4. See evidence.md for current revision, actual exits, exclusions and residual boundaries.

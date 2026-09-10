# ADR-002: Product ladder and layer boundaries

Decision date: 2026-06-05. Status: accepted.
Implementation status checked against source on 2026-09-05.

## Context and decision

Levara uses one engine across Personal / Local, Solo Pro, Team, and Enterprise
profiles. Shared retrieval, memory, and workspace mechanisms must not accumulate
edition-specific identity or infrastructure rules. Separate policy and adapter
boundaries allow stronger deployment configurations without duplicating the
engine. Product scope is maintained in the [product ladder](../product-ladder.md).

| Layer | Owns | Boundary |
|---|---|---|
| Core engine | Vector/WAL, BM25, graph algorithms/storage, retrieval, cognify, sync mechanics | Does not choose users, tenants, or product editions |
| Agent memory | Memory, wake-up, diaries, MCP tool behavior | Uses authenticated scope and policy; does not invent transport-specific authority |
| Identity/access | Users, keys, JWT/identity verification, shares, tenant membership, policy decisions | Transport registration stays outside policy |
| Workspace | Markdown truth, manifests, generations, jobs, citations, conflicts | Indexes are derivatives; corporate identity/storage details remain adapters |
| Enterprise adapters | External identity/provisioning, audit export, object storage and key-management contracts | Do not embed corporate protocols in core retrieval algorithms |

## Current implementation

The policy boundary is [pkg/access](../../pkg/access/policy.go). REST and MCP
workspace paths use it; profile validation and presets are available through
`LEVARA_PROFILE=personal|solo_pro|team|enterprise` and
`LEVARA_PROFILE_STRICT=1`. Strict validation rejects configured unsafe
combinations. It is a startup gate, not proof of authorization on every API.
Follow [profile presets](../profile-presets.md) for actual requirements.

[Audit](../../pkg/audit) and [storage](../../pkg/storage) have adapter boundaries.
Storage retention/key metadata and KMS/BYOK interfaces do not by themselves prove
that a deployment has a working corporate KMS, encryption, or retention service.
Validate the concrete provider and operational failure behavior separately.

Status update 2026-09-10: native LDAP/LDAPS/StartTLS, browser OIDC, SCIM
Users/Groups and SQL identity linking now exist in-tree. They remain optional
adapters and require real provider acceptance. Supported operations and exact
identity limitations are maintained in [enterprise identity](../enterprise-identity.md).

## Consequences and non-goals

Personal/local operation remains possible without optional corporate adapters.
Team and enterprise deployment requirements are explicit configuration choices.
Profile names are not licensing enforcement or a guarantee of complete tenant
isolation. Dataset shares do not imply individual-document/group ACLs; resource
permissions and document lifecycle have separate acceptance requirements.

This decision does not split binaries, rewrite the retrieval engine, change
public wire shapes, or authorize deployment. It requires new protocols to attach
to the existing policy/adapter seams and requires each changed transport to
preserve its denial behavior.

## Remaining work and review contract

Complete external provider scenarios and failure recovery with actual IdPs and
storage providers before claiming compatibility. Preserve stable identity
matching, deactivation checks, and provisioning-versus-sign-in boundaries.
Document any gaps in group mapping and credential revocation. Keep narrowing
handler dependencies where useful without treating that refactor as a feature.

Review against the [contributor checks](../../CONTRIBUTING.md)
and [testing](../testing.md). SQL-backed changes require SQLite/PostgreSQL
parity and rollback checks; public MCP changes require descriptor, schemas,
dispatch, feature visibility, and generated-contract checks together.

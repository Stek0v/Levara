# Design

## Context

See proposal.md. Next16/React19 use existing authenticated API helpers and React state. Imported-chat backend list/detail expose canonical/source/project identifiers without management capabilities. Existing policy permits owner attachment with project write authority and owner/project-admin detachment; audience grants are separately administrator-controlled. Current curated suite omits five existing deterministic specs.

## Goals / Non-Goals

Goals: honest browser workflows and evidence for original T30, including defensive repairs to existing Task and Graph read boundaries discovered by native checks. Non-goals: new domain capabilities, SQL schema changes, redesign, external model quality, production deployment and notebook sunset.

## Decisions

Use a small client component mounted inside the account-keyed chat surface. Existing API helpers retain cookie/bearer handling; encode every selector. Request epochs and cleanup prevent obsolete reads or mutations publishing across selection/unmount; failed authority checks clear transcript. Actions are server-authorized requests with policy text rather than client-inferred management rights. Project audience remains the existing dataset access screen.

Keep route-mocked browser checks separate from disposable backend proof. Run curated tests on a fresh Next port with backend origin 127.0.0.1:1; use a private SQLite backend/data directory for native browser integration. Do not target the existing personal server. PostgreSQL backend parity is supported by existing accepted native tests, not fabricated from SQLite browser evidence.

The backend remains SQL-authoritative, with existing profile/flag gates and index contracts. Existing Task REST reads now filter verified ownership, parse native timestamps/statuses and retain credential-bounded response fencing. Existing dataset Graph reads now authorize the dataset and each document/publication assertion under the same retained SQL transaction. Public routes and DTO shapes remain compatible; generated-contract drift checks remain final integration checks. Release packaging uses the existing six binaries/profiles/license and disables Darwin AppleDouble tar metadata after actual allowlist failure.

## Domain acceptance matrix

| Domain | Browser surface and required proof | Other accepted surfaces |
|---|---|---|
| Memory | room/hall create/read/delete, account isolation, denied/failed states | diary, supersede, consolidation and history remain MCP/operator workflows |
| Search | strategy labels, empty/denied/unavailable results | provider/model quality has separate opt-in proof |
| Documents | upload status, processing failure, grants/revocation and original download | backend native processing/authority |
| Graph | current graph and path state, empty/failed/denied UI | temporal snapshot queries through supported API/MCP |
| Workspace | read/save, stale CAS recovery, denied/unavailable | executor recovery and canonical index native acceptance |
| Tasks | criteria/steps/receipts, lease and failed state | Task Runtime native lease/receipt fencing |
| Imported chats | private read, owner share, admin audience/revoke, stale selection | source import and distillation backend/CLI/MCP |
| Projects/git | context, git and audience cards, locale, failed/denied state | Git publication native acceptance |
| Sync | type result, failed/partial status, refused protocol | backup/restore through operator binaries |
| Auth/profiles | login/logout, expired/revoked account and visible feature limits | external AD/IdP sandbox remains separate |

Every browser row must have observed current evidence before T30 closes. A backend-only supported action is documented as such, not invented as a new browser requirement.

## Risks / Trade-offs

- Capability-free DTO can show an action later denied → policy text plus authoritative server errors; never infer permissions.
- Mock success can conceal backend drift → dedicated authenticated backend workflows with native state inspection.
- Reusing live catalog can mutate personal data → explicit private origin/credentials and fresh data preflight.
- Async stale responses can leak obsolete details → clear state and epoch guards, reload/focus revalidation.

## Migration Plan

No data migration. Frontend and defensive server changes are built and inspected locally; deployment is outside this change. A later authorized rollout must preserve corrected read authority rather than restore the vulnerable handlers. No production restart, migration or publication is performed here.

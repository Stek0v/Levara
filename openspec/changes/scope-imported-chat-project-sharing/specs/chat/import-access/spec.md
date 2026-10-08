# Imported Chat Access

## Purpose

Provide personal imported conversations with explicit project collaboration while preserving verified identity, tenant boundaries and administrator-managed project access.

## ADDED Requirements

### Requirement: Immutable authenticated import identity

The system SHALL derive owner and tenant from verified transport identity, store an immutable scoped chat/run identity, preserve original source selectors, and default authenticated imports to private visibility.

#### Scenario: Same source imported twice
- **WHEN** two owners or tenants import identical source session and run IDs
- **THEN** their stored chats, messages and ledgers remain independent, and retries within one identity remain idempotent.

#### Scenario: Submitted owner or canonical-looking ID
- **WHEN** a caller submits another owner's identity or an identifier resembling an internal key
- **THEN** the system does not adopt the other scope or mutate its rows.

#### Scenario: Legacy physical key collision
- **WHEN** a legacy raw chat or run already occupies a future scoped key, or a local import targets a registered authenticated key
- **THEN** the write fails without adopting or mutating existing content.

#### Scenario: Mixed import run
- **WHEN** a colleague can read one project-shared chat from a run
- **THEN** its run ledger, source path, warnings and counters remain visible only to the importing owner in the exact tenant.

### Requirement: Explicit local legacy compatibility

The system SHALL retain unclaimed legacy imports only in explicitly permitted local mode, without assigning their ownership from an authenticated reader or daemon's ambient identity.

#### Scenario: Legacy populated database
- **WHEN** an authenticated user lists imports after migration
- **THEN** unclaimed legacy content is excluded, while explicit trusted-local access remains available.

### Requirement: Owner-consented project sharing

The system SHALL let an owner with project write authority attach their chat to an existing project. Project administrators SHALL manage its audience through current project grants and may detach a chat already shared there. Administrators SHALL NOT gain automatic access to another owner's private chat.

#### Scenario: Collaboration
- **WHEN** an owner shares into an authorized project and an active colleague holds a read grant
- **THEN** that colleague can read the shared chat in the same tenant, with original ownership preserved.

#### Scenario: Revocation
- **WHEN** an administrator revokes project access or removes the chat association
- **THEN** subsequent colleague reads and source publication are denied; the owner's private source remains.

#### Scenario: Administrator authority changes during audience mutation
- **WHEN** administrator, credential, account or selected-tenant authority is revoked before a grant or revoke acquires its write fence
- **THEN** the audience mutation is denied without partial changes; an already authorized mutation holds its fence through commit, and later revocation waits for that mutation.

#### Scenario: Editor attempts grant
- **WHEN** an editor attempts to change project audience or attach another owner's private chat
- **THEN** the operation is denied without content or membership mutation.

### Requirement: Live tenant and source authority

The system SHALL check live credential, account, exact tenant membership and chat/project access before list/detail/transcript use, provider egress and subsequent derived publication. Errors SHALL fail closed; collection names SHALL NOT imply project authorization.

#### Scenario: Revoked credential or foreign tenant
- **WHEN** a credential becomes invalid, membership is removed or a different tenant is selected
- **THEN** protected source use fails before provider egress or later publication.

#### Scenario: Derived content
- **WHEN** a private or shared chat is rendered or distilled
- **THEN** its canonical source identity is preserved and content is not published into an unrestricted global index.

### Requirement: Unambiguous selectors and additive migration

The system SHALL expose chat_id alongside original source selectors, reject ambiguous authorized source-only resolution, and migrate both SQL backends idempotently without destructive message-table replacement or legacy ownership inference.

#### Scenario: Multiple shared source matches
- **WHEN** a source-only selector matches multiple authorized shared chats and none is caller-owned
- **THEN** resolution returns an ambiguity error requiring chat_id.

#### Scenario: Repeated migration
- **WHEN** migration runs on fresh or populated SQLite/PostgreSQL databases more than once
- **THEN** source rows and scoped identities remain intact.

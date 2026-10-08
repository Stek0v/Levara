# Proposal

## Why

Imported conversations and import runs currently have global identifiers and no verified owner or tenant. The user requires personal chats that can be shared with colleagues within a project, with access controlled by administrators and an explicit separate local mode.

## What Changes

- Give authenticated imports an immutable caller/tenant identity and scoped run/session identifiers.
- Keep imports private initially; owners can publish a chat into an existing project where they have write access. Project administrators control the audience through existing project shares and may remove a published chat.
- Apply current tenant membership, credentials and project permissions to listings, transcripts, distillation and derived publication.
- Keep unclaimed legacy imports in explicit local mode; never assign them to the first authenticated reader.
- Add unambiguous chat identifiers, project-sharing endpoints and CLI selectors; regenerate public contracts.

## Capabilities

### New Capabilities

- `chat/import-access`: private imported chat ownership, tenant boundaries, existing project sharing and local compatibility.

### Modified Capabilities

None. The separate distillation-lifecycle change supplies cancellation and evidence behavior.

## Impact

Chat import SQL/writers, REST, MCP distillation, source daemon, RAG/distillation registries, CLI and generated contracts. SQLite and PostgreSQL use the same additive registry model. Existing project ACLs are reused; no new dependency, groups, chat synchronization or automatic private-content access for administrators.

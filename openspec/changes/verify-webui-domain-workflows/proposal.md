# Proposal

## Why

Original roadmap T30 requires browser workflows to reflect accepted backend domains and explicit denied, stale and failed states. Current imported chats lack a browser surface and the curated browser command omits five existing project/memory suites.

## What Changes

- Add imported chat list/detail and server-authorized owner-consent project attachment, owner/admin detachment and a link to administrator-managed project audience.
- Include existing omitted deterministic browser scenarios and close confirmed browser state/error defects.
- Record a domain acceptance matrix, prove applicable browser workflows against a disposable authenticated backend, and inspect the approved release artifact.
- Preserve account-scoped RAG chat, backend project authority and current release binary/profile allowlist.

## Capabilities

### New Capabilities

- `webui/domain-workflows`: browser state, authority and failure behavior for supported product domains.

### Modified Capabilities

None; existing imported-chat backend authority is preserved.

## Impact

Next.js WebUI API client, chat components, existing browser tests and documentation. No new dependencies, SQL migration, backend authority expansion or release deployment. SQLite/PostgreSQL backend contracts remain authoritative; route mocks prove browser behavior only. T30 completion requires its complete acceptance matrix, not only the imported-chat subtask. External identity/model quality, hardware SLOs and legacy notebook sunset remain separately gated.

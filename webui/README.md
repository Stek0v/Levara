# Levara WebUI

Next.js UI for document upload/processing, search, chat, workspace and operations.
The frontend is a separate service; the Go backend does not serve it at `/ui`.

## Local start

Start the SQLite-backed backend from [getting started](../docs/getting-started.md)
on `127.0.0.1:8080`. From this directory:

```bash
npm ci
LEVARA_API_URL=http://127.0.0.1:8080 npm run dev
```

Open `http://127.0.0.1:3000`. For semantic search and processing configure the
backend model services and matching dimension first. The repository's personal
macOS startup helper is not needed for this portable workflow.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LEVARA_API_URL` | `http://127.0.0.1:8081` | Backend origin for Next rewrites; no `/api/v1` suffix |
| `NEXT_PUBLIC_API_URL` | empty | Optional browser API base; empty uses same-origin rewrites |

Set `LEVARA_API_URL` explicitly to match your server. Keep
`NEXT_PUBLIC_API_URL` empty for the normal same-origin proxy setup. Configure
`LEVARA_API_URL` at both `npm run build` and `npm run start` because rewrites
are included in the build output. In a container, loopback is the container
itself, not the backend on another host.

## Document workflow

Select an existing dataset by ID or create a new named dataset, upload, inspect
extraction and wait for an explicit processing result. Failed extraction/run
status is not Ready; a missing run ID alone is not completion. Retry the same
file after correcting the cause. The original download goes through the
credentialed API and can be denied after access changes.

Shares are individual viewer/editor/admin grants on a dataset. Use a separate
dataset for one document; document/group ACLs are not implemented. Backend
OIDC/SAML endpoints do not imply a built-in browser OIDC login flow. See
[document management](../docs/document-management.md),
[acceptance scenarios](../docs/document-workflow-scenarios.md) and
[enterprise identity](../docs/enterprise-identity.md).

## Checks and tests

```bash
npm run lint
npx tsc --noEmit
LEVARA_API_URL=http://127.0.0.1:1 npm run test:e2e
```

The curated Playwright suite uses deterministic route mocks; its default WebUI
port is `3011`, configurable by the Playwright environment. It checks browser
behavior, not real backend authentication/extraction or external model quality.

`npm run test:e2e:integration` runs the wider suite containing live-backend
scenarios. Do not point it at an existing service by habit; inspect test targets,
provide a dedicated backend/data/credentials and follow [testing](../docs/testing.md).
The mocked upload suite can be run with
`LEVARA_API_URL=http://127.0.0.1:1 npx playwright test e2e/upload-flow.spec.ts`.

Build/start commands are `npm run build` and `npm run start`. Operational
configuration, proxying and deployment checks are in
[WebUI operations](../docs/webui-operations.md). Aggregate analytics should not
be treated as a fully isolated per-tenant reporting surface; verify its access
scope before exposing it to untrusted users.

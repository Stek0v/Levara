# Levara WebUI

Next.js operator UI for Levara.

Full operating guide: [`../docs/webui-operations.md`](../docs/webui-operations.md).
Document upload, processing, retries and individual sharing:
[document management](../docs/document-management.md),
[acceptance scenarios](../docs/document-workflow-scenarios.md).
Corporate login boundaries: [LDAP/AD and SSO](../docs/enterprise-identity.md).

## Local Start

The command below starts the project's macOS development stack with PostgreSQL
and a local embedding sidecar; it may restart its existing port 8081 process.
For a fresh portable server, use [getting started](../docs/getting-started.md).

Start that development backend first:

```bash
cd ..
./start-levara.sh
```

Then start the WebUI:

```bash
cd webui
npm install
LEVARA_API_URL=http://127.0.0.1:8081 npm run dev
```

Open `http://localhost:3000`.

## Configuration

| Variable | Default | Use |
|---|---|---|
| `LEVARA_API_URL` | `http://127.0.0.1:8081` | Backend target for Next rewrites from `/api/*`, `/health`, `/health/details` |
| `NEXT_PUBLIC_API_URL` | empty | Optional browser-visible API base; leave empty for same-origin rewrites |

Recommended local and team default: set `LEVARA_API_URL`, leave
`NEXT_PUBLIC_API_URL` empty. For a production build, set `LEVARA_API_URL`
when running `npm run build` as well as `npm run start`; rewrites are built
into the Next.js output. WebUI is a separate service, not backend `/ui`.

## Scripts

```bash
npm run dev
npm run lint
npm run build
npm run start
```

## Tests

Playwright starts the WebUI on port `3001` and points it at
`LEVARA_API_URL`:

```bash
LEVARA_API_URL=http://127.0.0.1:8081 npx playwright test
```

Useful targeted suites:

```bash
npx playwright test e2e/auth-flow.spec.ts
npx playwright test e2e/upload-flow.spec.ts
npx playwright test e2e/full-integration.spec.ts
```

## Screens

The sidebar exposes Dashboard, Datasets, Search, Chat, Graph, Collections,
Workspace, Sync, Memories, Notebooks, Analytics, Admin, Onboarding, and Settings.

Individual shares apply to a dataset; use a separate dataset for one document.
Group grants and independent document ACLs are not available. Backend OIDC/SAML
surfaces do not imply a built-in browser SSO flow.

For deployment, monitoring, reverse proxy, solo/team setup, and troubleshooting,
use [`../docs/webui-operations.md`](../docs/webui-operations.md).

# Levara WebUI

Next.js operator UI for Levara.

Full operating guide: [`../docs/webui-operations.md`](../docs/webui-operations.md).

## Local Start

Start the backend first:

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
`NEXT_PUBLIC_API_URL` empty.

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

For deployment, monitoring, reverse proxy, solo/team setup, and troubleshooting,
use [`../docs/webui-operations.md`](../docs/webui-operations.md).

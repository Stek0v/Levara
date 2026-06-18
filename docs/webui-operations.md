# Levara WebUI Operations Guide

This guide covers how to run, connect, configure, and monitor the Levara WebUI
for two deployment shapes:

- **Solo / Solo Pro**: one operator, usually local Mac or Mac <-> Pi sync.
- **Team**: shared backend, required auth, shared workspaces, audit, and
  operational monitoring.

For product/profile boundaries see `docs/product-ladder.md` and
`docs/profile-presets.md`. For the backend API surface see
`docs/api-reference.md`.

---

## What the WebUI is

The WebUI is the Next.js application in `webui/`. It is an operator and user UI
over the Levara backend. It does not own data storage. It talks to the backend
over REST and uses the backend for auth, datasets, search, memory, graph,
workspace operations, sync, MCP observability, and long-running job status.

Main screens:

| Screen | Path | Use |
|---|---|---|
| Dashboard | `/` | Health, collections, datasets, feedback, cache and recent errors |
| Datasets | `/datasets`, `/datasets/[id]` | Upload files, inspect records, run Cognify, inspect dataset graph |
| Search | `/search` | Text, hybrid, RAG, graph, and advanced search |
| Chat | `/chat` | Chat-style RAG workflow |
| Graph | `/graph` | Dataset graph and path exploration |
| Collections | `/collections` | Collection metadata and embedding contracts |
| Workspace | `/workspace` | Markdown workspace manifest, artifacts, search, indexing jobs, audit |
| Sync | `/sync` | Cross-instance sync manifest, status, and manual sync runs |
| Memories | `/memories` | Memory records |
| Notebooks | `/notebooks` | Notebook-style workflows |
| Analytics | `/analytics` | Health details, VSA status, embedding migration controls |
| Admin | `/admin` | MCP tools, MCP sessions, admin summary |
| Settings | `/settings` | User settings and visible API base |

---

## Architecture

```text
Browser
  -> Next.js WebUI (:3000 or :3001)
      -> /api/* rewrite to Levara backend
      -> /health and /health/details rewrite to Levara backend
  -> optional direct calls when NEXT_PUBLIC_API_URL is set

Levara backend (:8081 local dev, :8080 default deploy)
  -> SQLite or PostgreSQL
  -> embedding service
  -> optional LLM / reranker
  -> optional sync peer
```

The default local development stack on Mac is:

- Levara HTTP: `http://127.0.0.1:8081`
- WebUI dev server: `http://localhost:3000`
- Playwright WebUI server: `http://localhost:3001`
- PostgreSQL dev metadata: `localhost:5433` when using `start-levara.sh`
- Local embedding service: `http://127.0.0.1:9101/v1/embeddings` when using
  `start-levara.sh`

The default production-style backend port in older docs and Docker examples is
`:8080`. Always check the real backend URL with `/health`.

---

## Connection Model

There are two relevant WebUI environment variables:

| Variable | Where it applies | Default | Use |
|---|---|---|---|
| `LEVARA_API_URL` | Next.js server process | `http://127.0.0.1:8081` | Target for Next rewrites from `/api/*`, `/health`, `/health/details` |
| `NEXT_PUBLIC_API_URL` | Browser bundle | empty | Optional direct browser API base; only set when the browser must bypass Next rewrites |

Recommended default: set `LEVARA_API_URL` and leave `NEXT_PUBLIC_API_URL` empty.
That keeps browser calls same-origin through Next rewrites, which is simpler for
cookies and CORS.

Use `NEXT_PUBLIC_API_URL` only when:

- the WebUI is served statically or behind infrastructure that cannot proxy
  `/api/*`;
- SSE streams must connect directly to the backend; or
- you intentionally want the browser to call a different API origin.

When `NEXT_PUBLIC_API_URL` is non-empty, the browser will call that origin for
API client requests and Cognify SSE URLs. Configure backend CORS accordingly.

---

## Solo: Local Development / Personal Use

### 1. Start the backend

For the current Mac dev stack with PostgreSQL metadata and local embeddings:

```bash
cd /Users/stek0v/src/levara
./start-levara.sh
```

Verify:

```bash
curl -sS http://127.0.0.1:8081/health
curl -sS http://127.0.0.1:8081/health/details
```

For a simpler personal SQLite setup, copy `deploy/profiles/personal.local.env.example`
into your shell or `.env`, then start `levara-server` with the matching data
directory and port. Keep auth off only when the port is bound to a trusted local
interface.

### 2. Start the WebUI

```bash
cd /Users/stek0v/src/levara/webui
npm install
LEVARA_API_URL=http://127.0.0.1:8081 npm run dev
```

Open `http://localhost:3000`.

### 3. First solo checks

1. Open `/` and confirm Dashboard health is healthy.
2. Open `/datasets`, upload a small text file, and confirm the dataset appears.
3. Open the dataset detail page and run Cognify.
4. Watch the Cognify progress stream. If it stalls, check backend logs and
   `/api/v1/cognify/<runId>/status`.
5. Open `/search` and query the collection.
6. Open `/settings` and confirm the API base shown there is what you intended.

### 4. Solo Pro sync

Solo Pro is for one operator with multiple machines, for example Mac <-> Pi.
Use `deploy/profiles/solo_pro.sync.env.example` as the backend profile template.

Typical flow:

```bash
curl -sS http://127.0.0.1:8081/api/v1/sync/manifest
```

In the WebUI:

1. Open `/sync`.
2. Set the remote API URL, for example `http://10.23.0.53:8080/api/v1`.
3. Check local and remote manifests.
4. Run pull or push intentionally.
5. Confirm `/sync` status and backend logs.

Sync defaults should move memories, interactions, and graph data. Vector
collections are intentionally heavier because they require compatible embedding
contracts and re-embedding strategy.

---

## Team Deployment

Team mode means the WebUI is no longer just a local convenience. Treat it as a
shared operational surface.

### Backend profile

Use `deploy/profiles/team.postgres.env.example` as the baseline:

```bash
LEVARA_PROFILE=team
LEVARA_PROFILE_STRICT=1
DB_PROVIDER=postgres
POSTGRES_DSN=postgres://levara:change-me@localhost:5432/levara?sslmode=disable
JWT_SECRET=replace-with-stable-random-secret
LEVARA_WORKSPACE_WATCH=1
LEVARA_WORKSPACE_INDEX_WORKER=1
LEVARA_WORKSPACE_AUDIT_EXPORT=1
```

Start the backend with auth required:

```bash
./levara-server -standalone=true -port=8080 -require-auth=true
```

Minimum team requirements:

- PostgreSQL, not local SQLite, for shared state.
- Stable `JWT_SECRET`.
- Required auth.
- Audit export enabled for workspace operations.
- Stable backups for database and object storage.
- A clear owner for backend logs, Prometheus, and incident response.

### WebUI deployment options

**Option A: Next.js server next to backend**

Run WebUI as a Node service and point rewrites to the backend:

```bash
cd webui
npm ci
npm run build
LEVARA_API_URL=http://127.0.0.1:8080 npm run start -- -p 3000
```

Put a reverse proxy in front of the WebUI. The browser sees only the WebUI
origin; API calls go through Next rewrites.

**Option B: Reverse proxy routes `/api/*` directly**

Serve WebUI and route:

- `/` to Next.js
- `/api/*` to Levara backend
- `/health` and `/health/details` to Levara backend or to a proxy health page

Keep `NEXT_PUBLIC_API_URL` empty unless you intentionally expose the backend as
a separate browser origin.

### Reverse proxy notes

Required paths:

```text
/api/*            -> Levara backend
/health           -> Levara backend
/health/details   -> Levara backend
```

If Cognify progress or other SSE streams are used, the proxy must not buffer SSE
responses. For nginx-style proxies, disable response buffering for stream paths
such as `/api/v1/cognify/*/stream`.

Team CORS should include the WebUI origin if the browser ever calls the backend
directly:

```bash
CORS_ALLOWED_ORIGINS=https://levara.example.com
```

---

## Configuration Checklist

### Backend

| Area | Solo | Team |
|---|---|---|
| Profile | `personal` or `solo_pro` | `team` |
| Database | SQLite or local PostgreSQL | PostgreSQL |
| Auth | optional local only | required |
| JWT | optional if auth off | stable secret required |
| Workspace watcher | recommended | required for shared workspace UX |
| Audit export | optional | enabled |
| Sync token | required for Solo Pro sync | required only for configured sync |
| CORS | localhost origins | explicit WebUI origins |

### WebUI

| Setting | Solo | Team |
|---|---|---|
| `LEVARA_API_URL` | `http://127.0.0.1:8081` | internal backend URL |
| `NEXT_PUBLIC_API_URL` | empty | empty unless direct browser API origin is required |
| Node mode | `npm run dev` | `npm run build` + `npm run start` |
| Port | `3000` | behind reverse proxy |
| Tests | Playwright against `3001` | CI + smoke after deploy |

---

## Monitoring

### WebUI-level checks

Run from the WebUI host:

```bash
curl -sS http://127.0.0.1:3000/
curl -sS http://127.0.0.1:3000/health
curl -sS http://127.0.0.1:3000/health/details
```

Because `/health` and `/health/details` are rewritten by Next, these checks
validate both WebUI routing and backend reachability.

### Backend health

Run from the backend host:

```bash
curl -sS http://127.0.0.1:8081/health
curl -sS http://127.0.0.1:8081/health/details
curl -sS http://127.0.0.1:8081/api/v1/info
curl -sS http://127.0.0.1:8081/api/v1/errors?limit=10
```

For MCP/WebUI admin observability:

```bash
curl -sS http://127.0.0.1:8081/api/v1/admin/mcp/tools
curl -sS http://127.0.0.1:8081/api/v1/admin/mcp/summary
curl -sS http://127.0.0.1:8081/api/v1/admin/mcp/sessions?limit=20
```

### Prometheus

The backend exposes Prometheus metrics at:

```text
http://<backend-host>:<backend-port>/metrics
```

Track at least:

- request rate and latency for search, ingest, and MCP tools;
- insert/search error rates;
- vector count and collection growth;
- WAL/fsync latency for local storage;
- workspace job queue size and failures;
- MCP session count and tool error rates;
- sync failures and last successful sync time;
- recent backend errors.

The WebUI currently relies on Next.js process logs for frontend server
observability. In team deployments, capture stdout/stderr from the Node process
with systemd, Docker logs, or your process manager.

### In-product monitoring screens

Use:

- `/` for operator health summary, cache stats, feedback, recent errors.
- `/analytics` for health details, VSA status, embedding migrations.
- `/workspace` for workspace ops status, jobs, conflicts, artifacts, audit.
- `/sync` for sync manifests and recent sync runs.
- `/admin` for MCP tools, sessions, and summary.

---

## Smoke Tests

### Local smoke

```bash
cd /Users/stek0v/src/levara/webui
LEVARA_API_URL=http://127.0.0.1:8081 npm run dev -- -p 3001
```

In another shell:

```bash
curl -sS http://127.0.0.1:3001/health
curl -sS http://127.0.0.1:3001/api/v1/info
```

### Playwright

The Playwright config starts the WebUI on port `3001` and uses
`LEVARA_API_URL`, defaulting to `http://127.0.0.1:8081`.

```bash
cd /Users/stek0v/src/levara/webui
LEVARA_API_URL=http://127.0.0.1:8081 npx playwright test
```

Use targeted suites while debugging:

```bash
npx playwright test e2e/auth-flow.spec.ts
npx playwright test e2e/upload-flow.spec.ts
npx playwright test e2e/full-integration.spec.ts
```

### Build check

```bash
cd /Users/stek0v/src/levara/webui
npm run lint
npm run build
```

---

## Operational Workflows

### Upload and Cognify

1. Upload files in `/datasets`.
2. Open the dataset detail page.
3. Start Cognify.
4. Watch SSE progress in the page.
5. If progress disappears, check:
   - browser DevTools Network for `/api/v1/cognify/<runId>/stream`;
   - `GET /api/v1/cognify/<runId>/status`;
   - backend logs;
   - `/api/v1/errors?limit=10`.

### Search quality review

1. Use `/search` with the target collection.
2. Compare plain, hybrid, RAG, and graph modes when available.
3. Submit feedback on poor results.
4. Monitor `/analytics` and feedback stats.
5. If rerank is configured, verify result payloads include rerank indicators.

### Workspace operations

1. Open `/workspace`.
2. Select project and branch when applicable.
3. Check manifest, artifacts, conflicts, and audit.
4. Trigger index or reindex only when the project scope is correct.
5. For failed jobs, retry from the page or inspect backend workspace job APIs.

### Sync operations

1. Open `/sync`.
2. Confirm local and remote manifests.
3. Prefer pull before push when reconciling a secondary machine.
4. Check `sync_status` or `/api/v1/sync/status?limit=10` after the run.
5. Do not sync vector collections unless both sides have compatible embedding
   contracts and you intentionally opted into collection sync.

### MCP observability

Use `/admin` to inspect:

- available MCP tools;
- recent sessions;
- MCP summary and tool-level health.

For direct MCP endpoint checks:

```bash
curl -sS -X POST http://127.0.0.1:8081/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"webui-runbook-check","version":"0.1"}}}'
```

---

## Troubleshooting

| Symptom | Likely cause | Check / fix |
|---|---|---|
| WebUI loads but Dashboard cards fail | `LEVARA_API_URL` points at the wrong backend | `curl http://localhost:3000/health/details`; restart WebUI with correct env |
| Browser calls `localhost:8081` directly and hits CORS | `NEXT_PUBLIC_API_URL` is set | Prefer empty `NEXT_PUBLIC_API_URL`; otherwise add WebUI origin to backend CORS |
| Login/register returns 429 during tests | Auth rate limit | For local benches use `RATE_LIMIT_AUTH_MAX=10000`; do not copy this blindly to production |
| Cognify progress reconnects forever | SSE buffering or backend run disappeared | Disable proxy buffering; check `/api/v1/cognify/<runId>/status` |
| Upload succeeds but dataset list is stale | Client cache not invalidated or backend delayed | Refresh page; check `/api/v1/datasets`; inspect backend logs |
| `/workspace` is empty | No project scope or index worker disabled | Set project/branch, enable `LEVARA_WORKSPACE_INDEX_WORKER=1`, run index |
| `/sync` fails | bad remote URL or token mismatch | Check remote `/api/v1/sync/manifest`, `LEVARA_TOKEN`, and sync logs |
| Admin MCP page empty | backend lacks MCP/admin routes or auth blocks request | Check `/api/v1/admin/mcp/tools` with the same token/session |
| Team users randomly log out | unstable `JWT_SECRET` across restarts | Set a stable secret and redeploy |
| Health is green but search is poor | embedder/reranker/LLM not configured as expected | Check `/health/details`, `/api/v1/info`, collection metadata, and search payload |

---

## Security Notes

- Do not expose a no-auth backend outside localhost.
- In team mode, require auth and use a stable `JWT_SECRET`.
- Keep sync tokens and JWT secrets out of git.
- Use HTTPS at the reverse proxy for shared deployments.
- Keep `NEXT_PUBLIC_API_URL` unset unless you understand that it becomes visible
  to every browser.
- Enable workspace audit export for team deployments.
- Treat `/admin`, `/workspace`, and `/sync` as operational surfaces, not public
  end-user pages.

---

## Release / Change Checklist

Before changing WebUI connection or deployment behavior:

1. Update this guide and `webui/README.md`.
2. Run `npm run lint` and `npm run build` in `webui/`.
3. Run at least the affected Playwright suite.
4. Verify `/health`, `/health/details`, `/api/v1/info`, and the affected screen.
5. For team changes, verify auth, CORS, SSE, and reverse proxy behavior.
6. For sync or workspace changes, verify audit/status screens after the run.

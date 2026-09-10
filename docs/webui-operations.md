# Levara WebUI Operations Guide

_Last verified: 2026-09-05 against source; observed test runs and limitations
are recorded in [testing](testing.md)._

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
| Memory Behavior | `/memory-behavior` | MCP trajectory metrics: recall-before-save, repeats, zero-results, context bytes |
| Scaffold Proposals | `/memory-scaffold` | Human approval queue for AGENTS.md / memory policy recommendations |
| Tasks | `/tasks` | Read-only task, lease, receipt and checkpoint inspection |
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

Next.js rewrites default to `http://127.0.0.1:8081`; the server binary defaults
to port `8080`. Set `LEVARA_API_URL` to the actual backend when starting/building
the WebUI. Playwright uses port `3011` unless `PLAYWRIGHT_PORT` overrides it.
SQL and model endpoints come from the chosen deployment, not the WebUI.

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
API client requests and optional SSE clients. The current dataset page uses
status polling. Configure backend CORS accordingly.

---

## Solo: Local Development / Personal Use

### 1. Start the backend

Follow [getting started](getting-started.md) for Personal/SQLite or
[deployment](deployment.md) for PostgreSQL and embeddings. The repository's
`start-levara.sh` is a local development helper: it can stop a process on 8081
and relax auth rate limits. It is not the shared-server startup procedure.

For a backend on port 8080:

```sh
curl --fail-with-body -sS http://127.0.0.1:8080/health
```

### 2. Start the WebUI

```bash
cd webui # from the repository root
npm ci
LEVARA_API_URL=http://127.0.0.1:8080 npm run dev
```

Open `http://localhost:3000`.

For file types, extracted-text inspection, processing failures and reprocessing,
follow [document management](document-management.md). Upload completion means
bytes were accepted; verify terminal processing status and a known-answer search
before treating a document as usable. See [acceptance scenarios](document-workflow-scenarios.md).

### 3. First solo checks

1. Open `/` and confirm Dashboard health is healthy.
2. Open `/datasets`, upload a small text file, and confirm the dataset appears.
3. Open the dataset detail page and run Cognify.
4. Watch Cognify status polling. If it stalls, check backend logs and
   `/api/v1/cognify/<runId>/status`.
5. Open `/search` and query the collection.
6. Open `/settings` and confirm the API base shown there is what you intended.
7. Open `/memory-behavior` after a few MCP calls and confirm trajectories are
   appearing. If the page is empty while MCP calls are active, check the MCP
   audit read-model health.

### Memory behavior operations

The current trajectory/memory-behavior read model is not filtered by owner or
tenant. Collection/client selectors and sanitized arguments are not access
controls. Shared deployments must restrict these analytics routes at the
operator boundary until read-model isolation is implemented; see the
[remaining work](product/unimplemented-roadmap.md).

Use `/memory-behavior` to answer whether agents are using memory efficiently:

- low recall-before-save means agents are writing before checking memory;
- high repeat-save rate means scaffold/update policy is weak;
- high zero-result rate points to retrieval/indexing or query-formulation issues;
- rising context bytes points to noisy wake-up/pinned memory context.

Use `/memory-scaffold` only after a meta-review has produced proposals. The UI
does not apply proposals automatically. Approved proposals are a record of human
acceptance and should be applied intentionally in the relevant `AGENTS.md` or
memory policy file.

Run the deterministic behavior eval before and after scaffold changes:

```bash
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py --fake --label scaffold-check
```

### 4. Solo Pro sync

Solo Pro is for one operator with multiple machines, for example Mac <-> Pi.
Use `deploy/profiles/solo_pro.sync.env.example` as the backend profile template.

Typical flow:

```bash
curl --fail-with-body -sS -H "Authorization: Bearer ${LEVARA_TOKEN:?set active instance-admin token}" \
  http://127.0.0.1:8080/api/v1/sync/manifest
```

In the WebUI:

1. Open `/sync`.
2. Set the remote API URL, using the exact configured `LEVARA_SYNC_REMOTE_URL`.
3. Check local and remote manifests.
4. Run pull or push intentionally.
5. Confirm `/sync` status and backend logs.

With auth enabled, sync requires an active instance superuser. The server forwards
`LEVARA_TOKEN` only to the exact configured remote base URL.
Sync defaults move memories, interactions, and graph data. Vector
collections are intentionally heavier because they require compatible embedding
contracts and re-embedding strategy.

---

## Team Deployment

Team mode means the WebUI is no longer just a local convenience. Treat it as a
shared operational surface.

### Backend profile

Use `deploy/profiles/team.postgres.env.example` as the baseline:

Copy the preset into a private env file and set its PostgreSQL DSN and stable
JWT secret. Export the values before validating or starting the binary:

```sh
set -a
. ./team.env
set +a
./levara-server -config-check -require-auth=true
./levara-server -profile=standalone -port=8080 -require-auth=true
```

See [profile presets](profile-presets.md) for required fields and their limits.

Minimum team requirements:

- PostgreSQL, not local SQLite, for shared state.
- Stable `JWT_SECRET`.
- Required auth.
- Audit export enabled for workspace operations.
- Stable backups for database and object storage.
- A clear owner for backend logs, Prometheus, and incident response.

### Identity and individual sharing

The WebUI discovers configured login methods from `/api/v1/auth/methods`.
Local password, direct LDAP/AD and browser OIDC are wired to browser sessions;
SAML still has a separate response contract. Configure the identity bridge and
repeat the real AD/IdP flow described in
[enterprise identity](enterprise-identity.md) before a pilot.

The dataset detail page can grant an individual viewer/editor/admin for the
whole dataset. Each file row also opens a document Access panel. A manager can
enable a restricted policy, select an active user or group from that document's
tenant, grant/revoke roles, and recover from a stale ACL revision by refreshing
before retry. Direct and group document grants appear on the Projects page.
Follow [document management](document-management.md) and test grant/revoke
across download, retrieval and chat with separate users.

### WebUI deployment options

**Option A: Next.js server next to backend**

Run WebUI as a Node service and point rewrites to the backend:

```bash
cd webui
npm ci
LEVARA_API_URL=http://127.0.0.1:8080 npm run build
LEVARA_API_URL=http://127.0.0.1:8080 npm run start -- -p 3000
```

Set the same backend URL during build and runtime: Next.js records rewrites
in the build output. There is no default backend `/ui` route.

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
| Tests | Playwright against `3011` by default | CI + smoke after deploy |

---

## Monitoring

Set the actual backend origin (without `/api/v1`) and supply an active
instance-admin token through your secret store for protected/admin checks:

```sh
export LEVARA_ORIGIN=http://127.0.0.1:8080
: "${LEVARA_TOKEN:?set active instance-admin token}"
```


### WebUI-level checks

Run from the WebUI host:

```bash
curl -sS http://127.0.0.1:3000/
curl -sS http://127.0.0.1:3000/health
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" http://127.0.0.1:3000/health/details
```

Because `/health` and `/health/details` are rewritten by Next, these checks
validate both WebUI routing and backend reachability.

### Backend health

Run from the backend host:

```bash
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/health"
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/health/details"
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/api/v1/info"
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/api/v1/errors?limit=10"
```

For MCP/WebUI admin observability:

```bash
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/api/v1/admin/mcp/tools"
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/api/v1/admin/mcp/summary"
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" "$LEVARA_ORIGIN/api/v1/admin/mcp/sessions?limit=20"
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
cd webui # from the repository root
LEVARA_API_URL=http://127.0.0.1:8080 npm run dev -- -p 3001
```

In another shell:

```bash
curl -sS http://127.0.0.1:3001/health
curl --fail-with-body -sS -H "Authorization: Bearer $LEVARA_TOKEN" http://127.0.0.1:3001/api/v1/info
```

### Playwright

The curated suite uses mocked API responses and a real Chromium browser:

```sh
cd webui # from the repository root
LEVARA_API_URL=http://127.0.0.1:1 npm run test:e2e
```

The complete `npm run test:e2e:integration` also contains tests that contact the
configured backend. Use a separate test server and an explicit `LEVARA_API_URL`;
see [testing](testing.md) for commands, dependencies and observed results.

### Build check

```bash
cd webui # from the repository root
npm run lint
npm run build
```

---

## Operational Workflows

### Upload and Cognify

1. Upload files in `/datasets`.
2. Open the dataset detail page.
3. Start Cognify.
4. The dataset page polls `/api/v1/cognify/<runId>/status` once per second until terminal status.
5. If progress disappears, check:
   - browser DevTools Network for `/api/v1/cognify/<runId>/status`;
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
curl --fail-with-body -sS -X POST "$LEVARA_ORIGIN/mcp" \
  -H "Authorization: Bearer $LEVARA_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"webui-runbook-check","version":"0.1"}}}'
```

---

## Troubleshooting

| Symptom | Likely cause | Check / fix |
|---|---|---|
| WebUI loads but Dashboard cards fail | `LEVARA_API_URL` points at the wrong backend | Check authenticated health request; rebuild/restart WebUI with correct env |
| Browser calls `localhost:8081` directly and hits CORS | `NEXT_PUBLIC_API_URL` is set | Prefer empty `NEXT_PUBLIC_API_URL`; otherwise add WebUI origin to backend CORS |
| Test auth hits 429 | Auth rate limit reached | Wait for the window and reuse fixture credentials; rerun on an isolated test server with recorded rate policy |
| Cognify progress does not finish | Failed status request or backend run disappeared | Check polling errors and `/api/v1/cognify/<runId>/status`, then backend logs |
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
5. For team changes, verify auth, CORS, status polling and reverse proxy behavior; test SSE separately if a client uses it.
6. For sync or workspace changes, verify audit/status screens after the run.

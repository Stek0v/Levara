# Tutorial 04 — Deploy for a Team (30 minutes)

Take Levara from a laptop to a shared server: Postgres, authentication,
tenant isolation, and workspace indexing for the whole team. Every claim in
this tutorial was verified against a live server (2026-09-03).

Prerequisites: a Linux host (or Mac) with Go 1.26+, PostgreSQL 14+, and
optionally Docker.

## 1. Database and build

```bash
sudo -u postgres createuser levara --pwprompt
sudo -u postgres createdb levara -O levara

git clone https://github.com/Stek0v/Levara.git && cd Levara
make build
```

## 2. Team profile configuration

Copy the example and fill in real secrets:

```bash
cp deploy/profiles/team.postgres.env.example .env
# then edit: POSTGRES_DSN, JWT_SECRET (stable random string)
```

The team profile example enables:

- `LEVARA_PROFILE_STRICT=1` — refuses to boot with a broken config (no
  silent fallbacks to local SQLite)
- workspace watch + index worker + audit export — shared Markdown workspaces
  stay indexed and audited
- `LEVARA_TENANT_ENFORCED=0` — optional for teams, **required** for
  enterprise isolation

## 3. Start with auth enforced

```bash
set -a && source .env && set +a
./levara-server -config-check              # validate before serving
./levara-server -require-auth \
  -port=8080 -grpc-port=8081
```

With `-require-auth`, `/health` stays public for load balancers, but
everything else answers 401 without a token (verified: unauthenticated
`POST /api/v1/collections` → 401).

The MCP endpoint `http://your-host:8080/mcp` now requires
`Authorization: Bearer <token>` on every call — see
[02-agent-integration.md](02-agent-integration.md) for client config.

## 4. Create users

```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@team.dev","password":"<strong-password>"}'
# {"access_token":"...","token_type":"Bearer"}

curl -s -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@team.dev","password":"<strong-password>"}'
```

Register and login share a per-IP rate-limit bucket.

Use the returned `access_token` for API and MCP calls:

```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/collections \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"teamkb","dimension":256}'
# 201 Created
```

JWTs are signed with `JWT_SECRET` — keep it stable across restarts and
identical if you run more than one server process. For HMAC-peppered API-key
hashing, set `LEVARA_API_KEY_PEPPER` too.

## 5. Tenant isolation (optional, recommended)

With `LEVARA_TENANT_ENFORCED=1` in the environment, requests authenticated
but lacking a tenant context receive **403** — users can no longer see
each other's data by accident. Turn this on when the deployment crosses
team boundaries.

## 6. Point every agent at the server

Each teammate adds the same block (host + their own token) to Claude Code /
Cursor / Codex config — see
[02-agent-integration.md](02-agent-integration.md). Shared collections give
the team a common memory; `room × hall` taxonomy keeps it navigable;
`owner_id`-scoped records keep private notes private.

## 7. Verify the deployment

```bash
curl -s http://127.0.0.1:8080/health/details
# database: postgres/connected, embed: connected

# two users cannot see each other by default:
# user A saves to a collection with owner scoping, user B lists — count differs
```

Load expectation, verified 2026-09-03 (dual-core-class hardware): 50
concurrent agents, save p95 22 ms, recall p95 238 ms, zero cross-agent
leaks, zero duplicate task executions across two server processes on one
database (`benchmark/results/multi_user/run2_summary.json`).

## Operations

- Backups: `cmd/backup` wraps pg_dump/pg_restore, including schema-only and
  restore verification
- WebUI: `http://your-host:8080/ui` — memory, workspace, and task inspection
- Upgrades: rebuild the binary; indexes are disposable derivatives and
  reconcile/rebuild on demand

## Next

- [../deployment.md](../deployment.md) — systemd/launchd/Docker specifics
- [../deployment-matrix.md](../deployment-matrix.md) — which profile fits
  which operating model

_Last verified: 2026-09-03 (main; register/login/authenticated flows tested
live on a require-auth instance)._

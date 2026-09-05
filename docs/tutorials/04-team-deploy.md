# Tutorial 04 — Deploy for a Team (30 minutes)

Take Levara from a laptop to a shared server: Postgres, authentication,
workspace access checks, and indexing for the team. Core token-flow examples
were exercised on 2026-09-03; documentation was cross-checked with source on
2026-09-05. This is not an LDAP/AD or organizational-isolation acceptance test.

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
  -host=0.0.0.0 -port=8080 -grpc-port=0
```

This example explicitly binds the team listener; use HTTPS at the reverse proxy
and restrict direct backend access. Keep `-host=127.0.0.1` when the proxy runs
on the same host. The default listener is loopback, not a public team endpoint.

With `-require-auth`, `/health` stays public for load balancers, but
protected resource calls require credentials. Registration/login, enabled
identity handshakes and transport discovery have their own access rules.
gRPC raw-storage methods require an active global superuser with auth enabled;
use REST/MCP for ordinary users or disable gRPC with `-grpc-port=0`.

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

## 5. Identity and permission boundaries

`LEVARA_TENANT_ENFORCED=1` requires tenant context on guarded operations; it
does not create group grants or a document ACL. Dataset sharing is individual,
with viewer/editor/admin roles. To share one document, use a separate dataset
and verify both allowed and denied operations in
[document management](../document-management.md).

For LDAP/AD and SSO, follow [enterprise identity](../enterprise-identity.md).
Native LDAP, browser OIDC login and SCIM-to-SSO linking are absent; provisioning
an account is not proof that its browser/API identity or permissions are wired.

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
# follow document-workflow-scenarios.md with separate users and known IDs
```

Historical observation from the [2026-09-03 run](../../benchmark/results/multi_user/run2_summary.json): 50
concurrent agents, save p95 22 ms, recall p95 238 ms, zero cross-agent
leaks, zero duplicate task executions across two server processes on one
database. This workload does not certify every access path or set a latency SLA.

## Operations

- Backups: `cmd/backup` wraps pg_dump/pg_restore, including schema-only and
  restore verification
- WebUI: a separate Next.js service (port 3000 in development); configure
  its backend target/reverse proxy using [WebUI operations](../webui-operations.md)
- Upgrades: rebuild the binary; indexes are disposable derivatives and
  reconcile/rebuild on demand

## Next

- [../deployment.md](../deployment.md) — systemd/launchd/Docker specifics
- [../deployment-matrix.md](../deployment-matrix.md) — which profile fits
  which operating model

_Historical token-flow test: 2026-09-03. Documentation/source cross-check: 2026-09-05._

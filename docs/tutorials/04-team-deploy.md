# Tutorial 04 — Deploy for a Team

Take Levara from a laptop to a shared server: Postgres, authentication,
workspace access checks, and indexing for the team. This is a configuration guide; complete the
identity and document-access acceptance scenarios for the intended deployment.

Prerequisites: a Linux host (or Mac) with the Go version in [go.mod](../../go.mod), PostgreSQL, and
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
- `LEVARA_TENANT_ENFORCED=0` — optional for teams, required by the Enterprise preset; not complete group/document isolation

## 3. Start with auth enforced

```bash
set -a && source .env && set +a
./levara-server -require-auth -config-check # validate the same auth mode
./levara-server -require-auth \
  -host=127.0.0.1 -port=8080 -grpc-port=0 -dim=768
```

This listener stays on loopback behind a same-host HTTPS proxy. If a separate
proxy requires another bind address, explicitly configure it and restrict direct
backend access. Match `-dim` and provider settings before creating collections.

With `-require-auth`, `/health` stays public for load balancers, but
protected resource calls require credentials. Registration/login, enabled
identity handshakes and transport discovery have their own access rules.
Raw gRPC methods require an active global superuser with auth enabled.
The v1 document upload/cognify/status methods use JWT and live tenant/document
permissions with metadata SQL. See [document management](../document-management.md)
or use REST/MCP; `-grpc-port=0` disables the gRPC listener.

Protected MCP operations require a valid credential; use
`Authorization: Bearer <token>` on your host connection — see
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
  -d '{"name":"teamkb","embedding_dim":768}'
# 201 Created
```

`embedding_dim` matches this tutorial’s `-dim=768`. Before enabling embeddings,
choose a provider/model with that output dimension or change both values; the
collection request does not start an embedding model.

JWTs are signed with `JWT_SECRET` — keep it stable across restarts and
identical if you run more than one server process. For HMAC-peppered API-key
hashing, set `LEVARA_API_KEY_PEPPER` too.

## 5. Identity and permission boundaries

`LEVARA_TENANT_ENFORCED=1` requires tenant context on guarded operations.
Dataset sharing is individual, with viewer/editor/admin roles. Document/group
policy is exposed through the authenticated router, WebUI and CLI. Use the
file Access panel or `levara documents` and verify both allowed and denied operations in
[document management](../document-management.md).

For LDAP/AD and SSO, follow [enterprise identity](../enterprise-identity.md).
Direct LDAP/LDAPS/StartTLS, browser OIDC and SQL identity linking are implemented
locally; provisioning an account still does not prove that the real browser/API
identity and document permissions are wired on the target deployment.

## 6. Point every agent at the server

Each teammate adds the same block (host + their own token) to Claude Code /
Cursor / Codex config — see
[02-agent-integration.md](02-agent-integration.md). Shared collections give
the team a common memory; `room × hall` taxonomy keeps it navigable;
`owner_id`-scoped records keep private notes private.

## 7. Verify the deployment

```bash
curl --fail-with-body -sS -H "Authorization: Bearer $ACCESS_TOKEN" http://127.0.0.1:8080/health/details
# database: postgres/connected, embed: depends on configured provider

# two users cannot see each other by default:
# follow document-workflow-scenarios.md with separate users and known IDs
```

## Operations

- Backups: follow the complete SQL/files/object inventory in [deployment](../deployment.md#backup-and-recovery)
- WebUI: a separate Next.js service (port 3000 in development); configure
  its backend target/reverse proxy using [WebUI operations](../webui-operations.md)
- Upgrades: rebuild the binary; indexes are disposable derivatives and
  reconcile/rebuild on demand

## Next

- [../deployment.md](../deployment.md) — systemd/launchd/Docker specifics
- [Profile presets](../profile-presets.md) — configuration requirements by operating model

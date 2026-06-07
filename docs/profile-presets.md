# Profile Presets

Date: 2026-06-06
Status: operator guide

This guide maps the product ladder to runnable configuration presets. The
presets live under `deploy/profiles/` and are examples, not secrets; copy the
relevant file into your deployment environment and replace placeholder values.

## Preset Matrix

| Profile | Audience | Example env | Required services | Auth mode | Storage mode | Audit mode | Startup failure conditions |
|---|---|---|---|---|---|---|---|
| `personal` | One developer with local AI agents | `deploy/profiles/personal.local.env.example` | writable data dir, optional embedder | auth optional | SQLite + local filesystem | local workspace audit optional | data dir cannot be used |
| `solo_pro` | One power user syncing several machines | `deploy/profiles/solo_pro.sync.env.example` | writable data dir, stable sync token when sync is enabled | API key/bearer token for sync | SQLite or Postgres; local or S3-compatible storage | optional local export | strict mode fails when sync is configured without credentials |
| `team` | Small team with humans and per-agent credentials | `deploy/profiles/team.postgres.env.example` | Postgres, stable `JWT_SECRET`, server started with `-require-auth` | JWT/API keys | Postgres metadata + shared workspace root | workspace audit export expected | strict mode fails without Postgres, required auth, or stable JWT secret |
| `enterprise` | Corporate teams with tenant governance | `deploy/profiles/enterprise.strict.env.example` | Postgres, required auth or SSO bridge, tenant enforcement, audit export | required auth or SSO bridge | storage/KMS contracts exist; concrete corporate backends pending | audit export required | strict mode fails without Postgres, auth/SSO, stable signing config, tenant enforcement, or audit sink |

## Personal / Local

Use Personal for a single developer running local AI agents through MCP. Keep
auth off by default when the server listens only on loopback. SQLite and local
filesystem storage are the intended defaults; the memory palace and markdown
workspace tools should work without a team database.

Start from:

```bash
cp deploy/profiles/personal.local.env.example .env
```

Expected workflow:

- local MCP endpoint for Codex, Claude, Cursor, or similar agents;
- `workspace_context`, `workspace_write`, `workspace_search`, and
  `workspace_read` against a local markdown workspace;
- no required Postgres or SSO;
- lexical workspace search remains useful when dense embeddings are not
  configured.

## Solo Pro

Use Solo Pro when one person operates more than one Levara node, such as a Mac
and a Raspberry Pi. The key difference from Personal is stable sync identity:
if `LEVARA_SYNC_REMOTE_URL` is set, `LEVARA_TOKEN` must be stable.

Start from:

```bash
cp deploy/profiles/solo_pro.sync.env.example .env
```

Expected workflow:

- Mac/Pi sync via bearer token;
- local or S3-compatible raw-object storage;
- backup/restore runbooks before destructive maintenance;
- optional metrics for personal operations.

## Team

Use Team when multiple humans and AI agents share project workspaces. Team
deployments should use Postgres, required auth, stable JWT signing, API keys for
agents, and workspace audit export.

Start from:

```bash
cp deploy/profiles/team.postgres.env.example .env
```

Required runtime facts:

- `LEVARA_PROFILE=team`;
- `LEVARA_PROFILE_STRICT=1` for fail-fast validation;
- `DB_PROVIDER=postgres`;
- stable `JWT_SECRET`;
- server started with `-require-auth`.

## Enterprise

Use Enterprise when tenant governance, central identity, audit export, and
corporate storage controls matter. The current implementation has tenant
hardening, strict profile checks, audit export, an OIDC verified-claims adapter,
SSO/SCIM seams, and storage/KMS adapter contracts. Raw OIDC token verification,
SAML, SCIM HTTP surfaces, SIEM sinks, KMS/BYOK implementations, legal-hold
enforcement, and corporate object storage backends remain follow-up work.

Start from:

```bash
cp deploy/profiles/enterprise.strict.env.example .env
```

Do not treat the Enterprise preset as proof that KMS/BYOK or corporate object
storage is already production-ready. The adapter contracts are in place; the
concrete production backends remain follow-up work tracked after C4.

## Validation

Recommended checks before committing profile or deployment changes:

```bash
make profile-config-check
make test-commit
make test-release-candidate
```

`make profile-config-check` exercises the profile validation code and server
bootstrap config assembly without opening listeners, a database, or any network
connection. Use `server -config-check` with a copied preset to confirm one
deployment profile before starting it:

```bash
set -a; source deploy/profiles/personal.local.env.example; set +a
./levara-server -config-check
```

`make test-release-candidate` does not replace manual Pi and multi-node sync
smoke tests; it documents that gap in its output.

For access, tenant, audit export, storage/KMS, and MCP memory ownership changes,
also use `docs/security-diff-checklist.md`.

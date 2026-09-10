# Profile Presets

This guide maps the product ladder to runnable configuration presets. The
presets live under `deploy/profiles/` and are examples, not secrets; copy the
relevant file into your deployment environment and replace placeholder values.
The server does not load `.env` automatically. Export the edited file before
config-check or start (`set -a; source .env; set +a` in Bash). `LEVARA_PROFILE`
sets product requirements; the separate `-profile` flag selects functional
bootstrap behavior. Use `-require-auth` for Team/Enterprise token deployments.

## Preset Matrix

| Profile | Audience | Example env | Required services | Auth mode | Storage mode | Audit mode | Startup failure conditions |
|---|---|---|---|---|---|---|---|
| `personal` | One developer with local AI agents | `deploy/profiles/personal.local.env.example` | writable data dir, optional embedder | auth optional | SQLite + local filesystem | local workspace audit optional | data dir cannot be used |
| `solo_pro` | One power user syncing several machines | `deploy/profiles/solo_pro.sync.env.example` | writable data dir, stable sync token when sync is enabled | active-superuser credentials in auth mode | SQLite or Postgres; local or S3-compatible storage | optional local export | strict mode fails when sync is configured without credentials |
| `team` | Small team with humans and per-agent credentials | `deploy/profiles/team.postgres.env.example` | Postgres, stable `JWT_SECRET`, server started with `-require-auth` | JWT/API keys | Postgres metadata + shared workspace root | workspace audit export expected | strict mode fails without Postgres, required auth, or stable JWT secret |
| `enterprise` | Corporate teams with tenant governance | `deploy/profiles/enterprise.strict.env.example` | Postgres, required auth or SSO bridge, tenant enforcement, audit export | required auth or SSO bridge | storage/KMS contracts exist; concrete corporate backends pending | audit export required | strict mode fails without Postgres, auth/SSO, stable signing config, tenant enforcement, or audit sink |

## Personal / Local

Use Personal for a single developer running local AI agents through MCP. Keep
auth off by default when the server listens only on loopback. The Personal example explicitly sets `DB_PROVIDER=sqlite` and `DB_PATH`; a
bare functional `standalone` profile does not itself enable SQL. Local memory
and workspace metadata need that SQL configuration, even without Postgres.

Start from:

```bash
cp deploy/profiles/personal.local.env.example .env
# Edit .env for this deployment before exporting it.
set -a && source .env && set +a
./levara-server -config-check
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
replace the example remote URL with your own API base and set stable credentials.
In authenticated mode sync requires an active global superuser. The server
forwards its configured sync token only to the exact `LEVARA_SYNC_REMOTE_URL`;
it does not forward it to an arbitrary request URL or follow redirects.

Start from:

```bash
cp deploy/profiles/solo_pro.sync.env.example .env
# Edit .env for this deployment before exporting it.
set -a && source .env && set +a
./levara-server -config-check
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
# Edit .env for this deployment before exporting it.
set -a && source .env && set +a
./levara-server -require-auth -config-check
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
raw OIDC bearer verification against a JWKS (RS256/ES256, iss/aud allowlists,
key rotation; env: `LEVARA_OIDC_JWKS_URL`, `LEVARA_OIDC_ISSUERS`,
`LEVARA_OIDC_AUDIENCES`), a SAML 2.0 service provider (`/api/v1/saml/login`,
`/api/v1/saml/acs`, `/api/v1/saml/metadata`; env: `LEVARA_SAML_ENABLED`,
`LEVARA_SAML_ENTITY_ID`, `LEVARA_SAML_ACS_URL`, `LEVARA_SAML_IDP_METADATA_URL`
or `LEVARA_SAML_IDP_METADATA_FILE`, `LEVARA_SAML_KEY_FILE`,
`LEVARA_SAML_CERT_FILE` — SP-initiated flows only), a SCIM 2.0 provisioning
surface (`/scim/v2`; env: `LEVARA_SCIM_TOKEN`, `LEVARA_SCIM_ISSUER`, optional
`LEVARA_SCIM_TENANT_ID`; Users and managed Groups), direct LDAP/LDAPS/StartTLS,
browser OIDC with PKCE, SQL identity linking, authenticated document/group REST
policy, S3 and AWS KMS/BYOK implementations, and an audit webhook spool.
Document ACL REST/CLI/WebUI and document-scoped recipient discovery are present.
End-to-end legal hold, real AD/IdP/vendor provisioning, external object
storage/KMS and SIEM acceptance remain follow-up work.

Start from:

```bash
cp deploy/profiles/enterprise.strict.env.example .env
# Edit .env for this deployment before exporting it.
set -a && source .env && set +a
./levara-server -require-auth -config-check
```

Do not treat the Enterprise preset as proof that KMS/BYOK or corporate object
storage is production-ready. Concrete AWS adapters exist and pass local
contract tests; the target services and recovery procedures still need
deployment acceptance.

For a corporate pilot, follow [LDAP/AD and SSO setup](enterprise-identity.md).
The preset validates declared configuration; it does not test your identity
provider, establish group permissions, or make directory users interchangeable
with existing local accounts. Verify these boundaries before adding documents.

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
use the supported/gaps matrix and acceptance checks in
[enterprise identity](enterprise-identity.md) and
[document management](document-management.md).

# Proposal

## Why

Identity components have protocol and SQL tests, but original T25 still lacks one shared-store browser/provisioning/document lifecycle over real local TLS listeners. Add that evidence without changing a working authentication contract.

## What Changes

- Add a bounded local protocol sandbox check: SCIM provisioning and managed groups, signed OIDC and SAML browser login, downstream document access, rename, membership revoke, logout, deactivation and restart-state handling on SQLite/PostgreSQL.
- Retain existing LDAP TLS/BER and stable identity checks in the acceptance matrix.
- Record observed wire, persistence and denial results with a frozen source revision.
- Scope is local signed protocol acceptance. Corporate vendor interoperability and an actual server binary restart are separate claims; handler reconstruction must be labelled accurately.

## Capabilities

### New Capabilities

None. This change adds verification and evidence for existing supported behavior.

### Modified Capabilities

None; skip_specs is explicit. No new endpoint, schema, feature flag, migration or dependency.

## Impact

One new cmd/server test file and acceptance documentation. Existing native route registration, verified identity admission, SCIM SQL bridges and document authorization are used. No production configuration, deployment or live data changes.

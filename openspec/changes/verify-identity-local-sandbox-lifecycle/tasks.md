# Tasks

## 1. Local identity lifecycle

- [x] 1.1 Add one shared-store real TLS scenario on SQLite/PostgreSQL covering SCIM provisioning, signed OIDC/SAML login, exact identity/email rename, managed-group document authority and concurrent ETag updates. Verify `go test -race ./cmd/server -run TestIdentityLocalSandboxLifecycle -timeout=6m` with isolated PostgreSQL preflight; record actual denied and allowed wire responses in evidence.
- [x] 1.2 Extend that scenario with logout, deactivation and reconstructed pending browser state using retained SQL/key material. Verify the same focused command, exact preserved identity/document data and explicit restart-state coverage limits in evidence.

## 2. Current acceptance

- [x] 2.1 Run current LDAP/OIDC/SAML/SCIM/Session and managed-group matrices across auth/access/server/HTTP using actual SQLite/PostgreSQL, validate strict OpenSpec and generated contracts, and record frozen source/log digests plus independent review. REST owner-spoofing checks stay excluded by user instruction; vendor interoperability and actual binary restart are not claimed.

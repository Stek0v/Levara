# Design

## Context

See proposal.md. Existing OIDC/SAML fixtures each construct independent stores; SCIM, managed groups and document admission already share SQL bridges in production. Identity keys are exact issuer/directory plus immutable subject, never an email merge. Existing LDAP checks exercise local TLS/BER and entryUUID/objectGUID.

## Goals / Non-Goals

Goals: one persisted-store, real HTTP/TLS lifecycle in each SQL dialect using native route middleware and signed provider responses.
Non-goals: vendor AD/Entra certification, paid providers, a new auth abstraction, public contract change or a process/binary restart claim from object reconstruction.

## Decisions

- Keep the new scenario in one cmd/server test file, reuse existing helpers and dependencies. A few provider-only helpers are preferable to refactoring stable fixtures or inventing a reusable sandbox framework.
- Provision two users and a managed group through SCIM, bind exact provider identity, then exercise verified sessions against native document policy/grants/raw-read routes. OIDC and SAML use the same provisioned immutable identity with deliberately different email claims.
- Rename keeps SQL identity/ownership; membership removal denies the group-protected document; re-add permits it. Concurrent same-ETag group updates have one winner and one precondition failure. Logout revokes the chosen session; deactivation prevents new login and denies retained credentials.
- Reconstruct route/browser/SP objects with the same SQL store, JWT/key material and provider listeners. Pending browser state cannot survive reconstruction, while completed persisted sessions follow documented behavior. Explicitly record that this is process-local state semantics, not a server executable restart.
- Existing LDAP wire/identity tests and invalid OIDC/SAML collision, binding and replay checks form the broader acceptance matrix; do not duplicate every per-component test.

## Risks / Trade-offs

- Local signed protocol providers do not certify a third-party deployment → report only the bounded local protocol subset.
- Fixture constructors may hard-code listener redirects or create separate DBs → add only the minimum provider-only helpers in the owned new file.
- A genuine production defect could emerge → stop the test-only scope and give root an exact diagnosis and minimal proposed expansion, preserving all evidence.

## Migration Plan

No migration or rollout. Test resources are disposable and closed by cleanup; production services and data are untouched.

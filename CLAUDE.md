# CLAUDE.md — Levara Agent Guide

This repository is the Levara codebase. The Go module now lives at the
repository root; legacy adapter experiments and pre-rebrand code have been
removed from the working tree.

For the canonical memory/MCP playbook, use [`AGENTS.md`](AGENTS.md). Keep the
MCP tool catalog in `AGENTS.md` synchronized through the contract generator
instead of editing tool inventories by hand.

## Current Project Shape

- Main README: [`README.md`](README.md)
- Product ladder: [`docs/product-ladder.md`](docs/product-ladder.md)
- Runtime profiles: [`docs/profile-presets.md`](docs/profile-presets.md)
- Security review checklist: `docs/internal/security-diff-checklist.md` (local-only, gitignored)
- REST/gRPC/MCP contract artifacts: [`docs/api-contract.md`](docs/api-contract.md)
  and [`docs/contract.json`](docs/contract.json)

## Development Commands

Run commands from the repository root.

```bash
make build
make test-commit
make profile-config-check
make contract-check
```

For focused work inside the engine:

```bash
go test ./docs ./pkg/profile ./pkg/access ./pkg/storage
make test-release-candidate
```

## Cleanup Policy

- Do not reintroduce pre-Levara names, adapter experiments, or external
  benchmark scaffolding into top-level docs.
- Keep product claims aligned with `docs/product-ladder.md`.
- Keep enterprise claims aligned with `docs/enterprise-identity.md`: OIDC bearer,
  SAML SP, limited SCIM Users and basic S3 exist; native LDAP, browser OIDC,
  SCIM↔SSO linkage, group/document ACL and production KMS/SIEM remain gaps.
- Use `docs/testing.md` for observed test results and their limits; do not turn
  historical benchmark pass labels into current security or quality claims.

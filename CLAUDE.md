# CLAUDE.md — Levara Agent Guide

This repository is the Levara codebase. The current Go module lives in
[`Levara/`](Levara/); legacy adapter experiments and pre-rebrand code have been
removed from the working tree.

For the canonical memory/MCP playbook, use [`AGENTS.md`](AGENTS.md). Keep the
MCP tool catalog in `AGENTS.md` synchronized through the contract generator
instead of editing tool inventories by hand.

## Current Project Shape

- Main README: [`Levara/README.md`](Levara/README.md)
- Product ladder: [`Levara/docs/product-ladder.md`](Levara/docs/product-ladder.md)
- Runtime profiles: [`Levara/docs/profile-presets.md`](Levara/docs/profile-presets.md)
- Security review checklist: [`Levara/docs/security-diff-checklist.md`](Levara/docs/security-diff-checklist.md)
- REST/gRPC/MCP contract artifacts: [`docs/api-contract.md`](docs/api-contract.md)
  and [`docs/contract.json`](docs/contract.json)

## Development Commands

Run commands from the repository root unless a command explicitly changes into
`Levara/`.

```bash
make build
make test-commit
make profile-config-check
make contract-check
```

For focused work inside the engine:

```bash
cd Levara
go test ./docs ./pkg/profile ./pkg/access ./pkg/storage
make test-release-candidate
```

## Cleanup Policy

- Do not reintroduce pre-Levara names, adapter experiments, or external
  benchmark scaffolding into top-level docs.
- Keep product claims aligned with `Levara/docs/product-ladder.md`.
- Keep enterprise claims honest: SSO/storage/KMS/SIEM production backends are
  adapter roadmap unless concrete implementations and tests exist.

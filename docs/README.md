# Levara Documentation

Local-first context infrastructure for AI agents: durable memory, hybrid
search, a temporal knowledge graph, a verifiable Markdown workspace, sync,
observability, and scoped long-running tasks in one Go binary.

Entry points by role:

| I want to… | Start here |
|---|---|
| Understand what Levara is in 5 minutes | [README.md](../README.md) ([RU](../README_RU.md)) |
| Install and run the server | [getting-started.md](getting-started.md) |
| Learn by doing (RU, step-by-step) | [tutorials/00-getting-started-ru.md](tutorials/00-getting-started-ru.md) |
| Find the right tool/command for my task | [features-guide.md](features-guide.md) |
| Integrate my agent (Claude Code, Cursor, Codex) | [getting-started.md → MCP integration](getting-started.md) and [integrations.md](integrations.md) |
| Deploy for a team | [deployment.md](deployment.md), [deployment-matrix.md](deployment-matrix.md) |
| Configure runtime profiles | [profile-presets.md](profile-presets.md) |
| Understand search strategies | [search-strategies-guide.md](search-strategies-guide.md) |
| Use the Markdown workspace | [markdown-native-workspace.md](markdown-native-workspace.md), [recipes/](recipes/) |
| Run long-horizon tasks | [long-horizon-runtime.md](long-horizon-runtime.md) ([RU](long-horizon-runtime.ru.md)) |
| Operate and observe | [webui-operations.md](webui-operations.md), [cron-profiles.md](cron-profiles.md), [macos-levara-watchdog.md](macos-levara-watchdog.md) |
| Check the API surface | [api-contract.md](api-contract.md) (SSOT), [api-reference.md](api-reference.md), [contract.json](contract.json) |
| Evaluate Levara for my org | [marketing/](marketing/), [product-ladder.md](product-ladder.md) |

## Verified state

- 2026-09-03: full code review — 54 findings, 52 fixed, 2 documented as
  deliberate design choices. CI 19/19 green (lint, vet, race, govulncheck,
  npm audit, contract check). Multi-user load suite 6/6 PASS — see
  [../benchmark/results/multi_user/](../benchmark/results/multi_user/).
- Current operational snapshot: [current-state.md](current-state.md).

## Directory map

| Path | Contents |
|---|---|
| `docs/*.md` (this level) | User-facing and operator-facing documentation |
| `docs/tutorials/` | Step-by-step learning paths |
| `docs/recipes/` | Short task-specific integration recipes |
| `docs/marketing/` | GTM: positioning, audience landing pages (RU) |
| `docs/product/` | Market segments and product analysis |
| `docs/adr/` | Architecture decision records |
| `docs/internal/` | Working notes: eval reports, experiments, migration plans, design docs — historical context, not guaranteed current |
| `docs/swagger.*`, `docs/contract.json` | Generated API contract artifacts (do not hand-edit) |

## Conventions

- `api-contract.md` / `contract.json` are generated; edit the source and
  regenerate (`make contract-check` guards drift).
- Docs claiming runtime behavior should carry a `_Last verified: <date>_` note.
- Internal working documents live under `docs/internal/` and are not part of
  the user documentation contract.

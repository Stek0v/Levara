# Levara Documentation

Local-first context infrastructure for AI agents: durable memory, hybrid
search, a temporal knowledge graph, a verifiable Markdown workspace, sync,
observability, and scoped long-running tasks in one Go server binary. The optional WebUI is a separate Next.js service.

Entry points by role:

| I want to… | Start here |
|---|---|
| Understand what Levara is in 5 minutes | [README.md](../README.md) ([RU](../README_RU.md)) |
| Install and run the server | [getting-started.md](getting-started.md) |
| Learn by doing (RU, step-by-step) | [tutorials/00-getting-started-ru.md](tutorials/00-getting-started-ru.md) |
| First memory in 15 minutes | [tutorials/01-first-memory.md](tutorials/01-first-memory.md) |
| Connect Claude Code / Cursor / Codex | [tutorials/02-agent-integration.md](tutorials/02-agent-integration.md) |
| Upload, process and verify documents (WebUI / CLI) | [document-management.md](document-management.md), [document-workflow-scenarios.md](document-workflow-scenarios.md) |
| Share one document with a colleague; understand group limits | [document-management.md](document-management.md) |
| Connect LDAP/AD, OIDC, SAML and SCIM | [enterprise-identity.md](enterprise-identity.md) |
| Configure S3, AWS KMS and durable SIEM delivery | [enterprise-storage-audit.md](enterprise-storage-audit.md) |
| Onboard users and verify team access | [team-onboarding.md](team-onboarding.md) |
| Learn the ingestion API | [tutorials/03-knowledge-base.md](tutorials/03-knowledge-base.md) |
| Deploy for a team with auth | [tutorials/04-team-deploy.md](tutorials/04-team-deploy.md) |
| Find the right tool/command for my task | [features-guide.md](features-guide.md) |
| Integrate my agent (Claude Code, Cursor, Codex) | [getting-started.md → MCP integration](getting-started.md) and [integrations.md](integrations.md) |
| Deploy for a team | [deployment.md](deployment.md) |
| Configure runtime profiles | [profile-presets.md](profile-presets.md) |
| Understand search strategies | [search-strategies-guide.md](search-strategies-guide.md) |
| Use the Markdown workspace | [markdown-native-workspace.md](markdown-native-workspace.md), [recipes/](recipes/) |
| Run long-horizon tasks | [long-horizon-runtime.md](long-horizon-runtime.md) ([RU](long-horizon-runtime.ru.md)) |
| Operate and observe | [webui-operations.md](webui-operations.md), [cron-profiles.md](cron-profiles.md), [macos-levara-watchdog.md](macos-levara-watchdog.md) |
| Verify tests and published claims | [testing.md](testing.md) |
| Check the API surface | [api-contract.md](api-contract.md) (SSOT), [api-reference.md](api-reference.md), [contract.json](contract.json) |
| Evaluate Levara for my org | [product-ladder.md](product-ladder.md), [product/market-segments.md](product/market-segments.md), [unimplemented-roadmap.md](product/unimplemented-roadmap.md) |

## Evidence and document status

[Testing](testing.md) lists dated observed results, commands, mocks, skipped
integrations and remaining acceptance work. [Document scenarios](document-workflow-scenarios.md)
map current behavior to tests and distinguish source inspection from live validation.
The [CI definition](../.github/workflows/go-ci.yml) describes gates, not their latest result.

Obsolete deployment snapshots, completed plans and duplicate marketing articles
have been removed. Current guides describe supported workflows; ADRs retain
architecture rationale with explicit implementation limits. Historical raw
benchmark results remain separate from current product claims.

## Directory map

| Path | Contents |
|---|---|
| `docs/*.md` (this level) | User-facing and operator-facing documentation |
| `docs/tutorials/` | Step-by-step learning paths |
| `docs/recipes/` | Short task-specific integration recipes |
| `docs/product/` | Market segments and product analysis |
| `docs/adr/` | Architecture decision records |
| `docs/internal/`, `docs/marketing/` | Local-only (gitignored): internal working notes and marketing materials |
| `docs/swagger.*`, `docs/contract.json` | Generated API contract artifacts (do not hand-edit) |

## Conventions

- `api-contract.md` / `contract.json` are generated; edit the source and
  regenerate (`make contract-check` guards drift).
- Docs claiming runtime behavior should carry a `_Last verified: <date>_` note.
- Internal working documents (`docs/internal/`) and marketing materials
  (`docs/marketing/`) are gitignored — they live locally and are not part of
  the published documentation.

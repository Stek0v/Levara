# LevaraOS Market Segments

This document turns the original campaign notes into a usable GTM map. It should
stay aligned with `Levara/docs/marketing/*`, `Levara/docs/product-ladder.md`,
and `Levara/docs/profile-presets.md`.

## Segment Map

| Segment | ICP | Main pain | Levara hook | Best CTA |
|---|---|---|---|---|
| S1. AI-agent developers | Claude Code, Cursor, Codex, Cline users | agents forget project context between sessions | MCP-first memory palace, room x hall taxonomy, wake-up briefings, per-agent diaries | “Add Levara MCP and save your first decision” |
| S2. Self-hosters and privacy-conscious developers | homelab, local-first, air-gapped users | SaaS memory is opaque or unacceptable | one Go binary, SQLite/local files, Mac-Pi sync, no required cloud | “Run local memory on your own disk” |
| S3. RAG/KG researchers | retrieval, KG, temporal-memory builders | vector-only memory loses relationships and time | temporal KG, hybrid BM25/vector, graph-aware rerank, reproducible tests | “Use Levara as a temporal memory research harness” |
| S4. Small startups and product teams | 2-15 person teams replacing hosted vector/memory SaaS | cost, control, and multi-agent collaboration | self-hosted Team profile, Postgres, auth, audit, workspace ACL | “Pilot shared agent memory for one project” |
| S5. Edge/on-device AI | Pi, Jetson, local inference, field devices | memory must run near the agent and survive network loss | ARM64 build, local profiles, sync, local embeddings/LLMs | “Run memory at the edge” |

## Message Ladder

| Audience | Short message | Proof to show | Avoid saying |
|---|---|---|---|
| Individual developers | “Your AI remembers the project without sending memory to a SaaS.” | Personal preset, MCP tools, local SQLite/files, Markdown workspace | “Enterprise-ready KMS” |
| Power users | “One memory follows you across machines.” | Solo Pro sync token, backup/restore tests, Pi docs | “transparent multi-master cloud sync” unless implemented |
| Teams | “Humans and agents share context with auth, ACL, and audit.” | Team strict profile, `pkg/access`, workspace audit, policy boundary tests | “SSO/SCIM complete” |
| Enterprise | “Governed agent memory with clear adapter seams.” | enterprise strict checks, OIDC verified-claims adapter, audit export, storage/KMS contracts | “production SAML/SCIM/KMS/SIEM ready” |

## Campaign Backlog

### S1: AI-Agent Developers

1. **Memory Palace for Claude/Codex/Cursor**
   A 10-minute guide: start Levara, add MCP config, call `save_memory`, then
   `wake_up`. KPI: installs, MCP configs added, GitHub stars.
2. **Room x Hall content series**
   Seven short posts explaining `fact`, `event`, `decision`, `preference`,
   `advice`, `discovery`, and diaries. KPI: saves/bookmarks, docs clicks.
3. **Recall challenge**
   Ask users to share the most useful `recall_memory` that saved a session.
   KPI: UGC, social reach, examples for docs.
4. **Subagent diaries cookbook**
   Show reviewer/planner/oncall agents keeping isolated memory namespaces.
   KPI: forks, cookbook usage.

### S2: Self-Hosters and Privacy

1. **Your memory, your disk**
   Manifest-style post against opaque SaaS memory. KPI: self-hosted/homelab
   shares.
2. **One-binary install demo**
   60-second screencast: build, config-check, run, connect MCP. KPI: video views
   and install conversion.
3. **Mac-Pi sync deep dive**
   Explain bearer-auth sync, version-skew warning, and backup boundaries. KPI:
   Habr/dev.to views, Pi users.
4. **Air-gap lab**
   Tutorial for local embeddings + local LLM + Levara without external APIs.
   KPI: offline/local-first community adoption.

### S3: RAG/KG Researchers

1. **Temporal KG demo**
   Show edge supersession and `as_of` entity queries. KPI: research mentions.
2. **Hybrid retrieval notebook**
   Compare vector-only, BM25-only, hybrid RRF, and graph-aware rerank. KPI:
   reproducible benchmark runs.
3. **Memory-bench dataset proposal**
   Public dataset of agent sessions and ground-truth recall queries. KPI:
   dataset downloads and citations.

### S4: Small Startups and Product Teams

1. **Agent memory with ACL and audit**
   Team pilot guide: one project, two users, one agent token, workspace audit.
   KPI: pilot starts.
2. **TCO calculator**
   Compare hosted memory/vector SaaS against self-hosted Levara. KPI: inbound
   leads.
3. **Crash/recovery demo**
   Show WAL-backed recovery and backup restore, using only verified behavior.
   KPI: founder/platform-team trust.

### S5: Edge/On-Device AI

1. **Levara on inexpensive hardware**
   ARM64/Pi guide with local profile and memory sync. KPI: maker community
   shares.
2. **Local model stack**
   Ollama + local embeddings + Levara memory. KPI: local-AI installs.
3. **Smallest agent that remembers**
   Hackathon/demo prompt for Pi/Jetson agents. KPI: demos and forks.

## Cross-Segment Assets

| Asset | Reuse in |
|---|---|
| `Levara/README.md` first screen | all segments |
| `Levara/docs/marketing/personal.md` | S1, S2 |
| `Levara/docs/marketing/solo-pro.md` | S2, S5 |
| `Levara/docs/marketing/team.md` | S4 |
| `Levara/docs/marketing/enterprise.md` | enterprise discovery, security review |
| `Levara/docs/security-diff-checklist.md` | Team/Enterprise trust |

## Claim Guardrails

- Use “adapter contract”, “seam”, or “verified-claims adapter” for current
  enterprise identity/storage/KMS work.
- Do not claim production SAML, SCIM HTTP, SIEM, KMS/BYOK, Azure/GCS, or legal
  hold backend support until those adapters exist and have tests.
- Performance claims should link to a reproducible benchmark or be phrased as
  historical benchmark results, not universal guarantees.
- Product profile claims should be backed by `deploy/profiles/*` and
  `pkg/profile` tests.

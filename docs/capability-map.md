# Levara Capability Map

Moved from README on 2026-09-27 (Ф1, task T3) so the first screen stays
memory-first. The generated API inventory is
[api-contract.md](api-contract.md); the product ladder with tier boundaries is
[product-ladder.md](product-ladder.md).

With `LEVARA_PROFILE=personal` and no explicit `LEVARA_MCP_TOOLSET`, the
server advertises the 13-tool `core` set (room×hall memory including
`supersede_memory` and `delete_memory`, search, doctor). Everything below is
available through toolset profiles, REST, and runtime profiles as you climb
the ladder.

## Capabilities by area

| Area | Implemented capabilities |
|---|---|
| **Agent memory** | `save_memory`, filtered recall, wake-up briefings, pins, room × hall routing, per-agent diaries, chat recall, deletion, consolidation and revert, provenance-preserving supersession |
| **Search and knowledge** | WAL-backed HNSW, BM25, hybrid RRF, rerank routing, RAG and graph search, temporal validity, path queries, communities, structured filters, Git-aware analysis |
| **Ingestion** | Add/list/prune data, Cognify and Codify pipelines, status tracking, deduplication, embeddings, graph extraction, drift checks |
| **Verifiable workspace** | Markdown context and artifacts, search/read/write/commit/revert/delete, manifests, conflicts, access checks, audit log, watch mode, indexing and reindexing jobs, retries, reconciliation and GC |
| **Long-Horizon Task Runtime** | Scoped tasks, Definition of Done, versioned plans, dependent steps, atomic leases, immutable receipts, checkpoints, blockers, crash recovery, risk-based reviewer policy, deterministic completion and verified-memory promotion |
| **Operations** | Doctor checks, runtime and ingestion snapshots, recent errors, heartbeat, memory-index health/retry, SQL↔vector reconciliation, workspace job/watch health, Prometheus metrics |
| **Sync and storage** | Mac/Pi and peer sync, scoped manifests and status, backup/restore tooling, SQLite or PostgreSQL metadata, local or S3-compatible raw-object storage |
| **Identity and governance** | JWT and API keys, individual dataset sharing, workspace access checks, tenant membership checks, audit export, OIDC bearer verification, SAML SP, limited SCIM Users API, storage/KMS contracts |
| **Product surfaces** | MCP Streamable HTTP, REST, gRPC v1/v2, CLI tools, Next.js WebUI, feedback and memory-behavior analytics |

## MCP tool groups

| Group | Tools | Responsibility |
|---|---:|---|
| Workspace | 25 | Context, artifacts, authoring, revisions, indexing, jobs and audit |
| Memory | 14 | Lifecycle, recall, consolidation, supersession and wake-up |
| Operations | 9 | Health, errors, reconciliation, indexing and runtime state |
| Task | 8 | Long-Horizon Task Runtime (flag `LEVARA_LONG_HORIZON_RUNTIME`) |
| Data | 5 | Add, list, drift, delete and prune |
| Search | 4 | Hybrid/graph search, entities and communities |
| Cognify | 3 | Cognify, Codify and run status |
| Chat | 3 | Save, recall and search chat records |
| Git | 3 | Commit analysis, Git search and graph pruning |
| Context | 2 | Project context selection and retrieval |
| Diary | 2 | Per-agent isolated notes |
| Feedback | 2 | Retrieval feedback and statistics |
| Sync | 2 | Cross-instance synchronization and status |

Group counts describe the canonical catalogue, not what a given deployment
advertises: toolset profiles (`core`, `memory`, `workspace`, `ops`,
`long-horizon`, `full`) and feature flags narrow the live `tools/list`. Check
`tools/list` on your connection or `/admin/mcp/summary` on the server.

## Runtime notes

- Notebooks were cut from the product on 2026-09-27; the routes stay behind
  `LEVARA_NOTEBOOKS=1` only until the code removal in Ф2.
- Task Runtime requires `LEVARA_LONG_HORIZON_RUNTIME=1`; its built-in worker
  uses a logging/no-op executor.
- Memory recall works without an embedding endpoint (SQL-backed fallback);
  vector semantic recall, document search and cognify need a configured
  embedder.

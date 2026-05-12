# Markdown-Native Workspace Capability Gate

Date: 2026-05-10

This note records the first implementation gate for using Levara as the
retrieval backend of a markdown-native workspace. Markdown files remain the
source of truth; Levara stores derived dense and lexical indexes.

## Capability Matrix

| Capability | Current status | Decision |
|---|---|---|
| Collection isolation | Native `CollectionManager` with per-collection stores | Use collections for project/branch/generation isolation where practical |
| Batch insert | Native core and collection-manager support | Use for indexer batches |
| Repeated ID upsert | Supported after this gate: replacement tombstones the previous HNSW slot and removes stale pending entries | Deterministic chunk IDs are safe if the indexer replaces by ID |
| Delete by ID | Native | Workspace manifest should delete exact vector IDs |
| Delete by filter | Not native | Do not depend on filter deletes; scan the workspace manifest and delete IDs |
| Metadata filtering | Post-filter only in search paths | Security/branch filters must fail closed and overfetch before post-filtering |
| BM25 lexical search | Native in-memory/persistent BM25 package | Use as default lexical sidecar for PoC |
| Hybrid search | Native BM25 RRF helper; REST/gRPC had paths already; MCP is wired by this gate | Use RRF fusion through one agent-facing MCP contract |
| Branch-aware generations | Not native | Implement in workspace manifest/indexer layer |
| Markdown VCS workspace | Not native | Build outside Levara; Levara remains derived index |
| Durable run artifacts | Not native; current run registry is in-memory | Implement `/runs/<id>/...` in workspace layer |
| Workspace ACL | Uses existing dataset owner/share roles with `project_id == dataset_id` | Read requires viewer+, write/admin lifecycle requires editor/admin/owner |

## Tests Added

- Repeated insert with the same ID replaces the search vector and metadata.
- MCP `CHUNKS_LEXICAL` works without an embedding pipeline.
- MCP `HYBRID` fuses vector and lexical hits.
- MCP metadata filters return an empty set when no hit matches; they do not
  fall back to unfiltered results.
- `VectorStore` SPI covers batch upsert, get, scan, delete-many,
  manifest-style delete-by-filter, collection metadata, and checkpoint.
- Workspace manifest package records `project_id + branch + generation +
  path + file_digest + chunk_id + vector_id`, supports JSON roundtrip,
  active-generation filtering, exact vector ID lookup, and path/generation
  deletion planning.
- Markdown indexer package chunks `.md` content, preserves heading path in
  metadata, embeds through an injected gateway, writes vectors through
  `VectorStore`, and updates the manifest only after successful upsert.
- Memory sync preserves `room`, `hall`, `is_pinned`, and `pin_priority` so
  cross-instance sync keeps the memory-palace contract intact.
- Generation GC drops exclusive generation collections and deletes exact vector
  IDs for shared collections before removing stale manifest records.
- Graph sync preserves temporal edge metadata (`valid_from`, `valid_until`,
  `superseded_by`, `confidence`) and `dataset_id` scope.
- REST workspace endpoints index, delete, read manifests, and GC markdown
  generations against the shared vector/BM25 stores.
- MCP workspace tools dispatch the same lifecycle operations through
  `workspace_index`, `workspace_delete`, `workspace_gc`, and
  `workspace_manifest`.
- Workspace filesystem truth endpoints read/write exact markdown paths, reindex
  existing files from disk, and block absolute/path-traversal writes.
- Durable run artifacts are stored as markdown files under
  `projects/<project>/<branch>/runs/<run-id>/`.
- CLI workspace commands read local markdown, read/write workspace markdown,
  reindex paths, and create/read durable run artifacts through REST.
- Workspace commit/log/revert snapshots copy the filesystem truth layer into
  `.kb/commits/<project>/<branch>/<commit-id>/` and can restore the project
  tree exactly from a chosen commit.
- Workspace reconciliation can scan the current filesystem truth (`*.md`) and
  build a fresh active generation after write/revert workflows.
- Optional polling watcher (`LEVARA_WORKSPACE_WATCH=1`) detects markdown save
  bursts under `projects/<project>/<branch>/`, debounces them, and reconciles a
  fresh active generation automatically.
- Watcher status is observable through REST/MCP/CLI with scan/reconcile/error
  counters, pending branch count, and the last auto-created generation.
- MCP end-to-end cycle covers write, lexical search, commit, edit, revert,
  single-call reindex into a fresh generation, and GC of stale generation
  collections.
- CLI end-to-end cycle covers the same user-facing workflow through REST-backed
  commands, including collection-scoped lexical search after rollback.
- CLI `workspace reindex` and `workspace reconcile` now accept chunking flags so
  command behavior matches the REST/MCP workspace API surface.
- `workspace_revert` supports optional reindex-on-revert across REST/MCP/CLI so
  rollback can restore markdown files and publish a fresh active generation in
  one call.
- Watcher status is persisted to `.kb/watch-status.json` and loaded on watcher
  startup so scan/reconcile/error counters and last-generation metadata survive
  process restarts.
- Workspace REST/MCP lifecycle tools enforce project-level ACLs through the
  existing dataset owner/share model; foreign project denials do not disclose
  file paths or content.
- Workspace chunks now carry `dataset_id=project_id`, and REST/MCP search
  filters hits by the caller's allowed dataset/project scopes, with
  `project_id` fallback for older workspace metadata.
- MCP initialize now records the authenticated JWT/API-key user in the session
  so tool calls can apply the same project ACL checks as REST.
- Production workspace documentation now lives in
  `docs/markdown-native-workspace.md`: architecture, solo/team workflows, ACL,
  fast Codex/Claude retrieval, MCP/CLI packaging, and Markdown-to-vector sync.
- Detailed next-task backlog with DoD and corner-case test coverage lives in
  `docs/markdown-workspace-next-tasks.md`.
- MCP `workspace_search` resolves the active workspace generation/collection
  from the manifest, returns freshness metadata, enriches hits with exact
  markdown path/heading/text fields, and keeps generic `search` unchanged.
- `workspace_search` tests cover active collection resolution, explicit stale
  generation, ambiguous multi-collection generations, missing active
  generation remediation, result metadata enrichment, and ACL denial without
  leaking private path/snippet/collection details.
- Workspace retrieval evaluation fixtures now live under
  `testdata/workspace-eval/` with a Markdown corpus and query ground truth.
- `TestWorkspaceRetrievalQualityEval` indexes the fixture corpus through the
  workspace/MCP path and checks `CHUNKS_LEXICAL`, `CHUNKS`, and `HYBRID`
  against `Recall@1`, `Recall@5`, and `MRR` thresholds. Current deterministic
  fixture metrics are `1.00` for all three metrics on all three search modes.
- Search argument parsing now accepts integer `top_k` values as well as JSON
  float values, covering workspace-to-search internal calls.
- Watcher status now persists per-project/per-branch entries under
  `watch-status.json.branches`, and `workspace_search.freshness` uses the
  matching branch instead of treating any global pending watcher branch as
  stale for every project.
- Reindex/reconcile operations now write durable indexing jobs under
  `.kb/jobs/<project>/<branch>/`. Jobs record operation, generation, paths,
  attempts, `pending/running/completed/failed/dead_letter` status, retry
  schedule, dead-letter time, and last error.
- REST/MCP surfaces now expose `workspace_index_jobs` and
  `workspace_enqueue_index_job` / `workspace_retry_index_job` so async work can
  be queued, failed jobs can be inspected, and saved payloads can be replayed.
- `LEVARA_WORKSPACE_INDEX_WORKER=1` starts an optional polling worker that
  drains pending jobs, retries failed jobs with exponential backoff, and moves
  exhausted jobs to `dead_letter`. `LEVARA_WORKSPACE_WATCH_ASYNC_INDEX=1`
  changes the watcher fast path from synchronous reconcile to durable job
  enqueue.
- Tests cover branch-specific watcher freshness, watcher status persistence,
  successful job recording, failed job recording, retry to completion with
  incremented attempts, idempotent enqueue, async worker completion,
  backoff/dead-letter handling, async watcher enqueue, and MCP tool
  descriptor/dispatch coverage.
- REST/MCP now expose `workspace_access_check` as an explicit ACL preflight and
  `workspace_audit_log` as a sanitized audit reader.
- Workspace REST and MCP operations write best-effort audit events under
  `.kb/audit/<project>/audit-YYYY-MM.jsonl`. Events include user/bot id,
  project, branch, operation, result, status, timestamp, and sanitized metadata;
  they intentionally omit markdown text, search queries, snippets, and exact
  file paths.
- Tests cover owner/viewer/foreign preflight decisions, successful and denied
  audit events, content/path omission from audit logs, and MCP descriptor/
  dispatch coverage for the new access/audit tools.
- REST/MCP now expose `workspace_context` so Codex/Claude can bootstrap
  accessible projects, branches, active generation/collection, watcher status,
  indexing job status, recommended `HYBRID` search, and the exact-read rule in
  one call.
- Context bootstrap respects ACL, returns initialization guidance for projects
  without manifests, and reports corrupt manifests per branch without failing
  the full response.
- Production recipes now live in `docs/markdown-workspace-deployment-recipes.md`
  with single-node dev, team server, Mac/Pi sync, backup/restore,
  troubleshooting, and host setup.
- Copyable host packages now live in `examples/agent-hosts/` for
  Claude-compatible MCP JSON, Cursor MCP JSON, Codex TOML, and shared agent
  instructions requiring `workspace_context -> workspace_search ->
  workspace_read`.
- `docs/markdown_workspace_docs_test.go` validates agent-host JSON examples,
  required Codex TOML fields, required workspace tool names in agent
  instructions, and deployment recipe links.
- `cmd/agent-hosts` adds a tested safe installer for Claude/Cursor JSON and
  Codex TOML configs. It preserves unrelated settings, replaces only the
  `levara` server stanza, supports `-dry-run`, and writes timestamped backups
  before modifying existing targets.
- REST/MCP now expose `workspace_ops_status` and `/workspace/ops/status` for
  operational health. The response aggregates watcher pending/error state,
  durable indexing jobs by status, dead-letter count, max job lag, and
  sanitized audit volume.
- Workspace job transitions refresh Prometheus gauges for
  `levara_workspace_index_jobs`, `levara_workspace_index_job_max_lag_seconds`,
  `levara_workspace_index_dead_letters`,
  `levara_workspace_watcher_pending_branches`,
  `levara_workspace_watcher_errors`, and
  `levara_workspace_audit_stored_events`. Audit writes increment
  `levara_workspace_audit_events_total{source,operation,result}`.
- Tests cover ops status JSON, dead-letter and lag accounting, watcher pending
  branch metrics, audit counter/gauge updates, malformed audit rows, REST
  route behavior, MCP dispatch, and MCP descriptor/schema coverage.
- `.kb/context-artifacts.json` is now a first-class registry for OpenAPI, DDL,
  Terraform, ADR, runbook, Markdown, and other context artifacts. REST/MCP
  expose `workspace_context_artifacts` for listing and
  `workspace_reindex_artifacts` for publishing configured artifacts into the
  active searchable workspace generation.
- `workspace_search` now returns an `answer_contract` and every enriched hit
  includes a machine-readable `citation` with source URI, exact
  `workspace_read` args, path, heading, generation, collection, digest,
  chunk ID, and vector ID. `workspace_read` returns file-level and chunk-level
  citations.
- Team conflict handling now has two paths: `workspace_conflicts` reports
  filesystem-vs-active-generation drift, and `workspace_write` supports
  `expected_file_digest` optimistic locking.
- `workspace_gc` supports `dry_run=true` and reports exclusive/shared
  collections and exact vector IDs before mutation. CLI parity now includes
  `workspace context`, `workspace ops-status`, `workspace conflicts`, and
  `workspace gc --dry-run`.
- Operator examples now live in `examples/ops/` for Prometheus alerts and a
  Grafana dashboard covering dead letters, max job lag, watcher pending
  branches, indexing jobs by status, and audit volume.

## Next Gates

1. Build optional product UI/dashboard on top of the completed workspace
   APIs: health, jobs, audit, conflicts, search freshness, and citations.
2. Add richer retrieval-quality/reranker evaluation dashboards.

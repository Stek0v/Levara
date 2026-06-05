# Full Testing Scenarios

Date: 2026-06-05
Status: design

This document is the master testing design for Levara after the product ladder
split. It complements `testing-roadmap.md` and `test-scenarios.md`: those files
track historical package work and MCP brainstorming; this file defines release
gates across product profiles, architecture layers, and enterprise readiness.

## Goals

- Prove the same core engine works for Personal, Solo Pro, Team, and Enterprise
  profiles without forking behavior.
- Verify public REST, MCP, gRPC, CLI, and docs contracts remain stable while
  identity/access code moves behind `pkg/access`.
- Catch regressions in tenant isolation, workspace audit, sync, memory recall,
  search relevance, crash recovery, and operational observability.
- Produce repeatable local, CI, Pi, and team-server test runs with clear pass
  criteria.

## Test Suites

| Suite | Purpose | Primary command | Required services | Gate |
|---|---|---|---|---|
| S0 Static and docs | Formatting, docs links, generated docs tests | `git diff --check`; `go test ./docs` | none | every commit |
| S1 Unit packages | Pure package logic and regressions | `go test ./pkg/access ./pkg/profile ./pkg/audit ./pkg/workspace ./pkg/mcp` | none | every commit |
| S2 Core engine | HNSW/WAL/collections/BM25/vectorstore crash and concurrency | `go test ./internal/store ./pkg/vectorstore ./pkg/bm25` | none | every commit; race nightly |
| S3 HTTP/MCP contract | Route inventory, MCP descriptors, auth/rate-limit, workspace tools | `go test ./internal/http` | none for focused; optional embedder for live paths | every PR |
| S4 Server bootstrap | Runtime wiring, profile warnings, storage/sql init | `go test ./cmd/server` | none | every PR |
| S5 Search relevance | Workspace eval, hybrid/vector/BM25 recall and MRR | targeted `internal/http` eval tests | local deterministic fixtures | every release |
| S6 Sync and backup | Mac/Pi sync, auth token, version skew, backup/restore | sync tests and backup CLI tests | optional second local server | release candidate |
| S7 Profile smoke | Personal, Solo Pro, Team, Enterprise config behavior | scripted env matrix | local server + SQLite/Postgres where needed | release candidate |
| S8 Security isolation | RBAC, tenant, API key perms, audit non-leakage, denied-path hygiene | focused HTTP/MCP tests + security review | none | every PR touching access |
| S9 Performance/load | Insert/search latency, rerank budget, watcher job lag | benchmark/loadtest scripts | local or Pi target | release candidate/nightly |
| S10 Pi/edge validation | ARM64, SQLite, local embedder, Mac/Pi sync | Pi runbook scripts | Raspberry Pi + embedder | release candidate |

## Product Profile Scenarios

### P1 Personal / Local

Configuration:

```bash
export LEVARA_PROFILE=personal
export DB_PROVIDER=sqlite
export LEVARA_WORKSPACE_WATCH=1
export LEVARA_WORKSPACE_INDEX_WORKER=1
```

Scenarios:

- Start server without `JWT_SECRET` and without `-require-auth`; expect no
  profile warnings that block startup.
- Use MCP `workspace_context -> workspace_write -> workspace_search ->
  workspace_read -> workspace_commit` on a local Markdown project.
- Save memory with `room` and `hall`, then verify `wake_up` includes pinned
  memories and `recall_memory` respects room/hall filters.
- Run lexical-only workspace search with no embedder configured; expect useful
  `CHUNKS_LEXICAL` results and no dense-search panic.

Pass criteria:

- Server starts with SQLite and local filesystem only.
- No auth is required for local workflows.
- Workspace search always returns exact-read guidance and citations.

### P2 Solo Pro

Configuration:

```bash
export LEVARA_PROFILE=solo_pro
export DB_PROVIDER=sqlite
export LEVARA_TOKEN=<stable-token>
export STORAGE_BACKEND=local
```

Scenarios:

- Enable sync remote URL without `LEVARA_TOKEN`; expect warning
  `solo_pro_sync_without_token`.
- Run backup/export/import on a local data dir and verify datasets, memories,
  graph rows, and workspace manifests survive restore.
- Configure S3-compatible storage with mock S3 tests; verify raw upload mirror,
  direct raw URL/presign behavior, and idempotent delete.
- Run Mac-to-Pi style pull/push against two local servers with bearer auth and
  version-skew warning coverage.

Pass criteria:

- Sync never runs silently unauthenticated when configured.
- Restore produces query-equivalent search and memory results.

### P3 Team

Configuration:

```bash
export LEVARA_PROFILE=team
export DB_PROVIDER=postgres
export JWT_SECRET=<stable-secret>
levara-server -require-auth
```

Scenarios:

- Start with `LEVARA_PROFILE=team` and SQLite/no auth; expect warnings for
  `team_requires_postgres`, `team_requires_auth`, and
  `team_requires_stable_jwt_secret`.
- Seed owner/editor/viewer/foreign users and verify:
  - owner/admin can read/write/share/revert/GC;
  - editor can read/write but not grant admin-only operations where applicable;
  - viewer can read/search but cannot write/reindex/revert/GC;
  - foreign user receives generic denial with no path, snippet, or collection
    leakage.
- Verify `GetAllowedDatasetIDs`, `CheckDatasetAccess`, workspace access check,
  and MCP workspace tools all make decisions through the same policy behavior.
- Verify per-agent API key permissions: `read` can search/read; `write` can
  write; insufficient permissions deny before dataset role broadens access.
- Verify workspace audit records success and denial while omitting markdown
  text, search queries, snippets, and exact file paths.

Pass criteria:

- No shared admin token is needed for agent workflows.
- All denied responses are non-leaky and consistent across REST and MCP.

### P4 Enterprise

Configuration proposal:

```bash
export LEVARA_PROFILE=enterprise
export DB_PROVIDER=postgres
export JWT_SECRET=<stable-secret-or-sso-bridge>
export LEVARA_TENANT_ENFORCED=1
export LEVARA_WORKSPACE_AUDIT_EXPORT=1
levara-server -require-auth
```

Scenarios:

- Start without tenant enforcement or audit sink; expect warnings
  `enterprise_requires_tenant_enforcement` and
  `enterprise_requires_audit_sink`.
- Send `X-Tenant-Id` for a tenant the user does not belong to; expect HTTP 403
  and no downstream `tenant_id` local.
- Verify `TenantFilterSQL` never interpolates tenant IDs into SQL clauses and
  returns bind args for Postgres and SQLite dialects.
- Mirror workspace audit events into a generic audit sink and verify sanitized
  event shape: source, type, subject, actor, outcome, branch/access/status
  metadata, no document content.
- Future adapter contract tests:
  - OIDC/SAML maps external subject to Levara principal.
  - SCIM creates/deactivates users and tenant memberships.
  - KMS/BYOK envelope hooks are invoked for protected secrets/object metadata.
  - SIEM sink receives audit events and handles retry/backpressure.

Pass criteria:

- Tenant isolation cannot be selected by header spoofing.
- Enterprise warnings identify unsafe config before operators rely on it.
- External adapters remain outside core search/indexing code paths.

## Layer Scenarios

### L1 Core Engine

- HNSW insert/search/delete/reinsert under concurrency and `-race`.
- WAL replay after crash, including batch insert and collection-scoped writes.
- Collection replacement by deterministic ID tombstones stale HNSW entries.
- BM25 add/remove/search stays synchronized with workspace reindex/delete.
- Vectorstore adapter contract: insert, batch upsert, get, scan, delete-many,
  drop collection, checkpoint, metadata.

### L2 Cognify and Graph

- RAG mode indexes chunks without LLM extraction.
- Full mode extracts entities/edges, dedups semantically, and writes temporal
  graph metadata.
- Neo4j available path and SQL fallback path return equivalent graph traversal
  results for fixture graphs.
- Temporal query with `as_of` returns only currently valid edges.
- Graph sync preserves `valid_from`, `valid_until`, `superseded_by`, and
  confidence.

### L3 Search and Rerank

- `AUTO` router selects expected strategy for keyword, graph, code, temporal,
  and fallback queries.
- `CHUNKS`, `CHUNKS_LEXICAL`, and `HYBRID` meet workspace eval thresholds for
  Recall@1, Recall@5, MRR, and exact path hit rate.
- Rerank default-on only when endpoint exists; `rerank=false` opts out.
- Rerank budget and score-gap threshold return unreranked results rather than
  timing out the request.
- Graph-aware rerank boosts connected evidence without hiding lexical exact
  matches.

### L4 Memory Palace

- `set_context` scopes memory collection.
- `save_memory` validates hall vocabulary and requires room/hall where agent
  instructions require it.
- SQL row is source of truth; vector sidecar insert is verified and divergence
  emits heartbeat.
- `recall_memory` uses vector ranking plus SQL-authoritative hydration for
  room/hall filters.
- Pin/unpin priority controls `wake_up` ordering.
- `consolidate` dry-run previews merge/abstract candidates and revert restores
  superseded raw memories.

### L5 Workspace Plane

- `workspace_write` blocks path traversal and supports optimistic locking via
  `expected_file_digest`.
- `workspace_reconcile` publishes a fresh active generation and marks old
  generations `gc_pending`.
- `workspace_search` resolves active generation/collection and fails clearly
  when missing or ambiguous.
- `workspace_read` returns exact file text plus chunk/file citations.
- Watcher detects markdown changes, enqueues durable jobs, and does not
  activate generation until worker success in async mode.
- `workspace_gc --dry-run` reports exclusive/shared collections and exact IDs
  before mutation.

### L6 Transports

- REST route inventory matches registered handlers.
- MCP initialize, notification handling, ping, session delete, auth-required
  and API-key-required paths behave consistently.
- gRPC v1 and v2 start on the same listener, share JWT verification, and expose
  expected service metadata.
- CLI workspace commands are parity-tested against REST/MCP where applicable.

### L7 Observability and Ops

- Prometheus exposes workspace job, watcher, audit, rate-limit, rerank, and WAL
  counters/gauges with bounded user cardinality.
- `workspace_ops_status` aggregates watcher, job, dead-letter, lag, and audit
  volume.
- `doctor`, `runtime_stats`, and `recent_errors` do not leak secrets.
- Profile warnings are structured logs and do not fail startup until explicit
  fail-fast mode is introduced.

## Release Gates

| Gate | Required checks | Blocks release when |
|---|---|---|
| Commit gate | `git diff --check`; targeted package tests for touched code | formatting or focused tests fail |
| PR gate | `go test ./pkg/access ./pkg/profile ./pkg/audit ./cmd/server ./docs`; focused `internal/http` access/workspace tests | access, profile, docs, or bootstrap regress |
| Nightly gate | `go test ./...`; selected `-race` packages; deterministic workspace eval | any package fails or relevance metrics regress |
| Release candidate | profile smoke matrix, sync/backup restore, S3 mock, rerank budget, workspace worker/dead-letter, Pi smoke | unsafe profile warnings missing, sync/restore mismatch, Pi failure |
| Enterprise readiness | tenant spoofing, audit export, adapter contract tests, security diff scan | any denied path leaks private content or tenant isolation is bypassed |

## Automation Backlog

1. Add a `make test-commit` target for S0-S4 focused checks.
2. Add a `make test-release-candidate` target for profile, sync, backup, and
   workspace eval gates.
3. Add profile smoke scripts under `examples/ops/` or `scripts/test-profiles/`.
4. Add a local two-server sync harness that does not require Pi hardware.
5. Add audit sink contract tests once the first external sink is implemented.
6. Add security scan workflow for changes touching `pkg/access`,
   `internal/http/auth.go`, `internal/http/tenants.go`, workspace audit, or
   API key handling.


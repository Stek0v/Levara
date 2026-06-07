# Markdown Workspace Next Tasks

Date: 2026-05-11

This backlog extends the capability gate in
`docs/markdown-workspace-capability-gate.md`. The goal is to move the
markdown-native workspace from a working implementation to a production-grade
agent knowledge plane for Codex, Claude, and team workflows.

## T1. Agent-facing `workspace_search`

Status: implemented in the current workspace patch.

Problem: agents can already call generic `search`, but they must know the
current workspace collection. That is fragile because active collections are
derived from `project_id + branch + generation`.

Scope:

- Add MCP tool `workspace_search`.
- Inputs: `project_id`, `branch`, optional `generation`, optional
  `collection`, `search_query`, `search_type`, `top_k`, `room`, `tags`, and
  normal search tuning flags.
- Resolve `generation` from the workspace manifest when omitted.
- Resolve `collection` from active generation chunks when omitted.
- Return the same ranked hits as `search`, but add workspace context:
  `project_id`, `branch`, `generation`, `collection`, `active_generation`,
  freshness status, and exact-read guidance.
- Enrich each hit with parsed metadata fields such as `path`, `heading_path`,
  `text`, `project_id`, `branch`, and `generation` when present.

Definition of Done:

- Agents can search a project by `project_id + branch` without knowing the
  collection name.
- If no active generation exists, the tool fails with a clear remediation.
- If the requested generation is not active, the response marks it stale.
- If the active generation has multiple collections, callers must pass an
  explicit collection instead of getting a silent arbitrary choice.
- The response always includes freshness metadata and tells the agent that
  exact `workspace_read` is required before using a hit as source of truth.
- Existing generic `search` behavior is unchanged.

Tests:

- Missing `search_query` returns an MCP error.
- Missing active generation returns an MCP error with reconcile guidance.
- Active-generation lexical search works without embedding.
- Omitted collection resolves to the collection in the active manifest.
- Explicit collection overrides manifest collection.
- Requested stale generation sets `freshness.stale=true`.
- Multiple collections in one generation require explicit collection.
- Results are enriched with `path`, `heading_path`, and `text` parsed from
  metadata.
- Workspace ACL denial does not leak file paths, collection names, or snippets.

Corner cases:

- Active generation exists but has zero chunks.
- Legacy chunks have empty `collection`.
- Legacy metadata lacks `dataset_id` but has `project_id`.
- Metadata is malformed JSON.
- `top_k=0` falls back to search defaults.

## T2. Retrieval Quality Evaluation Fixtures

Status: implemented in the current workspace patch.

Problem: the workspace now supports dense, lexical, and hybrid search, but
there is no regression suite that measures retrieval quality on realistic
Markdown corpora.

Scope:

- Add `testdata/workspace-eval/` with Markdown corpus files.
- Add query fixtures with expected `path`, `heading_path`, and optional
  expected terms.
- Add Go tests or a small CLI evaluation command that runs lexical, dense, and
  hybrid retrieval.
- Record metrics: `Recall@1`, `Recall@5`, `MRR`, and exact path hit rate.

Definition of Done:

- Evaluation runs deterministically in CI without external services.
- The suite can compare lexical, dense, and hybrid retrieval on the same
  corpus.
- A failing metric points to the query and expected source that regressed.
- Hybrid quality has a minimum threshold that protects the workspace default.

Tests:

- Exact keyword query finds the expected Markdown path.
- Natural-language paraphrase query finds the expected Markdown path.
- Ambiguous query returns the correct project-scoped result, not a similarly
  named foreign project.
- Heading-specific query ranks the correct heading above sibling headings.
- Empty corpus and no-hit query return clean zero metrics, not panics.

Corner cases:

- Duplicate phrases across two files.
- Same filename in two branches.
- Very short headings.
- Long documents split into multiple chunks.
- Non-ASCII Markdown text.

## T3. Freshness Guard and Index Status Contract

Status: implemented in the current workspace patch.

Problem: agents need to know whether search reflects current Markdown. Watcher
status exists, but search responses do not yet provide enough project-specific
freshness context.

Scope:

- Standardize freshness fields on workspace search/status responses.
- Add manifest-derived `last_indexed_at`, `active_generation`,
  `active_chunk_count`, `active_path_count`, and `manifest_path`.
- Include watcher-derived `enabled`, `pending_branches`, `last_reconcile_at`,
  and `last_error`.
- Document agent behavior: if stale or potentially stale, reconcile before
  answering.

Definition of Done:

- Any agent search response can be audited for freshness without a second tool
  call.
- Stale generation and watcher lag are visible and machine-readable.
- The response distinguishes definitely stale from potentially stale.

Tests:

- Active generation returns `stale=false`.
- Explicit old generation returns `stale=true`.
- Watcher pending/error sets `potentially_stale=true`.
- Missing watcher returns valid zero-value watcher fields.
- Invalid timestamps in old manifest records do not panic.

Corner cases:

- Manifest exists with no chunks.
- Manifest active generation points to a generation with no records.
- Watcher state loaded from disk but not currently enabled.

## T4. Persistent Indexing Job Outbox

Status: implemented in the current workspace patch. Reindex/reconcile jobs are
persisted with attempts/status/error, can be listed/enqueued/retried through
REST/MCP, and can be processed by an optional async worker with backoff and
dead-letter handling.

Problem: watcher/reconcile are functional, but production retries need durable
job state. A process crash during embedding/upsert should be observable and
retryable.

Scope:

- Add an indexing job table/file under `.kb/jobs/`.
- Jobs include `project_id`, `branch`, `generation`, `paths`, `operation`,
  `idempotency_key`, `attempts`, `status`, `next_run_at`, `dead_letter_at`,
  `last_error`.
- Synchronous reindex/reconcile still execute immediately and record durable
  attempts.
- `workspace_enqueue_index_job` queues async `reindex`/`reconcile` payloads.
- `LEVARA_WORKSPACE_INDEX_WORKER=1` drains pending/failed jobs.
- `LEVARA_WORKSPACE_WATCH_ASYNC_INDEX=1` lets watcher enqueue instead of
  indexing inside the polling loop.
- Provide status, enqueue, and retry tools.

Definition of Done:

- Crashed or failed indexing work can be resumed.
- Duplicate save bursts coalesce through idempotency keys.
- Failed embedder/vector upsert leaves a visible job error.
- Manual reconcile still works as an authoritative path.
- Exhausted retries move to `dead_letter` instead of retrying forever.
- Async watcher mode never silently activates a generation before the worker
  completes the saved job.

Tests:

- Enqueue is idempotent for the same project/branch/path digest.
- Failed job is marked failed and can be retried.
- Successful job activates generation exactly once.
- Process restart reloads pending jobs.
- Concurrent jobs for different projects do not block each other.
- Worker schedules `next_run_at` after the first failure.
- Worker moves the job to `dead_letter` after `MaxAttempts`.
- Watcher async mode creates a pending reconcile job and leaves the manifest
  unchanged until worker execution.
- MCP and REST enqueue surfaces return the same saved job shape.

Corner cases:

- Embedder returns fewer vectors than chunks.
- Vector upsert partially fails.
- File deleted before job runs.
- Revert occurs while old job is pending.

## T5. Workspace ACL Preflight and Audit Log

Status: implemented in the current workspace patch.

Problem: ACL enforcement exists, but team operations need explicit preflight
and auditability.

Scope:

- Add `workspace_access_check` MCP/REST helper.
- Add audit events for workspace REST and MCP operations, including
  read/write/search/reindex/revert/gc and job lifecycle tools.
- Include user/bot id, project id, operation, result, and timestamp.
- Avoid storing document content, snippets, search queries, or exact file paths
  in audit logs.

Definition of Done:

- Agents can check access before attempting a workflow.
- Security reviews can answer who read/searched/wrote/reverted a project.
- Denied operations do not leak path or content.
- Audit logs are durable under `.kb/audit/<project>/audit-YYYY-MM.jsonl` and
  can be read through REST/MCP by users with project read access.

Tests:

- Owner/editor/viewer/admin permissions match the role table.
- Viewer can search/read but cannot write/reindex/revert/gc.
- Foreign user gets a generic denied result.
- Audit log records success and denial.
- Audit log omits Markdown text and snippets.
- REST and MCP access-check/audit-log surfaces are covered by descriptor and
  dispatch tests.

Corner cases:

- API key permissions conflict with dataset role.
- Superuser bypass.
- Anonymous dev mode with no DB.
- Search query and write text present in the original request.

## T6. Agent Context Bootstrap

Status: implemented in the current workspace patch.

Problem: agents need a cheap session-start call that tells them how to use the
workspace without rediscovering project state.

Scope:

- Add `workspace_context` MCP tool and REST context endpoint.
- Return accessible projects, active branch/generation, active collection,
  watcher status, job status summary, recommended search type, and exact-read
  rule.

Definition of Done:

- Codex/Claude can start with one call and get the right collection and rules.
- Context respects ACL and does not list foreign projects.
- Empty workspace returns a clear initialization path.
- Corrupt manifests are reported on the affected branch without failing the
  whole context response.

Tests:

- User sees owned and shared projects only.
- Project with active generation returns active collection.
- Project without manifest returns initialization guidance.
- No DB/dev mode returns workspace-local projects.
- MCP dispatch and descriptor coverage include `workspace_context`.

Corner cases:

- Multiple branches per project.
- Corrupt manifest is reported per project without breaking the full response.

## T7. Production Deployment Recipes

Status: implemented in the current workspace patch.

Problem: architecture docs describe concepts, but operators need copyable
recipes for concrete environments.

Scope:

- Add docs for single-node dev, team server, and Mac/Pi sync.
- Include env vars, auth setup, watcher setup, backup/restore, GC, and
  troubleshooting.
- Include Claude/Codex/Cursor MCP config examples.
- Include reusable host instructions that enforce
  `workspace_context -> workspace_search -> workspace_read`.

Definition of Done:

- A new user can run a local workspace from the recipe.
- A team can deploy with auth and project shares enabled.
- Backup/restore and sync limitations are explicit.
- Claude/Codex/Cursor config examples are copyable from
  `examples/agent-hosts/`.
- `cmd/agent-hosts` can safely merge the examples into existing host config
  files with timestamped backups and `-dry-run`.

Tests:

- Commands in docs are syntax-checked where possible.
- Config examples are valid JSON/TOML.
- Links point to existing files.
- Agent instruction template names `workspace_context`, `workspace_search`,
  `workspace_read`, `workspace_write`, and `workspace_commit`.
- Installer merge logic preserves unrelated JSON/TOML config and replaces only
  the `levara` MCP stanza.

Corner cases:

- No embedder configured.
- Watcher disabled.
- Remote sync excludes vector collections by default.

## T8. Operational Status and Metrics

Status: implemented in the current workspace patch.

Problem: async indexing, watcher freshness, and audit logs are durable, but
operators and agents need one cheap health surface plus stable Prometheus
series for dashboards and alerting.

Scope:

- Add REST `GET /workspace/ops/status`.
- Add MCP `workspace_ops_status`.
- Aggregate watcher pending branches/errors.
- Aggregate durable jobs by status, dead-letter count, max pending/failed lag,
  oldest pending time, newest update time, and oldest dead-letter time.
- Aggregate sanitized audit volume by source/result without loading Markdown
  content, search queries, snippets, or exact file paths.
- Refresh Prometheus workspace gauges from job transitions and ops/status.
- Increment audit event counters when sanitized audit rows are appended.

Definition of Done:

- A human or agent can identify stale watcher state, indexing backlog,
  dead-letter failures, and audit volume from one call.
- Prometheus exposes stable zero-initialized series for workspace job status
  and audit labels.
- Ops status can be scoped by `project_id` and `branch`, while metrics stay
  global to the running instance.
- Malformed audit rows do not break the operational status endpoint.
- Existing audit privacy guarantees remain intact.

Tests:

- REST ops status reports watcher pending branches, one dead-letter job, audit
  totals, and expected timestamps.
- Prometheus gauges update for dead-letter jobs, watcher pending branches, and
  stored audit events.
- Audit counters increment for REST workspace writes.
- Malformed JSONL audit rows are skipped, not fatal.
- MCP dispatch and descriptor/schema tests include `workspace_ops_status`.

Corner cases:

- Empty workspace returns zero counts.
- Project/branch filters exclude unrelated jobs and audit rows.
- Job has invalid timestamps.
- Audit directory is missing.
- Watcher state is nil.

## T9. Generation Compaction and GC Hardening

Status: implemented in the current workspace patch.

Problem: stale generations are cleaned, but long-running deployments need
explicit compaction and accounting for vector tombstones/BM25 state.

Scope:

- Add GC dry-run.
- Report records to delete, collections to drop, BM25 entries to remove.
- Add compaction guidance for repeated upserts/tombstones.
- Add copyable Prometheus alert and Grafana dashboard examples.

Definition of Done:

- Operators can preview GC impact.
- GC output explains exactly what was removed.
- Active generation is never removed.
- Dashboard/alert templates exist for workspace lag, dead letters, watcher
  pending branches, and audit volume.

Tests:

- Dry-run changes nothing.
- Shared collection deletes exact vector IDs only.
- Exclusive stale collection is dropped.
- Active collection survives.
- BM25 entries mirror vector deletion.
- Dashboard JSON is syntactically valid and names required metrics.

Corner cases:

- Missing BM25 index.
- Manifest references missing collection.
- Stale generation already partially deleted.

## T10. Context Artifact Registry

Status: implemented in the current workspace patch.

Problem: project knowledge often includes OpenAPI, DDL, Terraform, and ADR
files. Workspace needs a declarative registry that says what to index and how.

Scope:

- Add `.kb/context-artifacts.json` schema.
- Support explicit artifacts, include globs, tags, room, and artifact type.
- Reindex registry artifacts into workspace index through REST/MCP.

Definition of Done:

- Teams can declare non-note artifacts without custom scripts.
- Registry artifacts can be listed and selectively reindexed.
- Metadata identifies artifact type and source path.

Tests:

- Include globs work, including recursive `**`.
- Artifact tags propagate to metadata.
- Registry syntax errors are clear.
- Artifact reindex makes configured OpenAPI/DDL content searchable.

Corner cases:

- Artifact outside workspace root.
- Duplicate registry entries.
- Binary or huge files.

## T11. Answer-with-Citations Contract

Status: implemented in the current workspace patch.

Problem: search hits are not enough for team decisions. Agent answers should
cite exact Markdown sources and digests.

Scope:

- Add required citation payload to workspace search responses.
- Include `path`, `heading_path`, `file_digest`, `chunk_id`, `vector_id`.
- Document that agents must cite exact reads for architectural answers.
- Add citation metadata to `workspace_read` responses.

Definition of Done:

- Agent-facing responses can be traced back to exact workspace files.
- Citations survive branch/generation changes through digest/vector IDs.
- Search responses include an explicit `answer_contract`.

Tests:

- Citation fields come from manifest/metadata.
- Malformed metadata does not remove basic hit.
- Exact read can verify cited path content.
- `citation.read_args` can be passed directly to `workspace_read`.

Corner cases:

- Multiple hits from the same file.
- File changed after search but before read.

## T12. Team Conflict Model

Status: implemented in the current workspace patch.

Problem: team writes can overwrite a file that changed after an agent read it.

Scope:

- Add optional `expected_file_digest` to `workspace_write`.
- Reject writes when current digest differs.
- Return current digest and remediation.
- Add `workspace_conflicts` drift detector for dirty, unindexed, and deleted
  indexed paths.

Definition of Done:

- Agents can perform optimistic locking.
- Existing write flows remain backward compatible when no expected digest is
  supplied.
- Teams can inspect filesystem-vs-active-generation drift before answering or
  writing.

Tests:

- Matching digest writes successfully.
- Mismatched digest rejects without modifying file.
- Missing file with expected empty digest behaves predictably.
- Reindex is not triggered after rejected write.
- Drift detection reports changed, new, and deleted Markdown paths.
- MCP and REST expose the same conflict model.

Corner cases:

- Concurrent writes.
- Path traversal attempts still fail first.

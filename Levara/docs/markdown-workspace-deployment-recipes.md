# Markdown Workspace Deployment Recipes

Date: 2026-05-11

This document turns the markdown-native workspace architecture into copyable
operator recipes for solo development, a shared team server, and Mac/Pi sync.

## 1. Single-Node Development

Use this for one developer on one machine.

```bash
export DB_PROVIDER=sqlite
export DB_PATH="$PWD/data/levara.db"
export LEVARA_WORKSPACE_WATCH=1
export LEVARA_WORKSPACE_INDEX_WORKER=1
export LEVARA_WORKSPACE_WATCH_ASYNC_INDEX=1
export LEVARA_WORKSPACE_WATCH_CHUNK_STRATEGY=merged

levara-server \
  -standalone \
  -dim 1536 \
  -data-dir "$PWD/data" \
  -port 8080
```

Recommended workflow:

1. Write Markdown with `workspace_write`.
2. Let watcher enqueue reconcile jobs.
3. Let the worker drain `.kb/jobs`.
4. Ask agents to start with `workspace_context`.
5. Use `workspace_search -> workspace_read` for answers.

If no embedder is configured, use `CHUNKS_LEXICAL` or configure
`EMBEDDING_ENDPOINT` / `EMBEDDING_MODEL` before dense or hybrid search.

## 2. Team Server

Use this when multiple users/agents share projects with RBAC.

```bash
export ENV=prod
export DB_HOST=postgres.internal
export DB_PORT=5432
export DB_NAME=levara_db
export DB_USERNAME=levara
export DB_PASSWORD="<secret>"
export JWT_SECRET="<long-random-secret>"
export LEVARA_WORKSPACE_WATCH=1
export LEVARA_WORKSPACE_INDEX_WORKER=1
export LEVARA_WORKSPACE_WATCH_ASYNC_INDEX=1
export LEVARA_WORKSPACE_INDEX_WORKER_MAX_ATTEMPTS=3
export LEVARA_WORKSPACE_INDEX_WORKER_BACKOFF=5s
export EMBEDDING_ENDPOINT="http://embedder.internal/v1"
export EMBEDDING_MODEL="text-embedding-3-small"

levara-server \
  -standalone \
  -dim 1536 \
  -data-dir /var/lib/levara \
  -port 8080 \
  -require-auth
```

Team setup checklist:

- Create a dataset/project row for each `project_id`.
- Grant `viewer`, `editor`, or `admin` shares.
- Give agents user-scoped JWT/API keys, not a shared admin token.
- Require agents to run `workspace_access_check` before write/revert/gc.
- Use `workspace_ops_status` for one-call operational health.
- Use `workspace_conflicts` before team writes or when freshness is uncertain.
- Monitor `.kb/jobs` through `workspace_index_jobs`.
- Review `.kb/audit/<project>/audit-YYYY-MM.jsonl` through
  `workspace_audit_log`.

Operational health checks:

```bash
# REST
curl -H "Authorization: Bearer $LEVARA_TOKEN" \
  "http://localhost:8080/workspace/ops/status?project_id=payments&branch=main"

# MCP
workspace_ops_status(project_id="payments", branch="main")
```

Prometheus `/metrics` includes:

- `levara_workspace_index_jobs{status}`
- `levara_workspace_index_job_max_lag_seconds`
- `levara_workspace_index_dead_letters`
- `levara_workspace_watcher_pending_branches`
- `levara_workspace_watcher_errors`
- `levara_workspace_audit_events_total{source,operation,result}`
- `levara_workspace_audit_stored_events`

Copyable operator files:

- `examples/ops/prometheus-alerts.yml`
- `examples/ops/grafana-workspace-dashboard.json`

Context artifacts:

```json
{
  "version": 1,
  "includes": [
    {
      "project_id": "payments",
      "branch": "main",
      "glob": "artifacts/api/**/*.yaml",
      "kind": "openapi",
      "room": "api",
      "tags": ["payments"]
    },
    {
      "project_id": "payments",
      "branch": "main",
      "glob": "artifacts/db/**/*.sql",
      "kind": "ddl",
      "room": "db",
      "tags": ["schema"]
    }
  ]
}
```

Save that as `data/workspace/.kb/context-artifacts.json`, then call
`workspace_context_artifacts` to inspect it and `workspace_reindex_artifacts`
with a fresh generation to publish artifacts into search.

## 3. Mac ↔ Pi Sync

Use this when a Mac is the primary coding machine and a Raspberry Pi keeps a
durable Levara copy.

```bash
# Pull memory/graph/chat state from Pi to Mac.
sync_levara

# Manual MCP equivalent:
sync \
  --remote-url "http://10.23.0.53:8080/api/v1" \
  --direction pull
```

Vector collections are excluded by default because they require re-embedding.
For markdown workspace projects, sync or copy the Markdown truth layer first,
then run:

```bash
levara workspace reconcile \
  --project payments \
  --branch main \
  --generation "sync-$(date -u +%Y%m%dT%H%M%SZ)" \
  --chunk-strategy merged \
  --activate
```

## 4. Agent Host Configs

Copyable examples live in `examples/agent-hosts/`:

- `examples/agent-hosts/claude-mcp.json`
- `examples/agent-hosts/cursor-mcp.json`
- `examples/agent-hosts/codex-config.toml`
- `examples/agent-hosts/workspace-agent-instructions.md`

Host instructions must say:

1. Start every workspace session with `workspace_context`.
2. Use `workspace_search` for retrieval.
3. Use `workspace_read` before answering from a hit.
4. Use `workspace_write` for durable generated notes.
5. Use `workspace_commit` after meaningful changes.

You can also merge Levara into an existing host config without overwriting
other MCP servers:

```bash
# Claude-style project config: .mcp.json
go run ./cmd/agent-hosts \
  -host claude \
  -target .mcp.json \
  -server-url http://localhost:8080/mcp

# Cursor project config: .cursor/mcp.json
go run ./cmd/agent-hosts \
  -host cursor \
  -target .cursor/mcp.json \
  -server-url http://localhost:8080/mcp

# Codex project config: .codex/config.toml
go run ./cmd/agent-hosts \
  -host codex \
  -target .codex/config.toml \
  -server-url http://localhost:8080/mcp
```

The installer preserves unrelated config, replaces only the `levara` MCP
server stanza, and writes a timestamped `.bak-YYYYMMDDTHHMMSSZ` file before
modifying an existing target. Use `-dry-run` to inspect the merged config.

## 5. Backup and Restore

Back up:

- `data/workspace/projects/` - Markdown truth.
- `data/workspace/.kb/manifests/` - active generations and chunk metadata.
- `data/workspace/.kb/jobs/` - durable indexing jobs.
- `data/workspace/.kb/audit/` - sanitized access/change audit.
- vector collection storage under the configured Levara data dir.
- SQL database if auth, shares, graph, or API keys are enabled.

Restore order:

1. Restore SQL database and data dir.
2. Start Levara with watcher disabled.
3. Run `workspace_context` to inspect manifests and branches.
4. Run `workspace_reconcile` for projects with missing/stale generations.
5. Enable watcher and index worker.

## 6. Troubleshooting

| Symptom | Likely Cause | Action |
|---|---|---|
| `workspace_context` has no projects | No local workspace and no accessible DB projects | Create/share a project or write initial Markdown. |
| `workspace_search` says active generation missing | Manifest is not initialized | Run `workspace_reconcile` with `activate_generation=true`. |
| Search is stale | Watcher has pending branch or failed reconcile | Inspect `workspace_context`, then `workspace_ops_status`. |
| Jobs stay `pending` | Worker not running | Set `LEVARA_WORKSPACE_INDEX_WORKER=1`. |
| Job reaches `dead_letter` | Repeated embed/read/upsert failure | Fix root cause, then retry or enqueue a fresh generation. |
| Agent write is rejected with digest conflict | File changed since the agent read it | Run `workspace_read`, review diff, then retry with the new `expected_file_digest`. |
| Active generation disagrees with files | Manual edit, deleted file, or watcher lag | Run `workspace_conflicts`, then reconcile a fresh generation. |
| Viewer cannot write | Expected RBAC behavior | Grant `editor` or `admin`, or use read-only workflow. |

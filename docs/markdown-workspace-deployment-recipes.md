# Markdown Workspace Deployment Recipes

Use [getting started](getting-started.md) for the server, [profile presets](profile-presets.md)
for product configuration, and [workspace concepts](markdown-native-workspace.md)
for truth files, generations and guarded writes.

## 1. Single-Node Development

From the repository root, with no other test instance using this data path:

```bash
DB_PROVIDER=sqlite DB_PATH="$PWD/data/levara.db" \
LEVARA_WORKSPACE_WATCH=1 LEVARA_WORKSPACE_INDEX_WORKER=1 \
LEVARA_WORKSPACE_WATCH_ASYNC_INDEX=1 \
LEVARA_WORKSPACE_WATCH_CHUNK_STRATEGY=merged \
./levara-server -profile=standalone -host=127.0.0.1 -port=8080 \
  -grpc-port=0 -dim=768 -data-dir="$PWD/data"
```

Write Markdown truth, inspect watcher/jobs, then search and exact-read the source.
Lexical workspace retrieval works without embeddings; dense search needs a
provider, matching dimension and `standalone-embed` or explicit embedding flags.

## 2. Team Server

Follow [Team setup](tutorials/04-team-deploy.md): PostgreSQL, required auth,
stable signing secret and one credential per user/agent. Add the workspace flags
above to the exported environment. Use dataset IDs for `project_id`; granting
an individual viewer/editor/admin role does not establish group or organization
permissions automatically.

```bash
curl -fsS -H "Authorization: Bearer $LEVARA_TOKEN" \
  'http://127.0.0.1:8080/api/v1/workspace/ops/status?project_id=DATASET_ID&branch=main'
```

Every workspace REST path is under `/api/v1/workspace`, not root `/workspace`.
Inspect `workspace_conflicts` before concurrent edits and use MCP/REST
`expected_file_digest` for optimistic writes. The CLI currently does not expose
that digest parameter. `workspace_audit_log` reports recorded access/change
entries; it is not proof that every external filesystem edit is fully audited.

Useful metrics include workspace index job status/lag/dead letters, watcher
pending branches/errors and recorded audit events. Copyable operator fixtures:

- [examples/ops/prometheus-alerts.yml](../examples/ops/prometheus-alerts.yml)
- [examples/ops/grafana-workspace-dashboard.json](../examples/ops/grafana-workspace-dashboard.json)

Inspect labels and thresholds against your deployed `/metrics`; fixture
thresholds are examples, not a service-level objective.

## 3. Context artifacts

A rules file at `<workspace-root>/.kb/context-artifacts.json` can include
structured project artifacts:

```json
{
  "version": 1,
  "includes": [
    {"project_id":"DATASET_ID","branch":"main","glob":"artifacts/api/**/*.yaml","kind":"openapi","room":"api","tags":["api"]},
    {"project_id":"DATASET_ID","branch":"main","glob":"artifacts/db/**/*.sql","kind":"ddl","room":"db","tags":["schema"]}
  ]
}
```

Inspect with `workspace_context_artifacts` and reindex through
`workspace_reindex_artifacts` using a fresh generation. Review source contents
before indexing; include rules are not a secret scanner.

## 4. Sync between machines

Sync is an MCP/REST operation, not a `levara sync` CLI command. Configure an
exact `LEVARA_SYNC_REMOTE_URL` and credentials for your remote deployment.
Authenticated sync requires an active global superuser. The configured server
token is forwarded only to the exact configured URL, without redirects.

Example MCP arguments, with your own remote API base:

```text
sync(remote_url="https://levara.example.com/api/v1", direction="pull",
     types=["memories", "interactions", "graph"])
```

Vectors are excluded by default; collection sync is explicit and requires a
compatible embedding contract. Do not assume this also synchronizes workspace
truth files. Transfer/restore those through your chosen backup/file workflow,
then reconcile on the destination:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara workspace reconcile --project=DATASET_ID --branch=main \
  --generation=after-sync-001 --chunk-strategy=merged --activate
```

## 5. Agent Host Configs

- [examples/agent-hosts/claude-mcp.json](../examples/agent-hosts/claude-mcp.json)
- [examples/agent-hosts/cursor-mcp.json](../examples/agent-hosts/cursor-mcp.json)
- [examples/agent-hosts/codex-config.toml](../examples/agent-hosts/codex-config.toml)
- [examples/agent-hosts/workspace-agent-instructions.md](../examples/agent-hosts/workspace-agent-instructions.md)

Merge the selected template into the installed host's actual config. For current
Codex use `bearer_token_env_var = "LEVARA_TOKEN"`; the repository installer still
produces an older Codex header layout. [Agent integration](tutorials/02-agent-integration.md)
explains this limitation and direct configuration.

Workspace flow: context → search → exact read → optional guarded write → commit.
Add [memory instructions](memory-workflow-skill.md) separately for durable
room/hall records. Never replace existing project instructions blindly.

## 6. Backup and Restore

Use the [complete backup inventory](deployment.md#backup-and-recovery), including
SQL, vectors, truth files, manifests, jobs, audit, original uploads and derived
artifacts. Restore into an isolated destination with watcher disabled first.
Inspect project access and exact reads, reconcile stale generations, and only
then enable watcher/index worker. See [user scenarios](markdown-workspace-user-scenarios.md)
and [testing](testing.md) for verification boundaries.

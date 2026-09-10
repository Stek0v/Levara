# Deployment Guide

Choose a deployment from the table, validate the exact startup configuration,
and establish a restore procedure before storing shared data. Start a first
local instance with [getting started](getting-started.md); product requirements
and copyable environment files are in [profile presets](profile-presets.md).

## Operating modes

| Need | Configuration | Additional services |
|---|---|---|
| Local durable memory | `DB_PROVIDER=sqlite`, loopback, `-profile=standalone` | None for lexical memory |
| Local semantic search | SQLite, `-profile=standalone-embed`, correct `-dim` | Embedding endpoint; LLM only for generation/extraction |
| Shared team | PostgreSQL, `-require-auth`, stable `JWT_SECRET`, Team preset | TLS proxy, optional model services, backup destination |
| Corporate pilot | Enterprise preset plus verified integration | Follow [identity](enterprise-identity.md) and [document access](document-management.md); the preset is not complete directory/group enforcement |
| Containers | [deploy/docker](../deploy/docker/docker-compose.yml) | Model endpoints supplied separately |
| Raspberry Pi | [Pi guide](../deploy/raspberry/README.md) | Models and capacity selected for the actual device |

`-standalone` controls WAL versus Raft. `-profile` controls functional bootstrap
suppression. `LEVARA_PROFILE` validates product requirements. These are different
settings. Do not infer PostgreSQL, models, authentication or clustering from a
profile name alone.

## Configuration that must agree

The server reads exported environment variables and flags, not `.env` files
implicitly. For an environment file you have edited for this deployment:

```bash
set -a
source ./levara.env
set +a
./levara-server -require-auth -config-check
```

Use the same functional profile, authentication and connection flags in the
check and the actual service. Team strict checks require `-require-auth` during
validation too. `-config-check` validates declared configuration without opening
listeners or connecting to a database/provider; it is not a connectivity test.

| Setting | Actual server behavior |
|---|---|
| `DB_PROVIDER=sqlite`, `DB_PATH` | Embedded SQL at the chosen file; otherwise SQL may be absent |
| `DATABASE_URL` / `POSTGRES_DSN` / `-pg-url` | PostgreSQL DSN; choose one explicit source |
| `-data-dir` / `LEVARA_DATA_DIR` | Vector state and default local workspace/upload roots |
| `EMBEDDING_ENDPOINT` | Full embeddings URL, including `/v1/embeddings` |
| `EMBEDDING_MODEL`, `-dim` | Must match the provider output and collection contract |
| `LLM_PROVIDER`, `LLM_ENDPOINT`, `LLM_MODEL`, `LLM_API_KEY` | Optional language-model provider; no `-llm-model` flag |
| `-host` / `LEVARA_HTTP_HOST` | HTTP bind address; default loopback |
| `-grpc-host` / `LEVARA_GRPC_HOST`, `-grpc-port` | Separate gRPC listener; port `0` disables it |
| `JWT_SECRET`, `LEVARA_API_KEY_PEPPER` | Stable signing/key-hashing secrets; protect and back them up |
| `STORAGE_BACKEND=s3` | Optional S3 storage; configure `S3_BUCKET`, `S3_REGION`, `S3_ENDPOINT` and AWS credentials |

Local upload storage defaults to `<data-dir>/uploads`; `STORAGE_PATH` is not a
server override for that path. Configuring a remote model or extractor sends
relevant content to that provider. See [integrations](integrations.md).

## Linux/systemd

Install a built server at `/opt/levara/levara-server`, create a dedicated
`levara` service account, and give it write access to `/var/lib/levara`.
Place deployment secrets in `/etc/levara/levara.env` readable by that account.
Example for a PostgreSQL-backed team behind a local TLS proxy:

```ini
[Unit]
Description=Levara
After=network-online.target
Wants=network-online.target

[Service]
User=levara
Group=levara
WorkingDirectory=/var/lib/levara
EnvironmentFile=/etc/levara/levara.env
ExecStart=/opt/levara/levara-server -profile=full -require-auth -host=127.0.0.1 -port=8080 -grpc-port=0 -dim=768 -data-dir=/var/lib/levara
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/levara
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Fill the environment from the Team preset and configure the actual 768d model,
or change `-dim` before creating collections. Validate the exported environment
with the same flags before enabling the unit. Use `systemctl status levara` and
`journalctl -u levara` to inspect your installed service. Updating the binary
requires an intentional restart of this unit, not killing an arbitrary process
selected by a substring.

## macOS launchd

Use an absolute binary path, working directory, data directory and log paths in
your own LaunchAgent. Shell startup files and `.env` are not loaded by launchd.
Set needed variables in its `EnvironmentVariables` dictionary or use an explicit
wrapper that exports an environment file before `exec`-ing the server.

A minimal local `ProgramArguments` template is:

```xml
<key>ProgramArguments</key>
<array>
  <string>/absolute/path/levara-server</string>
  <string>-profile=standalone</string>
  <string>-host=127.0.0.1</string>
  <string>-port=8080</string>
  <string>-grpc-port=0</string>
  <string>-dim=768</string>
  <string>-data-dir=/absolute/path/levara-data</string>
</array>
<key>EnvironmentVariables</key>
<dict>
  <key>DB_PROVIDER</key><string>sqlite</string>
  <key>DB_PATH</key><string>/absolute/path/levara-data/levara.db</string>
</dict>
```

This is a fragment for a plist, not a complete install script. Supply `Label`,
`WorkingDirectory`, log destinations and your chosen restart policy. Inspect
that exact label with `launchctl print gui/$(id -u)/YOUR_LABEL` after installing.
The [watchdog](macos-levara-watchdog.md) is a separate opt-in notification helper.
Agent client configuration belongs in [agent integration](tutorials/02-agent-integration.md).

## Docker

The checked-in [Compose recipe](../deploy/docker/docker-compose.yml) includes
SQLite configuration; it does not launch an embedding or LLM service. Its
default port publication is broader than loopback, so review its bind/auth
settings before starting it. For the portable first run, use this loopback-only
container command:

```bash
docker build -t levara:local -f deploy/docker/Dockerfile .
docker run --rm --name levara-local \
  -p 127.0.0.1:8080:8080 -v levara-data:/app/data \
  -e DB_PROVIDER=sqlite -e DB_PATH=/app/data/levara.db \
  levara:local ./levara-server -profile=standalone -host=0.0.0.0 \
  -port=8080 -grpc-port=0 -dim=768 -data-dir=/app/data
```

The image uses `CMD`, not `ENTRYPOINT`: a command override must include
`./levara-server`. In containers `127.0.0.1` points at the container itself;
use a reachable provider address. `host.docker.internal` depends on the Docker
host setup and is not a universal Linux hostname.

## Authentication and network exposure

Keep the backend on loopback behind a TLS proxy where possible. Preserve MCP
streaming and authentication headers through the proxy; configure WebUI
separately using [its README](../webui/README.md). Health/version availability
is not proof that protected routes accept the intended user.

Use separate credentials for each person or agent. Authenticated raw-storage
gRPC requires an active global superuser; ordinary document workflows use
REST/MCP. A collection name or MCP toolset is not an ACL. Dataset sharing is
individual in the public UI. Document/group policy is available through
REST/CLI/WebUI, and native LDAP/LDAPS/StartTLS is implemented locally; real
directory acceptance remains open. See [enterprise identity](enterprise-identity.md).

## Monitoring and scheduled maintenance

Inspect `/health`, `/health/details`, `/version` and `/metrics` on your configured
origin. Use authenticated MCP `doctor`, `runtime_stats`, `recent_errors` and
`check_drift` where exposed. A provider health response does not demonstrate
retrieval quality; run representative queries and inspect source evidence.

[Workspace operations](markdown-workspace-deployment-recipes.md) covers watchers,
index jobs and audit files. [Cron profiles](cron-profiles.md) separates diagnostic
schedules from destructive maintenance. [Search strategies](search-strategies-guide.md)
explains rerank outcome and fan-out metrics.

## Backup and recovery

Back up one consistent recovery point across these stores:

- PostgreSQL with its backup tools, or a SQLite online backup / quiesced database
  including any WAL state; copying only an actively written `.db` is insufficient.
- The complete configured vector data directory, snapshots and WAL files.
- Markdown workspace truth, `.kb` manifests, job state, audit and artifact files,
  including an external workspace root if configured.
- Original uploads and derived text, extraction metadata/structured artifacts;
  if using S3, include the bucket/object backup policy rather than only local files.
- Configuration and secrets, model/embedding contract identifiers, and a binary
  version that can read the backup. The primary LLM cache is
  `<data-dir>/llm_cache.jsonl`; an optional LLM proxy also uses the node directory
  `<data-dir>/<node-id>/`.

Quiesce writes or use storage-consistent snapshots; stop the actual service
manager during a cold backup so it cannot immediately restart the process.
Restore into an isolated destination first. Restore SQL and file/object state,
start with workspace watchers disabled, inspect health and dataset counts, then
reconcile stale workspace generations and verify original downloads and search.
Vector indexes are derivatives, but reconstruction requires preserved source
text and compatible models. A successful WAL replay is not a full backup test.

## Embedding migration: shadow, evaluate, cut over, roll back

Use the migration API for one collection at a time. Inventory collection names,
record counts, source text availability, model/dimension contracts and a fixed
query set first. Keep old model access and a restorable backup. The following
JSON is a request template for `POST /api/v1/embedding-migrations`:

```json
{
  "source_collection": "manuals",
  "target_collection": "manuals_shadow",
  "target_endpoint": "http://127.0.0.1:11434/v1/embeddings",
  "target_model": "YOUR_TARGET_MODEL",
  "target_dim": 768,
  "batch_size": 32,
  "max_attempts": 3,
  "dry_run": true,
  "enable_dual_write": false
}
```

1. Validate with `dry_run:true`; this validates the request and records a run,
   not vector-quality parity. Change target endpoint/model/dimension to actual
   provider values, then submit `dry_run:false` to build a separate collection.
2. Poll `GET /api/v1/embedding-migrations/{run_id}/status`. Require `COMPLETED`,
   inspect `processed`, `failed` and `failed_ids`. `POST .../{run_id}/retry`
   retries failed records within the attempt cap.
3. Compare source and shadow using `POST /api/v1/embedding-migrations/shadow-read`.
   Supply `source_collection`, `shadow_collection`, `queries`, `top_k`, both
   endpoints/models and workload-specific gate thresholds. Review overlap,
   top-1 stability, empty-result rate, latency and the `cutover_ready` report;
   supplement overlap with labeled relevance and permission checks.
4. Quiesce writes and coordinate readers for cutover. Optional dual-write hooks
   are not a substitute for checking consistency and failures. Submit
   `POST .../{run_id}/cutover` with `{"archive_collection":"manuals_previous","retention_days":7}`.
   Update query embedding configuration to the new contract and verify again.
5. Keep the archive and old model for the agreed rollback window. To roll back
   during a maintenance window, rename the new live collection to a spare name
   with `POST /api/v1/collections/manuals/rename` and `{"new_name":"manuals_rejected"}`,
   then rename `manuals_previous` to `manuals` and restore the old query model.
   Verify the resulting names and records after each step; retain the backup if
   either operation fails.

The cutover implementation performs several renames with best-effort rollback;
it is not an atomic multi-collection transaction. It requires a completed run
but does not itself enforce a preceding shadow-read gate. Retention metadata is
not a promise of automatic deletion. Review/disable remaining dual-write rules
through `/api/v1/embedding-migrations/dual-write` before rollback. Do not reuse
production names or endpoints while rehearsing this procedure.

## Validation

See [testing](testing.md) for checks that do not touch live services. Source
contracts are in [API contract](api-contract.md); operational availability and
model quality must be checked on the intended deployment separately.

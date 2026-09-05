# Levara on Raspberry Pi

Use a 64-bit ARM64 system and size the server, vectors and separate model
processes together. A device's RAM size alone does not establish document
capacity or retrieval latency. Start with [local setup](../../docs/getting-started.md)
and [profile presets](../../docs/profile-presets.md).

## Installer path

The provided installer is an opinionated system setup, not just a binary copy.
It can install packages, configure swap, download models, create users/directories
and write/start services. Review [setup.sh](setup.sh) before applying it to a host.

```bash
make arm64
ssh pi@raspberrypi 'mkdir -p ~/levara-install'
scp levara-arm64 deploy/raspberry/setup.sh pi@raspberrypi:~/levara-install/
```

On the intended installation host, inspect the copied script and configuration,
then run `sudo bash setup.sh` from that directory when ready to install.
Replace the example SSH host with your own; this is not a production endpoint.

The script installs the server as `/usr/local/bin/levara` (despite the client
binary having that name in a source checkout). Its SQLite path is
`/var/lib/levara/levara.db`, while vector data is under `/var/lib/levara/data`.
The generated unit selects dimension/shards according to its model branch;
inspect `systemctl cat levara` rather than assuming the separate checked-in
[levara.service](levara.service) was copied verbatim.

## Network and credentials

The server defaults to loopback. Prefer a trusted tunnel or a local TLS proxy;
for direct remote access explicitly configure bind/auth and restrict access.
Use [Team setup](../../docs/tutorials/04-team-deploy.md) before sharing data.
Model sidecars have their own listeners and security settings.

MCP clients connect to `/mcp`; REST/CLI base includes `/api/v1`.
[Agent integration](../../docs/tutorials/02-agent-integration.md) covers tokens
and client configuration. Search quality depends on available models and indexes;
a Pi does not automatically gain a populated temporal graph.

## Measure before tuning

Current server flags are:

| Flag | Default | Purpose |
|---|---|---|
| `-hnsw-m` | 16 | Maximum graph neighbors per node |
| `-hnsw-ef-mult` | 8 | Search candidate multiplier relative to k |
| `-hnsw-ef-min` | 64 | Minimum search candidate budget |
| `-dim` | 128 | Must match the actual embedding output |
| `-shards` | See server help/config | Sharding choice affects resource use |

`HNSW_M`, `HNSW_EF_MULT` and `HNSW_EF_MIN` in an environment file do not override
these flags. Change the installed service's explicit `ExecStart` settings and
validate the final configuration. `ef` search settings are not a formula for
construction quality. Changing a graph's structure may require rebuilding it;
changing dimension/model requires a compatible collection/reindex plan.

Measure on a fixed document/query set: record model and dimension, k, index
size, recall/relevance, request p50/p95, build time, server RSS and each sidecar's
RSS. Repeat at quiet load and then at the required concurrency. Adjust one
setting at a time; reducing M/ef can trade recall for resources. Do not infer
quality solely from a successful health response.

The generated systemd `MemoryMax=2G` applies to the server unit, not to all model
processes. Check total host memory, swap, thermal throttling and storage latency
separately. Prefer an empirically adequate corpus/model budget to generic
claims about 50K/200K vectors or fixed latency on every Pi.

## Monitoring and backup

```bash
systemctl status levara --no-pager
journalctl -u levara -n 50 --no-pager
curl -fsS http://127.0.0.1:8080/health
```

[monitor.sh](monitor.sh) and [backup.sh](backup.sh) are repository helpers; the
installer does not copy them to `/opt/levara/`. Inspect their configured paths
and scope before installing or scheduling them. In particular, back up the
actual SQLite path as well as the separate data directory and any original
object storage. Follow the [complete inventory and restore process](../../docs/deployment.md#backup-and-recovery),
not an unchecked cron copy of a helper.

Further guides: [integrations](../../docs/integrations.md),
[document processing](../../docs/document-management.md),
[search/rerank](../../docs/search-strategies-guide.md),
[testing](../../docs/testing.md).

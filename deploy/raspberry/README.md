# Levara on Raspberry Pi

Deploy Levara on Raspberry Pi (ARM64) for edge AI and local knowledge graph use cases.

## Requirements

- Raspberry Pi 4/5 with 4GB+ RAM
- Raspberry Pi OS (64-bit) or Ubuntu Server 24.04 ARM64
- Go 1.26+ (for building from source) or pre-built ARM64 binary

## Quick Setup

```bash
# 1. Cross-compile from the repository root.
make arm64

# 2. Copy the binary and installer together under the expected names.
ssh pi@raspberrypi 'mkdir -p ~/levara-install'
scp levara-arm64 deploy/raspberry/setup.sh pi@raspberrypi:~/levara-install/

# 3. Inspect the installer/configuration before installing system services.
ssh pi@raspberrypi
cd ~/levara-install
sudo bash setup.sh
```

The installer may install packages, models, swap and system services; this is
more than copying a binary. Review it before using an existing host. The backend
now defaults to loopback. For access from another machine, configure the service
bind address and required auth, or use a trusted reverse proxy/SSH tunnel.
See [profile presets](../../docs/profile-presets.md).

## Automated Setup

The `setup.sh` script handles:
- Creating system user and directories
- Installing the systemd service
- Configuring log rotation
- Starting the service

## Files

| File | Description |
|------|-------------|
| `setup.sh` | Automated installation script |
| `levara.service` | systemd unit file |
| `levara.env` | Environment configuration |
| `backup.sh` | Data backup script |
| `monitor.sh` | Health monitoring script |
| `TUNING.md` | Performance tuning guide for Pi |

## Memory Considerations

- **4GB Pi**: size the vector index and local model together; historical
  planning estimates used dim 384/768 and roughly 50K vectors, not a capacity guarantee.
- **8GB Pi**: historical planning estimates used dim 768/1024 and roughly 200K
  vectors; measure resident memory with your documents, shards and sidecars.
- SQLite backend recommended (lower memory overhead than PostgreSQL)

## MCP Integration

Levara provides an MCP server for integration with MCP-compatible AI assistants.

### Available tools

The available MCP tools depend on `LEVARA_MCP_TOOLSET` and feature flags.
Use the [canonical catalogue](../../docs/api-contract.md) rather than a
Pi-specific tool list. Typical operations are `save_memory`/`recall_memory`,
`add`, `cognify`, `cognify_status`, `search` and `doctor`.

`add` stores input; processing it into searchable chunks is a separate step.
Follow [document management](../../docs/document-management.md) for upload,
extraction dependencies, processing and quality checks.

### MCP Config

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://raspberrypi:8080/mcp"
    }
  }
}
```

### Usage Examples

**Adding data:**
> "Remember this: distributed systems use consensus protocols for consistency"

Ask the agent to save a discrete memory, or use `add` followed by `cognify`
for a document. Confirm completion and an actual source-backed search result.

**Searching:**
> "What do I know about caching architecture?"

Levara performs semantic search across the knowledge graph.

**Temporal search:**
> "What did I learn last week about Go performance?"

Levara runs a temporal search filtered by time range.

## Monitoring

```bash
# Service status
sudo systemctl status levara

# Logs
journalctl -u levara -f

# Health check
curl http://localhost:8080/health

# Automated monitoring (add to cron)
*/5 * * * * /opt/levara/monitor.sh
```

## Backup

```bash
# Manual backup
bash /opt/levara/backup.sh

# Automated daily backup (add to cron)
0 2 * * * /opt/levara/backup.sh /mnt/usb/backups
```

## Troubleshooting

### Service fails to start
```bash
journalctl -u levara -n 50 --no-pager
```

### Out of memory
Reduce HNSW parameters in `/etc/levara/levara.env`:
```env
HNSW_M=8
HNSW_EF_MULT=4
HNSW_EF_MIN=16
```

### Slow performance
- Check CPU temperature: `vcgencmd measure_temp` (throttles at 80C)
- Use USB SSD instead of SD card
- See [TUNING.md](TUNING.md) for detailed optimization guide

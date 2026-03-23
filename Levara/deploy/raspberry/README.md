# Levara on Raspberry Pi

Deploy Levara on Raspberry Pi (ARM64) for edge AI and local knowledge graph use cases.

## Requirements

- Raspberry Pi 4/5 with 4GB+ RAM
- Raspberry Pi OS (64-bit) or Ubuntu Server 24.04 ARM64
- Go 1.26+ (for building from source) or pre-built ARM64 binary

## Quick Setup

```bash
# 1. Cross-compile on your dev machine
make arm64

# 2. Copy to Raspberry Pi
scp levara-arm64 pi@raspberrypi:~/levara/levara-server
scp deploy/raspberry/levara.service pi@raspberrypi:~/
scp deploy/raspberry/levara.env pi@raspberrypi:~/

# 3. On the Raspberry Pi, run setup
ssh pi@raspberrypi
chmod +x ~/levara/levara-server
sudo bash setup.sh
```

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

- **4GB Pi**: Use `dim=384` or `dim=768`, limit to ~50K vectors
- **8GB Pi**: Use `dim=768` or `dim=1024`, supports ~200K vectors
- SQLite backend recommended (lower memory overhead than PostgreSQL)

## MCP Integration

Levara provides an MCP server for integration with MCP-compatible AI assistants.

### Available Tools (15)

| Tool | Description |
|------|-------------|
| `add` | Add text/data to memory |
| `search` | Semantic search across memory |
| `cognify` | Run cognify pipeline (extract entities, build graph) |
| `dataset_delete` | Delete data from memory |
| `status` | Pipeline status |
| `graph_query` | Get knowledge graph |
| `extract_entities` | Get extracted entities |
| `summarize` | Get summaries |
| `datasets_list` | List collections |
| `health` | Health check |

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

Levara adds the data and runs the cognify pipeline automatically.

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

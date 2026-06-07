# Deployment Guide

## Docker

The recommended way to deploy Levara in production.

### Quick Start

```bash
docker compose -f deploy/docker/docker-compose.yml up -d
```

This starts:
- **Levara** on ports 8080 (HTTP) and 50051 (gRPC)
- **Prometheus** on port 9090 (metrics)

### Custom Configuration

Override environment variables in `docker-compose.yml` or pass them at runtime:

```bash
docker run -d \
  -p 8080:8080 \
  -p 50051:50051 \
  -v levara-data:/app/data \
  -e DB_PROVIDER=sqlite \
  -e VECTOR_DIM=768 \
  -e LLM_PROVIDER=ollama \
  -e OLLAMA_URL=http://host.docker.internal:11434 \
  --name levara \
  levara:latest
```

### Build Custom Image

```bash
docker build -t levara:latest -f deploy/docker/Dockerfile .
```

### Persistent Storage

Data is stored in `/app/data` inside the container. Mount a volume to persist across restarts:

```bash
-v /path/on/host:/app/data
```

### With Neo4j

```yaml
services:
  levara:
    # ... existing config ...
    environment:
      - NEO4J_URI=bolt://neo4j:7687
      - NEO4J_USER=neo4j
      - NEO4J_PASSWORD=your_password
    depends_on:
      - neo4j

  neo4j:
    image: neo4j:5
    ports:
      - "7474:7474"
      - "7687:7687"
    environment:
      - NEO4J_AUTH=neo4j/your_password
    volumes:
      - neo4j-data:/data
```

---

## Raspberry Pi (ARM64)

See [deploy/raspberry/](../deploy/raspberry/) for complete setup scripts.

### Cross-Compile

```bash
make arm64
```

### Deploy

```bash
scp levara-arm64 pi@raspberrypi:~/levara/levara-server
scp deploy/raspberry/{levara.service,levara.env,setup.sh} pi@raspberrypi:~/

ssh pi@raspberrypi
sudo bash ~/setup.sh
```

### Performance Tuning

See [deploy/raspberry/TUNING.md](../deploy/raspberry/TUNING.md) for Pi-specific optimizations:
- Memory configuration by Pi model
- Storage recommendations (USB SSD vs SD card)
- CPU governor settings
- WAL batch tuning

---

## systemd (Bare Metal)

### Install

```bash
# Build
make build

# Copy binaries
sudo mkdir -p /opt/levara
sudo cp levara-server /opt/levara/
sudo cp levara /usr/local/bin/

# Create data directory
sudo mkdir -p /opt/levara/data

# Create system user
sudo useradd --system --no-create-home --shell /usr/sbin/nologin levara
sudo chown -R levara:levara /opt/levara
```

### Service File

Create `/etc/systemd/system/levara.service`:

```ini
[Unit]
Description=Levara Knowledge Graph Engine
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=levara
Group=levara
WorkingDirectory=/opt/levara
ExecStart=/opt/levara/levara-server -standalone=true -dim=768 -port=8080
EnvironmentFile=/opt/levara/levara.env
Restart=always
RestartSec=5
LimitNOFILE=65535

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/levara/data
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

### Environment File

Create `/opt/levara/levara.env`:

```bash
DB_PROVIDER=sqlite
DATABASE_URL=/opt/levara/data/levara.db
VECTOR_DIM=768
HTTP_PORT=8080
GRPC_PORT=50051
MCP_ENABLED=true
LLM_PROVIDER=ollama
OLLAMA_URL=http://127.0.0.1:11434
```

### Enable and Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable levara
sudo systemctl start levara
sudo systemctl status levara
```

### Logs

```bash
journalctl -u levara -f
journalctl -u levara --since "1 hour ago"
```

---

## Monitoring

### Prometheus

Levara exposes metrics at `http://localhost:8080/metrics` in Prometheus format.

Key metrics:
- `levara_insert_requests_total` -- total insert operations
- `levara_insert_duration_seconds` -- insert latency histogram
- `levara_search_requests_total` -- total search operations
- `levara_search_duration_seconds` -- search latency histogram
- `levara_vectors_total` -- current vector count
- `levara_wal_sync_duration_seconds` -- WAL fsync latency
- `levara_arena_pages_allocated` -- memory pages in use

### Health Check

```bash
curl http://localhost:8080/health
```

### Automated Monitoring

Use `deploy/raspberry/monitor.sh` (works on any Linux):

```bash
# Add to cron
*/5 * * * * /opt/levara/monitor.sh
```

---

## Backup and Recovery

### Backup

```bash
# Stop the service (optional, for consistent snapshot)
sudo systemctl stop levara

# Backup data directory
tar -czf levara-backup-$(date +%Y%m%d).tar.gz /opt/levara/data/

# Restart
sudo systemctl start levara
```

Or use the provided backup script:

```bash
bash deploy/raspberry/backup.sh /path/to/backups
```

### Recovery

```bash
sudo systemctl stop levara
tar -xzf levara-backup-20260101.tar.gz -C /
sudo systemctl start levara
```

WAL ensures crash recovery even without explicit backups. On restart after a crash, Levara replays the WAL to restore the last consistent state.

---

## Reverse Proxy (nginx)

```nginx
upstream levara {
    server 127.0.0.1:8080;
}

server {
    listen 443 ssl;
    server_name levara.example.com;

    ssl_certificate /etc/letsencrypt/live/levara.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/levara.example.com/privkey.pem;

    location / {
        proxy_pass http://levara;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /mcp {
        proxy_pass http://levara/mcp;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

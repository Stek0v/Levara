# Deployment Guide

This guide separates generic deployment recipes from the verified local Mac runtime. For the exact current state, see [current-state.md](current-state.md).

## Verified local Mac launchd deployment

Current local service:

```text
LaunchAgent: ~/Library/LaunchAgents/com.stek0v.levara.plist
Binary:      /Users/stek0v/src/levara/levara-server
Working dir: /Users/stek0v/src/levara
HTTP/MCP:    http://127.0.0.1:8081 and /mcp
Log:         /Users/stek0v/src/levara/levara.log
Data:        /Users/stek0v/src/levara/data
```

Actual server args:

```bash
/Users/stek0v/src/levara/levara-server \
  -profile=standalone-embed \
  -dim=256 \
  -port=8081 \
  -grpc-port=0 \
  -data-dir=/Users/stek0v/src/levara/data \
  -node-id=mac1 \
  -require-auth=false \
  -embed-endpoint=http://127.0.0.1:9101/v1/embeddings \
  -embed-model=potion-code-16M \
  -llm-upstream=http://localhost:11434/v1 \
  -pg-url='postgres://stek0v@localhost:5432/levara?sslmode=disable' \
  -embed-keepalive-interval=5m
```

Current dependencies:

| Dependency | State |
|---|---|
| PostgreSQL | connected, `postgres://stek0v@localhost:5432/levara?sslmode=disable` |
| Embeddings | connected, `potion-code-16M`, 256d, `http://127.0.0.1:9101/v1/embeddings` |
| LLM | connected, OpenAI-compatible Ollama at `http://localhost:11434/v1`, model `gemma4:e2b` |
| Neo4j | disabled |
| Rerank | disabled |
| gRPC | disabled with `-grpc-port=0` |

Verify:

```bash
curl -fsS http://127.0.0.1:8081/health
curl -fsS http://127.0.0.1:8081/version
curl -fsS http://127.0.0.1:9101/health
ps -p $(pgrep -f '/levara-server' | head -1) -o pid,lstart,args=
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health --details
```

Expected current health:

```json
{"health":"healthy","status":"ready","version":"levara-go"}
```

Doctor currently reports `8/9 ok, 1 warn, 0 fail`; the only warning is BM25 coverage for `_memories_pd`.

### Rebuild and restart on Mac

```bash
cd /Users/stek0v/src/levara
make build
kill $(pgrep -f '/levara-server' | head -1)
sleep 2
curl -fsS http://127.0.0.1:8081/version
curl -fsS http://127.0.0.1:8081/health
```

Because launchd owns the service, `kill` restarts the server with the same plist args. Use `launchctl bootout/bootstrap` only when changing the plist itself.

### Update launchd registration

```bash
UID_NUM=$(id -u)
PLIST="$HOME/Library/LaunchAgents/com.stek0v.levara.plist"
launchctl bootout gui/$UID_NUM "$PLIST" 2>&1 || true
launchctl bootstrap gui/$UID_NUM "$PLIST"
launchctl enable gui/$UID_NUM/com.stek0v.levara 2>&1 || true
launchctl kickstart -kp gui/$UID_NUM/com.stek0v.levara
curl -fsS http://127.0.0.1:8081/health
```

### Embed sidecar

The embed sidecar is a separate service. Levara pings it but does not start it.

```text
Endpoint: http://127.0.0.1:9101
Embeddings: /v1/embeddings
Model: potion-code-16M
Dim: 256
Backend: model2vec
```

Verify:

```bash
curl -fsS http://127.0.0.1:9101/health
pgrep -f embed_bench.server
launchctl list | grep embed
```

The Levara embed endpoint must include `/v1/embeddings`, not just `/v1`.

## Bare-metal Linux/systemd

Use this as a template; adjust dimensions, endpoints, and auth for the target host.

### Install

```bash
make build
sudo mkdir -p /opt/levara/data
sudo cp levara-server /opt/levara/
sudo useradd --system --no-create-home --shell /usr/sbin/nologin levara || true
sudo chown -R levara:levara /opt/levara
```

### Service file

Create `/etc/systemd/system/levara.service`:

```ini
[Unit]
Description=Levara Memory and Search Server
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=levara
Group=levara
WorkingDirectory=/opt/levara
EnvironmentFile=-/opt/levara/levara.env
ExecStart=/opt/levara/levara-server -profile=standalone-embed -dim=256 -port=8080 -grpc-port=0 -data-dir=/opt/levara/data
Restart=always
RestartSec=5
LimitNOFILE=65535
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/levara
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

### Environment file

Create `/opt/levara/levara.env` as needed:

```bash
DATABASE_URL=postgres://levara:change-me@127.0.0.1:5432/levara?sslmode=disable
EMBEDDING_ENDPOINT=http://127.0.0.1:9101/v1/embeddings
EMBEDDING_MODEL=potion-code-16M
LLM_PROVIDER=openai
LLM_ENDPOINT=http://127.0.0.1:11434/v1
LLM_MODEL=gemma4:e2b
```

There is no `-llm-model` server flag. Use `LLM_MODEL`.

### Start and verify

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now levara
systemctl status levara --no-pager
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/version
journalctl -u levara -f
```

## Docker

The Docker path is still available for isolated deployments.

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --build
```

The compose file is the authority for container ports and services; do not assume it matches the local Mac launchd runtime. After startup:

```bash
docker compose -f deploy/docker/docker-compose.yml ps
curl -fsS http://127.0.0.1:8080/health
```

Build a custom image:

```bash
docker build -t levara:latest -f deploy/docker/Dockerfile .
```

Minimal docker run example:

```bash
docker run -d \
  -p 8080:8080 \
  -v levara-data:/app/data \
  -e EMBEDDING_ENDPOINT=http://host.docker.internal:9101/v1/embeddings \
  -e EMBEDDING_MODEL=potion-code-16M \
  -e LLM_PROVIDER=openai \
  -e LLM_ENDPOINT=http://host.docker.internal:11434/v1 \
  -e LLM_MODEL=gemma4:e2b \
  --name levara \
  levara:latest \
  -profile=standalone-embed -dim=256 -port=8080 -grpc-port=0
```

## Raspberry Pi / ARM64

See [../deploy/raspberry/](../deploy/raspberry/) for Pi-specific scripts and [../deploy/raspberry/TUNING.md](../deploy/raspberry/TUNING.md) for tuning.

Cross-compile the server:

```bash
make arm64
```

Deploy pattern:

```bash
scp levara-arm64 pi@raspberrypi:~/levara/levara-server
scp deploy/raspberry/{levara.service,levara.env,setup.sh} pi@raspberrypi:~/
ssh pi@raspberrypi
sudo bash ~/setup.sh
```

## Monitoring

Levara exposes Prometheus metrics on the HTTP port:

```bash
curl -fsS http://127.0.0.1:8081/metrics | head
```

Useful runtime checks:

```bash
curl -fsS http://127.0.0.1:8081/health
curl -fsS http://127.0.0.1:8081/health/details
curl -fsS http://127.0.0.1:8081/version
```

Inside Hermes, prefer the MCP diagnostics:

```text
mcp_levara_doctor(verbose=true)
mcp_levara_runtime_stats()
mcp_levara_check_drift()
mcp_levara_recent_errors(limit=20)
```

## Backup and recovery

File-backed state lives under the configured `-data-dir`. For the local Mac runtime that is `/Users/stek0v/src/levara/data`.

Cold backup:

```bash
# Optional for a fully quiescent snapshot:
kill $(pgrep -f '/levara-server' | head -1)

tar -czf levara-data-$(date +%Y%m%d_%H%M%S).tar.gz /Users/stek0v/src/levara/data

curl -fsS http://127.0.0.1:8081/health
```

Linux/systemd variant:

```bash
sudo systemctl stop levara
tar -czf levara-backup-$(date +%Y%m%d).tar.gz /opt/levara/data
sudo systemctl start levara
curl -fsS http://127.0.0.1:8080/health
```

WAL replay restores the last consistent file-backed vector state after process crash. PostgreSQL still needs its own backup policy if used.

## Reverse proxy

Example nginx config for an HTTP/MCP deployment on `127.0.0.1:8080`:

```nginx
upstream levara {
    server 127.0.0.1:8080;
}

server {
    listen 80;
    server_name levara.example.com;

    location / {
        proxy_pass http://levara;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable auth before exposing Levara outside a trusted network.

## Common pitfalls

- `-grpc-port=0` disables gRPC, but `/health/details` may still show a `grpc` row with `port=0`.
- `./levara-server -standalone=true` is legacy shorthand; prefer explicit `-profile=standalone` or `-profile=standalone-embed`.
- `LLM_MODEL` is an environment variable, not a CLI flag.
- The local CLI executable may be `./levara/cli` because a `levara/` directory exists.
- The local Mac runtime uses port `8081`; many generic examples use `8080`.
- Neo4j and rerank are optional and are not configured in the current Mac deployment.

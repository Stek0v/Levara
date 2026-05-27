# Bench stack on Pi (10.23.0.53)

Isolated from production `levara.service` on :8090. Lifecycle controlled by
`scripts/load-profiles/run_all_models.sh`.

## Architecture

| Service | Port | Notes |
|---------|------|-------|
| `embed-bench` | 9201 | FastAPI sidecar; serves `/v1/embeddings` for whatever model drop-in selects (was 9101, moved 2026-05-27 because prod `embed-potion.service` now owns 9101) |
| `levara-bench` | 8091 | Levara in sqlite mode; consumes embed-bench |

`embed-bench` runs from `WorkingDirectory=/home/stek0v/embed-bench/scripts/load-profiles`
so that `embed_bench` (the actual Python package directory) is importable directly.
The uvicorn invocation is `uvicorn embed_bench.server:app` — **not**
`scripts.load_profiles.embed_bench.server:app` (the latter would fail because
`load-profiles` contains a hyphen and cannot be a Python package name).

## One-time setup

1. Sync the repo to the Pi:
   ```bash
   rsync -a --delete /Users/stek0v/src/levara/ stek0v@10.23.0.53:/home/stek0v/levara-source/
   ```

2. Cross-compile the Levara binary for arm64 and place it on the Pi:
   ```bash
   GOOS=linux GOARCH=arm64 go build -o levara-arm64 ./Levara/cmd/server
   scp levara-arm64 stek0v@10.23.0.53:/home/stek0v/levara-bench/levara
   ```

3. Run the setup script (idempotent):
   ```bash
   ssh stek0v@10.23.0.53 bash -s < deploy/bench/setup_pi.sh
   ```

   The script:
   - Creates `$EMBED_DIR/hf-cache` and `$BENCH_DIR/data`
   - Creates a venv and installs `embed_bench/requirements.txt`
   - Syncs `scripts/` into `$EMBED_DIR/scripts/`
   - Generates a random `JWT_SECRET` drop-in for `levara-bench` (once)
   - Installs both `.service` files and reloads systemd

## Per-model run (three drop-ins)

For each model under test, create three drop-ins then restart both units:

```bash
# 1. Tell embed-bench which model to load
sudo mkdir -p /etc/systemd/system/embed-bench.service.d
sudo tee /etc/systemd/system/embed-bench.service.d/model.conf >/dev/null <<EOF
[Service]
Environment=EMBED_BENCH_MODEL=nomic-ai/nomic-embed-text-v2-moe
EOF

# 2. Tell levara-bench which embedding model name to advertise
sudo tee /etc/systemd/system/levara-bench.service.d/embed.conf >/dev/null <<EOF
[Service]
Environment=EMBEDDING_MODEL=nomic-embed-text-v2-moe
EOF

# 3. Override vector dimension if the model differs from the default
sudo tee /etc/systemd/system/levara-bench.service.d/dim.conf >/dev/null <<EOF
[Service]
ExecStart=
ExecStart=/home/stek0v/levara-bench/levara -standalone=true -port=8091 -grpc-port=0 -data-dir=/home/stek0v/levara-bench/data -node-id=pi-bench -dim=768
EOF

sudo systemctl daemon-reload
sudo systemctl restart embed-bench levara-bench
```

Wait for embed-bench to finish downloading the model weights (check
`journalctl -u embed-bench -f`) before running the benchmark suite.

## Cleanup

Stop both units to free memory:
```bash
sudo systemctl stop embed-bench levara-bench
```

Data persists in `/home/stek0v/levara-bench/data`. Remove manually for a fresh start:
```bash
rm -rf /home/stek0v/levara-bench/data && mkdir -p /home/stek0v/levara-bench/data
```

# Isolated model benchmark setup

This directory contains example systemd units for an embedding sidecar and a
Levara benchmark process. They are environment-specific templates, not a
portable installer or evidence that a model is suitable for a device.

Use a disposable host or VM with its own database, vector directory and ports.
Choose the host yourself; do not copy benchmark data over a running service.
See [testing](../../docs/testing.md) for result requirements and known limits.

## Components

| Component | Example port | Purpose |
|-----------|-------------:|---------|
| `embed-bench` | 9201 | Python `/v1/embeddings` service with a selected model |
| `levara-bench` | 8091 | Separate Levara instance using the embedding service |

The embedding module is `embed_bench.server:app`. Its working directory must
be `scripts/load-profiles` so Python can import `embed_bench`; the hyphenated
parent directory is not a Python package name.

## Prepare the experiment

1. Build the current server revision for the test machine. For an ARM64 Linux
   target, build from the repository root:

   ```bash
   GOOS=linux GOARCH=arm64 go build -mod=readonly -o ./levara-bench-arm64 ./cmd/server
   ```

2. Install the embedding sidecar's dependencies from
   `scripts/load-profiles/embed_bench/requirements.txt` into an isolated Python
   environment. Record Python, dependency and model revisions. Model downloads
   require network access and storage; allow them to finish before timing.
3. Configure two dedicated foreground processes or adapt copies of the unit
   templates to the test machine. Review `User`, `WorkingDirectory`, `ExecStart`,
   paths, ports, listener addresses and all provider endpoints. Bind locally
   when the test driver runs on the same host. The supplied embedding unit
   binds `0.0.0.0`; it is not a secured network deployment by itself.
4. Set `EMBED_BENCH_MODEL` for the sidecar. Set Levara's `EMBEDDING_ENDPOINT`,
   `EMBEDDING_MODEL` and `-dim` to the actual model output. Use a new vector
   directory for a different model or dimension.
5. Keep authentication and rate-limit settings consistent between candidates.
   If a script cannot exercise the intended authentication mode, report that
   limitation instead of using its results as access-control evidence.

`setup_pi.sh` embeds one developer's paths, uses `rsync --delete`, installs
systemd units and reloads systemd. Do not run it unchanged as a setup shortcut.
This guide does not require SSH to an existing host or service restarts.

## Per-model run

Run the same synthetic corpus, queries and load profile for each candidate.
Separate cold startup/model loading from warmed request timing. Record:

- server and runner revisions, command, backend and independent data paths;
- model identifier, immutable artifact digest, tokenizer and output dimension;
- hardware, available memory, process CPU/RSS and any swap activity;
- request count, concurrency, failures, latency distribution and retrieval
  results against labelled answers;
- skipped scenarios and external calls made by the providers.

A model's download size is not its runtime memory use. Compare quality and
resource measurements from actual runs before choosing a deployment model.

## Cleanup

Stop only the processes created for the experiment. Save logs and result
artifacts, then remove the explicitly recorded disposable data directories.
Do not use an existing service's data path as a fresh benchmark directory.

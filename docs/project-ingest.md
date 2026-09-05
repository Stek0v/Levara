# One-command project ingest

Use this when an existing project should become searchable/useful for Levara
and AI agents. Run commands from the repository root. For individual uploaded
files, extraction quality, retries and sharing, use
[document management](document-management.md) and
[acceptance scenarios](document-workflow-scenarios.md).

## Configure the target and inspect the corpus

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
# Set LEVARA_TOKEN for an authenticated server.
python3 scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --dry-run
```

Remove `--dry-run` after reviewing the selected corpus. The script has its own
directory/suffix filters and does **not** honor `.gitignore`. Inspect ignored
JSON/YAML/config files for secrets before sending content to Levara or a model.
The default URL is `http://127.0.0.1:8081/api/v1`; the explicit environment above
aligns with the portable first-run server. Python argparse accepts space-separated
values here; the Go CLI uses `--key=value`.

In authenticated workspace mode, add `--project-id DATASET_ID` using an existing
accessible dataset ID; a project slug is not automatically a valid authorized
dataset. Use [document management](document-management.md) to create/share it.

Default behavior:

- scans code, docs, SQL and config files;
- excludes `.git`, `node_modules`, `.venv`, `dist`, `build`, caches and other
  generated/heavy folders;
- creates or appends a local `AGENTS.md` Levara memory contract;
- runs classic `cognify` in `rag` mode;
- builds workspace search derivatives; confirm the corresponding truth files are
  available to `workspace_read` before treating an indexed hit as an exact source;
- writes a JSON report to stdout.

The default `rag` mode is intentional: it is the practical fast path for
project search. It indexes all selected code/docs without waiting for slow
LLM graph extraction.

## Dry run first

```bash
python3 scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --dry-run
```

Use this to inspect file count, bytes, excluded folders and the first sample
files before mutating Levara or writing `AGENTS.md`.

## Heavy full graph/LLM processing

```bash
python3 scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --mode full \
  --timeout-seconds 7200
```

Use `--mode full` only when entity/relationship graph extraction is required.
It can be slow even on small inputs depending on the local LLM provider.

## Workspace only

```bash
python3 scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --pipeline workspace
```

Use this when classic RAG collection already exists and only the exact-read
workspace index needs refresh.

## Smoke test

```bash
python3 scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project-smoke \
  --limit 3 \
  --no-agents
```

This validates API connectivity and indexing on a few files without touching
the project `AGENTS.md`.

## Nightly full enrichment for `~/src/*`

Example schedule to install after a successful manual run (replace `/path/to/levara`
and the log path; the server/preset does not install this cron job):

```cron
10 3 * * * /bin/bash /path/to/levara/scripts/levara_nightly_full_enrich.sh >> /path/to/logs/nightly-full-enrich.log 2>&1
```

The batch script:

- scans first-level directories under `~/src/*`;
- skips hidden directories;
- skips any project containing `.levara-no-nightly`;
- runs projects sequentially with a lock;
- uses `--mode full` and `--pipeline all`;
- writes per-project JSON reports under
  `~/Library/Logs/levara/nightly-full-enrich/reports/`;
- does not write project `AGENTS.md` by default during nightly runs
  (`WRITE_AGENTS=1` enables it);
- stops on classic full enrichment error/timeout by default to avoid stacking
  slow LLM/graph jobs (`STOP_ON_CLASSIC_ERROR=0` disables this).

Useful manual checks:

```bash
crontab -l
tail -f ~/Library/Logs/levara/nightly-full-enrich/cron.log
DRY_RUN=1 MAX_PROJECTS=2 /path/to/levara/scripts/levara_nightly_full_enrich.sh
```

Useful overrides:

```bash
PROJECT_ROOT="$HOME/src" \
TIMEOUT_SECONDS=21600 \
MODE=full \
PIPELINE=all \
/path/to/levara/scripts/levara_nightly_full_enrich.sh
```

## After ingest: check agent memory behavior

Once agents start using a newly ingested project, inspect whether the project
scaffold is working:

```bash
curl -fsS -H "Authorization: Bearer $LEVARA_TOKEN" \
  "${LEVARA_URL}/memory-behavior?hours=24&collection=my-project"
```

In WebUI open:

- `/memory-behavior` for recall-before-save, repeat-save, zero-result and
  context-byte metrics;
- `/memory-scaffold` after running a meta-review to approve/reject proposed
  `AGENTS.md` or memory-policy improvements.

The fake evaluator checks only the harness against fixed fixtures:

```bash
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py \
  --fake \
  --label harness-smoke
```

It does not read your project or `AGENTS.md`; changing the scaffold cannot
change these fixed-fixture results. To compare a memory contract before/after,
run the same real agent tasks with the same model/corpus and collect their audit
events, behavior score, context bytes and answer correctness. The canary driver
also emits scripted tool calls; it does not evaluate an agent following your
instructions. See [testing](testing.md) for measurement limits and
[workspace operations](markdown-workspace-deployment-recipes.md) for exact-read
and index freshness checks.

Collection/client analytics selectors are not owner/tenant access controls;
restrict shared analytics endpoints until that read-model boundary is enforced.

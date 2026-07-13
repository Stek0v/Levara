# One-command project ingest

Use this when an existing project should become searchable/useful for Levara
and AI agents.

## Fast default for an existing project

```bash
python3 /Users/stek0v/src/levara/scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project
```

Default behavior:

- scans code, docs, SQL and config files;
- excludes `.git`, `node_modules`, `.venv`, `dist`, `build`, caches and other
  generated/heavy folders;
- creates or appends a local `AGENTS.md` Levara memory contract;
- runs classic `cognify` in `rag` mode;
- builds a workspace index for `workspace_search` / `workspace_read`;
- writes a JSON report to stdout.

The default `rag` mode is intentional: it is the practical fast path for
project search. It indexes all selected code/docs without waiting for slow
LLM graph extraction.

## Dry run first

```bash
python3 /Users/stek0v/src/levara/scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --dry-run
```

Use this to inspect file count, bytes, excluded folders and the first sample
files before mutating Levara or writing `AGENTS.md`.

## Heavy full graph/LLM processing

```bash
python3 /Users/stek0v/src/levara/scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --mode full \
  --timeout-seconds 7200
```

Use `--mode full` only when entity/relationship graph extraction is required.
It can be slow even on small inputs depending on the local LLM provider.

## Workspace only

```bash
python3 /Users/stek0v/src/levara/scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project \
  --pipeline workspace
```

Use this when classic RAG collection already exists and only the exact-read
workspace index needs refresh.

## Smoke test

```bash
python3 /Users/stek0v/src/levara/scripts/levara_project_ingest.py \
  /path/to/project \
  --collection my-project-smoke \
  --limit 3 \
  --no-agents
```

This validates API connectivity and indexing on a few files without touching
the project `AGENTS.md`.

## Nightly full enrichment for `~/src/*`

Installed local cron entry:

```cron
10 3 * * * /bin/bash /Users/stek0v/src/levara/scripts/levara_nightly_full_enrich.sh >> /Users/stek0v/Library/Logs/levara/nightly-full-enrich/cron.log 2>&1
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
DRY_RUN=1 MAX_PROJECTS=2 /Users/stek0v/src/levara/scripts/levara_nightly_full_enrich.sh
```

Useful overrides:

```bash
PROJECT_ROOT=/Users/stek0v/src \
TIMEOUT_SECONDS=21600 \
MODE=full \
PIPELINE=all \
/Users/stek0v/src/levara/scripts/levara_nightly_full_enrich.sh
```

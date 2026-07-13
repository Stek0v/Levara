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

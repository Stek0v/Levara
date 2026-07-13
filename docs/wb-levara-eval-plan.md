# WB Levara Project Eval

This eval measures how useful Levara is for a real mixed project workspace:
`/Users/stek0v/Documents/WB`.

The WB workspace contains two different project shapes:

- `wb-mcp-server`: TypeScript MCP server for the Wildberries Seller API.
- `wb-analytics-platform`: Python ingestion, SQL schemas, ClickHouse,
  PostgreSQL metadata, MinIO/S3 archive, and Evidence dashboards.

## Corpus Rules

Include source and contract files:

- `.md`, `.ts`, `.py`, `.sql`, `.yaml`, `.yml`, `.json`, `.toml`

Exclude generated or heavy paths:

- `.git`, `.venv`, `node_modules`, `dist`, `build`, `output`, `tmp`,
  `__pycache__`, `.pytest_cache`

## Modes

- `filesystem`: lexical local baseline over project files.
- `memory`: current Levara MCP `memory` profile via `recall_memory`.
- `search_hybrid`, `search_chunks`, `search_chunks_lexical`: Levara RAG
  collection search after indexing the WB corpus.
- `workspace_hybrid`, `workspace_chunks_lexical`: Levara MCP workspace tools.
  This requires running with a workspace-capable tool profile and
  `--run-workspace`.

## Run

Read-only baseline plus memory:

```bash
python3 benchmark/wb_project_eval.py \
  --project /Users/stek0v/Documents/WB \
  --url http://127.0.0.1:8081 \
  --output benchmark/results/wb_project_eval_latest.json
```

Create a temporary Levara collection and include indexed search:

```bash
python3 benchmark/wb_project_eval.py \
  --project /Users/stek0v/Documents/WB \
  --url http://127.0.0.1:8081 \
  --collection wb_eval_manual \
  --index \
  --output benchmark/results/wb_project_eval_latest.json
```

Measure workspace mode after temporarily starting Levara with
`LEVARA_MCP_TOOLSET=workspace`:

```bash
python3 benchmark/wb_project_eval.py \
  --project /Users/stek0v/Documents/WB \
  --url http://127.0.0.1:8081 \
  --collection wb_eval_manual \
  --run-workspace \
  --workspace-project wb_eval \
  --workspace-generation wb_eval_manual_ws_gen \
  --output benchmark/results/wb_project_eval_workspace.json
```

## Metrics

- `Recall@3`: at least one expected source appears in the top 3 references.
- `MRR`: reciprocal rank of the first expected source.
- Latency p50/p95/p99 per mode.
- Average context bytes returned to the agent.
- Mode availability, especially whether the active MCP profile exposes
  workspace tools.

## Interpretation

The `memory` profile is expected to help with cross-session decisions and
discoveries, not fresh project-file retrieval. Project retrieval should be
measured through `search` or `workspace`.

For Codex local work, the practical target is:

- keep `memory` as the default always-on profile;
- use `search` for project-level semantic retrieval;
- switch to `workspace` only when Levara should own exact-read/project
  workspace workflows rather than Codex using the filesystem directly;
- for WB, compare `search_chunks_lexical` against `workspace_chunks_lexical`
  first, because many golden questions are path/keyword-heavy.

## 2026-07-13 local results

Profile catalog measurements on local launchd:

| Requested profile | Reported profile | Tools | tools/list bytes | Intended use |
|---|---:|---:|---:|---|
| `core` | `core` | 11 | 13,435 | Smallest conversational/search profile. |
| `memory` | `memory` | 19 | 20,194 | Best default for Codex + durable memory/feedback/consolidation. |
| `workspace` | `workspace` | 17 | 25,460 | Workspace search/read/write runtime; no operator indexing tools. |
| `ops` | `ops` | 16 | 21,004 | Operator health/audit/reconcile profile; not for normal agents. |
| `full` | `full` | 70 | 89,801 | Operator/dev superset; highest context cost. |
| `light` | `memory` | 19 | 20,194 | Compatibility alias. |
| unknown | `full` | 70 | 89,801 | Compatibility fallback. |

WB retrieval measurements on 12 golden cases, 114 indexed source/contract
files:

| Mode | Recall@3 | Recall@5 | MRR | p95 ms | Avg response bytes | Notes |
|---|---:|---:|---:|---:|---:|---|
| filesystem lexical | 0.5000 | 0.6667 | 0.4028 | 5.57 | 224 | Cheap local baseline. |
| memory recall | 0.0000 | 0.0000 | 0.0000 | 11.84 | 3,433 | Not intended for fresh project files. |
| search hybrid | 0.5833 | 0.5833 | 0.4583 | 10.10 | 25,046 | Better semantic recall than filesystem, large payload. |
| search chunks | 0.4167 | 0.5000 | 0.2917 | 11.60 | 26,661 | Worse than hybrid/lexical on this corpus. |
| search chunks lexical | 0.5833 | 0.5833 | 0.5417 | 3.16 | 21,490 | Best RAG collection mode. |
| workspace profile without operator index | 0.0000 | 0.0000 | 0.0000 | 7.17 | 2,996 | Expected: no active manifest; workspace profile cannot index. |
| workspace hybrid after full index | 0.6667 | 0.8333 | 0.5833 | 13.56 | 53,016 | Best semantic workspace mode, but high payload. |
| workspace chunks lexical after full index | 0.7500 | 0.8333 | 0.5833 | 6.34 | 49,955 | Best current quality for WB golden set. |

Current conclusion: keep local Codex on `memory`; use `full` or an operator
path to prepare workspace indexes; let agents consume the prepared workspace
through `workspace` only when exact-read/citation workflow is needed. For WB,
`workspace_chunks_lexical` is the strongest retrieval mode after preparation,
while `search_chunks_lexical` is the best low-friction mode without workspace
manifest setup.

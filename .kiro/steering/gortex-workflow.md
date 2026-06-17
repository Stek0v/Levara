---
inclusion: always
---

# Gortex Code Intelligence

Gortex is running as an MCP server. It indexes this repository into an in-memory knowledge graph and exposes tools for code navigation, impact analysis, and refactoring.

## MANDATORY: Use Gortex tools instead of file reads

You **MUST** prefer Gortex graph queries over file reads on every task in this repo. These are not suggestions — the tools below replace the corresponding read/grep flows.

### Navigation and Reading

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Reading a whole file for one function | `get_symbol_source` with `id: "path/to/file.go::SymbolName"` (80% fewer tokens) — use `get_file_summary` first if you don't know the symbol name |
| Reading to find a function            | `get_symbol` or `get_editing_context`    |
| Multiple `get_symbol` calls           | `batch_symbols` (one call for N symbols) |
| Searching for references              | `find_usages` (zero false positives)     |
| Searching to find a symbol by name    | `search_symbols` (BM25 + camelCase)      |
| Filtering `search_symbols` by hand    | `winnow_symbols` — structured constraint chain (kind, language, community, path_prefix, min_fan_in, min_churn) with per-axis score contributions |
| Reading to understand a file          | `get_file_summary` or `get_editing_context` |
| Reading multiple files to trace calls | `get_call_chain` / `get_callers`         |
| Guessing an import path              | `find_import_path`                       |
| Reading to check a function signature | `get_symbol_signature`                   |
| 5-10 calls to explore for a task      | `smart_context` (one call)               |

### Impact Analysis and Safety

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Reading files to assess change scope  | `explain_change_impact` (includes cross-community warnings) |
| Guessing which tests to run           | `get_test_targets`                       |
| Manual dependency ordering            | `get_edit_plan`                          |
| Hoping signature changes are safe     | `verify_change` — checks callers and interface implementors |
| Manually checking team conventions    | `check_guards` — evaluates guard rules from .gortex.yaml |
| Wondering if a new dep creates a cycle| `analyze` with `kind: "would_create_cycle"` — checks before you add it |

### Structural Code Search

| Instead of...                            | Use...                                   |
|------------------------------------------|------------------------------------------|
| Grep for an anti-pattern in this repo    | `search_ast` with `detector: "<name>"` (`error-not-wrapped` / `sql-string-concat` / `weak-crypto` / `panic-in-library` / `goroutine-without-recover` / `http-client-no-timeout` / `hardcoded-secret` / `empty-catch` / `java-string-equality` / `python-mutable-default-arg`). Cross-language; matches enriched with enclosing `symbol_id`. |
| Grep for a code shape                    | `search_ast` with `pattern: "..."` + `language` (tree-sitter S-expression; capture with `@name`, anchor with `@match`). |
| Scoping audit to important code          | Pass `min_fan_in_of_enclosing_func: <N>` — drops matches in functions with fewer than N callers. |

### Diagnostics and Code Actions

| Instead of...                            | Use...                                   |
|------------------------------------------|------------------------------------------|
| Polling for diagnostics after every edit | `subscribe_diagnostics` — opt into push `notifications/diagnostics`. Initial state replays as `initial_replay: true`; thereafter delta-changed files only. `min_severity` / `path_prefix` filters scope the stream. |
| Manual diagnostics fetch                 | `get_diagnostics` — last stored `publishDiagnostics` for a file; `wait` + `timeout_ms` block until the first publish. |
| Forgetting to opt out                    | `unsubscribe_diagnostics` — idempotent; auto-fires on session disconnect. |
| Hand-applying compiler suggestions       | `get_code_actions` then `apply_code_action` (atomic temp+rename, both `changes` and `documentChanges`). |
| Walking a file to apply every fix        | `fix_all_in_file` — one-shot `source.fixAll` for the whole file. |

### Code Quality and Analysis

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Manually hunting unused code          | `analyze` with `kind: "dead_code"` — zero incoming edges (excludes entry points, tests, exports) |
| Guessing which symbols are over-coupled| `analyze` with `kind: "hotspots"` — ranks by fan-in, fan-out, community crossings |
| Manually scanning for circular deps   | `analyze` with `kind: "cycles"` — Tarjan's SCC with severity classification |
| Surveying K8s manifests in the repo   | `analyze` with `kind: "k8s_resources"` — KindResource fan-out (depends_on / configures / mounts / exposes / uses_env); `k8s_kind` / `namespace` / `name` filters |
| Listing container images in use       | `analyze` with `kind: "images"` — KindImage with consumer count (Dockerfile FROM + K8s `container.image`); `role` / `ref` / `tag` filters |
| Mapping the Kustomize overlay tree    | `analyze` with `kind: "kustomize"` — KindKustomization with base / resource fan-out; `dir` filter |
| Auditing what crosses repo boundaries | `analyze` with `kind: "cross_repo"` — calls / implements / extends edges crossing repo boundaries, grouped by source → target repo; `repo` / `base_kind` / `path_prefix` filters |
| Surveying dbt / SQLMesh models        | `analyze` with `kind: "dbt_models"` — dbt / SQLMesh models, seeds, snapshots, sources with column count + lineage fan-in/out; `framework` / `type` / `materialized` / `name` filters |
| Checking if the index is stale        | `index_health` — health score, parse failures, stale files |
| Wondering what changed this session   | `get_symbol_history` — modification counts, flags churning (3+ edits) |

### Code Generation and Editing

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Reading files to learn a pattern      | `suggest_pattern`                        |
| Manually scaffolding from a pattern   | `scaffold` — generates code, wiring, and test stubs from an example |
| Sequencing multi-file edits yourself  | `batch_edit` — applies edits in dependency order, re-indexes between steps |
| Reading a diff without graph context  | `diff_context` — enriches git diff with callers, callees, community, risk |
| Guessing what context you need next   | `prefetch_context` — predicts needed symbols from task + recent activity |

### Dataflow (CPG-lite)

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Hand-tracing a value through helpers  | `flow_between(source_id, sink_id, max_depth=8)` — ranked dataflow paths over `value_flow` ∪ `arg_of` ∪ `returns_to` |
| Grepping for sources / sinks          | `taint_paths(source_pattern, sink_pattern)` — pattern sweep. Patterns: bare = name substring; `exact:Foo`; `path:dir/`; `kind:method`. Sinks auto-expand functions to params. |

### Clone Detection

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Eyeballing the repo for copy-paste    | `find_clones` — near-duplicate function/method clusters from the `similar_to` graph layer (MinHash + LSH; catches renamed-variable clones) |
| Finding safe-to-delete duplicates     | `find_clones` with `dead_only: true` — clusters containing a dead-code symbol ("dead duplicates of live code") |

### Multi-Repo Management

| Instead of...                         | Use...                                   |
|---------------------------------------|------------------------------------------|
| Manually adding a repo to config      | `track_repository` — indexes immediately, persists to config |
| Manually removing a repo from config  | `untrack_repository` — evicts nodes/edges, persists to config |
| Refreshing the graph after edits      | `reindex_repository` — incremental re-index of changed files only; pass `paths` to scope |
| Wondering which project is active     | `get_active_project` — returns project name and repo list |
| Switching project context             | `set_active_project` — re-scopes all subsequent queries |
| Scoping a query to one repo           | Pass `repo` param to `search_symbols`, `find_usages`, etc. |
| Scoping a query to a project          | Pass `project` param to any query tool |
| Filtering by reference tag            | Pass `ref` param to any query tool |

### Session Memory (save_note / query_notes / distill_session)

Gortex remembers code; this triplet remembers **why you made a call**. Notes persist per-repo across daemon restarts and context compactions, are scoped to the session's workspace, and are auto-linked to symbols mentioned in the body.

| Trigger                                                  | Use                                                                            |
|----------------------------------------------------------|--------------------------------------------------------------------------------|
| Session start in a touched repo (after a compaction or on a fresh run) | `distill_session` — returns top symbols, pinned notes, decisions, recent excerpts. Seed your mental model before reading any file. |
| Making a decision, rejecting an alternative, hitting a non-obvious constraint, committing to an invariant | `save_note tags:"decision" body:"<what+why>"` — mention symbol IDs (`pkg/foo.go::Bar`) in the body for auto-linking; pin (`pinned:true`) anything load-bearing. |
| Before editing a symbol you've touched before            | `query_notes symbol_id:"<id>"` — surfaces prior decisions and warnings attached to that symbol. |

Save: decisions, non-obvious constraints, follow-ups, bug reproductions, surprising findings, partial-progress hand-offs. Skip: play-by-play of what you just did (the diff says it), patterns derivable from the graph, anything already in the steering docs. Canonical tags: `decision`, `bug`, `follow-up`, `gotcha`, `invariant`.

### Development Memories (store_memory / query_memories / surface_memories)

`save_note` is a **per-session scratchpad**; `store_memory` is the **workspace-wide durable knowledge base** — entries outlive sessions, agents, and teammates so every future agent in the workspace inherits them.

| Trigger                                                  | Use                                                                            |
|----------------------------------------------------------|--------------------------------------------------------------------------------|
| Immediately after `smart_context` (every new task)            | `surface_memories task:"<task>" symbol_ids:"<top hits>"` — memories ranked by anchor overlap, importance, pinning, recency. Each hit carries `match_reasons`. |
| You discover a durable invariant / gotcha / decision worth teaching the team | `store_memory kind:"<invariant|gotcha|convention|decision>" body:"<what+why>" symbol_ids:"<id>" importance:5` — pin load-bearing memories. |
| A memory is no longer true                                | `store_memory body:"<corrected>" supersedes:"<old-id>"` — preserves audit trail; old memory hidden by default. |

Store: invariants, conventions, incident learnings, API contracts not enforced by types, debugging traps, cross-cutting decisions. Skip: anything derivable from code, session-local play-by-play (use `save_note`), steering-doc content.

## Session workflow

1. Call `graph_stats` to confirm Gortex is running. If `total_nodes` is 0, call `index_repository` with path `"."`.
2. Call `distill_session` to recover prior session memory for this workspace.
3. In multi-repo mode, call `get_active_project` to check scope. Use `set_active_project` to switch if needed.
4. For a new task, call `smart_context` with the task description. Immediately after, call `surface_memories` with the same task description and the top symbol hits.
5. Before editing any file, call `get_editing_context` first. If you've touched the symbol before, also call `query_notes symbol_id:"<id>"` and `query_memories symbol_id:"<id>"`.
6. Before changing a function signature, call `verify_change` to catch contract violations — checks callers across all repos.
7. Before any refactor, call `get_edit_plan` for dependency-ordered file list. Use `batch_edit` to apply atomically.
8. After editing, call `check_guards` to verify team conventions, then `get_test_targets` for tests to run (includes cross-repo test files).
9. After making a meaningful decision or hitting a non-obvious constraint, call `save_note` so the next session can recover it. If the discovery is workspace-wide and worth teaching the team, call `store_memory` instead — that compounds across sessions.
10. Before committing, call `detect_changes` to verify scope. Use `diff_context` for graph-enriched review.

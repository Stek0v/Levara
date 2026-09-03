# AGENTS.md — Levara MCP Memory playbook

This file is the canonical guide for any AI agent (Claude, Cursor, Cline, ChatGPT
via MCP, custom Agent SDK clients) on how to use the Levara MCP memory layer
effectively. The goal: nothing important from a session should ever be lost,
and every future session should be able to reconstruct context cheaply.

Mirror of the "Levara MCP Memory" section in `CLAUDE.md`. Update both together.

## Levara memory

- Collection: `levara`
- Default room: `mcp`
- Runtime rooms:
  - `memory` — recall, isolation, consolidation, supersession, and provenance
  - `task-runtime` — tasks, criteria, steps, leases, receipts, checkpoints, blockers, and promotion
  - `observability` — validation failures, runtime health, metrics, and rollout
  - `deploy` — feature flags, profiles, services, and security
- Use the hall vocabulary and pin policy defined below. Never save Task checkpoints, receipts, temporary TODO state, code snippets, file paths, git history, or speculation as durable memory.

---

## TL;DR

1. **Session start** → `set_context(collection="levara")`, then `wake_up(max_tokens=300)`.
2. **Every decision/discovery/event** → immediately `save_memory(...)` with `room` and `hall`. Don't batch — save in the moment.
3. **Critical facts** → `pin_memory(key, priority=8-10)` so they appear in future `wake_up`.
4. **Recall before research** → `recall_memory(query, room=, hall=)` to surface prior decisions before reinventing.
5. **Never save** code/paths/git history — those live in source.

Delegated-agent exception: a subagent returns confirmed durable-memory
candidates to the main agent instead of mutating shared project memory. The main
agent must validate, de-duplicate, and save a qualifying decision/discovery/fact
immediately after receiving the result. This preserves the immediate-save rule
without using shared memory as an inter-agent coordination channel.

---

## The room × hall model

Two orthogonal axes. Always fill both when saving — empty fields kill recall precision.

| Axis | Question it answers | Type |
|---|---|---|
| **room** | *About what?* (subsystem, topic) | Free string: `auth`, `deploy`, `ocr-bench`, `mcp`, `kg-temporal`, ... |
| **hall** | *What kind of fact?* (genre) | Controlled vocab (validation enforced) |

### Hall vocabulary

| hall | When to use | Example |
|---|---|---|
| `fact` | Objective stable property | "Levara HNSW dim=1024", "Pi IP 10.23.0.53" |
| `event` | Something happened at a specific time (always include date in value) | "2026-04-07 shipped memory-palace features" |
| `decision` | Architectural/project choice + WHY | "chose SQLite over Postgres because Pi RAM limit" |
| `preference` | User stylistic preference | "respond in Russian, terse, no emojis" |
| `advice` | Reusable practical rule | "before WAL changes — snapshot first" |
| `discovery` | Non-obvious insight, bug, gotcha worth remembering | "fasthttp breaks io.Closer pool; fix via QArgs" |

`save_memory` returns an error on unknown halls. Adding new ones is a deliberate
code change in `internal/http/mcp_palace.go`.

---

## Save triggers — when to call `save_memory` proactively

These are hard rules. When any of these conditions fire, call `save_memory`
**immediately**, not at end of conversation.

| Trigger | Action |
|---|---|
| User makes an architectural decision ("let's go with X, not Y") | `save_memory(hall="decision", room="<subsystem>")` — include the **why** |
| You found a root cause after debugging | `save_memory(hall="discovery", room="<subsystem>")` — symptom + cause + fix |
| User corrects your approach or specifies a style | `save_memory(hall="preference")` + `pin_memory(priority=10)` if global |
| New service/endpoint/IP/port appears | `save_memory(hall="fact")` + `pin_memory` if critical infra |
| Significant milestone completed (feature, refactor, release) | `save_memory(hall="event")` with absolute date in value |
| You gave the user a reusable recommendation | `save_memory(hall="advice")` |
| User mentions deadline, freeze window, external dependency | `save_memory(hall="event")` with absolute date |

### Do NOT save

- Code, file paths, function names — `git`/`grep` are authoritative; will go stale on rename.
- Git history, blame, who-changed-what — `git log` exists.
- In-progress task state — that's `TaskCreate`, not memory.
- Anything already in CLAUDE.md auto-memory (style, no_skip_tests, etc).
- Speculative future features.

---

## Pin policy

`pin_memory(key, priority)` marks a record so it always appears in `wake_up`.
Use sparingly — `wake_up` is bounded by `max_tokens`.

| priority | Use for |
|---|---|
| **10** | Global user preferences (style, language, hard rules) |
| **8** | Critical infrastructure (endpoints, IPs, ports, versions) |
| **5** | Currently-active major decisions |
| **1-3** | Optional contextual hints |

If `wake_up` becomes noisy → `unpin_memory(key)` for stale entries.

---

## Recall patterns

| Question | Command |
|---|---|
| "What did we decide about auth?" | `recall_memory(query="auth", hall="decision")` |
| "What are my style preferences?" | `list_memories(hall="preference")` |
| "What bugs hit migrations?" | `recall_memory(query="migration", hall="discovery")` |
| "Everything about deploy" | `list_memories(room="deploy")` |
| "Across multiple projects" | `cross_search(collections=["levara","other"])` |
| "Current owner of service X" | `query_entity(name="X")` |
| "Owner of X six months ago" | `query_entity(name="X", as_of="2025-10-01T00:00:00Z")` |

Recall **before** researching unfamiliar code or architecture — saves time
and ensures consistency with prior decisions.

---

## Knowledge graph: temporal validity

When `cognify` extracts entities and edges, edges carry validity windows.

- `query_entity(name)` — only currently-valid edges.
- `query_entity(name, as_of=ISO8601)` — snapshot at that time.
- Edges in the **exclusive relationships** whitelist auto-supersede on insert:
  `assigned_to, role_is, status_is, located_in, lives_in, works_at, owns,
  reports_to, current_state, is_a`. When a new edge with same source+rel
  appears, prior edges get `valid_until=now`, `superseded_by=<new id>`.
- Non-exclusive relations (`knows`, `mentions`, `related_to`) coexist
  meaningfully — never auto-superseded.

Extending the exclusive list = code change in
`pkg/orchestrator/pgupsert.go:exclusiveRelationships`.

---

## Per-agent diaries

Specialized subagents (reviewer, architect, oncall, planner) can keep an
isolated memory namespace under `owner_id="agent:<name>"`:

```
diary_write(agent="reviewer", key="schema_pr_27",
            value="CREATE INDEX vs ALTER TABLE order bug found")

diary_read(agent="reviewer", query="schema")
```

Use when an agent does repeated work (review, monitoring, planning) and wants
its own running context without polluting project-wide memory.

---

## Search with metadata filters

`search` accepts `room` and `tags`. With a filter set, HNSW overfetches ×3 and
post-filters chunks by metadata. Use this when the collection is large and
unfiltered search returns mixed results from unrelated rooms.

```
search(search_query="rate limiting", room="auth", tags=["security"])
```

`add` and `cognify` accept the same `room`/`tags` so chunks carry that metadata
into the vector store.

---

## Tool catalog (25 MCP tools)

**Knowledge graph & search:** `cognify`, `cognify_status`, `search`,
`cross_search`, `query_entity`, `analyze_commits`, `git_search`, `codify`

**Data ingestion:** `add`, `list_data`, `delete`, `prune`

**Memory palace:** `save_memory`, `recall_memory`, `list_memories`,
`pin_memory`, `unpin_memory`, `wake_up`, `diary_write`, `diary_read`

**Chat history:** `save_chat`, `recall_chat`, `search_chats`

**Context & sync:** `set_context`, `get_project_context`, `sync`,
`add_feedback`, `get_feedback_stats`

---

## Sync (Mac ↔ Pi)

- Mac (`localhost:8081`) ↔ Pi (`10.23.0.53:8080`)
- `sync(remote_url="http://10.23.0.53:8080/api/v1", direction="pull")`
- CLI shortcuts: `sync_levara` / `man_levara`

Sync is bidirectional but defaults to `memories + interactions + graph` and
**excludes vector collections** (those require re-embedding and must be
explicitly opted in via `types=["collections"]` + `collections=[...]`).

---

## Anti-patterns to avoid

1. **Saving with empty room/hall** — record becomes invisible to filtered recall.
2. **Saving the same fact in multiple halls** — pick one. Decisions go in `decision`, the resulting fact goes in `fact`, not both.
3. **Pinning everything** — wake_up budget runs out. Pin only what you'd want loaded in the first 200 tokens of every session.
4. **Saving code snippets** — store the *decision* and *why*, not the implementation.
5. **Forgetting `set_context` at session start** — saves end up in the wrong collection.
6. **Saving relative dates** — always convert "yesterday" / "last week" to absolute ISO date in value.

---

## Official Codex subagents

Use Codex's built-in subagent workflow for complex work that benefits from
independent exploration, disjoint implementation, or independent review. This
is the project orchestration mechanism; do not use `codex-flow`, `.hermes`, or a
third-party dynamic-workflow runner unless the user explicitly requests it.

Project-scoped roles live in `.codex/agents/`:

- `levara_code_mapper` — read-only architecture and root-cause mapping;
- `levara_implementation_worker` — one narrow implementation scope with focused
  tests;
- `levara_regression_reviewer` — read-only independent correctness and
  regression audit.

Built-in `explorer`, `worker`, and `default` remain available when a custom role
does not fit. The main agent owns decomposition, integration, final verification,
and the user-facing result.

### Delegation rules

1. Delegate only concrete, bounded work that can make progress independently.
   Parallel read-heavy exploration and review are preferred.
2. Before spawning write agents, assign an explicit non-overlapping file set to
   each agent. If two tasks need the same file, run them sequentially or give
   the file to one owner.
3. A delegated read-only agent must not edit files or mutate shared Levara
   memory/Task state. It returns confirmed discoveries and memory candidates to
   the main agent for de-duplication and persistence.
4. A write agent must preserve the dirty worktree, stay inside its allowed
   files, and stop when correct work requires scope expansion.
5. Subagents do not commit, push, publish, deploy, restart production services,
   apply live migrations, or expand authority. Those actions require the same
   explicit user authority as in the main thread.
6. The main agent waits for every required result, reviews the actual combined
   diff, resolves conflicts, runs integration-level checks, and reports both
   passed evidence and remaining gaps.
7. An agent result is not a receipt. Do not mark a task criterion complete from
   a summary alone; use actually observed, current-revision evidence.
8. For SQL-backed changes, PostgreSQL and SQLite mirrors, constraints, indexes,
   placeholder rewriting, transactions, and rollback behavior are one review
   unit.
9. For public MCP changes, verify descriptor, input/output schema, dispatch,
   profile/feature-flag visibility, backward-compatible result/error shapes,
   and generated-contract drift. Generated contract files have one sequential
   owner and are never hand-edited.
10. Shared memory is not a coordination channel. A subagent mutates Task Runtime
    only when the assignment supplies the exact task/step/version/lease context;
    summaries alone are never receipts.
11. A subagent must not spawn additional subagents unless the main agent
    explicitly delegates that authority and reserves the required concurrency
    slots.

### Delegated task template

Every subagent assignment should state:

```text
Role:
Concrete objective:
Context and confirmed facts:
Allowed files or read scope:
Forbidden actions:
Required invariants:
Corner cases:
Tests or checks to run:
Expected result for the main agent:
May edit code: yes/no
Must wait for another agent: agent name/no
```

The returned report must contain: outcome, evidence, changed files (if any),
tests with observed results, residual risks, blockers, and any durable-memory
candidates. Keep raw exploratory logs in the subagent thread; return a distilled
summary to the main thread.

---

## MCP Tools

<!-- BEGIN: contract-mcp -->

| Tool | Group | Status |
|---|---|---|
| add | data | canonical |
| add_feedback | feedback | canonical |
| analyze_commits | git | canonical |
| check_drift | data | canonical |
| codify | cognify | canonical |
| cognify | cognify | canonical |
| cognify_status | cognify | canonical |
| consolidate | memory | canonical |
| consolidation_revert | memory | canonical |
| consolidation_status | memory | canonical |
| cross_search | search | canonical |
| delete | data | canonical |
| delete_memory | memory | canonical |
| diary_read | diary | canonical |
| diary_write | diary | canonical |
| doctor | ops | canonical |
| get_feedback_stats | feedback | canonical |
| get_project_context | context | canonical |
| git_search | git | canonical |
| heartbeat | ops | canonical |
| ingestion_status | ops | canonical |
| levara_instructions | ops | canonical |
| list_communities | search | canonical |
| list_data | data | canonical |
| list_memories | memory | canonical |
| memory_garden | memory | canonical |
| memory_index_retry | ops | canonical |
| memory_index_status | ops | canonical |
| memory_markdown_digest | memory | canonical |
| memory_scaffold_block | memory | canonical |
| pin_memory | memory | canonical |
| prune | data | canonical |
| prune_graph | git | canonical |
| query_entity | search | canonical |
| recall_chat | chat | canonical |
| recall_memory | memory | canonical |
| recent_errors | ops | canonical |
| reconcile_memory | ops | canonical |
| runtime_stats | ops | canonical |
| save_chat | chat | canonical |
| save_memory | memory | canonical |
| search | search | canonical |
| search_chats | chat | canonical |
| set_context | context | canonical |
| supersede_memory | memory | canonical |
| sync | sync | canonical |
| sync_status | sync | canonical |
| task_bootstrap | task | canonical |
| task_checkpoint | task | canonical |
| task_complete | task | canonical |
| task_open | task | canonical |
| task_plan | task | canonical |
| task_receipt | task | canonical |
| task_step | task | canonical |
| task_validate | task | canonical |
| unpin_memory | memory | canonical |
| wake_up | memory | canonical |
| workspace_access_check | workspace | canonical |
| workspace_audit_log | workspace | canonical |
| workspace_commit | workspace | canonical |
| workspace_conflicts | workspace | canonical |
| workspace_context | workspace | canonical |
| workspace_context_artifacts | workspace | canonical |
| workspace_delete | workspace | canonical |
| workspace_enqueue_index_job | workspace | canonical |
| workspace_gc | workspace | canonical |
| workspace_index | workspace | canonical |
| workspace_index_jobs | workspace | canonical |
| workspace_log | workspace | canonical |
| workspace_manifest | workspace | canonical |
| workspace_ops_status | workspace | canonical |
| workspace_read | workspace | canonical |
| workspace_reconcile | workspace | canonical |
| workspace_reindex_artifacts | workspace | canonical |
| workspace_reindex_paths | workspace | canonical |
| workspace_retry_index_job | workspace | canonical |
| workspace_revert | workspace | canonical |
| workspace_run_get | workspace | canonical |
| workspace_run_start | workspace | canonical |
| workspace_search | workspace | canonical |
| workspace_watch_status | workspace | canonical |
| workspace_write | workspace | canonical |

<!-- END: contract-mcp -->

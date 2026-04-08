# AGENTS.md — Levara MCP Memory playbook

This file is the canonical guide for any AI agent (Claude, Cursor, Cline, ChatGPT
via MCP, custom Agent SDK clients) on how to use the Levara MCP memory layer
effectively. The goal: nothing important from a session should ever be lost,
and every future session should be able to reconstruct context cheaply.

Mirror of the "Levara MCP Memory" section in `CLAUDE.md`. Update both together.

---

## TL;DR

1. **Session start** → `set_context(collection="levara")`, then `wake_up(max_tokens=300)`.
2. **Every decision/discovery/event** → immediately `save_memory(...)` with `room` and `hall`. Don't batch — save in the moment.
3. **Critical facts** → `pin_memory(key, priority=8-10)` so they appear in future `wake_up`.
4. **Recall before research** → `recall_memory(query, room=, hall=)` to surface prior decisions before reinventing.
5. **Never save** code/paths/git history — those live in source.

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

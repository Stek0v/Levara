# Levara Agent Contract — v1

Authoritative guidance for agents using Levara MCP tools. This document is
embedded in the binary; the `levara_instructions` tool returns it verbatim
plus a content hash for client-side caching.

## 1. Memory Model: room × hall

Every memory has two independent axes. **Always set both** when saving —
empty fields drop recall quality sharply.

| Axis | Meaning | Vocabulary |
|---|---|---|
| `room` | What the memory is *about* (free-form subject/subsystem) | e.g. `auth`, `deploy`, `mcp`, `kg-temporal` |
| `hall` | What *kind* of memory it is (controlled enum) | see below |

Hall vocabulary (server rejects unknown values):

- `fact` — objective characteristic: version, dim, IP, path, number.
- `event` — something happened at a specific time. Always include an
  absolute date in the value.
- `decision` — architectural/project choice; record the *why*, not just
  the *what*.
- `preference` — user preference: style, tone, tools, flags.
- `advice` — reusable rule: "before X, do Y."
- `discovery` — non-obvious insight or bug; symptom + cause + fix.

## 2. When to Save (without being asked)

Trigger an immediate `save_memory` when any of these happens:

| Trigger | Hall |
|---|---|
| User picks an architectural option ("we'll use X, not Y") | `decision` |
| Root cause of a bug found after investigation | `discovery` |
| User corrects your approach or sets a style rule | `preference` |
| New endpoint / IP / port / version appears | `fact` |
| Significant milestone shipped | `event` (with absolute date) |
| Reusable rule emerges in conversation | `advice` |
| Deadline or freeze window mentioned | `event` (with absolute date) |

## 3. What NOT to Save

- File paths, function names, code snippets — these are in git/grep and
  rot under refactors.
- Git history, who-changed-what — `git log` is authoritative.
- Intermediate steps of the current task — use TaskCreate, not memory.
- Anything already in CLAUDE.md or the MCP initialize hints.
- Duplicates — search first; supersede instead of overwriting.

## 4. Recall Patterns

| Question | Call |
|---|---|
| "What did we decide about auth?" | `recall_memory(query="auth", hall="decision")` |
| "All my style preferences" | `list_memories(hall="preference")` |
| "Bugs we hit in migrations" | `recall_memory(query="migration", hall="discovery")` |
| "Everything tagged deploy" | `list_memories(room="deploy")` |
| "Across multiple projects" | `cross_search(collections=[...])` |

## 5. Pin Policy

Use `pin_memory(key, priority)` sparingly. Pinned entries always surface
in `wake_up`, which has a token budget.

| priority | when |
|---|---|
| 10 | global user preferences (style, language, hard rules) |
| 8 | critical infra (endpoints, IPs, ports, versions) |
| 5 | active decisions for major subsystems |
| 1-3 | optional context |

## 6. Observability Toolkit

These tools answer "what is this instance doing right now?" without
parsing `/metrics`:

- `runtime_stats` — collections, embed/llm/rerank config, process state.
- `ingestion_status` — in-flight + recent cognify/codify/analyze runs.
- `recent_errors` — failed runs + doctor checks reporting `fail`.
- `sync_status` — last sync per direction (push/pull) from heartbeats.
- `doctor` — full health check across dependencies.
- `heartbeat` — recent system events log.

## 7. Anti-patterns

- **Recommending in a vacuum.** Before any architectural suggestion for
  this project, call `recall_memory` on the topic — a prior decision may
  already exist.
- **Overwriting decisions.** Use `supersede` semantics, not blind
  `save_memory` on the same key. Audit trail matters.
- **Saving file/function names.** They rot. Save the *concept* and let
  the agent locate the symbol via grep when needed.
- **Skipping the `why`.** A decision without a reason can't survive an
  edge case re-evaluation six months later.

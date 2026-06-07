# Levara — Self-Hosted MCP Memory Server for AI Coding Assistants

> Single Go binary. Knowledge graph + vector + hybrid search. 15 MCP tools.
> Runs on Raspberry Pi 5. Zero cloud dependencies. Your code stays yours.

---

## Why This Exists

AI coding assistants (Claude Code, Cursor, Windsurf) are powerful — but they forget everything between sessions. Every new conversation starts from zero. The assistant re-reads files, re-discovers architecture, re-learns your conventions.

Built-in memory systems are primitive:
- **Claude Code**: CLAUDE.md files (static text, manually maintained)
- **Cursor**: .cursorrules + @codebase (file-level indexing, no relationships)
- **Windsurf**: workspace memories (volatile, no semantic search)

None of them offer:
- Semantic search over accumulated project knowledge
- Entity-relationship graphs (who calls what, what depends on what)
- Cross-session persistence of decisions and context
- Git history awareness
- Per-project isolation with team sharing
- Privacy (all data leaves your machine to cloud providers)

**Levara solves all of these** as a self-hosted MCP memory server.

---

## Architecture

```
Developer Machine                         Levara Server (Pi 5 / local / VPS)
┌──────────────────┐                     ┌─────────────────────────────────┐
│ Claude Code      │                     │ Levara (single Go binary, 30MB) │
│ or Cursor        │   MCP (HTTP)        │                                 │
│                  │ ◄─────────────────► │  ┌─ MCP Server (15 tools)       │
│ .mcp.json        │                     │  ├─ HNSW Vector Search          │
│ configured with  │                     │  ├─ BM25 Keyword Search         │
│ Levara endpoint  │                     │  ├─ Knowledge Graph (SQL)       │
└──────────────────┘                     │  ├─ Cognify Pipeline            │
                                         │  ├─ Memory Store                │
                                         │  ├─ Chat Persistence            │
                                         │  ├─ Git Analyzer                │
                                         │  └─ WAL Durability              │
                                         │                                 │
                                         │  Collections:                   │
                                         │   topka/     (project A)        │
                                         │   levara/    (project B)        │
                                         │   my-app/    (project C)        │
                                         │                                 │
                                         │  Ollama (local LLM + embed):    │
                                         │   qwen3:0.6b (522MB)            │
                                         │   nomic-embed-text (274MB)      │
                                         └─────────────────────────────────┘
```

**One project folder = one collection.** Each collection is an isolated HNSW index + WAL + graph. Search in collection A never returns data from collection B.

---

## What Problems Does It Solve?

| Problem | Without Levara | With Levara |
|---------|---------------|-------------|
| **Context window overflow** | Entire codebase can't fit; manual file selection | Semantic search retrieves only relevant chunks on demand |
| **Session amnesia** | Each conversation starts from zero | Accumulated knowledge persists: decisions, architecture, patterns |
| **No architecture understanding** | Must re-read code every session | Knowledge graph of entities + relationships persists |
| **Lost decisions** | "Why did we choose X?" — gone after session | Decisions stored as memories, queryable forever |
| **No git awareness** | Assistant doesn't know commit history | Commit analysis: who changed what, when, why |
| **No team sharing** | Memory is per-user, per-machine | Shared server, team queries same knowledge base |
| **Privacy concerns** | Cloud memory services see your code | Self-hosted, data never leaves your network |
| **Vendor lock-in** | Tied to one assistant's memory format | Standard MCP protocol — works with any MCP client |

---

## 15 MCP Tools

Levara exposes 15 tools via MCP (JSON-RPC 2.0 over HTTP):

### Knowledge Ingestion

| Tool | What It Does | Example |
|------|-------------|---------|
| **cognify** | Transform text into knowledge graph (entities + triplets + vectors) | `cognify(data="README.md contents", collection="my-app")` |
| **add** | Stage text data for later processing | `add(data="API spec", dataset_name="api-docs")` |
| **analyze_commits** | Parse git history into searchable knowledge | `analyze_commits(repo_path="/home/dev/my-app", since="2024-01-01")` |

### Search

| Tool | What It Does | Example |
|------|-------------|---------|
| **search** | Multi-strategy search (7 modes) | `search(query="auth middleware", search_type="HYBRID", collection="my-app")` |
| **git_search** | Search analyzed commit history | `git_search(query="who changed the database schema")` |

### Memory

| Tool | What It Does | Example |
|------|-------------|---------|
| **save_memory** | Store key-value memories (user/project/feedback) | `save_memory(key="tech_stack", value="Next.js + Go", type="project")` |
| **recall_memory** | Search memories semantically | `recall_memory(query="what framework")` |
| **list_memories** | List all stored memories | `list_memories(type="project", collection="my-app")` |

### Chat History

| Tool | What It Does | Example |
|------|-------------|---------|
| **save_chat** | Persist conversation messages | `save_chat(session_id="abc", messages=[...])` |
| **recall_chat** | Retrieve chat by session | `recall_chat(session_id="abc")` |
| **search_chats** | Full-text search across all chats | `search_chats(query="database migration")` |

### Management

| Tool | What It Does |
|------|-------------|
| **list_data** | Enumerate datasets and collections |
| **delete** | Remove dataset by ID |
| **prune** | Full system reset |
| **cognify_status** | Check async pipeline progress |

---

## 15 Search Types

Levara doesn't just do vector search. It offers 15 strategies that combine different signals:

| Type | How It Works | Best For |
|------|-------------|----------|
| **CHUNKS** | Pure vector similarity (HNSW) | "Find code similar to X" |
| **CHUNKS_LEXICAL** | BM25 keyword matching | Exact identifiers, error messages |
| **HYBRID** | Vector + BM25 with RRF fusion | General-purpose (recommended default) |
| **RAG_COMPLETION** | Vector search → LLM generates answer | "How does auth work?" |
| **GRAPH_COMPLETION** | Vector → entity extraction → 1-hop graph → LLM | "What depends on UserService?" |
| **GRAPH_COMPLETION_CONTEXT_EXTENSION** | 2-hop graph traversal → LLM | Deep relationship discovery |
| **GRAPH_COMPLETION_COT** | Chain-of-thought: decompose → multi-search → synthesize | Complex questions requiring reasoning |
| **TRIPLET_COMPLETION** | Search subject-predicate-object triplets → LLM | "What calls what?" |
| **NATURAL_LANGUAGE** | LLM converts question to structured query → execute | Natural language over knowledge graph |
| **CYPHER** | Raw graph query (Cypher syntax) | Advanced graph queries |
| **TEMPORAL** | Time-aware search with date extraction | "What changed last week?" |
| **CODING_RULES** | Code entity search (functions, classes, imports) | Code conventions, patterns |
| **SUMMARIES** | Search document summaries | High-level overview |
| **FEELING_LUCKY** | Auto-select best strategy | "Just find it" |

---

## The Cognify Pipeline

When you call `cognify`, Levara transforms raw text into structured knowledge:

```
Input: "UserService authenticates via JWT. It calls TokenStore
        which uses Redis for session storage."
                    │
                    ▼
        ┌─── Chunking ───┐
        │ Split by paragraphs, functions, or sentences │
        └────────┬────────┘
                 │
                 ▼
        ┌─── LLM Extraction ───┐
        │ qwen3:0.6b extracts:                        │
        │ Entities: UserService, JWT, TokenStore, Redis │
        │ Relations: AUTHENTICATES_VIA, CALLS, USES    │
        └────────┬──────────────┘
                 │
          ┌──────┴──────┐
          ▼             ▼
   ┌─ Embedding ─┐  ┌─ Graph Write ─┐
   │ nomic-embed  │  │ graph_nodes   │
   │ → HNSW index │  │ graph_edges   │
   └──────────────┘  └───────────────┘
```

**Result**: text is searchable by meaning (vector), by keywords (BM25), and by relationships (graph). All three in one query.

---

## Integration with Claude Code

### Configuration

`.mcp.json` at project root (shared via git):
```json
{
  "mcpServers": {
    "levara": {
      "type": "http",
      "url": "http://raspberrypi.local:8080/mcp"
    }
  }
}
```

Or via CLI:
```bash
claude mcp add --transport http levara http://raspberrypi.local:8080/mcp
```

### How It Works in Practice

1. **Developer opens project** → Claude Code connects to Levara MCP
2. **"How does auth work?"** → Claude calls `search("auth", type="GRAPH_COMPLETION", collection="my-app")` → Levara returns entities + relationships + relevant code → Claude answers with project-specific knowledge
3. **Developer saves a decision** → Claude calls `save_memory(key="auth_choice", value="JWT with httpOnly cookies, refresh via Redis", type="project")`
4. **Next week, different session** → Claude calls `recall_memory("auth")` → gets the saved decision, stays consistent
5. **After a sprint** → `analyze_commits(repo_path=".", since="2024-03-01")` → all commits indexed, searchable by author, file, or change type

### Typical User Flow

```
User: "Explain the payment flow in this project"

Claude (internally):
  1. Calls search("payment flow", type="GRAPH_COMPLETION", collection="payments-api")
  2. Gets: PaymentService → calls StripeGateway → emits PaymentCompleted event
         → NotificationService listens → sends receipt email
  3. Synthesizes answer from graph context + code chunks

User: "Remember: we're migrating from Stripe to Adyen next quarter"

Claude (internally):
  1. Calls save_memory(key="payment_migration", value="Migrating from Stripe to Adyen, Q2 2024", type="project", collection="payments-api")

User (next month): "What payment provider should new code target?"

Claude (internally):
  1. Calls recall_memory("payment provider", collection="payments-api")
  2. Gets: "Migrating from Stripe to Adyen, Q2 2024"
  3. Answers: "Target Adyen — the team decided to migrate from Stripe."
```

---

## Integration with Cursor

`.cursor/mcp.json`:
```json
{
  "mcpServers": {
    "levara": {
      "url": "http://raspberrypi.local:8080/mcp"
    }
  }
}
```

Same tools, same protocol, same server. One Levara instance serves both Claude Code and Cursor simultaneously.

---

## Competitive Comparison

| Feature | **Levara** | mem0 | Python KG stack | Zep | Basic MCP servers |
|---------|-----------|------|--------|-----|-------------------|
| **Language** | Go (single binary) | Python | Python | Go + Postgres | Python/Node |
| **Deployment** | 1 process, 30MB | Python + Redis + external search | Python + Neo4j + external search | Go + Postgres | Script |
| **Runs on Pi 5** | **Yes (proven)** | No | No | No | Maybe |
| **MCP tools** | **15** | 4-5 | 3-4 | 5-6 | 2-3 |
| **Knowledge graph** | **Built-in** | No | Yes (Neo4j) | Partial | No |
| **Vector search** | **HNSW (built-in)** | External DB | External DB | pgvector | No |
| **BM25 search** | **Built-in** | No | No | No | No |
| **Hybrid search** | **Vector + BM25 + Graph** | No | Partial | No | No |
| **Search types** | **15** | 1 | 2-3 | 3-4 | 1 |
| **Per-project isolation** | **Native collections** | Manual | Namespace | Session-based | File-based |
| **Git analysis** | **Built-in** | No | No | No | No |
| **Chat persistence** | **Built-in** | No | No | Yes | No |
| **Crash recovery** | **WAL + snapshots** | Backend-dependent | Backend-dependent | Postgres | No |
| **Multi-node** | **WAL shipping** | No | No | Postgres repl. | No |
| **External dependencies** | **None** | Redis, external search, Python | Neo4j, external search, Python | Postgres | Varies |
| **Search latency** | **2.6ms** | 10-50ms | 50-200ms | 10-30ms | N/A |
| **Concurrent QPS** | **719** | ~50-100 | ~10-30 | ~100-200 | N/A |
| **Price** | **Free** | Free / $99+/mo | Free | Free / paid | Free |

### Why Levara Wins for Self-Hosted

1. **Zero dependencies**: One `go build` → one binary. No Python, Docker, Redis, Neo4j, Qdrant. Deploy in 30 seconds.
2. **Edge-deployable**: Proven on Raspberry Pi 5 (8GB, $80). No other solution runs a full knowledge graph + vector + BM25 on a $80 SBC.
3. **Search depth**: 15 search types including chain-of-thought multi-hop, graph traversal, context extension. Competitors offer 1-5.
4. **Performance**: 2.6ms search, 719 QPS. MCP tool calls are invisible within the LLM's 2-5 second inference time.
5. **Privacy by default**: Your code, your hardware, your network. Zero cloud telemetry.

---

## Deployment on Raspberry Pi 5

### Hardware
- Raspberry Pi 5, 8GB RAM
- microSD or NVMe HAT (NVMe recommended)
- Cost: ~$80 one-time

### Resource Budget

| Component | RAM |
|-----------|-----|
| OS + runtime | 1.5GB |
| Ollama + qwen3:0.6b (LLM) | 522MB |
| Ollama + nomic-embed-text (embed) | 274MB |
| Levara (HNSW + WAL + graph) | 50-200MB |
| **Free** | **~5.5GB** |

### Setup (5 minutes)

```bash
# 1. Install Ollama
curl -fsSL https://ollama.ai/install.sh | sh
ollama pull qwen3:0.6b
ollama pull nomic-embed-text

# 2. Download Levara
wget https://github.com/stek0v/levara/releases/latest/download/levara-linux-arm64
chmod +x levara-linux-arm64
mv levara-linux-arm64 /usr/local/bin/levara

# 3. Start
export OLLAMA_MAX_LOADED_MODELS=2  # critical: both models in RAM
export DB_PROVIDER=sqlite
export DB_PATH=$HOME/levara/data/levara.db
export EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
export EMBEDDING_MODEL=nomic-embed-text
export LLM_ENDPOINT=http://localhost:11434/v1
export LLM_MODEL=qwen3:0.6b

levara -standalone=true -dim=768 -shards=1 -port=8080 -data-dir=$HOME/levara/data
```

### Performance on Pi 5

| Metric | Value |
|--------|-------|
| Search latency (p50) | 2.87ms |
| Search latency (p99) | 21.5ms |
| Concurrent QPS | 652 |
| Cognify time (per document) | ~25s |
| Memory recall | 3.45ms |
| Real data test | 30/30 queries, 100% hit rate |

---

## Multi-Node Replication

For teams or high availability:

```bash
# Primary (accepts writes + serves reads)
levara -node-id=pi1 -port=8080

# Replica (streams WAL from primary, serves reads)
levara -node-id=pi2 -port=8080 -join-addr=pi1:8080
```

Replicas receive WAL entries in real-time (~10-50ms lag). Reads can go to any node. Writes go to primary (replicas forward automatically).

---

## When To Use Levara

### Use Levara when:
- You want persistent, semantic memory across coding sessions
- You care about privacy (no code to cloud)
- You work on multiple projects and need isolation
- You want git history awareness in your assistant
- You run on minimal hardware (Pi, small VPS, air-gapped)
- You want knowledge graph relationships, not just text matching

### Don't use Levara when:
- You need managed cloud service with zero ops (use mem0 Cloud)
- You need conversation-focused memory with user profiling (use Zep)
- You already have a mature Python knowledge-graph pipeline around Neo4j
- You only need simple key-value storage (use basic MCP memory server)

---

## Configuration Reference

### Claude Code (`.mcp.json`)
```json
{
  "mcpServers": {
    "levara": {
      "type": "http",
      "url": "http://${LEVARA_HOST:-localhost}:8080/mcp"
    }
  }
}
```

### Cursor (`.cursor/mcp.json`)
```json
{
  "mcpServers": {
    "levara": {
      "url": "http://${LEVARA_HOST:-localhost}:8080/mcp"
    }
  }
}
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PROVIDER` | postgres | `sqlite` for Pi, `postgres` for production |
| `DB_PATH` | — | SQLite file path |
| `EMBEDDING_ENDPOINT` | — | Ollama embedding URL |
| `EMBEDDING_MODEL` | — | `nomic-embed-text` (768 dim) |
| `LLM_ENDPOINT` | — | Ollama LLM URL |
| `LLM_MODEL` | — | `qwen3:0.6b` for Pi |
| `OLLAMA_MAX_LOADED_MODELS` | 1 | Set to `2` (critical for Pi) |

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | 8080 | HTTP API port |
| `-dim` | 128 | Vector dimension (768 for nomic) |
| `-shards` | 3 | HNSW shards (1 for Pi) |
| `-standalone` | true | WAL-only mode (no Raft) |
| `-node-id` | hostname | Unique node identifier |
| `-join-addr` | — | Primary address for replica mode |
| `-data-dir` | data | Persistent storage directory |

---

## Technical Details

### Data Flow

```
Claude Code asks: "What changed in auth module?"
        │
        ▼
MCP call: search("auth module changes", type="HYBRID", collection="my-app")
        │
        ▼
Levara:
  1. Embed query → nomic-embed-text → 768-dim vector     [0.3ms]
  2. HNSW search → top-K by cosine similarity             [1.5ms]
  3. BM25 search → top-K by keyword relevance             [0.5ms]
  4. RRF fusion → merge and re-rank                       [0.1ms]
  5. Return top 5-10 results with metadata                 [0.2ms]
        │                                               ─────────
        ▼                                               Total: ~2.6ms
Claude receives context, generates answer                  [2-5s LLM]
```

### Storage Layout

```
~/levara/data/
├── levara.db              # SQLite (memories, sessions, graph, metadata)
├── node1/
│   └── shard_0/
│       ├── meta.bin       # Vector metadata (DiskStore)
│       ├── meta.bin.wal   # Write-ahead log
│       └── raft/          # Raft consensus (if enabled)
└── collections/
    ├── my-app/
    │   ├── meta.bin       # Per-collection HNSW + vectors
    │   └── meta.bin.wal
    └── other-project/
        ├── meta.bin
        └── meta.bin.wal
```

### MCP Protocol

Levara implements MCP via JSON-RPC 2.0 at `POST /mcp`:

```json
// Request
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"search_query":"auth","collection":"my-app"}}}

// Response
{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"[{\"id\":\"chunk-1\",\"score\":0.95,\"text\":\"UserService authenticates via JWT...\"}]"}]}}
```

Supported methods: `initialize`, `tools/list`, `tools/call`, `notifications/initialized`.

---

## Summary

Levara is a self-hosted MCP memory server that gives AI coding assistants persistent, searchable, project-aware knowledge. It combines a knowledge graph, vector search, BM25, and 15 search strategies in a single Go binary that runs on a Raspberry Pi.

**For developers**: your coding assistant finally remembers what you told it last week.

**For teams**: shared knowledge base that every team member's assistant can query.

**For enterprises**: zero cloud dependencies, full data control, air-gap ready.

```
pip install nothing
docker pull nothing
just run the binary
```

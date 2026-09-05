# Tutorial 01 — First Memory

Use the SQLite-backed loopback server in [getting started](../getting-started.md).
No embedding or LLM service is needed for this lexical memory exercise.
The commands below are source-checked examples, not a claim of a current live run.
[Русский старт](00-getting-started-ru.md).

## 1. Save a scoped record

```bash
curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_memory","arguments":{"collection":"tutorial","room":"onboarding","hall":"fact","key":"first-memory","value":"The tutorial stores durable memory in SQLite."}}}'
```

Inspect the tool result, including `result.isError` or JSON-RPC `error`. An HTTP
200 alone is insufficient. With authentication enabled, include your bearer
credential; this example targets the deliberately unauthenticated local server.

`collection` selects project context, `room` the subject, and `hall` the kind of
record: `fact`, `decision`, `event`, `preference`, `advice`, or `discovery`.
A collection name is not an access-control boundary by itself.

## 2. Recall an exact term

```bash
curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_memory","arguments":{"collection":"tutorial","query":"SQLite","limit":3}}}'
```

Expect the key `first-memory` and its stored text. This tests the SQL/lexical
path; synonym-only semantic recall requires configured embeddings and a ready
index. Do not use a fixed sleep as proof that background indexing completed.

## 3. Recover context and verify persistence

```bash
curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wake_up","arguments":{"collection":"tutorial","max_tokens":300}}}'
```

Stop your foreground test server with Ctrl-C, restart the same command and
repeat recall. Use the same `DB_PATH`; memory resides in SQL, not the process.
Each sessionless call supplies its own collection. A normal MCP host can keep
session context through `set_context`.

## 4. Use memory for real work

Recall before investigating unfamiliar decisions. Save verified durable facts
and decisions with their reasons, rather than raw chat, secrets, code paths or
temporary task state. Follow [the memory skill](../memory-workflow-skill.md) and
the repository's memory contract. Do not repeatedly create demonstration records
in a shared project collection.

Continue with [agent integration](02-agent-integration.md),
[documents](03-knowledge-base.md), or [search strategies](../search-strategies-guide.md).

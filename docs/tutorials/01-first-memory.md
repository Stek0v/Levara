# Tutorial 01 — First Memory (15 minutes)

Every command in this tutorial was verified against a live Levara server
(main, 2026-09-03). Prerequisites: Go 1.26+ and PostgreSQL (any 14+), or
Docker for the database.

_Русская версия: [00-getting-started-ru.md](00-getting-started-ru.md)._

## 1. Start Levara

```bash
git clone https://github.com/Stek0v/Levara.git && cd Levara
make build

createdb levara_tutorial   # or: docker run -e POSTGRES_HOST_AUTH_METHOD=trust -p 5432:5432 postgres:16

./levara-server -profile=standalone-embed -port=8080 -grpc-port=0 \
  -data-dir=./data -node-id=tutorial \
  -pg-url="postgres://$(whoami)@localhost:5432/levara_tutorial?sslmode=disable"
```

Health check (in a second terminal):

```bash
curl -s http://127.0.0.1:8080/health
# {"health":"healthy","status":"ready","version":"levara-go"}
```

## 2. Save a memory via MCP

Levara speaks MCP. The fastest way to see memory work is a raw JSON-RPC call
to the stateless transport (no session needed):

```bash
curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: save_memory' \
  -d '{
    "jsonrpc":"2.0","id":1,"method":"tools/call",
    "params":{
      "name":"save_memory",
      "arguments":{
        "collection":"notes",
        "room":"tutorial",
        "hall":"fact",
        "key":"first-memory",
        "value":"Levara runs as one Go binary; memory survives agent restarts"
      },
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }'
```

Expected: `"index_status":"pending"` and a `Memory saved: first-memory = …`
message. The record is durable immediately; the vector index builds in the
background (a second or two).

The `room × hall` pair is Levara's taxonomy: `room` answers "about what?",
`hall` answers "what kind of fact?" (`fact`, `decision`, `event`,
`preference`, `advice`, `discovery`).

In practice you won't hand-write JSON — your MCP client (Claude Code, Cursor,
Codex) sends these calls. Tutorial 02 covers that.

## 3. Recall it

Semantic recall — ask a question, get the memory back even though no word
matches exactly:

```bash
sleep 3   # let the background index finish

curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: recall_memory' \
  -d '{
    "jsonrpc":"2.0","id":2,"method":"tools/call",
    "params":{
      "name":"recall_memory",
      "arguments":{"collection":"notes","query":"does memory persist restarts","limit":3},
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }' | grep -o 'first-memory'
# first-memory
```

## 4. Get a morning briefing

`wake_up` condenses a collection into a bounded briefing:

```bash
curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: wake_up' \
  -d '{
    "jsonrpc":"2.0","id":3,"method":"tools/call",
    "params":{
      "name":"wake_up",
      "arguments":{"collection":"notes","max_tokens":200},
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }'
```

The response includes `scope_status` and the pinned/recent records that fit
the token budget.

## 5. Verify persistence

Restart the server (`Ctrl-C`, start it again) and repeat step 3. The memory
is still there — it lives in Postgres, not in the process.

## Next

- [02-agent-integration.md](02-agent-integration.md) — connect Claude Code,
  Cursor, or Codex instead of curl
- [features-guide.md](../features-guide.md) — everything else the server can do

_Last verified: 2026-09-03 (standalone-embed, potion-code-16M, main branch)._

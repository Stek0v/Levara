# Tutorial 03 — Build a Knowledge Base (20 minutes)

Tutorial 01 stored discrete memories. This one ingests **documents** — raw
text or files — and makes them searchable, so an agent can answer questions
from your own material. Every call below was verified against a live server
(2026-09-03).

Prerequisite: a running server with the embed sidecar (the
`standalone-embed` profile from [01-first-memory.md](01-first-memory.md)).

## 1. Ingest a document

The `add` tool stores raw text into a dataset (on disk and in the DB
metadata):

```bash
curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: add' \
  -d '{
    "jsonrpc":"2.0","id":1,"method":"tools/call",
    "params":{
      "name":"add",
      "arguments":{
        "data":"Alice works at Acme Corp. Bob reports to Alice. Acme is based in Berlin.",
        "dataset_name":"company-notes"
      },
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }'
```

Response: `Data ingested into dataset 'company-notes' (items: 1)` with
`data_id` and `dataset_id`.

## 2. Cognify — chunk, embed, index

`add` stores bytes; `cognify` turns them into searchable knowledge: it
chunks the text, embeds the chunks into the vector store, and (when an LLM
endpoint is configured) extracts entities and relations into the temporal
knowledge graph.

```bash
curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: cognify' \
  -d '{
    "jsonrpc":"2.0","id":2,"method":"tools/call",
    "params":{
      "name":"cognify",
      "arguments":{
        "data":"Alice works at Acme Corp. Bob reports to Alice. Acme is based in Berlin.",
        "collection":"kb"
      },
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }'
# Cognify pipeline started. Run ID: 3a95aa6c-... Use cognify_status tool to check progress.
```

Poll until `status` is `COMPLETED`:

```bash
curl -s -X POST http://127.0.0.1:8080/mcp/2026-07-28 \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: cognify_status' \
  -d '{
    "jsonrpc":"2.0","id":3,"method":"tools/call",
    "params":{
      "name":"cognify_status",
      "arguments":{"run_id":"<RUN-ID-FROM-STEP-2>"},
      "_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"curl","version":"0"},
        "io.modelcontextprotocol/clientCapabilities":{}
      }
    }
  }'
# {"status":"COMPLETED","chunks_created":1,"entities_extracted":...,"elapsed_ms":21,...}
```

Note the honest detail: without an LLM endpoint configured, `entities_extracted`
is `0` — chunking and embeddings still work, graph extraction needs the LLM.

## 3. Ask questions

Keyword search (BM25, no embedding needed):

```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/search/text \
  -H 'Content-Type: application/json' \
  -d '{"query_text":"Alice Acme","top_k":3}'
```

Full search from an agent — the MCP `search` tool runs hybrid retrieval
(semantic + keyword fusion, optional rerank) and logs an interaction id:

> search: { "search_query": "who works at Acme", "collection": "kb", "limit": 3 }

The `collection` argument scopes results; omit it to search everything the
caller can access.

## 4. The knowledge graph (with LLM)

With an LLM endpoint configured (`LEVARA_LLM_*` env), cognify also builds
the graph: entities like `Alice`, `Acme Corp` with edges like
`works_at`, `reports_to`. Query it:

> query_entity: { "name": "Acme Corp" }

Edges carry validity windows; `query_entity` returns currently-valid edges,
and relations like `assigned_to` / `role_is` auto-supersede when new facts
arrive (old edges keep `valid_until` + `superseded_by` — history is never
deleted, just no longer current).

Without an LLM configured the graph stays empty — the search path is fully
functional regardless.

## 5. Clean up

```text
delete:   { "data_id": "<DATA-ID>" }          # remove ingested document
prune:    { ... }                             # bulk cleanup
```

## Next

- [04-team-deploy.md](04-team-deploy.md) — take this from your laptop to a
  shared team deployment
- [search-strategies-guide.md](../search-strategies-guide.md) — how routing
  between semantic, keyword, and graph search works

_Last verified: 2026-09-03 (main; cognify without LLM = chunks+embeddings only)._

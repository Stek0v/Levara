# agent-memory-app

Minimal end-to-end example: embed three notes with Ollama, insert them
into Levara, then run a semantic search. ~70 lines of Python, pure HTTP,
no SDK.

## Prerequisites

Run a local Ollama service separately and download `nomic-embed-text` using
Ollama's supported setup. The example hardcodes `localhost:11434`, that model
and the `mem0` collection in `main.py`; environment variables do not change them.
Its vectors have 768 dimensions, so start a matching fresh Levara test instance:

```bash
LEVARA_DIM=768 docker compose up -d --build   # from the repository root
```

Compose starts Levara and Prometheus, not Ollama. Do not reuse a `mem0` collection
created with a different dimension. The script sends no token; run only against
your isolated local development server, or adapt its requests for authenticated
use. This is a raw-vector example, not the document upload/sharing workflow.

## Run

```bash
cd examples/agent-memory-app
pip install -r requirements.txt
python main.py
```

Expected output:

```
→ embedding 3 notes via Ollama (nomic-embed-text)
  inserted note-cooking
  inserted note-travel
  inserted note-bug

→ query: 'How do I fix a leaking goroutine?'
  1. id=note-bug      score=0.6295  Goroutine leak: forgot to close the ticker channel ...
  2. id=note-travel   score=0.4360  The night train from Vienna to Venice ...
  3. id=note-cooking  score=0.4067  Tomato pasta cooks in nine minutes ...
```

## What it shows

| Capability | Endpoint |
|---|---|
| Insert vector + metadata | `POST /api/v1/insert` |
| Semantic search | `POST /api/v1/search` |
| Embeddings (external) | `POST {ollama}/api/embeddings` |

The example uses `mem0`; its vector dimension must match the configured
server/collection. Do not treat the example scores as a retrieval-quality target.

## Next steps

- To use another embedding provider, change `main.py`'s embedding request
  and configure a collection with the matching dimension; this script does
  not read an `EMBEDDING_PROVIDER` environment variable.
- Use Levara's MCP surface (`POST /mcp`) for richer tools like
  `cognify`, `recall_memory`, and `query_entity` — call the
  `levara_instructions` tool first to get the agent contract.
- For multi-collection workloads, pass `"collection": "<name>"` in the
  insert/search payloads — Levara will create the collection on first use.

For files, processing status and individual sharing, use
[document management](../../docs/document-management.md).

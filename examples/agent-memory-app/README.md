# agent-memory-app

Minimal end-to-end example: embed three notes with Ollama, insert them
into Levara, then run a semantic search. ~70 lines of Python, pure HTTP,
no SDK.

## Prerequisites

The local LevaraOS stack must be running:

```bash
make stack-dev   # from the repo root
```

This brings up Levara on `:8080` and Ollama on `:11434` with the
`nomic-embed-text` model preloaded.

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

The default collection `mem0` is created automatically by the stack at
boot time (`embedding_dim=768`, cosine distance).

## Next steps

- Swap `nomic-embed-text` for `text-embedding-3-large` by setting
  `EMBEDDING_PROVIDER=openai` in `.env` and re-running `make stack-dev`.
- Use Levara's MCP surface (`POST /mcp`) for richer tools like
  `cognify`, `recall_memory`, and `query_entity` — call the
  `levara_instructions` tool first to get the agent contract.
- For multi-collection workloads, pass `"collection": "<name>"` in the
  insert/search payloads — Levara will create the collection on first use.

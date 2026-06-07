# Levara Examples

Self-contained starters that run against the local stack started by
`docker compose up -d --build`.

| Example | Stack | What it shows |
|---|---|---|
| [`agent-memory-app/`](agent-memory-app/) | Python + HTTP | Embed → insert → semantic search via Levara's REST API. ~70 LOC. |
| [`rag-with-temporal-kg/`](rag-with-temporal-kg/) | Python + HTTP + MCP | Cognify two facts that update the same exclusive relation, then read the active view and a historical `as_of` snapshot via `query_entity`. |

Each example is independent — pick the one closest to your use case
and copy it into your own project as a starting point.

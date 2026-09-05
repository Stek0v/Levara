# Levara Examples

Small API starters. The base Compose file starts Levara and Prometheus; it
does not install Ollama or download models. Read each example's provider and
embedding-dimension requirements before running it. These scripts are local
development examples and do not send authentication credentials.

| Example | Stack | What it shows |
|---|---|---|
| [`agent-memory-app/`](agent-memory-app/) | Python + HTTP | Embed → insert → semantic search via Levara's REST API. ~70 LOC. |
| [`rag-with-temporal-kg/`](rag-with-temporal-kg/) | Python + HTTP + MCP | Cognify two facts that update the same exclusive relation, then read the active view and a historical `as_of` snapshot via `query_entity`. |

Each example is independent — pick the one closest to your use case
and copy it into your own project as a starting point. For a supported user
workflow rather than raw API demonstrations, follow
[document management](../docs/document-management.md) and
[document acceptance scenarios](../docs/document-workflow-scenarios.md).

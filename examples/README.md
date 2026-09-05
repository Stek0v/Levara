# Levara Examples

Small API starters. The base Compose file starts Levara and Prometheus; it
does not install Ollama or download models. Read each example's provider and
embedding-dimension requirements before running it. These scripts are local
development examples and do not send authentication credentials.

| Example | Stack | What it shows |
|---|---|---|
| [`agent-memory-app/`](agent-memory-app/) | Python + HTTP | Embed → insert → semantic search via Levara's REST API. A raw-vector example, not durable memory APIs. |
| [`rag-with-temporal-kg/`](rag-with-temporal-kg/) | Python + HTTP + MCP | Cognify two facts that update the same exclusive relation, then read the active view and a historical `as_of` snapshot via `query_entity`. |

Each example is independent — pick the one closest to your use case
and copy it into your own project as a starting point. For a supported user
workflow rather than raw API demonstrations, follow
[document management](../docs/document-management.md) and
[document acceptance scenarios](../docs/document-workflow-scenarios.md).

For Markdown project operations see [agent-host examples](agent-hosts/README.md).
For a SQL-backed local start use [getting started](../docs/getting-started.md);
for isolated versus live checks see [testing](../docs/testing.md). The temporal
graph example additionally needs SQL/Neo4j and an LLM; base Compose alone is
insufficient.

---
inclusion: manual
---

# Exploring Codebases with Gortex

## Workflow

1. `graph_stats` — confirm index, get node/edge counts
2. `get_communities` — see functional clusters (architecture overview)
3. `search_symbols({query: "<concept>"})` — find symbols related to a concept
4. `get_processes` — discover execution flows
5. `get_process({id: "<process-id>"})` — trace a specific flow step by step
6. `get_editing_context({path: "<file>"})` — deep dive on a specific file

## When to use

- "How does authentication work?"
- "What's the project structure?"
- "Show me the main components"
- Understanding code you haven't seen before

## Key tools

- `get_communities` for architectural overview (functional clusters with cohesion scores)
- `get_processes` for execution flow discovery (entry points to call chains)
- `search_symbols` for concept-based symbol search (BM25 + camelCase-aware)
- `get_editing_context` for 360-degree file view (symbols, callers, callees, imports)

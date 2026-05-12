# Levara Markdown Workspace Agent Instructions

Use this when Levara is connected through MCP and the project uses the
markdown-native workspace.

1. At session start, call `workspace_context` with the active `project_id` and
   `branch` when known. Use its `active_generation`, `active_collection`,
   `recommended_search_type`, watcher status, job status, and
   `exact_read_required` policy.
2. Before a write, revert, reindex, GC, or run-artifact workflow, call
   `workspace_access_check` with `access="write"`.
3. For project questions, call `workspace_search` first. Prefer the search type
   returned by `workspace_context`; default to `HYBRID`.
4. Never treat a search hit as source of truth. Read the exact markdown file
   with `workspace_read` before answering or making a decision.
5. If `workspace_search.freshness.stale` or
   `workspace_search.freshness.potentially_stale` is true, reconcile before
   relying on the result, or clearly report that the index may lag Markdown.
6. For OpenAPI, DDL, Terraform, ADR, and runbook context, call
   `workspace_context_artifacts`; use `workspace_reindex_artifacts` when those
   configured artifacts need to be published into search.
7. Every answer based on search must include the result `citation` and first
   verify the exact source with `workspace_read` using `citation.read_args`.
8. Before team write workflows, call `workspace_conflicts`; when available,
   pass `expected_file_digest` from a prior read to `workspace_write`.
9. For operational triage, call `workspace_ops_status` to inspect watcher lag,
   indexing job backlog, dead-letter jobs, and audit volume.
10. Write durable project knowledge as Markdown with `workspace_write`; keep
   generated notes under `memory/generated/` until a human promotes them.
11. After meaningful documentation or memory changes, call `workspace_commit`
   with a concise message.
12. Use `workspace_audit_log` only for operational/security review. Do not copy
   markdown text, search queries, snippets, or exact file paths into external
   audit notes.

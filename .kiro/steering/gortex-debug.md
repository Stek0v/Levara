---
inclusion: manual
---

# Debugging with Gortex

## Workflow

1. `search_symbols({query: "<error or suspect>"})` — find related symbols
2. `get_callers({id: "<suspect>"})` — who calls it?
3. `get_call_chain({id: "<suspect>"})` — what does it call?
4. `get_editing_context({path: "<file>"})` — full file context
5. `get_process({id: "<process>"})` — trace execution flow

## Debugging patterns

| Symptom              | Gortex Approach |
| -------------------- | --------------- |
| Error message        | `search_symbols` for error-related names, then `get_callers` on throw sites |
| Wrong return value   | `get_call_chain` on the function, trace callees for data flow |
| Intermittent failure | `get_editing_context`, look for external calls and async deps |
| Performance issue    | `find_usages`, find symbols with many callers (hot paths) |
| Recent regression    | `detect_changes`, see what your changes affect |

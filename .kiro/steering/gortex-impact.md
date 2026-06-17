---
inclusion: manual
---

# Impact Analysis with Gortex

## Workflow

1. `search_symbols({query: "X"})` — find the symbol ID
2. `explain_change_impact({ids: "<id1>, <id2>"})` — risk-tiered blast radius
3. `get_dependents({id: "<symbol-id>", depth: 3})` — detailed dependent tree
4. `detect_changes({scope: "staged"})` — pre-commit check

## Risk tiers

| Depth | Risk Level     | Meaning                  |
| ----- | -------------- | ------------------------ |
| d=1   | WILL BREAK     | Direct callers/importers |
| d=2   | LIKELY AFFECTED| Indirect dependencies    |
| d=3   | MAY NEED TESTING| Transitive effects      |

## Before any non-trivial change

- Call `explain_change_impact` with all symbols you plan to modify
- Review the risk level (LOW/MEDIUM/HIGH/CRITICAL)
- Check `by_depth`: d=1 items WILL BREAK
- Note `affected_processes` and `affected_communities`
- Check `test_files` that need re-running
- Before commit: `detect_changes` to verify scope

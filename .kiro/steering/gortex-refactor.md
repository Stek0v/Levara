---
inclusion: manual
---

# Refactoring with Gortex

## Workflow

1. `search_symbols({query: "X"})` — find the symbol ID
2. `explain_change_impact({ids: "<id>"})` — map blast radius
3. `get_editing_context({path: "<file>"})` — see all symbols and relationships
4. `find_usages({id: "<id>"})` — every reference to change
5. `get_edit_plan({ids: "<ids>"})` — dependency-ordered edit sequence
6. Edit in order: interfaces -> implementations -> callers -> tests
7. `detect_changes({scope: "all"})` — verify after changes

## Rename symbol

- `find_usages` to get every reference location
- `explain_change_impact` to assess blast radius
- Edit in dependency order: definition, then callers, then tests

## Extract module

- `get_editing_context` on the source file to see all symbols
- `get_dependents` on symbols to extract to find external callers
- `find_import_path` for correct import paths in the new location

## Split function/service

- `get_call_chain` to understand all callees
- `get_callers` to map all call sites that need updating
- `explain_change_impact` for full blast radius

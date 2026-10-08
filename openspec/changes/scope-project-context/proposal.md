# Proposal

## Why

T05 requires project context to return only current memories accessible to the caller. Existing main/related queries omit owner and active-state filters, global graph/interactions and unguarded vector statistics have no proven project/access scope, and SQL failures silently return success.

## What Changes

- Apply exact collection, caller-plus-shared owner and active-memory predicates to primary and related summaries.
- Mark vector statistics, graph counts and interactions explicitly unavailable because current sources do not provide proven project/access scope; do not replace them with global fallbacks.
- Return tool errors on missing database or query/scan/iteration failures; never publish a partly assembled success payload after a read failure.
- Preserve successful collection/text shape, memory limits and legacy default/explicit/stateless collection routing.
- Correct the descriptor's narrow-record claim to describe the actual scoped summary.
- **BREAKING**: global auxiliary sections and vector-only success are intentionally withdrawn; revealing unverifiable data is not a compatibility guarantee.

## Capabilities

### New Capabilities

- `memory/project-context`: authenticated project summaries and explicit unavailable sections.

### Modified Capabilities

None; no existing main spec covers this behavior.

## Impact

Affects pkg/mcp project context handler/fixture/descriptor, a new shared SQLite/PostgreSQL regression and authenticated HTTP tests. No production DDL, dependency, Deps expansion, migration, deployment, or flag change. Profile membership and output schema remain compatible. Scoped graph analytics, collection-to-source mapping and interaction project provenance are separate backlog issues, not implemented by inventing an authorization shortcut.

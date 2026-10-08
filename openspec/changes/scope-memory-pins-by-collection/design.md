# Design

## Context

See proposal.md for the defect and scope. HTTP dispatch already injects a legacy session's selected collection for both tools, but the SQL handlers discard it. Current schemas omit the selector. SQLite rewrites numbered placeholders through Deps.Q; PostgreSQL retains them. No persistence change is needed.

## Goals / Non-Goals

**Goals:** one atomic, collection-aware UPDATE per operation; typed selector validation before effects; observed red-to-green checks on both dialects and actual MCP transports.

**Non-Goals:** introducing a generic selector abstraction, changing shared-row authorization or omitted-selector compatibility, modifying project-context/consolidation, or regenerating inventory without actual drift.

## Decisions

1. Append a parameterized collection predicate only for nonempty strings. Retain the existing owner predicate. This is the smallest fix and preserves older key-only callers; requiring collection everywhere would change their contract.
2. Reject present nonstring selectors instead of converting them to an absent filter. Treat empty strings exactly as current default resolution does; do not introduce name normalization.
3. Keep session/default logic in the existing transport adapter. Stateless requests pass collection explicitly; no new session state or dispatch layer.
4. Advertise the optional input and document empty/omitted compatibility. Preserve required key, result schemas, profile membership, and feature flags.
5. Reuse existing SQL fixtures and one shared scenario runner for dialect parity where practical. Capture failures before production edits, then run the same checks after the fix.

## Risks / Trade-offs

- Omitted/empty requests remain broad within the existing owner scope → describe this compatibility path explicitly; clients needing project isolation must select a collection or legacy default.
- Own and shared rows with the same key are both updated → preserve the established rule; redesigning shared-row mutation belongs to a separate reviewed change.
- SQL fixture columns differ from production → add the existing collection column to the PostgreSQL pin fixture; no production DDL.
- Contract inventory contains tool names/groups rather than complete input schemas → descriptor assertions are required in addition to make contract-check.

## Migration Plan

No migration, new dependency, or rollout flag. Run focused SQLite/PostgreSQL and transport tests, package/race checks, contract validation, and independent review. A binary rollback restores the previous selector behavior without a data rewrite. No deployment is part of this task.

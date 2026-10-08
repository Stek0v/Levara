# Proposal

## Why

Memory pin updates currently select by key and owner without using the collection already supplied by callers or injected by a legacy MCP session. Identical keys in another project can consequently change that project's wake-up briefing.

## What Changes

- Scope pin and unpin updates to the exact nonempty collection argument.
- Advertise the optional collection selector in both MCP input schemas.
- Reject malformed collection values before any mutation.
- Preserve omitted/empty-collection legacy behavior, caller-plus-shared ownership, default priority, missing-pin error, and idempotent missing-unpin success.
- Add red-to-green SQL regressions and MCP transport checks; publish the larger decomposition backlog separately.
- Non-goals: project-context aggregation, consolidation, shared-row authorization redesign, schema migration, new rooms/halls, deployment, or full-roadmap implementation in this change.

## Capabilities

### New Capabilities

- `memory/pin-scope`: Collection-aware memory pin lifecycle with compatible legacy selection and authenticated owner boundaries.

### Modified Capabilities

None; this repository has no existing OpenSpec capability specs.

## Impact

- Production: `pkg/mcp/tool_memory.go`, MCP descriptors in `pkg/mcp/tools.go`; HTTP shims should continue using their existing dispatch/default resolution.
- Tests: SQLite/PostgreSQL memory tests, descriptor tests, and bounded legacy/latest MCP transport tests.
- Persistence: one UPDATE statement per operation, existing collection column and owner predicate; no migration or new dependency.
- Compatibility: optional input addition; nonempty collection intentionally stops affecting sibling collections. Omitted/empty selection keeps historical behavior. Invalid typed selection becomes an error.
- Rollout: normal binary update; no data rewrite or feature flag. Generated contract drift is checked by its generator, never edited manually.

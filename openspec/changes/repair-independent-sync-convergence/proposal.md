# Proposal

## Why

T21 requires independent stores to converge. Equal-timestamp memory conflicts currently keep different values forever; memory exports omit retirement and supersession; unsupported selectors can report success without work. Existing auth and partial-result checks do not prove repeated two-node convergence.

## What Changes

- Validate selected types and explicit collection names before network effects, preserving default non-vector types.
- Define deterministic conflict precedence and preserve lifecycle information without inferring deletion from absence.
- Keep actual persisted identities and native index lifecycle consistent with accepted imports.
- Make repeated graph and interaction exchanges idempotent with truthful counters.
- Prevent silent export truncation and verify real independent roots, lost acknowledgements and retries.
- Keep collections opt-in with terminal import evidence; version skew remains an explicit warning where compatible.

## Capabilities

### New Capabilities

- `independent-sync-convergence`: deterministic, lifecycle-aware, idempotent two-node synchronization.

### Modified Capabilities

None; main specs do not define sync yet.

## Impact

Existing HTTP/MCP sync, memory mutation and native indexing, SQL schema mirrors where durable deletion evidence is required, and focused independent-root integration tests. No new dependencies or framework. Existing active-superuser and exact credential destination boundaries remain authoritative. Production services and stores are not changed by test fixtures.

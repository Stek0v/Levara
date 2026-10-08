# Proposal

## Why

T27 already has a bounded native workspace executor. Existing tests do not establish actual read-only ACL behavior, the long-horizon profile without workspace tools, deadline expiry while waiting for a native project lock, or independent-process recovery after a write before its receipt.

## What Changes

- Add native SQLite/PostgreSQL acceptance evidence for those existing executor behaviors, with actual effects and positive controls.
- Treat audit appends as expected observability and verify authored bytes, authority/publication manifests and receipts separately.
- Preserve observed failures and fix only a confirmed implementation defect; revise this change before adding a behavior change.
- No new executor, shell/network actions, public API, dependencies, production hook, deployment or live migration.

## Capabilities

### New Capabilities

None. This is a test/evidence change for existing roadmap behavior; skip_specs is explicit.

### Modified Capabilities

None; no requirement change is proposed.

## Impact

Native HTTP executor tests and existing process-child harness only. Both SQL dialects use actual storage and independent connections. Public contracts, persisted schema and runtime flags remain unchanged; contract drift still gates acceptance. No compatibility or rollout change.

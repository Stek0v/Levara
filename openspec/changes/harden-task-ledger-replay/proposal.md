# Proposal

## Why

T26 requires immutable receipts and checkpoints. Actual SQLite and PostgreSQL regressions show that reusing an idempotency key with changed evidence or checkpoint side effects silently returns the original ID.

## What Changes

- Persist an internal canonical request digest and reject changed-payload replay before effects, including insert-race replay.
- Preserve exact stale-version retries and return the current task version.
- Add real independent-process claim/reclaim and artifact-byte completion evidence for the existing T26 contract.
- Fail closed when authoritative validation queries or row scans fail.
- Retain bounded SQL authority and cooperating workspace-writer locks through completion commit/rollback; release locks on cancellation or credential expiry and recheck authority for terminal replay.
- **BREAKING**: historical receipts/checkpoints without a stored request digest cannot establish exact replay and return an explicit unverifiable idempotency conflict; existing rows remain readable and usable as evidence.
- Preserve task_open recovery without resending Definition of Done. No new executor, transports, identity policies, or dependencies.

## Capabilities

### New Capabilities

- `task-ledger-replay`: immutable exact request replay for receipt and checkpoint admission, with native lease and completion evidence.

### Modified Capabilities

None; the repository has no main OpenSpec specs yet.

## Impact

Task MCP handlers, PostgreSQL/SQLite additive schema and their test fixtures; current public success envelopes and descriptor fields remain unchanged. Generated contract drift is checked. Verified owner remains the authorization source; actor is only lease/audit identity. Production migrations, deployment and live service restarts are outside scope.

# Proposal

## Why

Workspace I/O follows symlinks after lexical path checks. Revert removes live files before verifying snapshot bytes, and truncating writes can expose partial content.

## What Changes

- Confine project/branch and workspace sidecar I/O with standard filesystem capabilities; reject redirected namespaces.
- Publish complete files using unique temporary files and rename; return actual read `file_digest`, preserve UTF-8 bytes and reject invalid text encoding.
- Validate complete snapshots and stage them before changing live files; keep the prior tree until publication succeeds and roll back failed replacement.
- Use native per-project file locks for cooperating processes, including trusted local operation, alongside existing SQL authority fences.
- Cover native SQLite/PostgreSQL, real child-process competition and failure preservation; retain ACL/tenant/SafeID/transport guarantees.

## Capabilities

### New Capabilities

- `workspace-file-integrity`: confined editing and byte-preserving history under authority and cooperative concurrency.

### Modified Capabilities

None; the main capability inventory is empty.

## Impact

`pkg/workspace` helpers/manifest and HTTP workspace/jobs/artifacts storage. Additive read `file_digest`; explicit invalid UTF-8 rejection. No SQL schema changes or new dependencies; standard Go and existing platform packages. Preserve task executor grants. Per-project serialization is intentional; external editors are outside cooperative CAS. Two-renames tree replacement is not an atomic swap; generation/crash recovery belongs to T20. No deploy, production restart or live migration.

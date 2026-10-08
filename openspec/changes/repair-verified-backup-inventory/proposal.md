# Proposal

## Why

T22 requires verified backup restored into an independent sandbox with consistent SQL references, object bytes, workspace Markdown and native indexes. Existing verification already performs native restore, but the new known workspace file inventory is not checked, leaving zero-chunk files outside semantic verification.

## What Changes

- Validate committed known file inventories, including empty files, through the existing verifier.
- Exercise current schemas and retained workspace state on SQLite and isolated PostgreSQL.
- Verify checksum-valid semantic corruption, missing objects, cancellation and interrupted staging without publishing a false successful receipt.
- Keep the existing offline lease, native restore and bounded verification budgets.

## Capabilities

### New Capabilities

- `verified-backup-workspace-inventory`: complete workspace inventory consistency during native sandbox restore.

### Modified Capabilities

None.

## Impact

Existing local verified backup verifier and focused native backup tests. No new framework, dependencies, production restore or external storage access. S3/KMS acceptance requires its separate sandbox prerequisite.

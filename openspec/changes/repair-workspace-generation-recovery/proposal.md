# Proposal

## Why

Workspace indexing activates each file before the requested batch succeeds; recovery, watcher restart and collection-wide GC can also diverge from authoritative files. T19 provides confined files and process locks, so T20 can now preserve the previous searchable state during failures.

## What Changes

- Publish one candidate manifest after the whole indexing batch succeeds, retaining compatible logical generation names and deferring exact-ID retirement.
- Enforce authoritative manifest membership and actual collection identity before workspace results reach context in authenticated and trusted-local modes.
- Record generation file digests, including zero-chunk files and explicitly empty inventories; honor full versus selected-path deletion scope.
- Recover running jobs under native project locking with a fresh state reload; preserve credential and target admission.
- Reconcile persisted watcher pending work against file digests on restart and coalesce outstanding branch work.
- Delete only recorded obsolete vector IDs during GC and retry persisted retirements without damaging shared collections.

## Capabilities

### New Capabilities

- `workspace-generation-lifecycle`: consistent batch publication, search eligibility, job/watcher recovery and safe derived-record retirement.

### Modified Capabilities

None; no main specs exist yet.

## Impact

Existing workspace indexer/manifest/GC, HTTP search eligibility, jobs and watcher; SQLite/PostgreSQL authority remains unchanged. Manifest fields are additive and old inventories are treated as unknown, never as empty. Existing profiles and public request/result shapes remain compatible; generated contracts are checked. No new dependency, generic publisher, SQL migration, deployment, or cross-store atomicity claim. External editors remain uncooperative; source files remain authoritative.

# Design

## Context

See proposal.md. Existing IndexMarkdown activates and retires per file. HTTP local search bypasses manifest eligibility; recovery uses only an in-process branch mutex. Watcher baseline uses size/mtime; GC treats one manifest as collection-wide exclusivity proof.

## Goals / Non-Goals

Goals: reuse the existing indexer, manifest and job sidecars with one candidate publication under T19 locks; maintain both SQL authority dialects.
Non-goals: new publisher framework, dependencies, global registry, SQL migrations, distributed transaction, or production rollout.

## Decisions

- Add internal attempt-ID salt to prepared vector IDs and a deferred-retirement option. Empty options preserve existing direct indexer behavior. Candidate indexing uses cloned maps and no live lexical mutation. Preserve prepared IDs on errors for exact compensation; do not physically overwrite active same-generation IDs.
- Add per-generation file digest inventory with an explicit empty inner map; absent inventory means legacy unknown. Candidate publication occurs once after the entire selected/full batch succeeds. DeleteMissing applies only to the chosen scope.
- Persist obsolete ChunkRecord IDs as pending retirements in the manifest. Publish before old cleanup; delete only IDs that are no longer authoritative. Retry exact IDs, never drop a shared collection from manifest-local evidence.
- Enforce manifest identity plus actual collection at every workspace result admission, including local mode. Generic search remains active-only; explicitly requested retained historical workspace lookup keeps its existing stale citations. Preserve verified scope per exact source through model/transport rechecks; refresh lexical records from retained manifest membership without granting pending records admission.
- Supersede old failed branch jobs only after a complete current inventory becomes active. Preserve their failed status/history; inactive or partial publication does not disable retries, and explicit retry clears supersession.
- Recovery acquires native project lock before fresh persisted state admission. Watcher rebuilds pending work from digest inventory and persisted status; check outstanding branch work before generation allocation.
- Reuse persisted job attempt identity for bounded process-death cleanup where possible; never claim cross-store atomicity. History two-rename interruption needs explicit recovery evidence, rather than assuming directory swaps are crash atomic.

## Risks / Trade-offs

- Per-project serialization limits throughput → retain T19 native lock; narrower locking only when measured.
- External editors can change files during preparation → validate source digests before publication; retry on change.
- Process-local lexical cache is derived → refresh/invalidate with manifest membership checks.
- Vector insertion and manifest publication are distinct stores → pending records are ineligible; exact attempt cleanup must be observable and retryable.

## Migration Plan

Additive manifest fields; old inventories remain unknown until reconciliation. Existing profiles, API shapes and both SQL backends retain admission. Run focused native fixtures, full integration, generated-contract checks and independent audit before acceptance. No live migration or deployment in this change.

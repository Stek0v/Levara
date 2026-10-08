# Design

## Context

See proposal.md for motivation. Existing SQL fences serialize authenticated workspace effects across processes; anonymous local mode currently uses process mutexes. Path helpers are lexical, sidecars use absolute reads and fixed temporary names, and restore removes the live tree before copying. Task executor already has narrower descriptor-relative grants and remains unchanged.

## Goals / Non-Goals

Goals: confined project/branch and sidecar storage, complete file visibility, exact supported text read, byte-preserving validated history, cooperative process CAS, and no authority regression.
Non-goals: new filesystem abstraction framework, schema migration, external-editor transaction, atomic directory exchange, generation reconciliation or crash recovery (T20), changing task grants.

## Decisions

- Use concrete stdlib os.Root capabilities. Anchor configured workspace root, descend namespace components after non-symlink Lstat checks, verify opened directory identity and retain descriptors. Reject symlinked namespace or file access rather than relying on workspace-level containment, which permits another project inside the same workspace. Existing SafeID ambiguity/SQL owner and tenant checks remain before effects.
- Provide small concrete root/subdirectory/read/atomic-write helpers; no new interface/dependency. Read only regular files through held roots; avoid blocking on special files. Unique exclusive temporaries, Sync/Close then Rename publish complete files. Preserve legacy manifest entrypoints while adding confined variants for public callers.
- Use native per-project filesystem locks from existing platform packages, cancellation-aware acquisition and release-on-close. Integrate with existing branch mutex then SQL authority fence before filesystem effects; direct trusted-local operations use the same project file lock. All lock participants use the same order. A ponytail comment records project serialization and branch locks as an upgrade only if measured. Unsupported platforms return an explicit error.
- Read materializes under authority/project locking before the existing retained response recheck. Avoid nested SQL acquisition with pool1. Byte digest comes from actual read; UTF-8 validation belongs to text API, binary snapshot copy remains supported.
- Commit prepares a complete temporary snapshot and record, using actual copied size and digest, then publishes the complete commit directory; failed preparation cleans it and corrupt records are not silently treated as valid head.
- Restore validates exact project/branch/commit, paths and duplicate/file-directory conflicts; reads every file through confined history capability and verifies digest/size. Stage the complete tree beside the live branch. Under cooperative locks, rename live to unique backup then stage to live; on second failure restore backup, keep recovery data if rollback itself fails. Check cancellation before effects/publication. This is two-renames publication, not atomic directory exchange.
- Apply helpers to workspace read/write/run/reindex/reconcile/history, manifests, job JSON and context artifacts, including enumeration. Keep job authorization/retry/generation policy intact.

## Risks / Trade-offs

- Project lock serializes branches and egress → bounded deadlines, actual Close release and explicit ceiling; no speculative lock manager.
- Uncooperative external editor → detect already-changed digest; no transactional promise.
- Crash between directory renames → retained backup, explicit T20 recovery work; no crash-proof claim.
- Historical lossy identities → preserve fail-closed checks; no production migration.
- Derived index failure after authoritative file write → retain existing documented semantics and T20 reconciliation.
- Platform coverage → native fixture controls and existing build support; fail explicitly where locking cannot be guaranteed.

## Migration Plan

No SQL/data migration. Existing successful UTF-8 callers gain file_digest; invalid text and unsafe symlinks fail explicitly. Full/core contract generators are the only owners of generated inventories. Verify native both-SQL/pool1, noDB trusted local, child processes, full gate and independent review before acceptance; no deploy/restart requested.

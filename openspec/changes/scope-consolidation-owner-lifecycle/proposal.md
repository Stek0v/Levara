# Proposal

## Why

Consolidation selects foreign memories, creates shared abstracts, loses verified actor context in async execution, and reverts by unscoped run ID. These paths can expose private source text and overwrite later changes; T06 requires a complete scoped lifecycle.

## What Changes

- **BREAKING**: ordinary callers consolidate their exact owner namespace; an explicit shared selector requires live administrator/trusted-local authority. Forged identity arguments grant no authority.
- Preserve owner, collection, type, room and public hall; prevent cross-namespace similarity edges before clustering/LLM and skip abstracts with invalid legacy halls explicitly.
- Validate captured candidates under the existing credential/SQL fence; preserve full merge/abstract reversibility through an atomic run journal with full-row stale fingerprints and only the previous retirement fields.
- Preserve verified context for detached execution; interrupted authenticated jobs fail with an unknown-outcome/resubmit explanation instead of executing from stored owner/args. Recovery closes reads before writes and status is exact-owner scoped.
- Enqueue derived-index effects atomically where the outbox exists; require a clear diagnostic when a vector deployment lacks the outbox. Stale-delete/requeue generation safety remains the next separate T06/T07 index step.
- Trusted-local maintenance processes owner namespaces independently; authenticated background execution without verified authority fails closed.

## Capabilities

### New Capabilities

- `memory/consolidation`: authorized owner lifecycle, homogeneous source classification, guarded atomic apply/revert and honest asynchronous recovery.

### Modified Capabilities

None in main specs; the earlier boolean change also introduces this same capability path and retains its atomicity requirements.

## Impact

MCP descriptors/dispatch adapters, pure consolidation engine, both SQL schema initialization paths, async HTTP jobs and focused tests. A small shared hall validator avoids duplicated vocabulary and an import cycle. The journal adds one table with no copied private memory content, new dependency or stored credentials. Legacy runs without a journal cannot be safely reverted and return an explicit error. No live migration, deployment or external provider is included; T06 remains open until its separate index/recovery acceptance passes.

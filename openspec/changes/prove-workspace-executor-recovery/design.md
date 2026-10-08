# Design

## Context

See proposal.md. Existing public task grants, worker/executor, HTTP dispatcher, native project lock, source CAS and process test harness provide the required seams. SQLite/PostgreSQL rows and workspace bytes are authoritative; receipts and audit events describe actual effects.

## Goals / Non-Goals

Goals: prove original T27 corners on both SQL dialects, preserving native denied-operation and positive controls. Non-goals: public authentication transport proof, arbitrary power loss, new execution actions or speculative production fixes.

## Decisions

- Reuse real grants, dispatcher and existing helpers; avoid a new mock executor or production hook because they would not establish native effects.
- A viewer read succeeds while write is denied; upgrade the same grant to editor as the write positive control. Test the actual long-horizon profile spelling and workspace visibility.
- Hold a real project lock until the worker action deadline expires; inspect unchanged bytes/manifests/no passing receipt and native SQL release, then allow a fresh task to write. The worker reserves at least five seconds before its lease expires: this proves the earlier action deadline, not natural lease expiry during a running action.
- Crash a real child after successful native write and before executor receipt admission, using the existing dispatcher callback seam. SIGKILL, wait persisted natural lease expiry without SQL edits, reopen SQL/workspace in a second child with unchanged HTTP executor, and verify exactly one passing receipt and CAS reconciliation without changing source inode/mtime.
- Keep legitimate audit appends separate from authored/index effects. Compare actual audit events, not an assertion that no log may appear.
- Current public contracts, schema and feature flags do not change. Both native SQL dialects and pool1 must execute; skips cannot satisfy their criteria.

## Risks / Trade-offs

- Native timing/driver behavior may invalidate a draft oracle -> preserve failed evidence and repair only demonstrated fixture/proof gaps.
- The first crash child uses the production MCP executor plus validated HTTP dispatcher callback; recovery uses the unchanged HTTP constructor -> state direct-core scope explicitly.
- An expected audit append can look like a filesystem effect -> exclude only the exact audit path from product-tree equality and check its events separately.

## Migration Plan

No production migration or deployment. Tests and evidence only; a confirmed behavioral defect requires a coherent plan/spec revision and focused native proof before repair.

# Design

## Context

See proposal.md. Distillation is synchronous and has no durable detached job. SQL remains authoritative and indexing is derived. Memory dispatch intentionally preserves an empty legacy namespace without a session collection; using write fallback `default` would change that behavior.

## Goals / Non-Goals

Propagate the caller's earlier cancellation/deadline through the four-minute maximum budget, model and SQL operations, and distillation's indexing. Keep ordinary save indexing semantics unchanged. Do not change imported transcript authorization/ownership, promise atomicity across all candidates or HTTP transfer-drain authority, add a background job or test model factual quality.

## Decisions

- Derive the operation timeout from the supplied context, with checks after model return and before each save/result. A provider that ignores cancellation must not enable new writes or a successful preview. Keep already committed SQL rows authoritative if cancellation occurs later; do not invent rollback across previously committed candidates.
- Add an internal context-aware indexing body and retain the existing detached bounded wrapper for unrelated callers. Distillation passes its operation context, checks before embedding/vector publication and returns an operation error on cancellation. Native vector interfaces have no context parameter; no claim of interrupting an insert already executing.
- Add chat_distill to the existing memory-tool collection injection group. Explicit nonempty collection wins, session default supplies omitted/empty values, and stateless/no-default calls retain empty namespace. Test actual legacy session dispatch and latest stateless explicit/no-default controls rather than resolver-only tests. Latest already hides/rejects set_context and instructs explicit collection; preserve that public contract.
- Skip whitespace-only content before adding role/kind labels. Preserve nonempty tool/system messages and current truncation/provenance behavior.

## Risks / Trade-offs

- Slow models now obey the earlier HTTP deadline → use the existing request timeout configuration; detached synchronous work is not a substitute for a real job.
- Cancellation during derived indexing can leave committed SQL awaiting reconciliation → retain divergence/reconciliation behavior and disclose the boundary.
- Shared indexing helper affects ordinary saves → preserve wrapper semantics and run existing indexing regressions alongside focused native cancellation checks.

## Migration Plan

Local implementation and fixtures only. No persisted format change or live migration. No production restart, external provider or deployment.

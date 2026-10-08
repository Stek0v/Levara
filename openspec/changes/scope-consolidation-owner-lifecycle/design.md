# Design

## Context

See proposal.md for motivation. The engine currently has no owner/collection/type fields. SQL apply retires IDs without validating the captured plan; revert trusts a run ID. Async execution replaces authenticated context with a background context. SQLite and PostgreSQL must enforce the same boundary, including single-connection pools.

## Goals / Non-Goals

Preserve full mechanical-merge and abstract reversibility with live authority and stale-plan protection. Use existing metadata write/read fences and verified actor context. Derived indexes remain rebuildable. Stale index-delete generation and completed-job requeue are the next T06/T07 step, so completing this change alone does not close T06. No provider dependency, stored credentials, live deployment or live migration.

## Decisions

1. The default namespace is the exact verified owner and explicit collection. `shared=true` selects the empty owner only after a live administrator or trusted-local check. Arguments never create actor authority. Trusted-local maintenance enumerates namespaces independently; authenticated maintenance without actor proof fails closed. Broad owner predicates would let private text reach providers.
2. Add owner, collection and memory type to engine records/actions. Remove edges whose endpoints differ on owner, collection, type, room or hall before clustering/provider use. A tiny shared hall package owns the existing six-value vocabulary; MCP keeps its public forwarding functions. Invalid legacy hall abstracts return a skip before LLM use; mechanical merges preserve their classification.
3. Use one SQL run journal, with namespace, status, source roles, previous retirement fields and SHA-256 fingerprints of complete persisted rows. It stores no private text. Capture candidates before planning, re-read every source/survivor under the existing write fence, then apply and journal in one transaction. A rollback restores only retirement fields after validating all recorded after-fingerprints. Deleting a generated abstract is allowed only after the same validation. A new arbitrary snapshot framework or copying memory values into a journal is unnecessary.
4. Read persisted after-rows to account for database defaults/precision. Fingerprints normalize SQL driver representations and timestamps but retain NULL distinctions. Include pins, provenance, classification and the unchanged merge survivor; checking updated_at alone misses writers that omit it. Runs lacking a journal return an explicit error. Authorized repeated revert is idempotent.
5. Hold existing credential fences across provider calls and recheck immediately before committing. Require bounded deadlines. Queue derived-index operations in the same SQL transaction when its outbox exists; a vector-enabled deployment missing that outbox fails explicitly.
6. Detach async work with context.WithoutCancel while retaining verified context, then attach a bounded deadline. Claim pending jobs with a SQL compare-and-set. Recover rows into memory and close them before any write. Interrupted authenticated jobs have unknown outcome and require resubmission; stored owner/arguments are not credentials. Trusted-local recovery can resume only jobs whose empty owner and local configuration provide a valid namespace. Status reads match the exact submitting owner.
7. Initialize the same native journal DDL for both SQL dialects. No additional index is needed for primary-key run lookup. Existing legacy jobs get only missing columns with checked errors.

## Risks / Trade-offs

- Full-row changes invalidate rollback, including pin/provenance changes → return conflict without partial effects; users retain later changes.
- Future schema columns change fingerprints → conservative refusal is safer than restoring unknown state; migration must explicitly handle old runs.
- Credentials cannot survive process restart safely → report unknown outcome/resubmit rather than fabricate authority.
- Existing coarse SQL fences can serialize work → reuse the established security contract; narrow locks only with measured need and separate evidence.
- Legacy unjournaled runs are no longer revertible → explicit error; no unsafe fallback.

## Migration Plan

Verify SQLite and a disposable PostgreSQL instance first. Additive journal/job initialization runs through existing schema startup. Public descriptors, generated contracts, core/full profile behavior and both adapters are checked together. Rollback code deployment leaves journal records intact; old code must not be used to revert journaled runs without their guards. No production rollout is authorized here.

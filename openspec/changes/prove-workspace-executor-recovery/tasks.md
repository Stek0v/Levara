# Tasks

All Go tests/list commands exclude ^TestMemoryREST per user. Preflight: dedicated native PostgreSQL test server plus SQLite; both dialects must execute, skips are not acceptance. Existing public behavior only; any confirmed behavior change requires plan/spec revision before repair.

## 1. Admission and observable effects

- [x] 1.1 Add real viewer ACL read/denied write and editor positive control, plus actual long-horizon profile missing workspace tools; compare authored bytes, publication/authority manifests, actual receipts and separately legitimate audit events. Verify native HTTP focused race selectors TaskExecutorReadOnlyACLGrant and TaskExecutorLongHorizonProfile after exact -list; record evidence and failure history.
- [x] 1.2 Add real project-lock waiting action-deadline rejection, native pool release and fresh successful action; preserve the earlier worker deadline versus live lease distinction. Verify focused native HTTP race TaskExecutorNativeProjectLockActionDeadline after -list; record evidence and failure history.
- [x] 1.3 Change authority manifest after initial dispatch while native project lock is held; reject publication without product/receipt effects, restore original manifest and prove fresh positive action. Verify focused native HTTP race TaskExecutorManifest after -list; record evidence and any confirmed gaps.

## 2. Independent process recovery

- [x] 2.1 Add actual child write-before-receipt checkpoint, confirmed SIGKILL and natural persisted lease expiry, then independent actual executor recovery on shared SQL/workspace. Verify bothSQL focused HTTP race TaskExecutorIndependentProcess after -list: exact one passing receipt, attempts/events, bytes, unchanged inode/mtime CAS, publication manifest and native pool/lease cleanup; document direct-core dispatcher scope.

## 3. Integration acceptance

- [x] 3.1 Run expanded native TaskWorker/TaskExecutor/TaskWorkspace/Authority race and whole make test-commit on frozen source; verify full/core contracts, strict OpenSpec, diff and before/after functional maps. Preserve all failures and opt-in skips.
- [x] 3.2 Independently audit original T27 DoD/corners against actual current evidence, publish evidence/issue ledger/runtime receipts and accept only proven criteria; keep global Goal/Task active until all roadmap work is done.

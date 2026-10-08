# Tasks

All Go commands exclude ^TestMemoryREST per user. PostgreSQL preflight uses the dedicated test server; SQLite and PostgreSQL must both execute, skips are not acceptance.

## 1. Semantic replay

- [x] 1.1 Publish native full-ledger receipt/checkpoint mismatch regressions and preserve observed failures: focused race exit1, 34 failing leaf cases across both dialects.
- [x] 1.2 Add canonical request digest and mirrored additive production/fixture schema; verify focused native replay race, exact stale-version retry, changed fields, legacy empty digest and ignored-field normalization.
- [x] 1.3 Verify additive historical schema upgrade and required-column fail-closed startup; run native HTTP migration cases and document compatibility in the task runbook.

## 2. Process authority and completion

- [x] 2.1 Prove independent-process single claim winner, SIGKILL plus natural expiry/reclaim and stale actor denial using actual shared SQLite/PostgreSQL rows; focused process race with no manual lease-expiry edit.
- [x] 2.2 Prove actual authorized artifact replacement by a separate process rejects validation/completion without ledger mutation; restore bytes as positive control and run focused process race.
- [x] 2.3 Audit original T26 stale version, dependency cycle, blockers/reviewer policy and atomic SQL promotion/rollback; run native relevant regressions and implement only confirmed gaps with focused checks.

## 3. Integration acceptance

- [x] 3.1 Run native task/server checks and whole make test-commit on frozen source, contracts, strict OpenSpec and diff guards; preserve all failed attempts.
- [x] 3.2 Independent review of actual evidence, original T26 mapping and current source; publish evidence/issue ledger/runtime receipts and update only fully satisfied criteria.

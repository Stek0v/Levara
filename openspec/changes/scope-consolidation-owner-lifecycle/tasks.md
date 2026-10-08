# Tasks

## 1. Classification boundary

- [x] 1.1 Reproduce cross-owner/collection/type/room/hall clustering and invalid-hall provider use, then preserve classification and filter edges before providers; DoD: `go test ./pkg/consolidate ./pkg/memoryhall -count=1` passes with a recorded failing baseline and no external service.
- [x] 1.2 Keep one six-hall vocabulary with MCP forwarding compatibility and defensive copies; DoD: focused hall tests and engine tests pass, documentation agrees with the unchanged public vocabulary.

## 2. Authorized SQL lifecycle

- [x] 2.1 Reproduce forged-owner HTTP candidate leakage on SQLite and disposable PostgreSQL, then enforce exact verified owner/collection and explicit shared authority; DoD: JWT transport and direct SQL scope tests pass on both dialects without skips. Preflight: isolated database identity/version verified.
- [x] 2.2 Capture full candidate state and add an atomic content-free run journal with guarded apply; DoD: uncaptured/duplicate/overlapping IDs, changed content/pins/provenance/classification, missing rows, revoked credentials and transaction failures have no partial effects in both SQL dialects.
- [x] 2.3 Implement guarded complete merge/abstract revert; DoD: mixed apply/revert, unchanged survivor, stale source/survivor/generated row, foreign run, legacy run and repeated revert tests pass on both dialects. Update lifecycle compatibility documentation with explicit legacy-run behavior.
- [x] 2.4 Enqueue apply/revert index effects in the existing SQL outbox atomically and fail diagnostically when a vector deployment lacks it; DoD: outbox rollback and SQL-only behavior tests pass, while separate stale-delete/requeue acceptance remains open in T06/T07.

## 3. Provider and background authority

- [x] 3.1 Fence provider calls and isolate trusted-local maintenance by namespace; DoD: provider spies observe no foreign source, live credential changes cannot authorize outgoing text, missing authority fails closed and bounded deadlines are enforced.
- [x] 3.2 Preserve verified actor context in detached async execution, claim jobs once and scope status exactly; DoD: JWT request cancellation, forged args, duplicate claim, foreign/shared status and deadline tests pass on both dialects.
- [x] 3.3 Repair recovery and legacy job schema initialization; DoD: a one-connection pool returns, authenticated restart reports unknown outcome/resubmit without private execution, trusted-local eligible recovery works and checked column upgrades pass on both dialects. Document recovery semantics.

## 4. Public integration and evidence

- [x] 4.1 Update descriptors and generated contracts with one sequential owner; DoD: `make contract-check`, focused registry/profile/HTTP tests and `openspec validate scope-consolidation-owner-lifecycle --strict` pass.
- [x] 4.2 Independently review the combined SQL/engine/HTTP diff, run targeted race tests and `make test-commit`, record current source hashes and observed RED/GREEN evidence, update the issue ledger; DoD: no unresolved lifecycle review findings, no skipped PostgreSQL acceptance and T06 remains open for its index-generation step.

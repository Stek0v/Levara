# Tasks

## 1. Scope and failures

- [x] 1.1 Add shared SQLite/PostgreSQL regression cases for own/shared/foreign/current/superseded main+related memories, unknown/literal collection, unscoped auxiliary data, nil DB, query/scan/row-evaluation/related failures, cancellation and pool=1. Observe original RED with `go test -count=1 -v ./pkg/mcp -run TestProjectContextScope`. Preflight: Go and isolated PostgreSQL16+, LEVARA_TEST_POSTGRES_DSN with schema creation rights; skip is not parity evidence.
- [x] 1.2 Implement scoped sequential reads, explicit unavailable auxiliary sections and errors without partial success; adjust old tests to the intentional scope contract and descriptor prose. Verify GREEN with the same matrix plus `go test -count=1 ./pkg/mcp -run 'TestToolGetProjectContext|TestToolOutputsMatchRegisteredSchemas_RoundTrip|TestEmptyToolBranchesMatchRegisteredSchemas'`; `make contract-check`. Preflight: same SQL setup. Document compatibility changes and issue-ledger follow-ups immediately.

## 2. Authenticated transports

- [x] 2.1 Add legacy default/override and stateless explicit/ignored-session checks with JWT owner isolation and supersession on real SQLite/PostgreSQL fixtures. Verify `go test -count=1 -v ./internal/http -run TestMCPProjectContextScope`. Preflight: Go, isolated PostgreSQL, migrated test schemas, locally signed JWT; no external provider. Keep successful collection/text and error shape checks.

## 3. Integration and evidence

- [x] 3.1 Verify current combined revision with `go test -count=1 ./pkg/mcp ./internal/http`, targeted `go test -race -count=1 ./pkg/mcp ./internal/http -run 'TestProjectContextScope|TestMCPProjectContextScope'`, `make test-commit`, `make contract-check`, strict OpenSpec validation and independent read-only review. Preflight: Go + isolated PostgreSQL. Record any skip and unresolved finding; no DoD completion from summaries alone.
- [x] 3.2 Record commands, source digests, RED/GREEN, review and limitations in evidence.md; update T05 only if all its DoD passes, and retain findings in the separate issue list. Verify OpenSpec apply progress and source hashes. Preflight: no new service; preserve unrelated dirty files; no commit/push/deploy/live migration.

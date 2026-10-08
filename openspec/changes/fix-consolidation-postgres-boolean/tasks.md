# Tasks

## 1. SQL parity and atomicity

- [x] 1.1 Add TestConsolidationSQLApplyRevert with SQLite/PostgreSQL mixed apply, unpinned abstract, forced later apply failure, failed revert rollback and successful revert/control state. Observe original PostgreSQL RED with `go test -count=1 -v ./pkg/mcp -run '^TestConsolidationSQLApplyRevert$'`. Preflight: Go and isolated PostgreSQL DSN/schema rights; zero PostgreSQL skips.
- [x] 1.2 Replace integer pin literal with FALSE and rerun that matrix GREEN. Document RED/GREEN and open owner/async/hall/index boundaries in evidence and issue ledger. Preflight: same isolated SQL setup; no migration/provider/production action.

## 2. Integration

- [x] 2.1 Run existing consolidation suite, targeted race, fresh affected package, `make test-commit`, `make contract-check`, strict change validation and independent review; record current source digests and actual skips. Preflight: Go and isolated PostgreSQL; T06 checkbox remains open until its other lifecycle requirements pass.

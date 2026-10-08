# Tasks

## 1. Retire execution and retain history

- [x] 1.1 Delete notebook execution handlers, registration and WebUI page; update old-flag regression checks; verify all ten operations return 404 for old enabled/off/invalid values with `go test ./internal/http -run Notebook` after PostgreSQL preflight, using `GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'`.
- [x] 1.2 Preserve populated SQLite/PostgreSQL rows, indexes and foreign keys across repeated schema migration and retired requests; verify complete field snapshots with the same focused Notebook command and both dialects actually running.

## 2. Integrate the sunset contract

- [x] 2.1 Root updates inventory/generated contract and public sunset documentation; verify `make contract-check`, WebUI lint/build and regression after their service/dependency preflights; legacy flag values cannot revive execution.
- [x] 2.2 Root backs up, verifies and restores populated disposable notebook history using existing administrative tooling; compare complete original/restored notebook and cell rows, record current revision and independent acceptance evidence without claiming a published release interval.

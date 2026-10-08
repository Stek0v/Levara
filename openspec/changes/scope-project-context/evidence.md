# Verification evidence

Date: 2026-10-05. Roadmap: T05. Base HEAD: `2eb1dca16b0047918185760b41dcb22dee79090a` plus the uncommitted source manifest below.
Environment: Go 1.27.1, darwin/arm64; SQLite via the existing driver; PostgreSQL 16.15 in a newly initialized loopback-only test instance, with a fresh schema per test. No production database or external provider was used. After the combined T03/T06 verification, this owned temporary PostgreSQL instance was stopped with `pg_ctl -m fast -w stop`; exit 0, server stopped.

## Reproduction and resulting contract

`go test -count=1 -v ./pkg/mcp -run TestProjectContextScope` failed against the original handler: all 23 scenario leaves failed, zero skips, exit 1. The matrix exposed foreign/superseded main and related memories, unscoped auxiliary data, and successful partial responses after failed reads. Fixture setup failures were corrected before this recorded RED; they are not production-defect evidence.

The handler now reads only current caller-owned/shared memories from each exact selected collection. It uses bound parameters, closes each result before the next query, and checks query, scan and iteration errors. A failed read returns an error without structured or partial successful context.

Graph entities, interactions and vector statistics have no proven project/access scope in these source paths. Their sections explicitly report unavailable; the handler does not fetch them. Building authorized aggregates remains a separate T15/T16 follow-up. The successful `collection`/`text` schema, primary limit 20 and related limit 3 remain compatible. Returning unavailable instead of global auxiliary data, and an error instead of nil-DB/partial success, are intentional behavior changes described in the proposal/spec and descriptor.

## Observed checks

All SQL commands used `LEVARA_TEST_POSTGRES_DSN` pointing to the isolated test database. An unset DSN and a skipped PostgreSQL case would not satisfy parity.

| Check | Command | Observed result |
|---|---|---|
| Original SQL RED | `go test -count=1 -v ./pkg/mcp -run TestProjectContextScope` | Exit 1; 23 failing leaves; zero skips |
| SQL GREEN and existing/schema branches | `go test -count=1 -v ./pkg/mcp -run 'TestProjectContextScope|TestToolGetProjectContext|TestToolOutputsMatchRegisteredSchemas_RoundTrip|TestEmptyToolBranchesMatchRegisteredSchemas'` | PASS; current 23 scope/failure leaves, compatibility and result-schema tests; zero skips |
| Authenticated transports | `go test -count=1 -v ./internal/http -run TestMCPProjectContextScope` | PASS; 12 scenario leaves across SQLite/PostgreSQL; zero skips |
| Fresh affected packages | `go test -count=1 -json ./pkg/mcp ./internal/http` | PASS; MCP 19.575s, HTTP 99.663s; one unrelated load-test skip below |
| Focused race | `go test -race -count=1 -v ./pkg/mcp ./internal/http -run 'TestProjectContextScope|TestMCPProjectContextScope'` | PASS; 23 SQL and 12 HTTP leaves; zero skips |
| Combined commit gate | `make test-commit` | PASS; S0–S4 green: docs/static, access/profile/audit/workspace/MCP, core engine, HTTP, server bootstrap; some unchanged packages cached |
| Generated public contract | `make contract-check` | PASS; no generated changes required |
| Planning | `openspec validate scope-project-context --strict` | PASS |
| Whitespace | `git diff --check` | PASS |

`TestDCDVSALoadBaseline` was skipped in the fresh HTTP package suite because its explicit `LEVARA_DCD_VSA_LOAD_CASES` opt-in was unset. This is not T05 acceptance evidence. No PostgreSQL project-context or pin case was skipped. Targeted test names were present and actually executed.

Local raw logs: `/tmp/levara-context-sql-red.log`, `/tmp/levara-context-current-focused.log`, `/tmp/levara-context-http-green.log`, `/tmp/levara-context-packages.jsonl`, `/tmp/levara-context-race.log`, `/tmp/levara-roadmap-test-commit.log`. These temporary logs are not durable memory or Task Runtime receipts; observed results are recorded here.

## Covered boundaries and independent review

- Own/shared active main and related records are visible; foreign, superseded, sibling and global control markers are absent.
- Anonymous calls see shared records only; forged owner/actor arguments do not override the JWT caller.
- Unknown, foreign-only and literal quoted collection names are safe; malformed related items retain their existing skip behavior.
- Legacy session default and explicit override work; stateless calls ignore the supplied legacy session/default and require explicit collection.
- Query, related-query, scan, row-evaluation and cancellation failures produce no partial success; nil DB is an error.
- Every result is closed with pool=1; the spy observes zero calls to actor-free vector metadata.

The independent read-only reviewer inspected the actual combined handler/descriptor/test/spec diff, checked source provenance/authorization limits and reran SQL and authenticated HTTP matrices with both dialects. No correctness finding or blocker remained. It did not edit files or mutate shared memory/Task state.

## Limits and authority

T05 does not implement scoped analytics or repair independent wake-up, REST, consolidation or graph paths. Those findings remain in the separate issue ledger. Shared curated memories retain their existing read visibility. No new dependency, migration, flag, commit, push or deployment was introduced; unrelated dirty files were preserved.

Levara MCP was unavailable. This is local OpenSpec evidence, not a claimed Task Runtime completion, receipt, lease or durable-memory save. The whole 32-task roadmap remains active.

## Verified source manifest

```text
8b3663c0b7f7a2aff5131024766a9ee8d0c274737e13cea3ee91344ac38d97fd  pkg/mcp/tool_project.go
af0a951e3559a97b664e226a0bb9876dc8897140b4469bfa8490a0f9d31a8ee7  pkg/mcp/tools.go
1bab686602106e4010d18ff7d4f00eba25571bcbdde1f278c81da770bbd373d5  pkg/mcp/tool_project_test.go
3609e14718f528c5c3e6e7511ee4747510c5e7e3135f25633d8d33d3a4d32b9b  pkg/mcp/tool_project_scope_test.go
f5722dea707e220ff941d86bb359b17378ef26aa02ddd2c9e3e1c9949614c1bf  internal/http/mcp_project_context_test.go
```

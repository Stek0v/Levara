# Verification evidence

Date: 2026-10-05. Change: scope-memory-pins-by-collection. Roadmap item: T04.
Base HEAD: `2eb1dca16b0047918185760b41dcb22dee79090a`; verification includes the uncommitted files below.
Environment: Go 1.27.1, darwin/arm64; PostgreSQL 16.15 (Homebrew); SQLite via the existing ncruces driver.
PostgreSQL used a newly initialized, loopback-only test instance and database, with fresh per-test schemas. No production database was used. The temporary instance was stopped after verification with `pg_ctl -m fast -w stop`; the command returned exit 0 and server stopped.

## Reproduction and fix

The new shared dialect regression failed against the original handlers before production edits:

```sh
go test -count=1 -v ./pkg/mcp -run TestToolMemoryPinCollectionScope
```

`LEVARA_TEST_POSTGRES_DSN` was set to the isolated test database. Exit 1; 44 failing scenario leaves, zero skips. Explicit selectors changed sibling collections, and present nonstring selectors still changed rows. Omitted/empty compatibility cases already passed. This was an observed defect on both SQLite and PostgreSQL, not a hypothesis from source inspection.

The handlers now append one parameterized exact collection predicate for nonempty strings and reject a present nonstring selector before UPDATE. Owner/shared selection, default priority, missing-pin error, idempotent unpin, and result schemas remain compatible. No migration or dependency was added.

Descriptor assertions also failed before advertising collection and passed after adding the optional string input to both tools.

## Observed checks

All commands run from the repository root. SQL commands use the isolated `LEVARA_TEST_POSTGRES_DSN`; unset DSN would skip PostgreSQL and is insufficient evidence.

| Check | Command | Observed result |
|---|---|---|
| Current SQL matrix and descriptors | `go test -count=1 -v ./pkg/mcp -run 'TestToolMemoryPinCollectionScope|TestMemoryPinDescriptor'` | PASS; 60 selection scenario leaves across two operations and two dialects, plus two database-failure cases; zero skips |
| Authenticated HTTP transports | `go test -count=1 -v ./internal/http -run TestMCPMemoryPinCollectionScope` | PASS; current 20 scenario leaves; zero skips |
| Descriptors and profiles | `go test -count=1 ./pkg/mcp -run 'TestMemoryPinDescriptor|TestToolProfiles|TestToolDescriptors'` | PASS; collection optional string, key remains required, output schema unchanged, core/full present and ops absent |
| Existing pin compatibility | `go test -count=1 ./pkg/mcp -run 'TestToolPinMemory|TestToolUnpinMemory|TestToolMemoryPostgresPinUnpin|TestToolMemoryPinCollectionScope'` | PASS with PostgreSQL DSN |
| Fresh affected packages | `go test -count=1 -json ./pkg/mcp ./internal/http` | PASS; pkg/mcp 15.815s, internal/http 181.916s; HTTP matrix then contained 16 leaves, followed by the current 20-leaf checks below |
| SQL, transport and auth race | `go test -race -count=1 -v ./pkg/mcp ./internal/http -run 'TestToolMemoryPinCollectionScope|TestMCPMemoryPinCollectionScope|TestMCPReadOnlyAPIKeyCannotCallMutatingTool'` | PASS; both dialects, original 16 HTTP leaves and read-only API-key rejection; zero skips |
| Current expanded HTTP/auth race | `go test -race -count=1 -v ./internal/http -run 'TestMCPMemoryPinCollectionScope|TestMCPReadOnlyAPIKeyCannotCallMutatingTool'` | PASS; current 20 HTTP leaves and API-key rejection; zero skips |
| Repository commit gate | `make test-commit` | PASS; S0–S4 green on current implementation and expanded HTTP matrix; includes access/profile/audit/workspace/mcp, store/vectorstore/bm25, HTTP and server bootstrap (some unchanged packages cached) |
| Generated contract | `make contract-check` | PASS; no generated-artifact changes required |
| Planning validation | `openspec validate scope-memory-pins-by-collection --strict` | PASS |
| Whitespace and docs | `git diff --check`; `go test ./docs` | PASS |

The fresh package suite skipped `TestDCDVSALoadBaseline`: its explicit `LEVARA_DCD_VSA_LOAD_CASES` opt-in was unset. This unrelated load benchmark is not acceptance evidence for T04. No PostgreSQL pin case was skipped. Initial HTTP fixture setup failures (missing password field and identity schemas) were corrected in the new fixture; these are not counted as defect reproduction.

## Covered boundaries

- Own/shared selected rows change; sibling collections, foreign owners and other keys preserve pin, priority and timestamp.
- Collection names with whitespace and SQL quote characters match literally.
- Missing key/collection and foreign-only rows preserve existing error/success behavior.
- Null, number, bool, array and object selectors leave every row unchanged.
- Anonymous calls affect shared rows only; forged owner/actor arguments do not change the authenticated owner.
- Legacy omitted/empty selectors use the session default; explicit collection overrides it.
- Stateless requests ignore the supplied existing legacy session and its default, including omitted/empty collection.
- Default priority is 1, repeated unpin succeeds, and SQL failure remains an error.

## Independent review

The read-only pin_transport_map reviewer inspected the combined handler, descriptor, SQL/HTTP test and OpenSpec changes and ran the focused SQL/transport/descriptor command with the isolated PostgreSQL DSN: both packages passed. No correctness finding or blocker remained. Its suggested stateless omitted/empty cases were added, passed, and received a clean follow-up source review. The reviewer did not edit files or shared memory/Task state.

## Limits and authority

Without collection and without a legacy session default, historical key-and-owner selection intentionally spans collections. Own and shared rows with an identical selected key are both updated. Superseded-record policy, project-context scope and consolidation are separate backlog items.

Levara MCP was unavailable in this session. Planning and evidence are local OpenSpec artifacts; no Task Runtime receipt/completion or durable-memory save is claimed. No commit, push, deployment or live migration was performed; unrelated dirty files were preserved.

## Verified source manifest

SHA-256 of the implementation and test files used by the current focused checks:

```text
b27808cdb77d4f8a683fa348fd4b06a8a8468a2fc22a29f96a9be4da9638390a  pkg/mcp/tool_memory.go
60b1fd29646abc9dec7d260777d24fdddfeefd5a02f5dbe11b788d56c566a8a4  pkg/mcp/tools.go
e69c07ab94eb26f03ebeb8950600f5955b80fd2826116b75c909448c14c0dcff  pkg/mcp/tools_test.go
f923e6a0666ad8862b3018fd28942408cc160705ff5e91b8d250342179a53739  pkg/mcp/tool_memory_pin_scope_test.go
45b016f5a4f0ecb4439edcf649181df5af428ba772ced74c5cd63942afd8a71c  internal/http/mcp_memory_pin_test.go
```

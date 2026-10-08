# Tasks

## 1. SQL selection and compatibility

- [x] 1.1 Add a shared pin/unpin regression matrix for SQLite and PostgreSQL: selected own/shared rows, sibling collections, foreign owner, other key, unknown collection, omitted/empty selector, invalid selector types, default priority, unchanged control timestamps and idempotent unpin. Verify the scoped/type cases fail on the original implementation with `go test -count=1 -v ./pkg/mcp -run TestToolMemoryPinCollectionScope`; preflight: Go, isolated PostgreSQL 16+, `LEVARA_TEST_POSTGRES_DSN`, successful connection and CREATE SCHEMA permission. A skipped PostgreSQL case is not passing evidence.
- [x] 1.2 Implement exact nonempty collection predicates and reject nonstring values in both handlers while preserving existing owner/shared and result semantics. Verify the same command goes green on both dialects; preflight as 1.1. DoD: all control rows remain byte-for-byte equivalent in the checked pin/priority/timestamp fields.

## 2. Public MCP contract and transport behavior

- [x] 2.1 Advertise optional string collection for both tools and document omitted/empty compatibility in their descriptions; assert required key, unchanged output schema and core/full visibility. Verify `go test -count=1 ./pkg/mcp -run 'TestMemoryPinDescriptor|TestToolProfiles|TestToolDescriptors'` and `make contract-check`; preflight: Go only. Generated artifacts change only if the generator detects actual drift.
- [x] 2.2 Add real MCP transport tests for legacy default, explicit override, stateless explicit selection, authenticated owner isolation and malformed-selector no-effect behavior. Verify `go test -count=1 -v ./internal/http -run TestMCPMemoryPinCollectionScope`; preflight: Go, real SQLite fixture and locally signed test JWT, no external server/model. Keep read-only API-key rejection covered by the existing auth suite.

## 3. Review and integration evidence

- [x] 3.1 Run affected packages with both SQL backends and targeted race/transport/auth checks: `go test -count=1 ./pkg/mcp ./internal/http`, `go test -race -count=1 ./pkg/mcp ./internal/http -run 'TestToolMemoryPinCollectionScope|TestMCPMemoryPinCollectionScope|TestMCPReadOnlyAPIKeyCannotCallMutatingTool'`. Verify `make test-commit`, `make contract-check`, `openspec validate scope-memory-pins-by-collection --strict`, and the combined diff with an independent read-only reviewer. Preflight: Go, isolated PostgreSQL as 1.1; report any integration skips explicitly. DoD: current-revision evidence has no unresolved change-induced failure or review finding.
- [x] 3.2 Record observed RED/GREEN commands, SQL/transport coverage, review outcome and remaining compatibility limits in evidence.md; update roadmap T04 only after its DoD passes. Verify all completed checkboxes against evidence and `openspec instructions apply --change scope-memory-pins-by-collection --json`; preflight: no additional service. Stop the temporary test PostgreSQL after verification; preserve unrelated dirty files.

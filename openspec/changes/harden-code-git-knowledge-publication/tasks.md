# Tasks

## 1. Supported analysis and repository outcomes

- [x] 1.1 Add checked source analysis and preserve legacy adapter; cover Go syntax errors, JS/TS/unknown rejection, valid empty graph, Python heuristic and supported case-insensitive extensions with `go test ./pkg/extract -run 'Analyze|Code' -skip '^TestMemoryREST'` (no external service), documenting supported limits.
- [x] 1.2 Make Git subprocesses use caller context, preserve compatibility adapter and define initialized empty/filter-empty/invalid/repeated results; cover cancellation and normal git repositories with `go test ./pkg/git ./pkg/mcp -run 'ParseLog|Git|AnalyzeCommits' -skip '^TestMemoryREST'` (local git executable), documenting no commit exactly-once claim.

## 2. Static native graph publication

- [x] 2.1 Add one concrete precomputed graph pipeline input, bypass model/semantic/temporal extraction and preserve guarded later stages; verify no model requests, resolvable module/declaration/reference endpoints, same-name collisions, source stamping and errors with `go test ./pkg/orchestrator ./pkg/mcp -run 'Static|Codify' -skip '^TestMemoryREST'` (in-process fixtures).
- [x] 2.2 Replace raw codify effects with synchronous existing immutable ingestion/claim/pipeline publication and drained progress; preserve summary and local nilDB/noEmbed behavior, document code_knowledge default and partial failed-ingestion semantics, and verify explicit SQL/embed/pipeline failure through focused MCP tests (no external service).

## 3. Public authority and native lifecycle

- [x] 3.1 Apply verified active administrator/no-selected-tenant admission, expiry-bounded execution and protected dispatch; cover real legacy/latest MCP callers, read-only/foreign/expired/revoked authority, no-effects denial and retained response with `go test ./internal/http -run 'Codify|GitAuthority' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight).
- [x] 3.2 Add native both-SQL/pool1 publication/source/endpoint controls, repeated calls and retirement/replacement eligibility; verify current generation and no external LLM, failures and source-state revocation with focused native Codify tests (dedicated PostgreSQL preflight).

## 4. CLI and public contract

- [x] 4.1 Propagate HTTP/JSON-RPC/MCP/malformed-response errors from Git CLI, preserve successful output/auth behavior and document supported commands; verify httptest command exit/status controls with `go test ./cmd/cli -run 'Git' -skip '^TestMemoryREST'` (no external service).
- [x] 4.2 Update code/Git descriptors and guide, regenerate full/core contracts with existing generators and verify advertised schema/profile behavior, documented examples, `make contract-check` and contract fixtures (no external service).

## 5. Integration acceptance

- [x] 5.1 Run current frozen native race selection covering Extract/Analyze/ParseLog/Static/Codify/Git and required S0–S4 test-commit gate, all excluding `^TestMemoryREST`; record actual terminal outcomes, manifests and failures after dedicated PostgreSQL preflight.
- [x] 5.2 Independently audit combined diff/contracts and original DoD, run strict OpenSpec and git diff --check, and reconcile roadmap/issues/evidence; own acceptance must not certify unavailable original T15/Neo4j prerequisites.

# Tasks

## 1. Native diary isolation and authority

- [x] 1.1 Preserve observed original wire RED and reproduce the chosen policy with corrected legacy expectations; implement caller/tenant/agent namespace and native SQL authority fences in tool_diary.go, FALSE pin and complete read error handling. DoD: both SQL and legacy/latest actual JWT GREEN/race, same caller across two tenants, forged hints, tuple collisions, local legacy positive/authenticated legacy negative, own canonical upsert, unchanged controls, whitespace agent, read-only/revoked/empty-permission credentials, cancellation and pool=1. Verify: `go test -count=1 -v ./internal/http -run 'MCPDiary'`, repeat `-race`; `go test -count=1 -v ./pkg/mcp -run 'Diary'`. Preflight P: verified root-owned isolated PostgreSQL, local fixtures only; no paid providers or live services.
- [x] 1.2 Align diary descriptors and guides with verified caller/tenant scoping and historical local-only compatibility. DoD: independent source-to-guide review, generated contracts regenerated only by root, `make contract-check`, `go test ./docs ./cmd/contract` and descriptor/profile tests pass. Preflight G; root owns all generated files.

- [x] 1.3 Close the independently reproduced REST writer bypass before accepting diary isolation. Root owns internal/http/memories.go and memories_owner_scope_test.go. DoD: real JWT REST rejects foreign/synthetic owner with 403 and leaves the full victim row unchanged; victim diary_read returns original text on both SQL and MCP transports. Own omitted/explicit owner retains canonical ID; authenticated requests remain scoped when RequireAuth=false; local anonymous explicit owner compatibility remains. Preserve hall validation and other REST issues as separate scope. Verify focused native GREEN/race and independent combined review. Observed root native/race completed before the user's request to stop additional owner probes; no further owner tests scheduled.

## 2. Bounded integration

- [x] 2.1 Review actual combined diff independently and run current-revision stable S0–S4 gate, strict OpenSpec validation and original chat/distill regressions; record actual source hashes, commands, exits and limitations. DoD: no skipped SQL acceptance and no unresolved gate failure disguised as PASS. T08 transcript authority, cancellation/routing and dedupe/empty security controls are accepted; model quality remains separate. Preflight P/G; root owns integration, worker owns only explicitly assigned files.

Accepted 2026-10-06 after native focused/race, independent combined review and stable S0–S4. See ../scope-imported-chat-project-sharing/evidence.md for current revision, actual exits, exclusions and residual boundaries.

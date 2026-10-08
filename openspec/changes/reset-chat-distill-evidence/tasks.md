# Tasks

## 1. Distilled-memory evidence boundary

- [x] 1.1 Reproduce evidence-backed Save → same-key distillation retaining old proof, then reset the three native SQL evidence fields; DoD: actual RED then GREEN/race on SQLite and isolated PostgreSQL for receipt-backed/legacy overwrite, fresh insertion, forged hints, dry-run and owner/shared/sibling controls, preserving canonical ID and timestamp/schema compatibility. Update the existing test DDL only as required. Verify: `go test -count=1 -v ./pkg/mcp -run 'ChatDistill|MemoryWriteEvidence'`; repeat with `-race`. Preflight: P, verified root-owned test DB, stub model only; no external provider.
- [x] 1.2 Align descriptor and relevant guides with unverified model publication and unchanged origin/preview semantics; DoD: source-to-guide review, descriptor/profile regressions, `go test ./docs ./cmd/contract` and `make contract-check` pass after generator-owned regeneration. Preflight: G; root owns all generated artifacts.

## 2. Bounded integration

- [x] 2.1 Independently review the actual combined diff and observe current full MCP/HTTP dispatch regressions, strict OpenSpec validation and stable-source S0–S4 gate; DoD: current hashes, actual RED/GREEN and limitations recorded with no skipped SQL acceptance. Verify: `go test ./pkg/mcp`, existing real-JWT chat dispatch matrix on both SQL dialects/transports, `openspec validate reset-chat-distill-evidence --strict`, `make test-commit`. T08 diary/import/authority/routing/dedupe/empty security boundaries are accepted; model quality remains separate.

Accepted 2026-10-06 after native focused/race, independent combined review and stable S0–S4. See ../scope-imported-chat-project-sharing/evidence.md for current revision, actual exits, exclusions and residual boundaries.

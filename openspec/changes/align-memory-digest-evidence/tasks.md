# Tasks

## 1. Evidence-label digest eligibility

- [x] 1.1 Reproduce valid evidence-backed save missing from its selected digest, then extend the native eligibility predicate with legacy compatibility; DoD: actual RED then focused GREEN/race on SQLite and isolated PostgreSQL for validated decision/discovery, stored labels/provenance, legacy verified, caller hint rejection, own/shared/foreign/sibling/retired/genre exclusions and duplicate IDs. Command: `go test -count=1 -v ./pkg/mcp -run 'MemoryMarkdownDigest|MemoryWriteEvidence'`. Preflight: verified root-owned PostgreSQL and SQL-only evidence fixtures; no external provider.
- [x] 1.2 Align the MCP descriptor and relevant guides, regenerate contracts with the existing generator; DoD: source-to-guide review, `go test ./docs`, descriptor/profile regressions and `make contract-check` pass with unchanged input/result/error shapes and explicit publication-evidence versus truth distinction. Preflight: G; generated files have one root owner.

## 2. Bounded acceptance

- [x] 2.1 Independently review the actual combined digest diff and observe fresh focused SQL/race, full MCP package, contract and strict OpenSpec checks; DoD: no scope blocker or skipped SQL acceptance, current source hashes and RED/GREEN documented. Commands: `go test -count=1 ./pkg/mcp`, `make contract-check`, `openspec validate align-memory-digest-evidence --strict`. T07 remains open until its separate history/provenance/recall DoD is satisfied.

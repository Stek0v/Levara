# Tasks

## 1. Seed grammar and contract

- [x] 1.1 Document the exact bounded Markdown grammar, private caller/tenant/dataset scope, REST reports and CLI examples in docs/taxonomy-import.md; verify parser tests for BOM/CRLF, Unicode normalization, malformed second domain, duplicate siblings, missing sources, bounds and alias warnings with `go test ./pkg/taxonomy -skip '^TestMemoryREST'` (no external service).

## 2. Native transactional writer

- [x] 2.1 Add the mirrored content-free knowledge_taxonomy_runs journal and concrete additive writer using the existing metadata fence; verify fresh/repeated/populated initialization, stable IDs, concurrent imports, identical replay/conflicting replay and SQL/audit rollback in SQLite and disposable PostgreSQL with focused taxonomy tests (preflight: configured dedicated test PostgreSQL accepts connections).
- [x] 2.2 Expose authenticated import/list/remove handlers with exact verified scope and dataset/source permissions; verify empty arrays, unknown fields, foreign/read-only dataset, cross-tenant isolation, cancellation, credential recheck, pool-size-one behavior and forced removal preserving source/graph rows through native HTTP tests (same PostgreSQL preflight).
- [x] 2.3 Reconcile the old writer design and document journal/removal semantics; verify the documented successful and rejected requests through the focused native HTTP suite.

## 3. CLI lifecycle

- [x] 3.1 Add taxonomy import/list/remove dispatch using existing auth/server settings; verify httptest command checks for exact requests, revision, source basename, missing arguments, unknown options and non-success HTTP failures with `go test ./cmd/cli -run Taxonomy -skip '^TestMemoryREST'` (no external service), and document runnable examples.

## 4. Native DCD source bindings

- [x] 4.1 Carry source_document_id separately from taxonomy IDs in resolver and authoritative SQL VSA candidates, match native dataset plus source, and derive exact authenticated tenant scope; verify PostgreSQL/SQLite binding, foreign dataset, legacy route compatibility and tenant isolation with focused DCD/VSA tests (dedicated PostgreSQL preflight).
- [x] 4.2 Freeze supported graph-strategy HTTP quality fixtures using actually imported document bindings; verify off/observe ordering equality, eligible boost lift, zero-result counts and denied/retired-source exclusion, and record bounded native evidence without changing the default DCD mode (dedicated PostgreSQL preflight; deterministic in-process model/vector fixtures).

## 5. Integration acceptance

- [x] 5.1 Run current-revision focused native race checks and the required broader test-commit gate with `-skip '^TestMemoryREST'`; record terminal results and frozen manifests after service preflight, preserving any initial failures.
- [x] 5.2 Generate REST/schema contracts through the existing generators, inspect the delta, run `make contract-check`, `git diff --check` and `openspec validate implement-taxonomy-seed-writer --strict`; independently audit the combined diff and update the roadmap/issues/evidence without accepting unavailable original T15/Neo4j prerequisites.

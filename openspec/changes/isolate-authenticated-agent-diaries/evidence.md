# Diary isolation evidence — 2026-10-06

Original roadmap remains 7/32 accepted. T08 and this change's integration remain open.

## Observed reproduction and implementation

Real JWT MCP original RED: `/tmp/levara-diary-scope-red.log`, exit 1, eight failing SQL/transport leaves. SQLite public writes demonstrated cross-caller read and canonical-row overwrite. PostgreSQL public writes separately failed with SQLSTATE 42804; typed legacy SQL seeds demonstrated read visibility and are not public-write evidence.

Corrected policy RED: `/tmp/levara-diary-policy-red.log`, exit 1, 28 failing and four expected passing leaves. Supplemental whitespace/scan RED: `/tmp/levara-diary-policy-read-validation-red.log`, exit 1, eight failing leaves. The first invalid NULL seed failed the fixture NOT NULL constraint; the meaningful probe used a native read projection.

Worker changed only `pkg/mcp/tool_diary.go` and `internal/http/mcp_diary_scope_test.go`. Caller/tenant/normalized agent identity, native SQL action and live credential/membership fences, BOOLEAN FALSE, complete scan/iteration/close errors. Historical agent-only rows remain local anonymous compatible; no ownership migration.

Worker native GREEN/race: `go test [-race] -count=1 -v ./internal/http -run 'MCPDiary'` with root-owned PostgreSQL DSN, actual exit 0, 40/40 leaves and 58 RUN nodes, no skips, 7.417s / 9.173s. Legacy unit: `go test -count=1 -v ./pkg/mcp -run 'Diary'`, exit 0, 13/13, 1.073s. Raw `/tmp/levara-diary-policy-{green,race,unit}.log`.

## Alternate writer regression

Root actual REST RED: `go test -count=1 -v ./internal/http -run '^TestMemoryRESTCannotOverwriteAuthenticatedDiary$'`, exit 1, 1.603s. Both native SQL databases and both real JWT MCP transports: attacker REST POST returned 201 with victim canonical ID, changed the full victim row, and victim diary_read returned forged text. Raw `/tmp/levara-rest-diary-owner-red.log`.

Root guarded `internal/http/memories.go` rejects authenticated foreign/synthetic owner before SQL/outbox/event; own omitted/explicit owner derives from caller. Anonymous configured local explicit-owner compatibility remains. New `memories_owner_scope_test.go` verifies victim-row preservation and actual reads plus canonical own updates and optional-auth authenticated controls. Hall validation, other REST authorization/evidence and SSE issues remain separate.

Root combined REST/recovery native GREEN: `/tmp/levara-t08-rest-recovery-green.log`, actual exit 0, HTTP 4.461s. Root combined diary/REST/recovery race: `/tmp/levara-t08-diary-rest-recovery-race.log`, actual exit 0, HTTP 15.920s, no race warning. These precede the final test-strengthening edits described below.

## Review and remaining verification

Independent read-only reviewer found no blocking production defect in diary or REST owner guard. Root strengthened two test proofs after review: deterministic valid-row-first scan error, and a fresh write deadline with independent pool waits. The updated tests passed root's fresh race run (17.047s) recorded below; earlier worker hashes/results certify only their earlier revision.

I31 recovery harness now witnesses actual pool wait, instead of classifying SQL latency by 500ms. Original held-cursor diagnostic ended with cleanup join timeouts and is retained. Corrected temporary probe bounds its injected write context: `/tmp/levara-i31-held-rows-bounded-red.log`, actual exit 1, HTTP 1.649s; both databases explicitly reject the one-connection self-wait. This is a diagnostic, not a production performance benchmark. The synchronous-runner temporary overlay was observed exit 1, HTTP21.862s, four failing SQL/transport leaves: enqueue returned only after downstream completed, so the strengthened async assertion rejected it. Raw `/tmp/levara-i31-sync-runner-red.log`. Final strengthened diary/recovery race observed exit 0, HTTP17.047s, no skipped SQL or race warnings, raw `/tmp/levara-t08-diary-final-race.log`. It excludes REST owner probes, which the user subsequently asked to stop; the already observed REST fix and evidence remain recorded. Contract generation succeeded. Root `make contract-check` observed exit0. Full `go test -count=1 ./docs ./cmd/contract ./pkg/profile` observed exit0 (0.338s/0.639s/0.199s); the earlier descriptor-focused command also passed pkg/mcp0.752s, but matched no cmd/contract or profile tests, so the full three-package command supplies that evidence. Strict OpenSpec validation observed exit0. Broader original T08 integration remains open. SQL authority does not cover HTTP transfer drain. No live rollout, migration or external model quality test performed.

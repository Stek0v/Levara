# Digest evidence eligibility — observed checks

2026-10-06, Europe/Moscow. Bounded change accepted, 3/3; original T07 accepted separately, roadmap 7/32. I27 was reproduced natively and resolved with a production-equivalent test fixture. T08 and later milestones remain open.

## Implementation and regression

The only digest production change is the native SQL eligibility predicate: active selected decisions/discoveries accept canonical `receipt-validated` alongside legacy `verified`. Owner/shared visibility, collection, ID selection, renderer and result/error shapes stay intact. Labels describe publication provenance; exporting a historical validation label does not revalidate its receipt or prove factual truth.

The new regression uses actual ToolSaveMemory with actual owned Task command receipts on SQLite and isolated PostgreSQL. PostgreSQL timestamps are native TIMESTAMPTZ. It verifies stored labels, IDs, task/receipt provenance and per-card freshness; own/shared positives; foreign, sibling collection, retired, unverified, caller hint and wrong-genre exclusions; duplicate/trimmed IDs; the 100-ID boundary; malformed selectors; and snapshots all memories/tasks/receipts/events columns before/after read-only export and rejected calls.

- `/tmp/levara-digest-evidence-red.log`: four failing leaves, both SQL dialects; valid writer output was missing from its digest. An intermediate post-fix test used an invalid aggregate freshness count when two saves shared a second; it was corrected to assert each card separately, without a further production change.
- `/tmp/levara-digest-evidence-green.log`: exit 0, 75 RUN/PASS, zero skips, MCP 4.287s.
- `/tmp/levara-digest-evidence-race.log`: exit 0, 75 RUN/PASS, zero skips, MCP 6.487s.
- `/tmp/levara-t07-strengthened-race.log`: fresh combined evidence/history suite after descriptor/docs updates and attribution-test strengthening; exit 0, 232 RUN/PASS, zero failures/skips, MCP 93.948s. Covers digest, writer evidence, receipt/revision/artifact failures, rollback, history and attribution; both SQL dialects.

Root observed `make contract` and `make contract-check` exit 0, and `go test ./docs ./cmd/contract` exit 0 (0.534s/0.909s). The generator's MCP table contains name/group/status, not descriptions; regeneration does not produce an artificial descriptor diff. The source descriptor and relevant guides explain both eligibility labels and the evidence/truth distinction. Full MCP package passed freshly in the combined gate, 235.291s, including descriptor/profile/result-schema regressions.

## Separate T07 attribution/history work

Recall's shared SQL/vector/history projection inherited retirement reason/time from any provenance predecessor, including a predecessor actually replaced by another row. Both predecessor subqueries now require reciprocal replacement in the exact owner and collection namespace. SQL B remains active after provenance-only B→A and actual retirement A→C; C still inherits its real retirement metadata. This work is recorded with original T07, outside this digest change's spec scope.

Corrected original RED had ten failing SQL leaves; focused GREEN passed 48 RUN with no skips, MCP 2.169s. A subsequent Go overlay restored only the former projection for a stronger baseline, without reverting working files: `/tmp/levara-t07-strengthened-overlay-red.log`, observed exit 1, ten failing leaves (16 FAIL including parents), both dialects. Vector/history use a nonliteral query with zero SQL key/value matches and exact Embed/Search call counts; SQL fallback cannot hide failure. Current combined race above passes these cases. Literal historical matches beyond ten unrelated semantic candidates and with embedding disabled also pass, with exact identity/scope controls.

Independent read-only reviewer `/root/pin_transport_map` inspected the actual four production/test files and raw logs. No blocking production findings. Its test proof gap was fixed and independently rechecked against current assertions and race output. The reviewer did not run tests or audit the remaining roadmap.

## Current gate and revision

Base HEAD: `2eb1dca16b0047918185760b41dcb22dee79090a`, uncommitted source. Test revision manifest `/tmp/levara-t07-gate-source-manifest.json`: 111 dirty files, aggregate `b5cc7b88c366df9e5e7997e83041e6321ab5af35e8e520097ed78e8390203ba7`; all unchanged at gate completion.

`/tmp/levara-t07-current-test-commit.log`: observed make exit 2. S0–S2 passed; HTTP failed only the reported `TestMCPInlineCognifyTransportsPersistServerSource/sqlite/legacy` leaf with `search access revoked or unavailable`, package 259.679s. S4 was not run. A fresh isolated reproduction passed all four dialect/transport cases, 19.007s, but a ten-repeat SQLite leaf run reproduced one refusal. A separate 100-repeat native-error overlay (not working source edits) reproduced `sqlite3.ExtendedErrorCode: database is locked`, provider SQLite, at fence acquisition. The shared fixture specified WAL without a busy handler; this disables the driver's implicit timeout. Its DSN now mirrors the server's explicit `busy_timeout(5000)` and immediate transaction mode. Production authorization and fences are unchanged. Fresh four-matrix × ten-repeat race passed: 50 RUN/PASS, zero failures/skips, HTTP 23.014s; `/tmp/levara-i27-fixture-green-race.log`. Independent reviewer confirmed the one-line fixture diff and native driver semantics; complete HTTP fixture blast-radius gate is still required. This failed gate is not replaced by a focused PASS. The repaired whole-gate attempt `/tmp/levara-t07-repaired-test-commit.log` was deliberately interrupted with SIGQUIT for diagnostics (make exit 2, HTTP 354.454s), not accepted: its stack caught PostgreSQL schema migration during `TestConsolidationRecoverySingleConnection`. An isolated both-SQL probe then passed, HTTP 1.587s; the stack alone does not prove a deadlock or fixture regression. A complete HTTP run with JSON progress is underway, `/tmp/levara-t07-http-progress-gate.jsonl`; final S0–S4 acceptance was pending at that checkpoint.

Completed acceptance: `GOFLAGS=-json LEVARA_TEST_POSTGRES_DSN=<isolated test DB> make test-commit` observed exit 0, S0–S4 green; `/tmp/levara-t07-final-test-commit.log`. Complete fresh HTTP JSON run passed 1504 tests, with only the opt-in `TestDCDVSALoadBaseline` skipped; package 189.375s. This does not claim that load baseline passed. The final gate reused that completed HTTP result and freshly checked full MCP, core store and server, plus unchanged other packages. The final 113-file manifest `/tmp/levara-t07-json-http-source-manifest.json`, aggregate `8bafaa1387228043809de64f1cb08656c2ef6911334dcb2d1a39ecf0aa7acd77`, stayed unchanged throughout the final gate. Subsequent acceptance-only documentation edits are checked separately; implementation hashes above remain current. Shared SQLite fixture SHA-256: `0d2c1c1ea804f3854c65c065b01ece5d95222a08e59fdc3a53e82b992f4b2e16`. Current contract check and strict OpenSpec validation observed exit 0.

Current implementation SHA-256:

```text
4e55bc10da7b4157588cc84f6aee8719318fc7dd5eb09354e9a3f46c57bf2673 pkg/mcp/tool_memory_markdown_digest.go
e0badd1dc8cbed10de354e46029ebb8ea8fcf2b8ff529444a2ea0351caa4599e pkg/mcp/tool_memory_markdown_digest_test.go
f9bd293af1ca2cde413ecd70dadc6ae6e5e718e40bdbea00f2de743c48b87f13 pkg/mcp/tool_save_recall_memory.go
dfa9454134b9a9a5f8808203485bbabb9d200d6b673b80571bad0e4f4a48d125 pkg/mcp/tool_memory_attribution_test.go
d0f28faddb377f197fb8a43ecd0bfbe579bd723962ba67de8def641eacd43077 pkg/mcp/tools.go
```

The eleven accepted T06 index/native source/test/spec hashes were compared again and remain unchanged. Their prior root race evidence establishes late-retirement/requeue/deferred-shadow protection for those byte-identical components; it is historical execution, not a fresh T07 command. Export does not write workspace/Git or add import/migration/revalidation behavior. No commits, deployment, live migrations or external provider calls were performed. Local logs/manifests are not durable-memory claims or a release acceptance report.
